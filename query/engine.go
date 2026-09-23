package query

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iampat/cloudy-neigh/kvfs"
	"github.com/iampat/cloudy-neigh/objectstore"
	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
)

type Request struct {
	Namespace    string
	VectorColumn string
	Vector       []float32
	TopK         int
	Filter       *cloudyneighpb.EqualityFilter
}

type branchState struct {
	table  atomic.Pointer[Table]
	loader *Loader
}

type Engine struct {
	store        objectstore.Store
	syncInterval time.Duration
	mu           sync.Mutex
	branches     map[string]*branchState
}

func NewEngine(store objectstore.Store, syncInterval time.Duration) (*Engine, error) {
	if store == nil {
		return nil, errors.New("query: nil store")
	}
	if syncInterval <= 0 {
		return nil, errors.New("query: non-positive sync interval")
	}
	return &Engine{
		store:        store,
		syncInterval: syncInterval,
		branches:     make(map[string]*branchState),
	}, nil
}

func (e *Engine) getOrCreateBranch(branch string) (*branchState, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if b, ok := e.branches[branch]; ok {
		return b, nil
	}

	b := &branchState{}
	b.table.Store(NewTable())
	loader, err := NewLoader(e.store, &b.table)
	if err != nil {
		return nil, fmt.Errorf("create loader: %w", err)
	}
	b.loader = loader
	e.branches[branch] = b
	return b, nil
}

func (e *Engine) SyncOnce(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	branches, err := kvfs.ListBranches(ctx, e.store)
	if err != nil {
		return fmt.Errorf("list branches: %w", err)
	}

	for _, branch := range branches {
		if err := ctx.Err(); err != nil {
			return err
		}
		b, err := e.getOrCreateBranch(branch)
		if err != nil {
			slog.Error("create loader failed", "branch", branch, "err", err)
			continue
		}
		if _, err := b.loader.Sync(ctx, branch); err != nil {
			slog.Error("sync branch failed", "branch", branch, "err", err)
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
	defer ticker.Stop()

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
	b, ok := e.branches[req.Namespace]
	e.mu.Unlock()
	if !ok {
		return nil, SearchStats{}, nil
	}

	table := b.table.Load()
	return table.Search(col, req.Vector, req.TopK, cloudyneighpb.DistanceMetric_DISTANCE_METRIC_COSINE, req.Filter)
}
