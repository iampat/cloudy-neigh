package grpcapi

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/iampat/cloudy-neigh/namespace"
	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var ErrNilIngester = errors.New("grpcapi: nil ingester")

type Ingester interface {
	Upsert(ctx context.Context, namespace string, records []*cloudyneighpb.Record) error
	Delete(ctx context.Context, namespace string, ids []string) error
}

type IngestServer struct {
	cloudyneighpb.UnimplementedIngestServiceServer
	ingester Ingester
}

func NewIngestServer(ingester Ingester) (*IngestServer, error) {
	if ingester == nil {
		return nil, ErrNilIngester
	}
	return &IngestServer{ingester: ingester}, nil
}

func (s *IngestServer) Upsert(ctx context.Context, req *cloudyneighpb.UpsertRequest) (*cloudyneighpb.UpsertResponse, error) {
	start := time.Now()
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "grpcapi: nil request")
	}
	if err := namespace.ValidateNamespace(req.Namespace); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "grpcapi: invalid namespace: %v", err)
	}
	if len(req.Records) == 0 {
		return &cloudyneighpb.UpsertResponse{}, nil
	}

	for i, rec := range req.Records {
		if rec == nil {
			return nil, status.Errorf(codes.InvalidArgument, "grpcapi: record at index %d is nil", i)
		}
		if rec.Id == "" {
			return nil, status.Errorf(codes.InvalidArgument, "grpcapi: record at index %d has empty id", i)
		}
	}

	if err := s.ingester.Upsert(ctx, req.Namespace, req.Records); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, status.Error(codes.Canceled, err.Error())
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, status.Error(codes.DeadlineExceeded, err.Error())
		}
		return nil, status.Errorf(codes.Internal, "grpcapi: upsert: %v", err)
	}

	slog.Debug("upsert",
		"namespace", req.Namespace,
		"records", len(req.Records),
		"total_dur", time.Since(start),
	)

	return &cloudyneighpb.UpsertResponse{
		UpsertedCount: uint32(len(req.Records)),
	}, nil
}

func (s *IngestServer) Delete(ctx context.Context, req *cloudyneighpb.DeleteRequest) (*cloudyneighpb.DeleteResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "grpcapi: nil request")
	}
	if err := namespace.ValidateNamespace(req.Namespace); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "grpcapi: invalid namespace: %v", err)
	}
	if len(req.Ids) == 0 {
		return &cloudyneighpb.DeleteResponse{}, nil
	}

	for i, id := range req.Ids {
		if id == "" {
			return nil, status.Errorf(codes.InvalidArgument, "grpcapi: id at index %d is empty", i)
		}
	}

	if err := s.ingester.Delete(ctx, req.Namespace, req.Ids); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, status.Error(codes.Canceled, err.Error())
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, status.Error(codes.DeadlineExceeded, err.Error())
		}
		return nil, status.Errorf(codes.Internal, "grpcapi: delete: %v", err)
	}

	return &cloudyneighpb.DeleteResponse{
		DeletedCount: uint32(len(req.Ids)),
	}, nil
}
