package server

import (
	"context"
	"errors"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	balloonsv1 "github.com/BHenkemans/balloons/gen/balloons/v1"
	"github.com/BHenkemans/balloons/internal/domjudge"
	"github.com/BHenkemans/balloons/internal/printer"
	"github.com/BHenkemans/balloons/internal/state"
)

// ErrBalloonNotFound is returned by Reprint when the requested id is not in
// the hub's current view of DOMjudge (e.g. hidden by HIDE_GROUP_IDS or simply
// stale on the client).
var ErrBalloonNotFound = errors.New("balloon not found")

type Hub struct {
	dj      *domjudge.Client
	printer printer.Printer
	store   *state.Store

	hideGroups         map[string]bool
	noFirstSolveGroups map[string]bool

	scanBaseURL string
	loc         *time.Location

	mu     sync.Mutex
	state  map[int64]*balloonsv1.Balloon
	last   snapshot // most recently applied snapshot; kept for reprint ticket data
	frozen bool
	subs   map[*subscriber]struct{}

	// Runner-side streams: each connected runner phone gets a runnerSub, the
	// admin panel gets a rosterSub. Multiple runnerSubs per id are allowed
	// (e.g. runner has two tabs open).
	runnerSubs map[int64]map[*runnerSub]struct{}
	rosterSubs map[*rosterSub]struct{}

	trigger chan struct{}
}

type subscriber struct {
	ch chan *balloonsv1.StreamBalloonsResponse
}

type runnerSub struct {
	runnerID int64
	ch       chan *balloonsv1.WatchRunnerStateResponse
}

type rosterSub struct {
	ch chan *balloonsv1.StreamRunnersResponse
}

func NewHub(dj *domjudge.Client, p printer.Printer, store *state.Store, hideGroupIDs, noFirstSolveGroupIDs []string, scanBaseURL string, loc *time.Location) *Hub {
	if loc == nil {
		loc = time.Local
	}
	return &Hub{
		dj:                 dj,
		printer:            p,
		store:              store,
		hideGroups:         toSet(hideGroupIDs),
		noFirstSolveGroups: toSet(noFirstSolveGroupIDs),
		scanBaseURL:        strings.TrimRight(scanBaseURL, "/"),
		loc:                loc,
		state:              map[int64]*balloonsv1.Balloon{},
		subs:               map[*subscriber]struct{}{},
		runnerSubs:         map[int64]map[*runnerSub]struct{}{},
		rosterSubs:         map[*rosterSub]struct{}{},
		trigger:            make(chan struct{}, 1),
	}
}

func toSet(s []string) map[string]bool {
	out := map[string]bool{}
	for _, v := range s {
		if v != "" {
			out[v] = true
		}
	}
	return out
}

func (h *Hub) Snapshot() []*balloonsv1.Balloon {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*balloonsv1.Balloon, 0, len(h.state))
	for _, b := range h.state {
		out = append(out, b)
	}
	return out
}

func (h *Hub) Subscribe() (snapshot []*balloonsv1.Balloon, frozen bool, ch <-chan *balloonsv1.StreamBalloonsResponse, cancel func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := &subscriber{ch: make(chan *balloonsv1.StreamBalloonsResponse, 256)}
	h.subs[s] = struct{}{}
	snap := make([]*balloonsv1.Balloon, 0, len(h.state))
	for _, b := range h.state {
		snap = append(snap, b)
	}
	return snap, h.frozen, s.ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subs[s]; ok {
			delete(h.subs, s)
			close(s.ch)
		}
	}
}

func (h *Hub) print(t printer.Ticket) {
	// Dedupe against the local store so restarts don't reprint and prints
	// requested twice (e.g. two refreshes racing) only fire once.
	printed, err := h.store.IsPrinted(t.BalloonID)
	if err != nil {
		log.Printf("print balloon %d: state check: %v", t.BalloonID, err)
		return
	}
	if printed {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := h.printer.Print(ctx, t); err != nil {
		log.Printf("print balloon %d: %v", t.BalloonID, err)
		return
	}
	if err := h.store.RecordPrinted(t.BalloonID); err != nil {
		log.Printf("print balloon %d: record: %v", t.BalloonID, err)
	}
}

// Reprint clears the local "already printed" mark for this balloon and
// re-dispatches the print goroutine using the most recently cached snapshot.
// Returns ErrBalloonNotFound if the id isn't in the current view.
func (h *Hub) Reprint(id int64) error {
	h.mu.Lock()
	b, ok := h.state[id]
	if !ok {
		h.mu.Unlock()
		return ErrBalloonNotFound
	}
	t := h.ticketFor(b, h.last)
	h.mu.Unlock()

	if err := h.store.ClearPrinted(id); err != nil {
		return err
	}
	go h.print(t)
	return nil
}

func (h *Hub) TriggerRefresh() {
	select {
	case h.trigger <- struct{}{}:
	default:
	}
}

func (h *Hub) Run(ctx context.Context) {
	h.refresh(ctx)
	go h.runEventFeed(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-h.trigger:
			h.refresh(ctx)
		}
	}
}

