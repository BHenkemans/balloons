package domjudge

import "testing"

// FrozenNow drives the freeze banner in the UI. The contract: the scoreboard
// is frozen when `frozen` is set and `thawed` is not — both nil means the
// contest hasn't reached the freeze yet, both set means it's already thawed.
func TestStateFrozenNow(t *testing.T) {
	s := func(frozen, thawed *string) State { return State{Frozen: frozen, Thawed: thawed} }
	str := func(v string) *string { return &v }
	ts := str("2026-05-23T16:00:00+02:00")

	cases := []struct {
		name string
		in   State
		want bool
	}{
		{"neither set (pre-freeze)", s(nil, nil), false},
		{"frozen only (active freeze)", s(ts, nil), true},
		{"frozen and thawed (post-thaw)", s(ts, ts), false},
		{"thawed without frozen (impossible, defensive)", s(nil, ts), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.FrozenNow(); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
