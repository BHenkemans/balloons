package server

import (
	"context"
	"log"
	"regexp"

	"connectrpc.com/connect"

	balloonsv1 "github.com/BHenkemans/balloons/gen/balloons/v1"
	"github.com/BHenkemans/balloons/gen/balloons/v1/balloonsv1connect"
	"github.com/BHenkemans/balloons/internal/domjudge"
	"github.com/BHenkemans/balloons/internal/state"
)

var teamPrefixRE = regexp.MustCompile(`^\S+:\s+`)

type Server struct {
	balloonsv1connect.UnimplementedBalloonServiceHandler
	Hub   *Hub
	DJ    *domjudge.Client
	Store *state.Store
}

func (s *Server) ListBalloons(_ context.Context, _ *connect.Request[balloonsv1.ListBalloonsRequest]) (*connect.Response[balloonsv1.ListBalloonsResponse], error) {
	return connect.NewResponse(&balloonsv1.ListBalloonsResponse{Balloons: s.Hub.Snapshot()}), nil
}

func (s *Server) MarkDone(ctx context.Context, req *connect.Request[balloonsv1.MarkDoneRequest]) (*connect.Response[balloonsv1.MarkDoneResponse], error) {
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
