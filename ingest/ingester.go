package ingest

import (
	"context"
	"errors"
	"fmt"
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
	log   *logstream.Log
}

func NewIngester(store objectstore.Store, log *logstream.Log) (*Ingester, error) {
	if store == nil {
		return nil, ErrNilStore
	}
	if log == nil {
		return nil, ErrNilLog
	}
	return &Ingester{
		store: store,
		log:   log,
	}, nil
}

func (in *Ingester) Upsert(ctx context.Context, namespace string, records []*cloudyneighpb.Record) error {
	if len(records) == 0 {
		return nil
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
					Branch:  namespace,
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
	_, err := in.log.Append(ctx, walRecords)
	return err
}

func (in *Ingester) Delete(ctx context.Context, namespace string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	walRecords := make([]logstream.Record, len(ids))
	for i, id := range ids {
		walRec := &storagepb.WalRecord{
			Record: &storagepb.WalRecord_Mutation{
				Mutation: &storagepb.DocumentMutation{
					Branch: namespace,
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
	_, err := in.log.Append(ctx, walRecords)
	return err
}

func (in *Ingester) Fork(ctx context.Context, source, target string) error {
	parentManifest, _, err := manifest.Read(ctx, in.store, source)
	if err != nil {
		return err
	}
	if _, _, err := manifest.Read(ctx, in.store, target); err == nil {
		return manifest.ErrBranchAlreadyExists
	} else if !errors.Is(err, objectstore.ErrNotFound) {
		return err
	}
	if _, err := manifest.Write(ctx, in.store, target, parentManifest, ""); err != nil {
		if errors.Is(err, objectstore.ErrPreconditionFailed) {
			return manifest.ErrBranchAlreadyExists
		}
		return err
	}
	_ = namespace.AddBranch(ctx, in.store, "", target)

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
		_, err = in.log.Append(ctx, []logstream.Record{recBytes})
	}
	if err != nil {
		delCtx, delCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer delCancel()
		_ = in.store.Delete(delCtx, target)
		_ = namespace.RemoveBranch(delCtx, in.store, "", target)
		return err
	}
	return nil
}
