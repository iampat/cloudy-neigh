package grpcapi

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/iampat/cloudy-neigh/kvfs"
	"github.com/iampat/cloudy-neigh/namespace"
	"github.com/iampat/cloudy-neigh/objectstore"
	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var ErrNilIngester = errors.New("grpcapi: nil ingester")

type Ingester interface {
	Upsert(ctx context.Context, namespace string, records []*cloudyneighpb.Record) error
	Delete(ctx context.Context, namespace string, ids []string) error
	CreateNamespace(ctx context.Context, namespace string) error
	Fork(ctx context.Context, source, target string) error
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
	ns := req.Namespace
	if ns == "" {
		ns = namespace.DefaultNamespace
	}
	if err := namespace.ValidateNamespace(ns); err != nil {
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

	if err := s.ingester.Upsert(ctx, ns, req.Records); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, status.Error(codes.Canceled, err.Error())
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, status.Error(codes.DeadlineExceeded, err.Error())
		}
		return nil, status.Errorf(codes.Internal, "grpcapi: upsert: %v", err)
	}

	slog.Debug("upsert",
		"namespace", ns,
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
	ns := req.Namespace
	if ns == "" {
		ns = namespace.DefaultNamespace
	}
	if err := namespace.ValidateNamespace(ns); err != nil {
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

	if err := s.ingester.Delete(ctx, ns, req.Ids); err != nil {
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

func (s *IngestServer) CreateNamespace(ctx context.Context, req *cloudyneighpb.CreateNamespaceRequest) (*cloudyneighpb.CreateNamespaceResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "grpcapi: nil request")
	}
	ns := req.Namespace
	if ns == "" {
		ns = namespace.DefaultNamespace
	}
	if err := namespace.ValidateNamespace(ns); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "grpcapi: invalid namespace: %v", err)
	}

	if err := s.ingester.CreateNamespace(ctx, ns); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, status.Error(codes.Canceled, err.Error())
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, status.Error(codes.DeadlineExceeded, err.Error())
		}
		if errors.Is(err, kvfs.ErrBranchAlreadyExists) {
			return nil, status.Errorf(codes.AlreadyExists, "grpcapi: namespace already exists: %s", ns)
		}
		return nil, status.Errorf(codes.Internal, "grpcapi: create namespace: %v", err)
	}

	return &cloudyneighpb.CreateNamespaceResponse{}, nil
}

func (s *IngestServer) Fork(ctx context.Context, req *cloudyneighpb.ForkRequest) (*cloudyneighpb.ForkResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "grpcapi: nil request")
	}
	src := req.SourceNamespace
	if src == "" {
		src = namespace.DefaultNamespace
	}
	if err := namespace.ValidateNamespace(src); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "grpcapi: invalid source namespace: %v", err)
	}

	target := req.TargetNamespace
	if target == "" {
		return nil, status.Error(codes.InvalidArgument, "grpcapi: target namespace cannot be empty")
	}
	if err := namespace.ValidateNamespace(target); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "grpcapi: invalid target namespace: %v", err)
	}
	if src == target {
		return nil, status.Error(codes.InvalidArgument, "grpcapi: source and target namespace cannot be identical")
	}

	if err := s.ingester.Fork(ctx, src, target); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, status.Error(codes.Canceled, err.Error())
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, status.Error(codes.DeadlineExceeded, err.Error())
		}
		if errors.Is(err, objectstore.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "grpcapi: source namespace not found: %s", src)
		}
		if errors.Is(err, kvfs.ErrBranchAlreadyExists) {
			return nil, status.Errorf(codes.AlreadyExists, "grpcapi: target namespace already exists: %s", target)
		}
		return nil, status.Errorf(codes.Internal, "grpcapi: fork: %v", err)
	}

	return &cloudyneighpb.ForkResponse{}, nil
}
