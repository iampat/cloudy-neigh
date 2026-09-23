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
)

type Request struct {
	Namespace    string
	VectorColumn string
	Vector       []float32
	TopK         int
	Filter       *cloudyneighpb.EqualityFilter
}

type Engine struct {
	store        objectstore.Store
	syncInterval time.Duration
	mu           sync.Mutex
	loaders      map[string]*Loader
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
		loaders:      make(map[string]*Loader),
	}, nil
}

func (e *Engine) SyncOnce(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	branches, err := namespace.ListBranches(ctx, e.store, "")
	if err != nil {
		return fmt.Errorf("list branches: %w", err)
	}

	for _, branch := range branches {
		if err := ctx.Err(); err != nil {
			return err
		}
		e.mu.Lock()
		loader, ok := e.loaders[branch]
		if !ok {
			var table atomic.Pointer[Table]
			table.Store(NewTable())
			var err error
			loader, err = NewLoader(e.store, &table)
			if err == nil {
				e.loaders[branch] = loader
			}
		}
		e.mu.Unlock()
		if loader == nil {
			continue
		}
		if _, err := loader.Sync(ctx, branch); err != nil {
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
	loader, ok := e.loaders[req.Namespace]
	e.mu.Unlock()
	if !ok {
		return nil, SearchStats{}, nil
	}

	table := loader.Table()
	return table.Search(col, req.Vector, req.TopK, cloudyneighpb.DistanceMetric_DISTANCE_METRIC_COSINE, req.Filter)
}
