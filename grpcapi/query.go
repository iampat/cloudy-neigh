package grpcapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/iampat/cloudy-neigh/kvfs"
	"github.com/iampat/cloudy-neigh/namespace"
	"github.com/iampat/cloudy-neigh/objectstore"
	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	"github.com/iampat/cloudy-neigh/query"
	"github.com/iampat/cloudy-neigh/query/distance"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type branchState struct {
	table  *query.Table
	loader *query.Loader
}

type QueryServer struct {
	cloudyneighpb.UnimplementedQueryServiceServer
	store        objectstore.Store
	syncInterval time.Duration
	mu           sync.RWMutex
	branches     map[string]*branchState
}

func NewQueryServer(store objectstore.Store, syncInterval time.Duration) (*QueryServer, error) {
	if store == nil {
		return nil, errors.New("grpcapi: nil store")
	}
	if syncInterval <= 0 {
		return nil, errors.New("grpcapi: non-positive sync interval")
	}
	return &QueryServer{
		store:        store,
		syncInterval: syncInterval,
		branches:     make(map[string]*branchState),
	}, nil
}

const listLimit = 1000

func (s *QueryServer) getOrCreateBranch(branch string) (*branchState, error) {
	s.mu.RLock()
	b, ok := s.branches[branch]
	s.mu.RUnlock()
	if ok {
		return b, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.branches[branch]; ok {
		return b, nil
	}

	table := query.NewTable()
	loader, err := query.NewLoader(s.store, table)
	if err != nil {
		return nil, fmt.Errorf("create loader: %w", err)
	}
	b = &branchState{table: table, loader: loader}
	s.branches[branch] = b
	return b, nil
}

func (s *QueryServer) SyncOnce(ctx context.Context) error {
	var startAfter string
	for {
		branches, err := kvfs.ListBranches(ctx, s.store, startAfter, listLimit)
		if err != nil {
			return fmt.Errorf("list branches: %w", err)
		}
		if len(branches) == 0 {
			break
		}

		for _, branch := range branches {
			b, err := s.getOrCreateBranch(branch)
			if err != nil {
				slog.Error("create loader failed", "branch", branch, "err", err)
				continue
			}
			if _, err := b.loader.Sync(ctx, branch); err != nil {
				slog.Error("sync branch failed", "branch", branch, "err", err)
			}
		}

		if len(branches) < listLimit {
			break
		}
		startAfter = branches[len(branches)-1]
	}
	return nil
}

func (s *QueryServer) Run(ctx context.Context) error {
	if err := s.SyncOnce(ctx); err != nil {
		slog.Error("initial sync failed", "err", err)
	}

	ticker := time.NewTicker(s.syncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.SyncOnce(ctx); err != nil {
				slog.Error("sync failed", "err", err)
			}
		}
	}
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

	var sum float32
	for _, x := range req.Vector {
		sum += x * x
	}
	if sum == 0 {
		return nil, status.Error(codes.InvalidArgument, "grpcapi: zero query vector")
	}

	col := req.VectorColumn
	if col == "" {
		col = "default"
	}

	s.mu.RLock()
	b, ok := s.branches[req.Namespace]
	s.mu.RUnlock()
	if !ok {
		return &cloudyneighpb.QueryResponse{}, nil
	}

	hits, err := b.table.Search(col, req.Vector, int(req.TopK), query.MetricCosine, req.Filter)
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
