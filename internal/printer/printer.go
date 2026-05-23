// Package printer renders and delivers balloon tickets. The Printer interface
// abstracts over the delivery mechanism (IPP today, receipt printer later) so
// the hub can stay agnostic.
package printer

import (
	"context"
	"time"
)

type Ticket struct {
	BalloonID    int64
	ProblemLabel string
	ProblemRGB   string
	TeamName     string
	TeamID       string
	FirstSolve   bool

	// AllProblems is the full set of problem labels in the contest, in
	// contest order. Used to render the strip of balloons on the ticket.
	AllProblems []string
	// Delivered are problem labels this team has already had a balloon
	// marked done for (excluding the current ticket).
	Delivered []string
	// InDelivery are problem labels this team has outstanding (not done).
	// Includes the current ticket's problem.
	InDelivery []string

	// IssuedAt is the timestamp printed on the ticket.
	IssuedAt time.Time
}

type Printer interface {
	Print(ctx context.Context, t Ticket) error
}
