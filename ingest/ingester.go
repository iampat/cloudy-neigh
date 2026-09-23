package ingest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/iampat/cloudy-neigh/logstream"
	"github.com/iampat/cloudy-neigh/manifest"
	"github.com/iampat/cloudy-neigh/namespace"
	"github.com/iampat/cloudy-neigh/objectstore"
	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	storagepb "github.com/iampat/cloudy-neigh/proto/storage/v1"
	"google.golang.org/protobuf/proto"
)

var (
	ErrNilStore = errors.New("ingest: nil store")
	ErrNilLog   = errors.New("ingest: nil log")
)

type Ingester struct {
	store objectstore.Store

	mu   sync.Mutex
	logs map[string]*logstream.Log
}

func NewIngester(store objectstore.Store) (*Ingester, error) {
	if store == nil {
		return nil, ErrNilStore
	}
	return &Ingester{
		store: store,
		logs:  make(map[string]*logstream.Log),
	}, nil
}

func (ing *Ingester) Store() objectstore.Store {
	return ing.store
}

func (ing *Ingester) getOrCreateLog(walPrefix string) (*logstream.Log, error) {
	ing.mu.Lock()
	defer ing.mu.Unlock()

	if l, ok := ing.logs[walPrefix]; ok {
		return l, nil
	}
	l, err := logstream.New(ing.store, walPrefix)
	if err != nil {
		return nil, fmt.Errorf("create logstream %s: %w", walPrefix, err)
	}
	ing.logs[walPrefix] = l
	return l, nil
}

func (ing *Ingester) Log(branchRef string) (*logstream.Log, error) {
	scope, _ := namespace.ScopeFromRef(branchRef)
	return ing.getOrCreateLog(scope.WALPrefix())
}

func (ing *Ingester) Upsert(ctx context.Context, branch string, records []*cloudyneighpb.Record) error {
	if len(records) == 0 {
		return nil
	}
	scope, _ := namespace.ScopeFromRef(branch)
	log, err := ing.getOrCreateLog(scope.WALPrefix())
	if err != nil {
		return err
	}

	walRecords := make([]logstream.Record, len(records))
	for i, rec := range records {
		payload, err := proto.Marshal(rec)
		if err != nil {
			return fmt.Errorf("marshal record: %w", err)
		}
		walRec := &storagepb.WalRecord{
			Record: &storagepb.WalRecord_Mutation{
				Mutation: &storagepb.DocumentMutation{
					Branch:  branch,
					DocId:   rec.Id,
					Op:      storagepb.MutationOp_PUT,
					Payload: payload,
				},
			},
		}
		recBytes, err := proto.Marshal(walRec)
		if err != nil {
			return fmt.Errorf("marshal wal record: %w", err)
		}
		walRecords[i] = recBytes
	}
	_, err = log.Append(ctx, walRecords)
	return err
}

func (ing *Ingester) Delete(ctx context.Context, branch string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	scope, _ := namespace.ScopeFromRef(branch)
	log, err := ing.getOrCreateLog(scope.WALPrefix())
	if err != nil {
		return err
	}

	walRecords := make([]logstream.Record, len(ids))
	for i, id := range ids {
		walRec := &storagepb.WalRecord{
			Record: &storagepb.WalRecord_Mutation{
				Mutation: &storagepb.DocumentMutation{
					Branch: branch,
					DocId:  id,
					Op:     storagepb.MutationOp_DELETE,
				},
			},
		}
		recBytes, err := proto.Marshal(walRec)
		if err != nil {
			return fmt.Errorf("marshal wal record: %w", err)
		}
		walRecords[i] = recBytes
	}
	_, err = log.Append(ctx, walRecords)
	return err
}

func (ing *Ingester) Fork(ctx context.Context, source, target string) error {
	targetScope, _ := namespace.ScopeFromRef(target)

	parentManifest, _, err := manifest.Read(ctx, ing.store, source)
	if err != nil {
		return err
	}
	if _, _, err := manifest.Read(ctx, ing.store, target); err == nil {
		return manifest.ErrBranchAlreadyExists
	} else if !errors.Is(err, objectstore.ErrNotFound) {
		return err
	}
	if _, err := manifest.Write(ctx, ing.store, target, parentManifest, ""); err != nil {
		if errors.Is(err, objectstore.ErrPreconditionFailed) {
			return manifest.ErrBranchAlreadyExists
		}
		return err
	}
	_ = targetScope.AddBranch(ctx, ing.store, target)
	if targetScope.BranchesPath() != namespace.BranchesFile {
		_ = namespace.AddBranch(ctx, ing.store, "", target)
	}

	eventRec := &storagepb.WalRecord{
		Record: &storagepb.WalRecord_BranchEvent{
			BranchEvent: &storagepb.BranchLifecycleEvent{
				Type:         storagepb.BranchLifecycleEvent_FORK,
				Branch:       target,
				ParentBranch: source,
			},
		},
	}
	recBytes, err := proto.Marshal(eventRec)
	if err == nil {
		var log *logstream.Log
		log, err = ing.getOrCreateLog(targetScope.WALPrefix())
		if err == nil {
			_, err = log.Append(ctx, []logstream.Record{recBytes})
		}
	}
	if err != nil {
		delCtx, delCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer delCancel()
		_ = ing.store.Delete(delCtx, target)
		_ = targetScope.RemoveBranch(delCtx, ing.store, target)
		if targetScope.BranchesPath() != namespace.BranchesFile {
			_ = namespace.RemoveBranch(delCtx, ing.store, "", target)
		}
		return err
	}
	return nil
}
