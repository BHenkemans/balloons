package server

import (
	"testing"

	"github.com/BHenkemans/balloons/internal/state"
)

// TestNextAvailabilityStatus pins the runner state-machine rules for the
// "I'm available" / "Take a break" buttons. Each case is one transition: the
// current status, what the phone asked for, and either the resulting status
// or an error.
func TestNextAvailabilityStatus(t *testing.T) {
	cases := []struct {
		current   string
		available bool
		want      string
		wantErr   bool
	}{
		// Going available — allowed from idle, available (no-op), and after delivery.
		{state.RunnerIdle, true, state.RunnerAvailable, false},
		{state.RunnerAvailable, true, state.RunnerAvailable, false},
		{state.RunnerDeliveredPendingAck, true, state.RunnerAvailable, false},
		// Going available — blocked from busy/pending/offline/rejected.
		{state.RunnerBusy, true, "", true},
		{state.RunnerPendingAdmit, true, "", true},
		{state.RunnerOffline, true, "", true},
		{state.RunnerRejected, true, "", true},

		// Going idle ("take a break") — allowed from anything except busy.
		{state.RunnerAvailable, false, state.RunnerIdle, false},
		{state.RunnerIdle, false, state.RunnerIdle, false},
		{state.RunnerDeliveredPendingAck, false, state.RunnerIdle, false},
		{state.RunnerPendingAdmit, false, state.RunnerIdle, false},
		// Going idle while busy is rejected — the runner has a balloon in hand.
		{state.RunnerBusy, false, "", true},
	}
	for _, c := range cases {
		t.Run(c.current+"_"+boolStr(c.available), func(t *testing.T) {
			got, err := nextAvailabilityStatus(c.current, c.available)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got status=%q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestSortPendingsForDispatch pins the dispatch priority: first-solves jump
// the queue, then ties break on DOMjudge's fixed-width time string.
func TestSortPendingsForDispatch(t *testing.T) {
	p := []pendingBalloon{
		{id: 1, firstSolve: false, time: "0000010.000000000"},
		{id: 2, firstSolve: true, time: "0000020.000000000"},
		{id: 3, firstSolve: false, time: "0000005.000000000"},
		{id: 4, firstSolve: true, time: "0000001.000000000"},
	}
	sortPendingsForDispatch(p)
	wantOrder := []int64{4, 2, 3, 1}
	for i, w := range wantOrder {
		if p[i].id != w {
			t.Fatalf("position %d: got id=%d, want %d (full order: %+v)", i, p[i].id, w, p)
		}
	}
}

func boolStr(b bool) string {
	if b {
		return "available"
	}
	return "idle"
}
