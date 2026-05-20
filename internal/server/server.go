package server

import (
	"context"
	"regexp"
	"strings"

	"connectrpc.com/connect"

	balloonsv1 "github.com/BHenkemans/balloons/gen/balloons/v1"
	"github.com/BHenkemans/balloons/gen/balloons/v1/balloonsv1connect"
	"github.com/BHenkemans/balloons/internal/domjudge"
)

var teamPrefixRE = regexp.MustCompile(`^\S+:\s+`)

type Server struct {
	balloonsv1connect.UnimplementedBalloonServiceHandler
	Hub *Hub
	DJ  *domjudge.Client
}

func (s *Server) ListBalloons(_ context.Context, _ *connect.Request[balloonsv1.ListBalloonsRequest]) (*connect.Response[balloonsv1.ListBalloonsResponse], error) {
	return connect.NewResponse(&balloonsv1.ListBalloonsResponse{Balloons: s.Hub.Snapshot()}), nil
}

func (s *Server) MarkDone(ctx context.Context, req *connect.Request[balloonsv1.MarkDoneRequest]) (*connect.Response[balloonsv1.MarkDoneResponse], error) {
	if err := s.DJ.MarkDone(ctx, req.Msg.BalloonId); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	s.Hub.TriggerRefresh()
	return connect.NewResponse(&balloonsv1.MarkDoneResponse{}), nil
}

func (s *Server) StreamBalloons(ctx context.Context, _ *connect.Request[balloonsv1.StreamBalloonsRequest], stream *connect.ServerStream[balloonsv1.StreamBalloonsResponse]) error {
	snap, ch, unsub := s.Hub.Subscribe()
	defer unsub()

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

func buildFirstSolveSet(awards []domjudge.Award) map[string]bool {
	out := map[string]bool{}
	const prefix = "first-to-solve-"
	for _, a := range awards {
		if !strings.HasPrefix(a.ID, prefix) {
			continue
		}
		problemID := strings.TrimPrefix(a.ID, prefix)
		for _, tid := range a.TeamIDs {
			out[problemID+"|"+tid] = true
		}
	}
	return out
}

func toProto(b domjudge.Balloon, firstSolve map[string]bool) *balloonsv1.Balloon {
	return &balloonsv1.Balloon{
		Id:           b.BalloonID,
		ProblemLabel: b.ContestProblem.Label,
		ProblemRgb:   b.ContestProblem.RGB,
		TeamName:     teamPrefixRE.ReplaceAllString(b.Team, ""),
		Done:         b.Done,
		FirstSolve:   firstSolve[b.ContestProblem.ID+"|"+b.TeamID],
	}
}
