package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"time"

	"google.golang.org/protobuf/proto"

	balloonsv1 "github.com/BHenkemans/balloons/gen/balloons/v1"
	"github.com/BHenkemans/balloons/internal/state"
)

// ErrAssignmentNotForCaller is returned when CompleteAssignment is invoked by
// a runner that doesn't own the referenced assignment.
var ErrAssignmentNotForCaller = errors.New("assignment does not belong to caller")

// SubscribeRunner registers a server-streaming subscriber for a single
// runner's state events. The returned snapshot is the runner's current
// state event (with current_assignment populated if busy).
func (h *Hub) SubscribeRunner(runnerID int64) (*balloonsv1.WatchRunnerStateResponse, <-chan *balloonsv1.WatchRunnerStateResponse, func(), error) {
	r, err := h.store.GetRunner(runnerID)
	if err != nil {
		return nil, nil, nil, err
	}

	sub := &runnerSub{
		runnerID: runnerID,
		ch:       make(chan *balloonsv1.WatchRunnerStateResponse, 32),
	}

	h.mu.Lock()
	if _, ok := h.runnerSubs[runnerID]; !ok {
		h.runnerSubs[runnerID] = map[*runnerSub]struct{}{}
	}
	h.runnerSubs[runnerID][sub] = struct{}{}
	snap := h.runnerEventLocked(r, "")
	h.mu.Unlock()

	cancel := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if subs, ok := h.runnerSubs[runnerID]; ok {
			if _, alive := subs[sub]; alive {
				delete(subs, sub)
				close(sub.ch)
			}
			if len(subs) == 0 {
				delete(h.runnerSubs, runnerID)
			}
		}
	}
	return snap, sub.ch, cancel, nil
}

// SubscribeRoster registers an admin-side subscriber. The first event delivered
// is a SNAPSHOT of all currently-known runners.
func (h *Hub) SubscribeRoster() (*balloonsv1.StreamRunnersResponse, <-chan *balloonsv1.StreamRunnersResponse, func(), error) {
	runners, err := h.store.ListRunners()
	if err != nil {
		return nil, nil, nil, err
	}
	sub := &rosterSub{ch: make(chan *balloonsv1.StreamRunnersResponse, 64)}
	h.mu.Lock()
	h.rosterSubs[sub] = struct{}{}
	protoRunners := make([]*balloonsv1.Runner, 0, len(runners))
	for _, r := range runners {
		protoRunners = append(protoRunners, h.runnerProtoLocked(r))
	}
	h.mu.Unlock()

	cancel := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.rosterSubs[sub]; ok {
			delete(h.rosterSubs, sub)
			close(sub.ch)
		}
	}
	return &balloonsv1.StreamRunnersResponse{
		Kind:     balloonsv1.StreamRunnersResponse_KIND_SNAPSHOT,
		Snapshot: protoRunners,
	}, sub.ch, cancel, nil
}

// --- Runner-state mutations -------------------------------------------------

// CreateRunner registers a runner request from a phone. The returned runner is
// in pending_admit until an admin admits.
func (h *Hub) CreateRunner(name string) (state.Runner, error) {
	r, err := h.store.CreateRunner(name)
	if err != nil {
		return state.Runner{}, err
	}
	h.mu.Lock()
	h.broadcastRunnerLocked(r, "")
	h.broadcastRosterUpsertLocked(r)
	h.mu.Unlock()
	return r, nil
}

func (h *Hub) AdmitRunner(id int64) (state.Runner, error) {
	r, err := h.store.GetRunner(id)
	if err != nil {
		return state.Runner{}, err
	}
	if r.Status != state.RunnerPendingAdmit {
		return state.Runner{}, fmt.Errorf("runner %d: cannot admit from status %q", id, r.Status)
	}
	r, err = h.store.SetRunnerStatus(id, state.RunnerIdle)
	if err != nil {
		return state.Runner{}, err
	}
	h.mu.Lock()
	h.broadcastRunnerLocked(r, "Admitted. Tap \"I'm available\" when you're ready.")
	h.broadcastRosterUpsertLocked(r)
	h.mu.Unlock()
	return r, nil
}

