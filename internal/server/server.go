package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"regexp"
	"strings"

	"connectrpc.com/connect"

	balloonsv1 "github.com/BHenkemans/balloons/gen/balloons/v1"
	"github.com/BHenkemans/balloons/gen/balloons/v1/balloonsv1connect"
	"github.com/BHenkemans/balloons/internal/domjudge"
	"github.com/BHenkemans/balloons/internal/state"
)

var teamPrefixRE = regexp.MustCompile(`^\S+:\s+`)

// RunnerCookieName is the cookie holding the runner's opaque session token.
const RunnerCookieName = "runner_session"

// CookieSecureMode controls whether the runner_session cookie is issued with
// the Secure attribute.
//
// - CookieSecureAuto: derive from the request — Secure when the inbound
//   request was TLS (r.TLS != nil) or carried X-Forwarded-Proto: https.
//   This is the default so localhost HTTP dev works without setting env
//   vars (Safari silently drops Secure cookies over HTTP, which otherwise
//   leaves the phone unable to authenticate).
// - CookieSecureAlways: always Secure. Use behind a TLS-terminating proxy
//   that doesn't forward the scheme.
// - CookieSecureNever: never Secure.
type CookieSecureMode int

const (
	CookieSecureAuto CookieSecureMode = iota
	CookieSecureAlways
	CookieSecureNever
)

// ParseCookieSecureMode reads the COOKIE_SECURE env value. Empty / "auto"
// means auto-detect; "true"/"1" means always; "false"/"0" means never.
func ParseCookieSecureMode(v string) CookieSecureMode {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "auto":
		return CookieSecureAuto
	case "true", "1", "yes", "on":
		return CookieSecureAlways
	case "false", "0", "no", "off":
		return CookieSecureNever
	default:
		return CookieSecureAuto
	}
}

type Server struct {
	balloonsv1connect.UnimplementedBalloonServiceHandler
	Hub        *Hub
	DJ         *domjudge.Client
	Store      *state.Store
	CookieMode CookieSecureMode
}

// requestSchemeKey is a context key carrying "https" or "http" for the
// current request. WithRequestScheme installs it via HTTP middleware so
// connectRPC handlers (which only see context.Context, not *http.Request)
// can still tell whether the inbound request was TLS-terminated.
type requestSchemeKey struct{}

// WithRequestScheme returns an HTTP middleware that records the request
// scheme on the request context. Wrap the connectRPC handler with this so
// setRunnerCookie can decide the Secure cookie attribute per-request.
func WithRequestScheme(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		} else if v := r.Header.Get("X-Forwarded-Proto"); v != "" {
			// Pick the first value when multiple proxies stack them.
			if i := strings.IndexByte(v, ','); i >= 0 {
				v = v[:i]
			}
			if strings.EqualFold(strings.TrimSpace(v), "https") {
				scheme = "https"
			}
		}
		ctx := context.WithValue(r.Context(), requestSchemeKey{}, scheme)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func requestIsHTTPS(ctx context.Context) bool {
	v, _ := ctx.Value(requestSchemeKey{}).(string)
	return v == "https"
}

// cookieSecure resolves the per-request Secure attribute based on the
// configured mode and the request scheme.
func (s *Server) cookieSecure(ctx context.Context) bool {
	switch s.CookieMode {
	case CookieSecureAlways:
		return true
	case CookieSecureNever:
		return false
	default:
		return requestIsHTTPS(ctx)
	}
}

func (s *Server) ListBalloons(_ context.Context, _ *connect.Request[balloonsv1.ListBalloonsRequest]) (*connect.Response[balloonsv1.ListBalloonsResponse], error) {
	return connect.NewResponse(&balloonsv1.ListBalloonsResponse{Balloons: s.Hub.Snapshot()}), nil
}

func (s *Server) MarkDone(ctx context.Context, req *connect.Request[balloonsv1.MarkDoneRequest]) (*connect.Response[balloonsv1.MarkDoneResponse], error) {
	// Backup-scanner override: if a runner is currently assigned to this
	// balloon, cancel the assignment and bump the runner back to available
	// before we mark the balloon Done in DOMjudge. Runs first so the
	// runner's phone sees the "admin took it" note before the upcoming
	// refresh removes the balloon from view.
	s.Hub.handleBackupScannerOverride(req.Msg.BalloonId)

	if err := s.DJ.MarkDone(ctx, req.Msg.BalloonId); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	if err := s.Store.RecordDelivered(req.Msg.BalloonId); err != nil {
		// Don't fail the RPC — DOMjudge is already updated; just log.
		log.Printf("markDone: record delivered: %v", err)
	}
	s.Hub.TriggerRefresh()
	return connect.NewResponse(&balloonsv1.MarkDoneResponse{}), nil
}

func (s *Server) Reprint(_ context.Context, req *connect.Request[balloonsv1.ReprintRequest]) (*connect.Response[balloonsv1.ReprintResponse], error) {
	if err := s.Hub.Reprint(req.Msg.BalloonId); err != nil {
		if errors.Is(err, ErrBalloonNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&balloonsv1.ReprintResponse{}), nil
}

func (s *Server) StreamBalloons(ctx context.Context, _ *connect.Request[balloonsv1.StreamBalloonsRequest], stream *connect.ServerStream[balloonsv1.StreamBalloonsResponse]) error {
	snap, frozen, ch, unsub := s.Hub.Subscribe()
	defer unsub()

	if err := stream.Send(&balloonsv1.StreamBalloonsResponse{
		Kind:   balloonsv1.StreamBalloonsResponse_KIND_FREEZE,
		Frozen: frozen,
	}); err != nil {
		return err
	}

	for _, b := range snap {
		if err := stream.Send(&balloonsv1.StreamBalloonsResponse{
			Kind:    balloonsv1.StreamBalloonsResponse_KIND_ADDED,
			Balloon: b,
		}); err != nil {
			return err
		}
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			if err := stream.Send(ev); err != nil {
				return err
			}
		}
	}
}

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
