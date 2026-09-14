package grpcapi

import (
	"context"
	"errors"

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
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "grpcapi: nil request")
	}
	if err := namespace.ValidateNamespace(req.Namespace); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "grpcapi: invalid namespace: %v", err)
	}
	if len(req.Vector) == 0 {
		return nil, status.Error(codes.InvalidArgument, "grpcapi: empty query vector")
	}
	if req.TopK == 0 {
		return nil, status.Error(codes.InvalidArgument, "grpcapi: top_k must be positive")
	}

	hits, err := s.engine.Query(ctx, query.QueryRequest{
		Namespace:    req.Namespace,
		VectorColumn: req.VectorColumn,
		Vector:       req.Vector,
		TopK:         int(req.TopK),
		Filter:       req.Filter,
	})
	if err != nil {
		if errors.Is(err, query.ErrDimensionMismatch) {
			return nil, status.Errorf(codes.InvalidArgument, "grpcapi: %v", err)
		}
		if errors.Is(err, distance.ErrZeroVector) {
			return nil, status.Errorf(codes.InvalidArgument, "grpcapi: %v", err)
		}
		return nil, status.Errorf(codes.Internal, "grpcapi: search: %v", err)
	}

	return &cloudyneighpb.QueryResponse{Hits: hits}, nil
}
