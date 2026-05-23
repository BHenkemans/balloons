package server

import (
	"testing"

	"github.com/BHenkemans/balloons/internal/domjudge"
)

// firstSolveIDs derives the per-problem first-solve set from /balloons,
// because /awards is empty during a live contest. These tests pin down the
// three behaviors we care about: earliest-time wins, excluded teams are
// skipped (so the *next* eligible team gets the flag), and ties are broken
// deterministically by the input's `time` string compare.
func TestFirstSolveIDs(t *testing.T) {
	// DOMjudge emits `time` as a fixed-width string (zero-padded seconds +
	// nanoseconds), so plain `<` lexical compare matches numeric order.
	// All cases below use that format — non-fixed-width values would expose
	// a real bug if anything ever fed them in.

	t.Run("earliest time per problem wins", func(t *testing.T) {
		balloons := []domjudge.Balloon{
			{BalloonID: 1, TeamID: "t1", Time: "0000010.000000000", ContestProblem: domjudge.ContestProblem{ID: "A"}},
			{BalloonID: 2, TeamID: "t2", Time: "0000005.000000000", ContestProblem: domjudge.ContestProblem{ID: "A"}},
			{BalloonID: 3, TeamID: "t3", Time: "0000020.000000000", ContestProblem: domjudge.ContestProblem{ID: "B"}},
		}
		got := firstSolveIDs(balloons, nil, nil)
		want := map[int64]bool{2: true, 3: true}
		if !equalIntBoolMap(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("excluded teams cannot win first solve", func(t *testing.T) {
		balloons := []domjudge.Balloon{
			{BalloonID: 1, TeamID: "excluded", Time: "0000001.000000000", ContestProblem: domjudge.ContestProblem{ID: "A"}},
			{BalloonID: 2, TeamID: "t2", Time: "0000005.000000000", ContestProblem: domjudge.ContestProblem{ID: "A"}},
		}
		groups := map[string][]string{"excluded": {"company"}, "t2": {"student"}}
		excl := map[string]bool{"company": true}
		got := firstSolveIDs(balloons, groups, excl)
		want := map[int64]bool{2: true}
		if !equalIntBoolMap(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("zero-padded seconds order correctly across decades", func(t *testing.T) {
		// Without the zero-pad, "100.0" would lex-compare *less* than "99.0"
		// and the wrong balloon would win. Pin the format requirement.
		balloons := []domjudge.Balloon{
			{BalloonID: 1, TeamID: "t1", Time: "0000100.000000000", ContestProblem: domjudge.ContestProblem{ID: "A"}},
			{BalloonID: 2, TeamID: "t2", Time: "0000099.000000000", ContestProblem: domjudge.ContestProblem{ID: "A"}},
		}
		got := firstSolveIDs(balloons, nil, nil)
		if !got[2] || got[1] {
			t.Fatalf("expected balloon 2 to win, got %v", got)
		}
	})
}

func TestAnyInSet(t *testing.T) {
	set := map[string]bool{"a": true, "b": true}
	cases := []struct {
		name    string
		needles []string
		want    bool
	}{
		{"hit", []string{"x", "a"}, true},
		{"miss", []string{"x", "y"}, false},
		{"empty needles", nil, false},
		{"empty set", []string{"a"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := set
			if tc.name == "empty set" {
				s = map[string]bool{}
			}
			if got := anyInSet(tc.needles, s); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestToProtoStripsTeamLabelPrefix(t *testing.T) {
	// DOMjudge prepends `{label}: ` to the team display name. toProto strips
	// it so the UI shows just the team name.
	b := domjudge.Balloon{
		BalloonID:      42,
		Team:           "T17: Eindhoven University of Technology",
		Done:           false,
		ContestProblem: domjudge.ContestProblem{Label: "C", RGB: "#cc2222"},
	}
	out := toProto(b, map[int64]bool{42: true})
	if out.TeamName != "Eindhoven University of Technology" {
		t.Errorf("TeamName: got %q, want stripped form", out.TeamName)
	}
	if out.ProblemLabel != "C" || out.ProblemRgb != "#cc2222" {
		t.Errorf("problem fields not copied: %+v", out)
	}
	if !out.FirstSolve {
		t.Errorf("FirstSolve not propagated")
	}
	if out.Id != 42 {
		t.Errorf("Id: got %d, want 42", out.Id)
	}
}

func TestToProtoLeavesUnprefixedNameAlone(t *testing.T) {
	b := domjudge.Balloon{BalloonID: 1, Team: "PlainName"}
	if got := toProto(b, nil).TeamName; got != "PlainName" {
		t.Fatalf("got %q, want PlainName", got)
	}
}

func equalIntBoolMap(a, b map[int64]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
