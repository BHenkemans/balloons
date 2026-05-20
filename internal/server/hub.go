package server

import (
	"context"
	"log"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	balloonsv1 "github.com/BHenkemans/balloons/gen/balloons/v1"
	"github.com/BHenkemans/balloons/internal/domjudge"
)

type Hub struct {
	dj *domjudge.Client

	mu    sync.Mutex
	state map[int64]*balloonsv1.Balloon
	subs  map[*subscriber]struct{}

	trigger chan struct{}
}

type subscriber struct {
	ch chan *balloonsv1.StreamBalloonsResponse
}

func NewHub(dj *domjudge.Client) *Hub {
	return &Hub{
		dj:      dj,
		state:   map[int64]*balloonsv1.Balloon{},
		subs:    map[*subscriber]struct{}{},
		trigger: make(chan struct{}, 1),
	}
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

func (h *Hub) Subscribe() (snapshot []*balloonsv1.Balloon, ch <-chan *balloonsv1.StreamBalloonsResponse, cancel func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := &subscriber{ch: make(chan *balloonsv1.StreamBalloonsResponse, 256)}
	h.subs[s] = struct{}{}
	snap := make([]*balloonsv1.Balloon, 0, len(h.state))
	for _, b := range h.state {
		snap = append(snap, b)
	}
	return snap, s.ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subs[s]; ok {
			delete(h.subs, s)
			close(s.ch)
		}
	}
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
		err := h.dj.StreamEvents(ctx, []string{"judgements", "awards", "balloons"}, func(_ []byte) error {
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

func (h *Hub) refresh(ctx context.Context) {
	balloons, err := h.dj.ListBalloons(ctx)
	if err != nil {
		log.Printf("refresh: ListBalloons: %v", err)
		return
	}
	awards, err := h.dj.ListAwards(ctx)
	if err != nil {
		log.Printf("refresh: ListAwards: %v", err)
		return
	}

	firstSolve := buildFirstSolveSet(awards)
	newState := make(map[int64]*balloonsv1.Balloon, len(balloons))
	for _, b := range balloons {
		newState[b.BalloonID] = toProto(b, firstSolve)
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	var events []*balloonsv1.StreamBalloonsResponse
	for id, b := range newState {
		prev, existed := h.state[id]
		if !existed {
			events = append(events, &balloonsv1.StreamBalloonsResponse{
				Kind:    balloonsv1.StreamBalloonsResponse_KIND_ADDED,
				Balloon: b,
			})
		} else if !proto.Equal(prev, b) {
			events = append(events, &balloonsv1.StreamBalloonsResponse{
				Kind:    balloonsv1.StreamBalloonsResponse_KIND_UPDATED,
				Balloon: b,
			})
		}
	}
	h.state = newState

	if len(events) == 0 {
		return
	}

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
