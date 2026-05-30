package server

import (
	"regexp"

	balloonsv1 "github.com/GEHACK/balloons/gen/balloons/v1"
	"github.com/GEHACK/balloons/internal/domjudge"
)

var teamPrefixRE = regexp.MustCompile(`^\S+:\s+`)

// firstSolveIDs returns the set of balloon IDs that represent the first solve
// of their problem (earliest `time` per problem), skipping any team whose
// group_ids intersect `excludeGroups`. DOMjudge's /awards endpoint is empty
// during a live contest, so we derive this ourselves.
func firstSolveIDs(balloons []domjudge.Balloon, teamGroups map[string][]string, excludeGroups map[string]bool) map[int64]bool {
	earliest := map[string]string{} // problemID -> time
	firstID := map[string]int64{}   // problemID -> balloonID
	for _, b := range balloons {
		if anyInSet(teamGroups[b.TeamID], excludeGroups) {
			continue
		}
		pid := b.ContestProblem.ID
		if cur, ok := earliest[pid]; !ok || b.Time < cur {
			earliest[pid] = b.Time
			firstID[pid] = b.BalloonID
		}
	}
	out := make(map[int64]bool, len(firstID))
	for _, id := range firstID {
		out[id] = true
	}
	return out
}

func anyInSet(needles []string, set map[string]bool) bool {
	for _, n := range needles {
		if set[n] {
			return true
		}
	}
	return false
}

func toProto(b domjudge.Balloon, firstSolve map[int64]bool) *balloonsv1.Balloon {
	return &balloonsv1.Balloon{
		Id:           b.BalloonID,
		ProblemLabel: b.ContestProblem.Label,
		ProblemRgb:   b.ContestProblem.RGB,
		TeamName:     teamPrefixRE.ReplaceAllString(b.Team, ""),
		Done:         b.Done,
		FirstSolve:   firstSolve[b.BalloonID],
	}
}