func (h *Hub) RejectRunner(id int64) error {
	r, err := h.store.SetRunnerStatus(id, state.RunnerRejected)
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.broadcastRunnerLocked(r, "Your session was not approved. Talk to an admin.")
	h.broadcastRosterUpsertLocked(r)
	h.mu.Unlock()
	return nil
}

// SetRunnerAvailable flips a runner between idle and available. Caller must be
// in idle, available, or delivered_pending_ack — other states are no-ops.
func (h *Hub) SetRunnerAvailable(id int64, available bool) (state.Runner, error) {
	r, err := h.store.GetRunner(id)
	if err != nil {
		return state.Runner{}, err
	}
	target, err := nextAvailabilityStatus(r.Status, available)
	if err != nil {
		return state.Runner{}, fmt.Errorf("runner %d: %w", id, err)
	}
	if target == r.Status {
		return r, nil
	}
	r, err = h.store.SetRunnerStatus(id, target)
	if err != nil {
		return state.Runner{}, err
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	h.broadcastRunnerLocked(r, "")
	h.broadcastRosterUpsertLocked(r)
	if target == state.RunnerAvailable {
		h.tryDispatchLocked()
	}
	return r, nil
}

// nextAvailabilityStatus returns the runner status to transition to when the
// phone toggles availability. Pure — no Hub or DB dependency, so it's the
// place the state-transition rules live and can be tested in isolation.
func nextAvailabilityStatus(current string, available bool) (string, error) {
	if available {
		switch current {
		case state.RunnerIdle, state.RunnerDeliveredPendingAck, state.RunnerAvailable:
			return state.RunnerAvailable, nil
		default:
			return "", fmt.Errorf("cannot become available from %q", current)
		}
	}
	if current == state.RunnerBusy {
		return "", fmt.Errorf("cannot go idle while busy")
	}
	return state.RunnerIdle, nil
}

// CompleteAssignment marks the runner's current assignment delivered. Calls
// DOMjudge MarkDone synchronously so the rest of the system sees the change.
func (h *Hub) CompleteAssignment(ctx context.Context, runnerID, assignmentID int64) (state.Runner, error) {
	a, err := h.store.GetAssignment(assignmentID)
	if err != nil {
		return state.Runner{}, err
	}
	if a.RunnerID != runnerID {
		return state.Runner{}, ErrAssignmentNotForCaller
	}
	if a.State != state.AssignmentAssigned {
		return state.Runner{}, fmt.Errorf("assignment %d: already %s", assignmentID, a.State)
	}

	if err := h.dj.MarkDone(ctx, a.BalloonID); err != nil {
		return state.Runner{}, fmt.Errorf("domjudge MarkDone: %w", err)
	}
	if err := h.store.MarkAssignmentDelivered(assignmentID); err != nil {
		return state.Runner{}, err
	}
	if err := h.store.RecordDelivered(a.BalloonID); err != nil {
		log.Printf("CompleteAssignment: record delivered: %v", err)
	}
	r, err := h.store.SetRunnerStatus(runnerID, state.RunnerDeliveredPendingAck)
	if err != nil {
		return state.Runner{}, err
	}

	h.mu.Lock()
	h.clearBalloonAssignmentLocked(a.BalloonID)
	h.broadcastRunnerLocked(r, "")
	h.broadcastRosterUpsertLocked(r)
	h.mu.Unlock()

	h.TriggerRefresh()
	return r, nil
}

// ReadyForNext flips a runner from delivered_pending_ack back to available and
// triggers the dispatcher.
func (h *Hub) ReadyForNext(runnerID int64) (state.Runner, error) {
	r, err := h.store.GetRunner(runnerID)
	if err != nil {
		return state.Runner{}, err
	}
	if r.Status != state.RunnerDeliveredPendingAck && r.Status != state.RunnerIdle {
		return state.Runner{}, fmt.Errorf("runner %d: cannot ready-for-next from %q", runnerID, r.Status)
	}
	r, err = h.store.SetRunnerStatus(runnerID, state.RunnerAvailable)
	if err != nil {
		return state.Runner{}, err
	}
	h.mu.Lock()
	h.broadcastRunnerLocked(r, "")
	h.broadcastRosterUpsertLocked(r)
	h.tryDispatchLocked()
	h.mu.Unlock()
	return r, nil
}

// ForceReturnAssignment is the admin escape hatch for stuck assignments.
// Cancels the assignment, drops the runner back to idle (not available — the
// admin is intervening because the runner may be unresponsive).
func (h *Hub) ForceReturnAssignment(assignmentID int64) error {
	a, err := h.store.GetAssignment(assignmentID)
	if err != nil {
		return err
	}
	if a.State != state.AssignmentAssigned {
		return fmt.Errorf("assignment %d: already %s", assignmentID, a.State)
	}
	if err := h.store.CancelAssignment(assignmentID, state.CancelReasonAdminForce); err != nil {
		return err
	}
	r, err := h.store.SetRunnerStatus(a.RunnerID, state.RunnerIdle)
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.clearBalloonAssignmentLocked(a.BalloonID)
	h.broadcastRunnerLocked(r, "Admin returned your balloon to the queue.")
	h.broadcastRosterUpsertLocked(r)
	h.tryDispatchLocked()
	h.mu.Unlock()
	return nil
}

// KickRunner forces a runner offline. Any active assignment is cancelled.
func (h *Hub) KickRunner(runnerID int64) error {
	if a, err := h.store.ActiveAssignmentForRunner(runnerID); err == nil {
		if err := h.store.CancelAssignment(a.ID, state.CancelReasonAdminForce); err != nil {
			return err
		}
		h.mu.Lock()
		h.clearBalloonAssignmentLocked(a.BalloonID)
		h.mu.Unlock()
	} else if !errors.Is(err, state.ErrNotFound) {
		return err
	}
	r, err := h.store.SetRunnerStatus(runnerID, state.RunnerOffline)
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.broadcastRunnerLocked(r, "Admin kicked your session.")
	h.broadcastRosterUpsertLocked(r)
	h.tryDispatchLocked()
	h.mu.Unlock()
	return nil
}

// handleBackupScannerOverride is called by MarkDone before it succeeds against
// DOMjudge. If the balloon currently has an active assignment, the assignment
// is cancelled and the runner is bumped back to `available` with a note.
func (h *Hub) handleBackupScannerOverride(balloonID int64) {
	a, err := h.store.ActiveAssignmentForBalloon(balloonID)
	if err != nil {
		if !errors.Is(err, state.ErrNotFound) {
			log.Printf("backup-scanner override: lookup: %v", err)
		}
		return
	}
	if err := h.store.CancelAssignment(a.ID, state.CancelReasonAdminBackupScan); err != nil {
		log.Printf("backup-scanner override: cancel: %v", err)
		return
	}
	r, err := h.store.SetRunnerStatus(a.RunnerID, state.RunnerAvailable)
	if err != nil {
		log.Printf("backup-scanner override: set runner: %v", err)
		return
	}
	h.mu.Lock()
	h.clearBalloonAssignmentLocked(balloonID)
	h.broadcastRunnerLocked(r, "Admin delivered the balloon you were working on. Returned to the queue.")
	h.broadcastRosterUpsertLocked(r)
	// Don't dispatch yet — the balloon is about to be marked Done by the
	// caller, so the upcoming refresh will reflect that.
	h.mu.Unlock()
}

// --- Dispatcher -------------------------------------------------------------

// pendingBalloon describes a balloon eligible for dispatch in the sort-key
// shape used by tryDispatchLocked.
type pendingBalloon struct {
	id         int64
	firstSolve bool
	// DOMjudge fixed-width seconds.nanoseconds — lexically comparable.
	time string
}

// tryDispatchLocked greedy-pairs the oldest pending balloons with the
// longest-waiting available runners. First-solves jump the queue. Caller must
// hold h.mu.
func (h *Hub) tryDispatchLocked() {
	if len(h.state) == 0 {
		return
	}
	pendings, runners, ok := h.collectDispatchCandidatesLocked()
	if !ok {
		return
	}
	sortPendingsForDispatch(pendings)
	for i := range min(len(pendings), len(runners)) {
		h.dispatchOneLocked(pendings[i].id, runners[i])
	}
}

// collectDispatchCandidatesLocked returns the pending balloons and available
// runners that should be paired this round. ok is false when either side is
// empty (or a DB error precludes a safe pairing).
func (h *Hub) collectDispatchCandidatesLocked() (pendings []pendingBalloon, runners []state.Runner, ok bool) {
	runners, err := h.store.ListAvailableRunners()
	if err != nil {
		log.Printf("dispatch: ListAvailableRunners: %v", err)
		return nil, nil, false
	}
	if len(runners) == 0 {
		return nil, nil, false
	}
	assigned, err := h.activeAssignmentBalloonSet()
	if err != nil {
		log.Printf("dispatch: ListActiveAssignments: %v", err)
		return nil, nil, false
	}
	pendings = h.pendingBalloonsLocked(assigned)
	if len(pendings) == 0 {
		return nil, nil, false
	}
	return pendings, runners, true
}

// activeAssignmentBalloonSet returns the set of balloon IDs currently held by
// an active (`assigned`-state) assignment.
func (h *Hub) activeAssignmentBalloonSet() (map[int64]bool, error) {
	active, err := h.store.ListActiveAssignments()
	if err != nil {
		return nil, err
	}
	out := make(map[int64]bool, len(active))
	for _, a := range active {
		out[a.BalloonID] = true
	}
	return out, nil
}

// pendingBalloonsLocked walks h.state and returns the balloons that are still
// pending and not already covered by an active assignment. Caller must hold h.mu.
func (h *Hub) pendingBalloonsLocked(assigned map[int64]bool) []pendingBalloon {
	var out []pendingBalloon
	for id, b := range h.state {
		if b.Done || assigned[id] {
			continue
		}
		dj, exists := h.last.byID[id]
		if !exists {
			continue
		}
		out = append(out, pendingBalloon{id: id, firstSolve: b.FirstSolve, time: dj.Time})
	}
	return out
}

func sortPendingsForDispatch(p []pendingBalloon) {
	sort.Slice(p, func(i, j int) bool {
		if p[i].firstSolve != p[j].firstSolve {
			return p[i].firstSolve
		}
		return p[i].time < p[j].time
	})
}

// dispatchOneLocked pairs balloon id with runner r: creates the assignment,
// flips the runner to busy (rolling back on failure to avoid orphans), and
// broadcasts the change. Caller must hold h.mu.
func (h *Hub) dispatchOneLocked(balloonID int64, r state.Runner) {
	a, err := h.store.CreateAssignment(balloonID, r.ID)
	if err != nil {
		log.Printf("dispatch: CreateAssignment(b=%d r=%d): %v", balloonID, r.ID, err)
		return
	}
	updated, err := h.store.SetRunnerStatus(r.ID, state.RunnerBusy)
	if err != nil {
		log.Printf("dispatch: SetRunnerStatus(%d, busy): %v", r.ID, err)
		if cancelErr := h.store.CancelAssignment(a.ID, state.CancelReasonDispatchFailed); cancelErr != nil {
			log.Printf("dispatch: rollback CancelAssignment(%d): %v", a.ID, cancelErr)
		}
		return
	}
	h.setBalloonAssignmentLocked(balloonID, updated.Name)
	h.broadcastRunnerLocked(updated, "")
	h.broadcastRosterUpsertLocked(updated)
	log.Printf("dispatch: balloon %d -> runner %d (%s) [assignment %d]", balloonID, r.ID, r.Name, a.ID)
}

// --- Helpers ----------------------------------------------------------------

// setBalloonAssignmentLocked updates the assigned_runner_name on the cached
// proto and broadcasts an UPDATED event to balloon-stream subscribers. Caller
// must hold h.mu.
func (h *Hub) setBalloonAssignmentLocked(balloonID int64, runnerName string) {
	b, ok := h.state[balloonID]
	if !ok {
		return
	}
	if b.AssignedRunnerName == runnerName {
		return
	}
	updated := proto.Clone(b).(*balloonsv1.Balloon)
	updated.AssignedRunnerName = runnerName
	h.state[balloonID] = updated
	h.broadcast([]*balloonsv1.StreamBalloonsResponse{{
		Kind:    balloonsv1.StreamBalloonsResponse_KIND_UPDATED,
		Balloon: updated,
	}})
}

func (h *Hub) clearBalloonAssignmentLocked(balloonID int64) {
	h.setBalloonAssignmentLocked(balloonID, "")
}

func (h *Hub) broadcastRunnerLocked(r state.Runner, note string) {
	ev := h.runnerEventLocked(r, note)
	subs := h.runnerSubs[r.ID]
	for sub := range subs {
		select {
		case sub.ch <- ev:
		default:
			log.Printf("runner %d: stream too slow, closing", r.ID)
			delete(subs, sub)
			close(sub.ch)
		}
	}
	if len(subs) == 0 {
		delete(h.runnerSubs, r.ID)
	}
}

func (h *Hub) broadcastRosterUpsertLocked(r state.Runner) {
	ev := &balloonsv1.StreamRunnersResponse{
		Kind:   balloonsv1.StreamRunnersResponse_KIND_UPSERT,
		Runner: h.runnerProtoLocked(r),
	}
	for sub := range h.rosterSubs {
		select {
		case sub.ch <- ev:
		default:
			log.Printf("roster subscriber too slow, closing")
			delete(h.rosterSubs, sub)
			close(sub.ch)
		}
	}
}

func (h *Hub) runnerEventLocked(r state.Runner, note string) *balloonsv1.WatchRunnerStateResponse {
	return &balloonsv1.WatchRunnerStateResponse{
		Runner: h.runnerProtoLocked(r),
		Note:   note,
	}
}

// RunnerProto converts a state.Runner to its proto form. Acquires h.mu;
// callers that already hold the lock should use runnerProtoLocked instead.
func (h *Hub) RunnerProto(r state.Runner) *balloonsv1.Runner {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.runnerProtoLocked(r)
}

func (h *Hub) runnerProtoLocked(r state.Runner) *balloonsv1.Runner {
	out := &balloonsv1.Runner{
		Id:         r.ID,
		Name:       r.Name,
		Status:     runnerStatusToProto(r.Status),
		CreatedAt:  r.CreatedAt.Format(time.RFC3339Nano),
		LastSeenAt: r.LastSeenAt.Format(time.RFC3339Nano),
	}
	if r.AvailableSince != nil {
		out.AvailableSince = r.AvailableSince.Format(time.RFC3339Nano)
	}
	if r.Status == state.RunnerBusy {
		if a, err := h.store.ActiveAssignmentForRunner(r.ID); err == nil {
			out.CurrentAssignment = h.assignmentProtoLocked(a)
		}
	}
	return out
}

func (h *Hub) assignmentProtoLocked(a state.Assignment) *balloonsv1.Assignment {
	dj, ok := h.last.byID[a.BalloonID]
	if !ok {
		// We dispatched on this balloon but DOMjudge has since dropped it
		// from the visible set. Return a minimal record so the phone still
		// shows something (very unlikely path).
		return &balloonsv1.Assignment{
			Id:         a.ID,
			BalloonId:  a.BalloonID,
			AssignedAt: a.AssignedAt.Format(time.RFC3339Nano),
		}
	}
	b := h.state[a.BalloonID]
	loc := ""
	if h.last.teamLocations != nil {
		loc = h.last.teamLocations[dj.TeamID]
	}
	return &balloonsv1.Assignment{
		Id:           a.ID,
		BalloonId:    a.BalloonID,
		ProblemLabel: dj.ContestProblem.Label,
		ProblemRgb:   dj.ContestProblem.RGB,
		TeamName:     b.TeamName,
		TeamLocation: loc,
		FirstSolve:   b.FirstSolve,
		AssignedAt:   a.AssignedAt.Format(time.RFC3339Nano),
	}
}

func runnerStatusToProto(s string) balloonsv1.RunnerStatus {
	switch s {
	case state.RunnerPendingAdmit:
		return balloonsv1.RunnerStatus_RUNNER_STATUS_PENDING_ADMIT
	case state.RunnerIdle:
		return balloonsv1.RunnerStatus_RUNNER_STATUS_IDLE
	case state.RunnerAvailable:
		return balloonsv1.RunnerStatus_RUNNER_STATUS_AVAILABLE
	case state.RunnerBusy:
		return balloonsv1.RunnerStatus_RUNNER_STATUS_BUSY
	case state.RunnerDeliveredPendingAck:
		return balloonsv1.RunnerStatus_RUNNER_STATUS_DELIVERED_PENDING_ACK
	case state.RunnerOffline:
		return balloonsv1.RunnerStatus_RUNNER_STATUS_OFFLINE
	case state.RunnerRejected:
		return balloonsv1.RunnerStatus_RUNNER_STATUS_REJECTED
	default:
		return balloonsv1.RunnerStatus_RUNNER_STATUS_UNSPECIFIED
	}
}
