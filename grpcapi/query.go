package grpcapi

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/iampat/cloudy-neigh/namespace"
	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	"github.com/iampat/cloudy-neigh/query"
	"github.com/iampat/cloudy-neigh/vector"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type QueryEngine interface {
	Query(ctx context.Context, req query.Request) ([]*cloudyneighpb.ScoredRecord, query.SearchStats, error)
}

type QueryServer struct {
	cloudyneighpb.UnimplementedQueryServiceServer
	engines map[string]QueryEngine
}

func NewQueryServer(engines map[string]QueryEngine) (*QueryServer, error) {
	if engines == nil {
		return nil, errors.New("grpcapi: nil engines")
	}
	return &QueryServer{
		engines: engines,
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

	tenant := TenantFrom(ctx)
	eng, ok := s.engines[tenant]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "grpcapi: unknown tenant %q", tenant)
	}

	branch, err := resolveBranch(req.Branch)
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
	hits, stats, err := eng.Query(ctx, query.Request{
		Namespace:    req.Namespace,
		Branch:       branch,
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
		if errors.Is(err, vector.ErrZeroVector) {
			return nil, status.Errorf(codes.InvalidArgument, "grpcapi: %v", err)
		}
		return nil, status.Errorf(codes.Internal, "grpcapi: search: %v", err)
	}

	totalDur := time.Since(valStart)
	slog.Debug("query",
		"namespace", req.Namespace,
		"branch", branch,
		"validate_dur", valDur,
		"search_dur", searchDur,
		"scan_dur", stats.ScanDuration,
		"materialize_dur", stats.MaterializeDuration,
		"hits", len(hits),
		"total_dur", totalDur,
	)
	if err := grpc.SetTrailer(ctx, metadata.Pairs("server-time-us", strconv.FormatInt(totalDur.Microseconds(), 10))); err != nil {
		return nil, status.Errorf(codes.Internal, "grpcapi: set trailer: %v", err)
	}

	return &cloudyneighpb.QueryResponse{Hits: hits}, nil
}
