package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"connectrpc.com/connect"

	balloonsv1 "github.com/BHenkemans/balloons/gen/balloons/v1"
	"github.com/BHenkemans/balloons/internal/state"
)

// runnerFromCookie reads the runner_session cookie from the connectRPC
// request header, looks up the runner in the store, and bumps last_seen_at.
// Returns a Connect error suitable for direct return on auth failure. When
// the cookie refers to an unknown or terminated (offline/rejected) session,
// the returned error carries a Set-Cookie that expires the runner_session
// cookie so the browser drops it after a single failed request — otherwise
// the phone is stuck looping reconnects with a dead token until cookies are
// cleared by hand.
func (s *Server) runnerFromCookie(ctx context.Context, headers http.Header) (state.Runner, error) {
	r := &http.Request{Header: headers}
	c, err := r.Cookie(RunnerCookieName)
	if err != nil || c == nil || c.Value == "" {
		return state.Runner{}, connect.NewError(connect.CodeUnauthenticated, errors.New("no runner session"))
	}
	rec, err := s.Store.GetRunnerByToken(c.Value)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return state.Runner{}, s.errClearingCookie(ctx, connect.CodeUnauthenticated, errors.New("unknown runner session"))
		}
		return state.Runner{}, connect.NewError(connect.CodeInternal, err)
	}
	if rec.Status == state.RunnerOffline || rec.Status == state.RunnerRejected {
		return state.Runner{}, s.errClearingCookie(ctx, connect.CodePermissionDenied, fmt.Errorf("runner session %s", rec.Status))
	}
	if err := s.Store.TouchRunner(rec.ID); err != nil {
		// Non-fatal — log via the standard error return path of the caller.
		_ = err
	}
	return rec, nil
}

// errClearingCookie builds a Connect error whose response metadata includes a
// Set-Cookie header that expires runner_session, so the next request from the
// same browser arrives cookie-less.
func (s *Server) errClearingCookie(ctx context.Context, code connect.Code, cause error) *connect.Error {
	e := connect.NewError(code, cause)
	c := &http.Cookie{
		Name:     RunnerCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cookieSecure(ctx),
		MaxAge:   -1,
	}
	e.Meta().Add("Set-Cookie", c.String())
	return e
}

func (s *Server) setRunnerCookie(ctx context.Context, headers http.Header, token string) {
	c := &http.Cookie{
		Name:     RunnerCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cookieSecure(ctx),
		MaxAge:   24 * 60 * 60, // 24 hours
	}
	headers.Add("Set-Cookie", c.String())
}

// --- Runner-facing ----------------------------------------------------------

func (s *Server) RequestRunnerSession(ctx context.Context, req *connect.Request[balloonsv1.RequestRunnerSessionRequest]) (*connect.Response[balloonsv1.RequestRunnerSessionResponse], error) {
	name := strings.TrimSpace(req.Msg.Name)
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name is required"))
	}
	if len(name) > 64 {
		name = name[:64]
	}
	r, err := s.Hub.CreateRunner(name)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := connect.NewResponse(&balloonsv1.RequestRunnerSessionResponse{
		Runner: s.Hub.RunnerProto(r),
	})
	s.setRunnerCookie(ctx, resp.Header(), r.SessionToken)
	return resp, nil
}

