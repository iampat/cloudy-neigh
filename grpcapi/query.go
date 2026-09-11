package grpcapi

import (
	"context"

	"github.com/iampat/cloudy-neigh/namespace"
	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type QueryServer struct {
	cloudyneighpb.UnimplementedQueryServiceServer
}

func NewQueryServer() *QueryServer {
	return &QueryServer{}
}

func (s *QueryServer) Query(ctx context.Context, req *cloudyneighpb.QueryRequest) (*cloudyneighpb.QueryResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "grpcapi: nil request")
	}
	if err := namespace.ValidateNamespace(req.Namespace); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "grpcapi: invalid namespace: %v", err)
	}
	return &cloudyneighpb.QueryResponse{}, nil
}
