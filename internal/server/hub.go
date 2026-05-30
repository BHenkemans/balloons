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

	balloonsv1 "github.com/GEHACK/balloons/gen/balloons/v1"
	"github.com/GEHACK/balloons/internal/domjudge"
	"github.com/GEHACK/balloons/internal/printer"
	"github.com/GEHACK/balloons/internal/state"
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

	trigger chan struct{}
}

type subscriber struct {
	ch chan *balloonsv1.StreamBalloonsResponse
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
	backoff := 2 * time.Second
	const maxBackoff = 30 * time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		log.Printf("event-feed: connecting")
		err := h.dj.StreamEvents(ctx, []string{"judgements", "balloons", "state"}, func(_ []byte) error {
			h.TriggerRefresh()
			return nil
		})
		if ctx.Err() != nil {
			return
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
	frozen           bool
}

func (h *Hub) refresh(ctx context.Context) {
	snap, ok := h.buildSnapshot(ctx)
	if !ok {
		return
	}
	h.applySnapshot(snap)
}

// buildSnapshot fetches all four DOMjudge endpoints and derives the per-refresh
// state (visible balloons, first-solve set, per-team delivery sets, ticket
// strip). Returns ok=false on any fetch error — the error is logged and the
// caller leaves the existing hub state untouched.
func (h *Hub) buildSnapshot(ctx context.Context) (snapshot, bool) {
	balloons, err := h.dj.ListBalloons(ctx)
	if err != nil {
		log.Printf("refresh: ListBalloons: %v", err)
		return snapshot{}, false
	}
	teams, err := h.dj.ListTeams(ctx)
	if err != nil {
		log.Printf("refresh: ListTeams: %v", err)
		return snapshot{}, false
	}
	state, err := h.dj.GetState(ctx)
	if err != nil {
		log.Printf("refresh: GetState: %v", err)
		return snapshot{}, false
	}
	problems, err := h.dj.ListProblems(ctx)
	if err != nil {
		log.Printf("refresh: ListProblems: %v", err)
		return snapshot{}, false
	}

	teamGroups := make(map[string][]string, len(teams))
	for _, t := range teams {
		teamGroups[t.ID] = t.GroupIDs
	}

	visible := make([]domjudge.Balloon, 0, len(balloons))
	for _, b := range balloons {
		if anyInSet(teamGroups[b.TeamID], h.hideGroups) {
			continue
		}
		visible = append(visible, b)
	}

	firstSolve := firstSolveIDs(visible, teamGroups, h.noFirstSolveGroups)
	snap := snapshot{
		balloons:         make(map[int64]*balloonsv1.Balloon, len(visible)),
		byID:             make(map[int64]domjudge.Balloon, len(visible)),
		allProblemLabels: make([]string, len(problems)),
		deliveredByTeam:  make(map[string][]string, len(teams)),
		inDeliveryByTeam: make(map[string][]string, len(teams)),
		frozen:           state.FrozenNow(),
	}
	for i, p := range problems {
		snap.allProblemLabels[i] = p.Label
	}
	for _, b := range visible {
		snap.balloons[b.BalloonID] = toProto(b, firstSolve)
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
	if len(events) == 0 {
		return
	}
	h.broadcast(events)
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
