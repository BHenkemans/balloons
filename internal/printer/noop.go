package printer

import (
	"context"
	"log"
)

// Noop logs the ticket and discards it. Default when no printer is configured.
type Noop struct{}

func (Noop) Print(_ context.Context, t Ticket) error {
	log.Printf("printer(noop): would print balloon %d (problem %s, team %q, first_solve=%v)",
		t.BalloonID, t.ProblemLabel, t.TeamName, t.FirstSolve)
	return nil
}
