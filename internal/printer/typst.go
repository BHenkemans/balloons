package printer

import (
	"strconv"
	"strings"
	"time"
)

// typstInputs returns the `--input k=v` argument pairs shared by every Typst
// render in this package. Backend-specific args (page_width_mm, --format,
// --ppi) are appended at the callsite.
func typstInputs(t Ticket) []string {
	issued := t.IssuedAt
	if issued.IsZero() {
		issued = time.Now()
	}
	return []string{
		"--input", "datetime=" + issued.Format("02-01-2006 15:04"),
		"--input", "ticket_id=" + strconv.FormatInt(t.BalloonID, 10),
		"--input", "problem=" + t.ProblemLabel,
		"--input", "team_name=" + t.TeamName,
		"--input", "team_id=" + t.TeamID,
		"--input", "balloons=" + strings.Join(t.AllProblems, ","),
		"--input", "delivered=" + strings.Join(t.Delivered, ","),
		"--input", "in_delivery=" + strings.Join(t.InDelivery, ","),
		"--input", "first_solve=" + strconv.FormatBool(t.FirstSolve),
		"--input", "scan_url=" + t.ScanURL,
	}
}
