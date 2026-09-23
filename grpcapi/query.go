package grpcapi

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/iampat/cloudy-neigh/namespace"
	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	"github.com/iampat/cloudy-neigh/query"
	"github.com/iampat/cloudy-neigh/query/distance"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type QueryServer struct {
	cloudyneighpb.UnimplementedQueryServiceServer
	engine *query.Engine
}

func NewQueryServer(engine *query.Engine) (*QueryServer, error) {
	if engine == nil {
		return nil, errors.New("grpcapi: nil engine")
	}
	return &QueryServer{
		engine: engine,
	}, nil
}

func (s *QueryServer) Query(ctx context.Context, req *cloudyneighpb.QueryRequest) (*cloudyneighpb.QueryResponse, error) {
	valStart := time.Now()
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "grpcapi: nil request")
	}
	if err := namespace.ValidateName(req.Namespace); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "grpcapi: invalid namespace: %v", err)
	}

	targetBranch, err := resolveBranch(req.Namespace, req.Branch)
	if err != nil {
		return nil, err
	}

	if len(req.Vector) == 0 {
		return nil, status.Error(codes.InvalidArgument, "grpcapi: empty query vector")
	}
	if req.TopK == 0 {
		return nil, status.Error(codes.InvalidArgument, "grpcapi: top_k must be positive")
	}

	valDur := time.Since(valStart)
	searchStart := time.Now()
	hits, stats, err := s.engine.Query(ctx, query.Request{
		Namespace:    targetBranch,
		VectorColumn: req.VectorColumn,
		Vector:       req.Vector,
		TopK:         int(req.TopK),
		Filter:       req.Filter,
	})
	searchDur := time.Since(searchStart)
	if err != nil {
		if errors.Is(err, query.ErrDimensionMismatch) {
			return nil, status.Errorf(codes.InvalidArgument, "grpcapi: %v", err)
		}
		if errors.Is(err, distance.ErrZeroVector) {
			return nil, status.Errorf(codes.InvalidArgument, "grpcapi: %v", err)
		}
		return nil, status.Errorf(codes.Internal, "grpcapi: search: %v", err)
	}

	slog.Debug("query",
		"namespace", req.Namespace,
		"validate_dur", valDur,
		"search_dur", searchDur,
		"scan_dur", stats.ScanDuration,
		"materialize_dur", stats.MaterializeDuration,
		"hits", len(hits),
		"total_dur", time.Since(valStart),
	)

	return &cloudyneighpb.QueryResponse{Hits: hits}, nil
}