func (h *Hub) runEventFeed(ctx context.Context) {
	const (
		initialBackoff = 2 * time.Second
		maxBackoff     = 30 * time.Second
		// Reset backoff if the stream stayed up at least this long — a stable
		// connection that drops briefly should not inherit the previous outage's
		// climbed-up retry interval.
		stableThreshold = 30 * time.Second
	)
	backoff := initialBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		log.Printf("event-feed: connecting")
		start := time.Now()
		err := h.dj.StreamEvents(ctx, []string{"judgements", "balloons", "state"}, func(_ []byte) error {
			h.TriggerRefresh()
			return nil
		})
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > stableThreshold {
			backoff = initialBackoff
		}
		log.Printf("event-feed: disconnected: %v (retry in %s)", err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// snapshot is everything one refresh cycle derives from DOMjudge, computed
// outside the hub lock so the lock window only covers diff + broadcast.
type snapshot struct {
	balloons         map[int64]*balloonsv1.Balloon
	byID             map[int64]domjudge.Balloon
	allProblemLabels []string
	deliveredByTeam  map[string][]string
	inDeliveryByTeam map[string][]string
	// Locations come from /teams. Empty string when DOMjudge doesn't have one.
	teamLocations map[string]string
	frozen        bool
}

func (h *Hub) refresh(ctx context.Context) {
	snap, ok := h.buildSnapshot(ctx)
	if !ok {
		return
	}
	h.applySnapshot(snap)
}

// djInputs bundles the four DOMjudge fetches one refresh cycle needs.
type djInputs struct {
	balloons []domjudge.Balloon
	teams    []domjudge.Team
	state    domjudge.State
	problems []domjudge.ContestProblem
}

// fetchDOMjudgeInputs pulls the four endpoints buildSnapshot needs. Returns
// ok=false on any error; the error is logged and the caller skips the refresh.
func (h *Hub) fetchDOMjudgeInputs(ctx context.Context) (djInputs, bool) {
	balloons, err := h.dj.ListBalloons(ctx)
	if err != nil {
		log.Printf("refresh: ListBalloons: %v", err)
		return djInputs{}, false
	}
	teams, err := h.dj.ListTeams(ctx)
	if err != nil {
		log.Printf("refresh: ListTeams: %v", err)
		return djInputs{}, false
	}
	state, err := h.dj.GetState(ctx)
	if err != nil {
		log.Printf("refresh: GetState: %v", err)
		return djInputs{}, false
	}
	problems, err := h.dj.ListProblems(ctx)
	if err != nil {
		log.Printf("refresh: ListProblems: %v", err)
		return djInputs{}, false
	}
	return djInputs{balloons: balloons, teams: teams, state: state, problems: problems}, true
}

// indexTeams turns a /teams response into the two lookup maps buildSnapshot
// needs: group memberships (for filters and first-solve) and locations (for
// the runner-facing assignment view).
func indexTeams(teams []domjudge.Team) (groups map[string][]string, locations map[string]string) {
	groups = make(map[string][]string, len(teams))
	locations = make(map[string]string, len(teams))
	for _, t := range teams {
		groups[t.ID] = t.GroupIDs
		locations[t.ID] = t.Location
	}
	return groups, locations
}

// filterVisible drops balloons whose team is in HIDE_GROUP_IDS.
func filterVisible(balloons []domjudge.Balloon, teamGroups map[string][]string, hide map[string]bool) []domjudge.Balloon {
	out := make([]domjudge.Balloon, 0, len(balloons))
	for _, b := range balloons {
		if anyInSet(teamGroups[b.TeamID], hide) {
			continue
		}
		out = append(out, b)
	}
	return out
}

// buildSnapshot fetches all four DOMjudge endpoints and derives the per-refresh
// state (visible balloons, first-solve set, per-team delivery sets, ticket
// strip). Returns ok=false on any fetch error — the error is logged and the
// caller leaves the existing hub state untouched.
func (h *Hub) buildSnapshot(ctx context.Context) (snapshot, bool) {
	in, ok := h.fetchDOMjudgeInputs(ctx)
	if !ok {
		return snapshot{}, false
	}
	teamGroups, teamLocations := indexTeams(in.teams)
	visible := filterVisible(in.balloons, teamGroups, h.hideGroups)
	firstSolve := firstSolveIDs(visible, teamGroups, h.noFirstSolveGroups)

	// Preserve assigned_runner_name across refreshes — buildSnapshot rebuilds
	// the proto from DOMjudge, which doesn't know about our local assignments.
	assignedNames, err := h.store.ActiveAssignedRunnerNames()
	if err != nil {
		log.Printf("refresh: ActiveAssignedRunnerNames: %v", err)
		assignedNames = map[int64]string{}
	}

	snap := snapshot{
		balloons:         make(map[int64]*balloonsv1.Balloon, len(visible)),
		byID:             make(map[int64]domjudge.Balloon, len(visible)),
		allProblemLabels: make([]string, len(in.problems)),
		deliveredByTeam:  make(map[string][]string, len(in.teams)),
		inDeliveryByTeam: make(map[string][]string, len(in.teams)),
		teamLocations:    teamLocations,
		frozen:           in.state.FrozenNow(),
	}
	for i, p := range in.problems {
		snap.allProblemLabels[i] = p.Label
	}
	for _, b := range visible {
		p := toProto(b, firstSolve)
		p.AssignedRunnerName = assignedNames[b.BalloonID]
		snap.balloons[b.BalloonID] = p
		snap.byID[b.BalloonID] = b
		if b.Done {
			snap.deliveredByTeam[b.TeamID] = append(snap.deliveredByTeam[b.TeamID], b.ContestProblem.Label)
		} else {
			snap.inDeliveryByTeam[b.TeamID] = append(snap.inDeliveryByTeam[b.TeamID], b.ContestProblem.Label)
		}
	}
	return snap, true
}

// applySnapshot diffs the snapshot against the current hub state under the
// lock, dispatches print goroutines for newly-added pending balloons, and
// broadcasts events to subscribers.
func (h *Hub) applySnapshot(snap snapshot) {
	h.mu.Lock()
	defer h.mu.Unlock()

	events := h.diffEvents(snap)
	h.state = snap.balloons
	h.last = snap
	if snap.frozen != h.frozen {
		h.frozen = snap.frozen
		events = append(events, &balloonsv1.StreamBalloonsResponse{
			Kind:   balloonsv1.StreamBalloonsResponse_KIND_FREEZE,
			Frozen: snap.frozen,
		})
	}
	if len(events) > 0 {
		h.broadcast(events)
	}
	// New balloons may have appeared in this refresh; try to assign them.
	// tryDispatchLocked is cheap when there's nothing to do.
	h.tryDispatchLocked()
}

// diffEvents compares snap.balloons against the existing hub state and returns
// one ADDED or UPDATED event per change. For each newly added pending balloon
// it also dispatches a print goroutine; the printer's own dedupe (state.Store)
// guarantees we don't reprint on a restart that observes the same balloon as
// "newly added".
func (h *Hub) diffEvents(snap snapshot) []*balloonsv1.StreamBalloonsResponse {
	var events []*balloonsv1.StreamBalloonsResponse
	for id, b := range snap.balloons {
		prev, existed := h.state[id]
		switch {
		case !existed:
			events = append(events, &balloonsv1.StreamBalloonsResponse{
				Kind:    balloonsv1.StreamBalloonsResponse_KIND_ADDED,
				Balloon: b,
			})
			if !b.Done {
				go h.print(h.ticketFor(b, snap))
			}
		case !proto.Equal(prev, b):
			events = append(events, &balloonsv1.StreamBalloonsResponse{
				Kind:    balloonsv1.StreamBalloonsResponse_KIND_UPDATED,
				Balloon: b,
			})
		}
	}
	return events
}

func (h *Hub) ticketFor(b *balloonsv1.Balloon, snap snapshot) printer.Ticket {
	dj := snap.byID[b.Id]
	return printer.Ticket{
		BalloonID:    b.Id,
		ProblemLabel: b.ProblemLabel,
		ProblemRGB:   b.ProblemRgb,
		TeamName:     b.TeamName,
		TeamID:       dj.TeamID,
		FirstSolve:   b.FirstSolve,
		AllProblems:  snap.allProblemLabels,
		Delivered:    snap.deliveredByTeam[dj.TeamID],
		InDelivery:   snap.inDeliveryByTeam[dj.TeamID],
		IssuedAt:     time.Now().In(h.loc),
		ScanURL:      h.scanBaseURL + "/scan?id=" + strconv.FormatInt(b.Id, 10),
	}
}

// broadcast fans events out to all subscribers. Callers must hold h.mu.
// A subscriber that can't keep up (any send blocks) is force-closed; it will
// reconnect and pick up a fresh snapshot from Subscribe().
func (h *Hub) broadcast(events []*balloonsv1.StreamBalloonsResponse) {
	for s := range h.subs {
		dropped := false
		for _, ev := range events {
			select {
			case s.ch <- ev:
			default:
				dropped = true
			}
		}
		if dropped {
			log.Printf("subscriber too slow, closing")
			delete(h.subs, s)
			close(s.ch)
		}
	}
}