func (s *Server) WatchRunnerState(ctx context.Context, req *connect.Request[balloonsv1.WatchRunnerStateRequest], stream *connect.ServerStream[balloonsv1.WatchRunnerStateResponse]) error {
	r, err := s.runnerFromCookie(ctx, req.Header())
	if err != nil {
		return err
	}
	initial, ch, cancel, err := s.Hub.SubscribeRunner(r.ID)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	defer cancel()

	if err := stream.Send(initial); err != nil {
		return err
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

func (s *Server) SetRunnerAvailable(ctx context.Context, req *connect.Request[balloonsv1.SetRunnerAvailableRequest]) (*connect.Response[balloonsv1.SetRunnerAvailableResponse], error) {
	r, err := s.runnerFromCookie(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	updated, err := s.Hub.SetRunnerAvailable(r.ID, req.Msg.Available)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&balloonsv1.SetRunnerAvailableResponse{Runner: s.Hub.RunnerProto(updated)}), nil
}

func (s *Server) CompleteAssignment(ctx context.Context, req *connect.Request[balloonsv1.CompleteAssignmentRequest]) (*connect.Response[balloonsv1.CompleteAssignmentResponse], error) {
	r, err := s.runnerFromCookie(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	updated, err := s.Hub.CompleteAssignment(ctx, r.ID, req.Msg.AssignmentId)
	if err != nil {
		switch {
		case errors.Is(err, ErrAssignmentNotForCaller):
			return nil, connect.NewError(connect.CodePermissionDenied, err)
		case errors.Is(err, state.ErrNotFound):
			return nil, connect.NewError(connect.CodeNotFound, err)
		default:
			return nil, connect.NewError(connect.CodeUnavailable, err)
		}
	}
	return connect.NewResponse(&balloonsv1.CompleteAssignmentResponse{Runner: s.Hub.RunnerProto(updated)}), nil
}

func (s *Server) ReadyForNext(ctx context.Context, req *connect.Request[balloonsv1.ReadyForNextRequest]) (*connect.Response[balloonsv1.ReadyForNextResponse], error) {
	r, err := s.runnerFromCookie(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	updated, err := s.Hub.ReadyForNext(r.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&balloonsv1.ReadyForNextResponse{Runner: s.Hub.RunnerProto(updated)}), nil
}

// --- Admin-facing -----------------------------------------------------------
// No auth in MVP — same trust model as the main page's MarkDone today.

func (s *Server) ListRunners(_ context.Context, _ *connect.Request[balloonsv1.ListRunnersRequest]) (*connect.Response[balloonsv1.ListRunnersResponse], error) {
	runners, err := s.Store.ListRunners()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := make([]*balloonsv1.Runner, 0, len(runners))
	for _, r := range runners {
		out = append(out, s.Hub.RunnerProto(r))
	}
	return connect.NewResponse(&balloonsv1.ListRunnersResponse{Runners: out}), nil
}

func (s *Server) StreamRunners(ctx context.Context, _ *connect.Request[balloonsv1.StreamRunnersRequest], stream *connect.ServerStream[balloonsv1.StreamRunnersResponse]) error {
	initial, ch, cancel, err := s.Hub.SubscribeRoster()
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	defer cancel()

	if err := stream.Send(initial); err != nil {
		return err
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

func (s *Server) AdmitRunner(_ context.Context, req *connect.Request[balloonsv1.AdmitRunnerRequest]) (*connect.Response[balloonsv1.AdmitRunnerResponse], error) {
	r, err := s.Hub.AdmitRunner(req.Msg.RunnerId)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&balloonsv1.AdmitRunnerResponse{Runner: s.Hub.RunnerProto(r)}), nil
}

func (s *Server) RejectRunner(_ context.Context, req *connect.Request[balloonsv1.RejectRunnerRequest]) (*connect.Response[balloonsv1.RejectRunnerResponse], error) {
	if err := s.Hub.RejectRunner(req.Msg.RunnerId); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&balloonsv1.RejectRunnerResponse{}), nil
}

func (s *Server) ForceReturnAssignment(_ context.Context, req *connect.Request[balloonsv1.ForceReturnAssignmentRequest]) (*connect.Response[balloonsv1.ForceReturnAssignmentResponse], error) {
	if err := s.Hub.ForceReturnAssignment(req.Msg.AssignmentId); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&balloonsv1.ForceReturnAssignmentResponse{}), nil
}

func (s *Server) KickRunner(_ context.Context, req *connect.Request[balloonsv1.KickRunnerRequest]) (*connect.Response[balloonsv1.KickRunnerResponse], error) {
	if err := s.Hub.KickRunner(req.Msg.RunnerId); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&balloonsv1.KickRunnerResponse{}), nil
}

