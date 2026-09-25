package query

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iampat/cloudy-neigh/namespace"
	"github.com/iampat/cloudy-neigh/objectstore"
	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	"github.com/iampat/cloudy-neigh/vector"
)

type Request struct {
	Namespace    string
	Branch       string
	VectorColumn string
	Vector       []float32
	TopK         int
	Filter       *cloudyneighpb.EqualityFilter
}

type Engine struct {
	store        objectstore.Store
	syncInterval time.Duration
	kernels      vector.Kernels
	mu           sync.Mutex
	loaders      map[branchKey]*Loader
}

type branchKey struct {
	namespace string
	branch    string
}

func NewEngine(store objectstore.Store, syncInterval time.Duration, kernels vector.Kernels) (*Engine, error) {
	if store == nil {
		return nil, errors.New("query: nil store")
	}
	if syncInterval <= 0 {
		return nil, errors.New("query: non-positive sync interval")
	}
	return &Engine{
		store:        store,
		syncInterval: syncInterval,
		kernels:      kernels,
		loaders:      make(map[branchKey]*Loader),
	}, nil
}

func (e *Engine) SyncOnce(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	namespaces, err := namespace.ActiveNamespaces(ctx, e.store)
	if err != nil {
		return fmt.Errorf("list namespaces: %w", err)
	}
	for _, ns := range namespaces {
		scope := namespace.Scope{Namespace: ns}
		branches, err := scope.ListBranches(ctx, e.store)
		if err != nil {
			return fmt.Errorf("list branches for %s: %w", scope.Prefix(), err)
		}
		for _, branch := range branches {
			key := branchKey{namespace: ns, branch: branch}
			e.mu.Lock()
			loader, ok := e.loaders[key]
			if !ok {
				var table atomic.Pointer[Table]
				table.Store(NewTable(e.kernels))
				var err error
				loader, err = NewLoader(e.store, &table, scope, branch)
				if err == nil {
					e.loaders[key] = loader
				}
			}
			e.mu.Unlock()
			if loader == nil {
				continue
			}
			if _, err := loader.Sync(ctx); err != nil {
				slog.Error("sync branch failed", "namespace", ns, "branch", branch, "err", err)
			}
		}
	}
	return nil
}

func (e *Engine) Run(ctx context.Context) error {
	if err := e.SyncOnce(ctx); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		slog.Error("initial sync failed", "err", err)
	}

	ticker := time.NewTicker(e.syncInterval)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := e.SyncOnce(ctx); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				slog.Error("sync failed", "err", err)
			}
		}
	}
}

func (e *Engine) Query(ctx context.Context, req Request) ([]*cloudyneighpb.ScoredRecord, SearchStats, error) {
	if err := ctx.Err(); err != nil {
		return nil, SearchStats{}, err
	}
	col := req.VectorColumn
	if col == "" {
		col = "default"
	}

	e.mu.Lock()
	loader, ok := e.loaders[branchKey{namespace: req.Namespace, branch: req.Branch}]
	e.mu.Unlock()
	if !ok {
		return nil, SearchStats{}, nil
	}

	table := loader.table.Load()
	return table.Search(col, req.Vector, req.TopK, cloudyneighpb.DistanceMetric_DISTANCE_METRIC_COSINE, req.Filter)
}
