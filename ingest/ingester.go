package ingest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/iampat/cloudy-neigh/kvfs"
	"github.com/iampat/cloudy-neigh/logstream"
	"github.com/iampat/cloudy-neigh/objectstore"
	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	storagepb "github.com/iampat/cloudy-neigh/proto/storage/v1"
	"google.golang.org/protobuf/proto"
)

var (
	ErrNilStore = errors.New("ingest: nil store")
	ErrNilLog   = errors.New("ingest: nil log")
	ErrClosed   = errors.New("ingest: batcher closed")
)

type BatchConfig struct {
	MaxDocs     int
	MaxInterval time.Duration
}

func withBatchDefaults(c BatchConfig) BatchConfig {
	if c.MaxDocs <= 0 {
		c.MaxDocs = 1000
	}
	if c.MaxInterval <= 0 {
		c.MaxInterval = 10 * time.Millisecond
	}
	return c
}

type writeOp struct {
	records []*storagepb.WalRecord
	done    chan error
}

type BatchIngester struct {
	store   objectstore.Store
	log     *logstream.Log
	cfg     BatchConfig
	inCh    chan writeOp
	closeCh chan struct{}
	doneCh  chan struct{}
	once    sync.Once
}

func NewBatchIngester(store objectstore.Store, log *logstream.Log, cfg BatchConfig) (*BatchIngester, error) {
	if store == nil {
		return nil, ErrNilStore
	}
	if log == nil {
		return nil, ErrNilLog
	}
	b := &BatchIngester{
		store:   store,
		log:     log,
		cfg:     withBatchDefaults(cfg),
		inCh:    make(chan writeOp, 64),
		closeCh: make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
	go b.run()
	return b, nil
}

func (b *BatchIngester) Upsert(ctx context.Context, namespace string, records []*cloudyneighpb.Record) error {
	if len(records) == 0 {
		return nil
	}
	walRecs := make([]*storagepb.WalRecord, len(records))
	for i, rec := range records {
		payload, err := proto.Marshal(rec)
		if err != nil {
			return fmt.Errorf("marshal record: %w", err)
		}
		walRecs[i] = &storagepb.WalRecord{
			Record: &storagepb.WalRecord_Mutation{
				Mutation: &storagepb.DocumentMutation{
					Branch:  namespace,
					DocId:   rec.Id,
					Op:      storagepb.MutationOp_PUT,
					Payload: payload,
				},
			},
		}
	}
	return b.submit(ctx, walRecs)
}

func (b *BatchIngester) Delete(ctx context.Context, namespace string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	walRecs := make([]*storagepb.WalRecord, len(ids))
	for i, id := range ids {
		walRecs[i] = &storagepb.WalRecord{
			Record: &storagepb.WalRecord_Mutation{
				Mutation: &storagepb.DocumentMutation{
					Branch: namespace,
					DocId:  id,
					Op:     storagepb.MutationOp_DELETE,
				},
			},
		}
	}
	return b.submit(ctx, walRecs)
}

func (b *BatchIngester) CreateNamespace(ctx context.Context, namespace string) error {
	_, _, err := kvfs.CreateEmptyBranch(ctx, b.store, namespace)
	return err
}

func (b *BatchIngester) Fork(ctx context.Context, source, target string) error {
	if _, _, err := kvfs.ResolveBranch(ctx, b.store, source); err != nil {
		return err
	}
	if _, _, err := kvfs.ResolveBranch(ctx, b.store, target); err == nil {
		return kvfs.ErrBranchAlreadyExists
	} else if !errors.Is(err, objectstore.ErrNotFound) {
		return err
	}
	if _, _, err := kvfs.CreateBranch(ctx, b.store, target, source); err != nil {
		return err
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
	return b.submit(ctx, []*storagepb.WalRecord{eventRec})
}

func (b *BatchIngester) submit(ctx context.Context, recs []*storagepb.WalRecord) error {
	op := writeOp{
		records: recs,
		done:    make(chan error, 1),
	}

	select {
	case <-b.closeCh:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	case b.inCh <- op:
	}

	select {
	case err := <-op.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *BatchIngester) Close() error {
	b.once.Do(func() {
		close(b.closeCh)
		<-b.doneCh
	})
	return nil
}

func (b *BatchIngester) run() {
	defer close(b.doneCh)

	var currentBatch []writeOp
	var currentCount int
	var timer *time.Timer
	var timerCh <-chan time.Time

	flush := func() {
		if len(currentBatch) == 0 {
			return
		}
		err := b.flushBatch(currentBatch)
		for _, op := range currentBatch {
			op.done <- err
		}
		currentBatch = nil
		currentCount = 0
		if timer != nil {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timerCh = nil
		}
	}

	for {
		select {
		case op := <-b.inCh:
			currentBatch = append(currentBatch, op)
			currentCount += len(op.records)
			if currentCount >= b.cfg.MaxDocs {
				flush()
			} else if len(currentBatch) == 1 && b.cfg.MaxInterval > 0 {
				if timer == nil {
					timer = time.NewTimer(b.cfg.MaxInterval)
				} else {
					timer.Reset(b.cfg.MaxInterval)
				}
				timerCh = timer.C
			}
		case <-timerCh:
			flush()
		case <-b.closeCh:
			for {
				select {
				case op := <-b.inCh:
					currentBatch = append(currentBatch, op)
				default:
					flush()
					return
				}
			}
		}
	}
}

func (b *BatchIngester) flushBatch(ops []writeOp) error {
	var totalRecs int
	for _, op := range ops {
		totalRecs += len(op.records)
	}
	records := make([]logstream.Record, totalRecs)
	idx := 0
	for _, op := range ops {
		for _, rec := range op.records {
			recBytes, err := proto.Marshal(rec)
			if err != nil {
				return fmt.Errorf("marshal wal record: %w", err)
			}
			records[idx] = recBytes
			idx++
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := b.log.Append(ctx, records)
	return err
}
