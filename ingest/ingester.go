package ingest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/iampat/cloudy-neigh/logstream"
	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	storagepb "github.com/iampat/cloudy-neigh/proto/storage/v1"
	"google.golang.org/protobuf/proto"
)

var (
	ErrNilLog = errors.New("ingest: nil log")
	ErrClosed = errors.New("ingest: batcher closed")
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
	mutations []*storagepb.DocumentMutation
	done      chan error
}

type BatchIngester struct {
	log     *logstream.Log
	cfg     BatchConfig
	inCh    chan writeOp
	closeCh chan struct{}
	doneCh  chan struct{}
	once    sync.Once
}

func NewBatchIngester(log *logstream.Log, cfg BatchConfig) (*BatchIngester, error) {
	if log == nil {
		return nil, ErrNilLog
	}
	b := &BatchIngester{
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
	muts := make([]*storagepb.DocumentMutation, len(records))
	for i, rec := range records {
		payload, err := proto.Marshal(rec)
		if err != nil {
			return fmt.Errorf("marshal record: %w", err)
		}
		muts[i] = &storagepb.DocumentMutation{
			Branch:  namespace,
			DocId:   rec.Id,
			Op:      storagepb.MutationOp_PUT,
			Payload: payload,
		}
	}
	return b.submit(ctx, muts)
}

func (b *BatchIngester) Delete(ctx context.Context, namespace string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	muts := make([]*storagepb.DocumentMutation, len(ids))
	for i, id := range ids {
		muts[i] = &storagepb.DocumentMutation{
			Branch: namespace,
			DocId:  id,
			Op:     storagepb.MutationOp_DELETE,
		}
	}
	return b.submit(ctx, muts)
}

func (b *BatchIngester) submit(ctx context.Context, muts []*storagepb.DocumentMutation) error {
	op := writeOp{
		mutations: muts,
		done:      make(chan error, 1),
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
			currentCount += len(op.mutations)
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
	var totalMuts int
	for _, op := range ops {
		totalMuts += len(op.mutations)
	}
	records := make([]logstream.Record, totalMuts)
	idx := 0
	for _, op := range ops {
		for _, mut := range op.mutations {
			walRec := &storagepb.WalRecord{
				Record: &storagepb.WalRecord_Mutation{
					Mutation: mut,
				},
			}
			recBytes, err := proto.Marshal(walRec)
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
