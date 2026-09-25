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

var ErrNilStore = errors.New("ingest: nil store")

type Ingester struct {
	store objectstore.Store

	mu         sync.Mutex
	logs       map[string]*logstream.Log
	registered map[string]bool
}

func NewIngester(store objectstore.Store) (*Ingester, error) {
	if store == nil {
		return nil, ErrNilStore
	}
	return &Ingester{
		store:      store,
		logs:       make(map[string]*logstream.Log),
		registered: make(map[string]bool),
	}, nil
}

func (ing *Ingester) getOrCreateLog(ctx context.Context, scope namespace.Scope) (*logstream.Log, error) {
	walPrefix := scope.WALPrefix()
	ing.mu.Lock()
	if l, ok := ing.logs[walPrefix]; ok {
		ing.mu.Unlock()
		return l, nil
	}

	cachePath := scope.Prefix()
	registered := ing.registered[cachePath]
	ing.mu.Unlock()

	if !registered {
		_, _, _ = namespace.CreateNamespace(ctx, ing.store, scope.Namespace, time.Now())
		ing.mu.Lock()
		ing.registered[cachePath] = true
		ing.mu.Unlock()
	}

	l, err := logstream.New(ing.store, walPrefix)
	if err != nil {
		return nil, fmt.Errorf("create logstream %s: %w", walPrefix, err)
	}

	ing.mu.Lock()
	if existing, ok := ing.logs[walPrefix]; ok {
		ing.mu.Unlock()
		return existing, nil
	}
	ing.logs[walPrefix] = l
	ing.mu.Unlock()
	return l, nil
}

func (ing *Ingester) Upsert(ctx context.Context, ns, branch string, records []*cloudyneighpb.Record) error {
	if len(records) == 0 {
		return nil
	}
	log, err := ing.getOrCreateLog(ctx, namespace.Scope{Namespace: ns})
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

func (ing *Ingester) Delete(ctx context.Context, ns, branch string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	log, err := ing.getOrCreateLog(ctx, namespace.Scope{Namespace: ns})
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

func (ing *Ingester) Fork(ctx context.Context, ns, source, target string) error {
	scope := namespace.Scope{Namespace: ns}
	targetKey := scope.ManifestKey(target)

	parentManifest, _, err := manifest.Read(ctx, ing.store, scope.ManifestKey(source))
	if err != nil {
		return err
	}
	if _, _, err := manifest.Read(ctx, ing.store, targetKey); err == nil {
		return manifest.ErrBranchAlreadyExists
	} else if !errors.Is(err, objectstore.ErrNotFound) {
		return err
	}
	if _, err := manifest.Write(ctx, ing.store, targetKey, parentManifest, ""); err != nil {
		if errors.Is(err, objectstore.ErrPreconditionFailed) {
			return manifest.ErrBranchAlreadyExists
		}
		return err
	}
	_ = scope.AddBranch(ctx, ing.store, target)

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
		log, err = ing.getOrCreateLog(ctx, scope)
		if err == nil {
			_, err = log.Append(ctx, []logstream.Record{recBytes})
		}
	}
	if err != nil {
		delCtx, delCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer delCancel()
		_ = ing.store.Delete(delCtx, targetKey)
		_ = scope.RemoveBranch(delCtx, ing.store, target)
		return err
	}
	return nil
}
