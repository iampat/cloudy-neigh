package grpcapi

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/iampat/cloudy-neigh/manifest"
	"github.com/iampat/cloudy-neigh/namespace"
	"github.com/iampat/cloudy-neigh/objectstore"
	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var ErrNilIngester = errors.New("grpcapi: nil ingesters")

type Ingester interface {
	Upsert(ctx context.Context, branch string, records []*cloudyneighpb.Record) error
	Delete(ctx context.Context, branch string, ids []string) error
	Fork(ctx context.Context, source, target string) error
}

type IngestServer struct {
	cloudyneighpb.UnimplementedIngestServiceServer
	ingesters map[string]Ingester
}

func NewIngestServer(ingesters map[string]Ingester) (*IngestServer, error) {
	if ingesters == nil {
		return nil, ErrNilIngester
	}
	return &IngestServer{ingesters: ingesters}, nil
}

func resolveNamespace(raw string) (string, error) {
	ns := namespace.DefaultNamespace
	if raw != "" {
		ns = raw
	}
	if err := namespace.ValidateName(ns); err != nil {
		return "", status.Errorf(codes.InvalidArgument, "grpcapi: invalid namespace: %v", err)
	}
	return ns, nil
}

func resolveBranch(ns, rawBranch string) (string, error) {
	branch := namespace.DefaultBranch
	if rawBranch != "" {
		branch = rawBranch
	}
	if err := namespace.ValidateName(branch); err != nil {
		return "", status.Errorf(codes.InvalidArgument, "grpcapi: invalid branch: %v", err)
	}
	return namespace.BranchRef(ns, branch), nil
}

func (s *IngestServer) Upsert(ctx context.Context, req *cloudyneighpb.UpsertRequest) (*cloudyneighpb.UpsertResponse, error) {
	start := time.Now()
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "grpcapi: nil request")
	}

	tenant := TenantFrom(ctx)
	ing, ok := s.ingesters[tenant]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "grpcapi: unknown tenant %q", tenant)
	}

	ns, err := resolveNamespace(req.Namespace)
	if err != nil {
		return nil, err
	}
	targetBranch, err := resolveBranch(ns, req.Branch)
	if err != nil {
		return nil, err
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

	if err := ing.Upsert(ctx, targetBranch, req.Records); err != nil {
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
		"branch", req.Branch,
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

	tenant := TenantFrom(ctx)
	ing, ok := s.ingesters[tenant]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "grpcapi: unknown tenant %q", tenant)
	}

	ns, err := resolveNamespace(req.Namespace)
	if err != nil {
		return nil, err
	}
	targetBranch, err := resolveBranch(ns, req.Branch)
	if err != nil {
		return nil, err
	}

	if len(req.Ids) == 0 {
		return &cloudyneighpb.DeleteResponse{}, nil
	}

	for i, id := range req.Ids {
		if id == "" {
			return nil, status.Errorf(codes.InvalidArgument, "grpcapi: id at index %d is empty", i)
		}
	}

	if err := ing.Delete(ctx, targetBranch, req.Ids); err != nil {
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

func (s *IngestServer) Fork(ctx context.Context, req *cloudyneighpb.ForkRequest) (*cloudyneighpb.ForkResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "grpcapi: nil request")
	}

	tenant := TenantFrom(ctx)
	ing, ok := s.ingesters[tenant]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "grpcapi: unknown tenant %q", tenant)
	}

	ns, err := resolveNamespace(req.Namespace)
	if err != nil {
		return nil, err
	}
	if req.TargetBranch == "" {
		return nil, status.Error(codes.InvalidArgument, "grpcapi: target branch cannot be empty")
	}
	srcBranch := namespace.DefaultBranch
	if req.SourceBranch != "" {
		srcBranch = req.SourceBranch
	}
	if err := namespace.ValidateName(req.TargetBranch); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "grpcapi: invalid target branch: %v", err)
	}
	if srcBranch == req.TargetBranch {
		return nil, status.Error(codes.InvalidArgument, "grpcapi: source and target branch cannot be identical")
	}

	src, err := resolveBranch(ns, srcBranch)
	if err != nil {
		return nil, err
	}
	target, err := resolveBranch(ns, req.TargetBranch)
	if err != nil {
		return nil, err
	}

	if err := ing.Fork(ctx, src, target); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, status.Error(codes.Canceled, err.Error())
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, status.Error(codes.DeadlineExceeded, err.Error())
		}
		if errors.Is(err, objectstore.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "grpcapi: source branch not found: %s", srcBranch)
		}
		if errors.Is(err, manifest.ErrBranchAlreadyExists) {
			return nil, status.Errorf(codes.AlreadyExists, "grpcapi: target branch already exists: %s", req.TargetBranch)
		}
		return nil, status.Errorf(codes.Internal, "grpcapi: fork: %v", err)
	}

	return &cloudyneighpb.ForkResponse{}, nil
}
