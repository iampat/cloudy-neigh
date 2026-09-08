package ingest_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/iampat/cloudy-neigh/ingest"
	"github.com/iampat/cloudy-neigh/kvfs"
	"github.com/iampat/cloudy-neigh/logstream"
	"github.com/iampat/cloudy-neigh/objectstore"
	storagepb "github.com/iampat/cloudy-neigh/proto/storage/v1"
	"github.com/iampat/cloudy-neigh/segment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func appendDoc(t *testing.T, ctx context.Context, log *logstream.Log, branch, docID string, payload []byte) uint64 {
	t.Helper()
	walRec := &storagepb.WalRecord{
		Record: &storagepb.WalRecord_Mutation{
			Mutation: &storagepb.DocumentMutation{
				Branch:  branch,
				DocId:   docID,
				Op:      storagepb.MutationOp_PUT,
				Payload: payload,
			},
		},
	}
	recBytes, err := proto.Marshal(walRec)
	require.NoError(t, err)

	seq, err := log.Append(ctx, []logstream.Record{recBytes})
	require.NoError(t, err)
	return seq
}

func waitForManifest(t *testing.T, ctx context.Context, store objectstore.Store, branch string, cond func(*storagepb.BranchManifest) bool) *storagepb.BranchManifest {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.After(5 * time.Second)

	for {
		select {
		case <-timeout:
			t.Fatalf("timed out waiting for branch %s manifest", branch)
			return nil
		case <-ticker.C:
			m, _, err := kvfs.ResolveBranch(ctx, store, branch)
			if err == nil && cond(m) {
				return m
			}
		}
	}
}

func readSegmentMutations(t *testing.T, ctx context.Context, store objectstore.Store, branch, segID string) []*storagepb.DocumentMutation {
	t.Helper()
	key := fmt.Sprintf("segments/%s/%s.recordio", branch, segID)
	rc, _, err := store.Get(ctx, key)
	require.NoError(t, err)
	defer rc.Close()

	r := segment.NewReader(rc)
	var mutations []*storagepb.DocumentMutation
	for {
		m, err := r.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			require.NoError(t, err)
		}
		mutations = append(mutations, m)
	}
	return mutations
}

func TestDocThresholdFlush(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	defer store.Close()

	log, err := logstream.New(store, "wal")
	require.NoError(t, err)

	for i := 1; i <= 3; i++ {
		appendDoc(t, ctx, log, "main", fmt.Sprintf("doc-%d", i), []byte(fmt.Sprintf("val-%d", i)))
	}

	flusher, err := ingest.NewFlusher(store, log, ingest.Config{
		DocThreshold:  3,
		TimeThreshold: 10 * time.Minute,
		PollInterval:  10 * time.Millisecond,
	})
	require.NoError(t, err)

	flusherErrCh := make(chan error, 1)
	go func() {
		flusherErrCh <- flusher.Run(ctx)
	}()

	manifest := waitForManifest(t, ctx, store, "main", func(m *storagepb.BranchManifest) bool {
		return len(m.Segments) == 1 && m.CheckpointSeq == 3
	})
	require.NotNil(t, manifest)
	assert.Equal(t, uint64(3), manifest.CheckpointSeq)
	require.Len(t, manifest.Segments, 1)
	assert.Equal(t, uint64(3), manifest.Segments[0].DocCount)

	mutations := readSegmentMutations(t, ctx, store, "main", manifest.Segments[0].SegmentId)
	require.Len(t, mutations, 3)
	for i, m := range mutations {
		assert.Equal(t, fmt.Sprintf("doc-%d", i+1), m.DocId)
		assert.Equal(t, []byte(fmt.Sprintf("val-%d", i+1)), m.Payload)
	}

	cancel()
	err = <-flusherErrCh
	require.NoError(t, err)
}

func TestTimeThresholdFlush(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	defer store.Close()

	log, err := logstream.New(store, "wal")
	require.NoError(t, err)

	appendDoc(t, ctx, log, "main", "doc-1", []byte("val-1"))

	flusher, err := ingest.NewFlusher(store, log, ingest.Config{
		DocThreshold:  1000,
		TimeThreshold: 50 * time.Millisecond,
		PollInterval:  10 * time.Millisecond,
	})
	require.NoError(t, err)

	flusherErrCh := make(chan error, 1)
	go func() {
		flusherErrCh <- flusher.Run(ctx)
	}()

	manifest := waitForManifest(t, ctx, store, "main", func(m *storagepb.BranchManifest) bool {
		return len(m.Segments) == 1 && m.CheckpointSeq == 1
	})
	require.NotNil(t, manifest)
	assert.Equal(t, uint64(1), manifest.CheckpointSeq)
	require.Len(t, manifest.Segments, 1)
	assert.Equal(t, uint64(1), manifest.Segments[0].DocCount)

	mutations := readSegmentMutations(t, ctx, store, "main", manifest.Segments[0].SegmentId)
	require.Len(t, mutations, 1)
	assert.Equal(t, "doc-1", mutations[0].DocId)

	cancel()
	err = <-flusherErrCh
	require.NoError(t, err)
}

func TestRestartResume(t *testing.T) {
	ctx := context.Background()

	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	defer store.Close()

	log, err := logstream.New(store, "wal")
	require.NoError(t, err)

	for i := 1; i <= 5; i++ {
		appendDoc(t, ctx, log, "main", fmt.Sprintf("doc-%d", i), []byte(fmt.Sprintf("val-%d", i)))
	}

	flusher1Ctx, cancel1 := context.WithCancel(ctx)
	flusher1, err := ingest.NewFlusher(store, log, ingest.Config{
		DocThreshold:  5,
		TimeThreshold: 10 * time.Minute,
		PollInterval:  10 * time.Millisecond,
	})
	require.NoError(t, err)

	flusher1ErrCh := make(chan error, 1)
	go func() {
		flusher1ErrCh <- flusher1.Run(flusher1Ctx)
	}()

	manifest1 := waitForManifest(t, ctx, store, "main", func(m *storagepb.BranchManifest) bool {
		return len(m.Segments) == 1 && m.CheckpointSeq == 5
	})
	require.NotNil(t, manifest1)

	cancel1()
	require.NoError(t, <-flusher1ErrCh)

	for i := 6; i <= 7; i++ {
		appendDoc(t, ctx, log, "main", fmt.Sprintf("doc-%d", i), []byte(fmt.Sprintf("val-%d", i)))
	}

	flusher2Ctx, cancel2 := context.WithCancel(ctx)
	flusher2, err := ingest.NewFlusher(store, log, ingest.Config{
		DocThreshold:  2,
		TimeThreshold: 10 * time.Minute,
		PollInterval:  10 * time.Millisecond,
	})
	require.NoError(t, err)

	flusher2ErrCh := make(chan error, 1)
	go func() {
		flusher2ErrCh <- flusher2.Run(flusher2Ctx)
	}()

	manifest2 := waitForManifest(t, ctx, store, "main", func(m *storagepb.BranchManifest) bool {
		return len(m.Segments) == 2 && m.CheckpointSeq == 7
	})
	require.NotNil(t, manifest2)
	assert.Equal(t, uint64(7), manifest2.CheckpointSeq)
	require.Len(t, manifest2.Segments, 2)
	assert.Equal(t, uint64(5), manifest2.Segments[0].DocCount)
	assert.Equal(t, uint64(2), manifest2.Segments[1].DocCount)

	muts1 := readSegmentMutations(t, ctx, store, "main", manifest2.Segments[0].SegmentId)
	require.Len(t, muts1, 5)
	assert.Equal(t, "doc-1", muts1[0].DocId)
	assert.Equal(t, "doc-5", muts1[4].DocId)

	muts2 := readSegmentMutations(t, ctx, store, "main", manifest2.Segments[1].SegmentId)
	require.Len(t, muts2, 2)
	assert.Equal(t, "doc-6", muts2[0].DocId)
	assert.Equal(t, "doc-7", muts2[1].DocId)

	cancel2()
	require.NoError(t, <-flusher2ErrCh)
}

type casConflictStore struct {
	objectstore.Store
	mu         sync.Mutex
	conflictOn string
	triggered  bool
}

func (s *casConflictStore) Put(ctx context.Context, key string, r io.Reader, cond objectstore.Condition) (string, error) {
	s.mu.Lock()
	if key == s.conflictOn && !s.triggered {
		s.triggered = true
		s.mu.Unlock()

		concurrentManifest := &storagepb.BranchManifest{
			SchemaVersion: 1,
			CheckpointSeq: 0,
			Segments: []*storagepb.SegmentRef{
				{
					SegmentId: "concurrent-seg-1",
					DocCount:  10,
				},
			},
		}
		data, err := proto.Marshal(concurrentManifest)
		if err != nil {
			return "", err
		}
		if _, err := s.Store.Put(ctx, key, bytes.NewReader(data), objectstore.Condition{Absent: true}); err != nil {
			return "", err
		}
		return "", objectstore.ErrPreconditionFailed
	}
	s.mu.Unlock()
	return s.Store.Put(ctx, key, r, cond)
}

func TestCASRetryPreconditionFailed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	memStore, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	defer memStore.Close()

	store := &casConflictStore{
		Store:      memStore,
		conflictOn: "refs/heads/main",
	}

	log, err := logstream.New(store, "wal")
	require.NoError(t, err)

	appendDoc(t, ctx, log, "main", "doc-1", []byte("val-1"))

	flusher, err := ingest.NewFlusher(store, log, ingest.Config{
		DocThreshold:  1,
		TimeThreshold: 10 * time.Minute,
		PollInterval:  10 * time.Millisecond,
	})
	require.NoError(t, err)

	flusherErrCh := make(chan error, 1)
	go func() {
		flusherErrCh <- flusher.Run(ctx)
	}()

	manifest := waitForManifest(t, ctx, store, "main", func(m *storagepb.BranchManifest) bool {
		return len(m.Segments) == 2 && m.CheckpointSeq == 1
	})
	require.NotNil(t, manifest)
	assert.Equal(t, uint64(1), manifest.CheckpointSeq)
	require.Len(t, manifest.Segments, 2)
	assert.Equal(t, "concurrent-seg-1", manifest.Segments[0].SegmentId)
	assert.Equal(t, uint64(1), manifest.Segments[1].DocCount)

	cancel()
	err = <-flusherErrCh
	require.NoError(t, err)
}

func TestGracefulShutdownFlush(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	defer store.Close()

	log, err := logstream.New(store, "wal")
	require.NoError(t, err)

	appendDoc(t, ctx, log, "main", "doc-1", []byte("val-1"))
	appendDoc(t, ctx, log, "main", "doc-2", []byte("val-2"))

	idleCh := make(chan struct{}, 1)
	flusher, err := ingest.NewFlusher(store, log, ingest.Config{
		DocThreshold:  1000,
		TimeThreshold: 10 * time.Minute,
		PollInterval:  10 * time.Millisecond,
		OnIdle: func() {
			select {
			case idleCh <- struct{}{}:
			default:
			}
		},
	})
	require.NoError(t, err)

	flusherErrCh := make(chan error, 1)
	go func() {
		flusherErrCh <- flusher.Run(ctx)
	}()

	<-idleCh
	cancel()

	err = <-flusherErrCh
	require.NoError(t, err)

	drainCtx := context.Background()
	manifest, _, err := kvfs.ResolveBranch(drainCtx, store, "main")
	require.NoError(t, err)
	assert.Equal(t, uint64(2), manifest.CheckpointSeq)
	require.Len(t, manifest.Segments, 1)
	assert.Equal(t, uint64(2), manifest.Segments[0].DocCount)

	mutations := readSegmentMutations(t, drainCtx, store, "main", manifest.Segments[0].SegmentId)
	require.Len(t, mutations, 2)
	assert.Equal(t, "doc-1", mutations[0].DocId)
	assert.Equal(t, "doc-2", mutations[1].DocId)
}

func TestBatchSequenceBoundaryFlush(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	defer store.Close()

	log, err := logstream.New(store, "wal")
	require.NoError(t, err)

	var records []logstream.Record
	for i := 1; i <= 5; i++ {
		walRec := &storagepb.WalRecord{
			Record: &storagepb.WalRecord_Mutation{
				Mutation: &storagepb.DocumentMutation{
					Branch:  "main",
					DocId:   fmt.Sprintf("doc-%d", i),
					Op:      storagepb.MutationOp_PUT,
					Payload: []byte(fmt.Sprintf("val-%d", i)),
				},
			},
		}
		data, err := proto.Marshal(walRec)
		require.NoError(t, err)
		records = append(records, data)
	}
	seq, err := log.Append(ctx, records)
	require.NoError(t, err)
	require.Equal(t, uint64(1), seq)

	flusher, err := ingest.NewFlusher(store, log, ingest.Config{
		DocThreshold:  3,
		TimeThreshold: 10 * time.Minute,
		PollInterval:  10 * time.Millisecond,
	})
	require.NoError(t, err)

	flusherErrCh := make(chan error, 1)
	go func() {
		flusherErrCh <- flusher.Run(ctx)
	}()

	manifest := waitForManifest(t, ctx, store, "main", func(m *storagepb.BranchManifest) bool {
		return len(m.Segments) == 1 && m.CheckpointSeq == 1
	})
	require.NotNil(t, manifest)
	assert.Equal(t, uint64(1), manifest.CheckpointSeq)
	require.Len(t, manifest.Segments, 1)
	assert.Equal(t, uint64(5), manifest.Segments[0].DocCount)

	mutations := readSegmentMutations(t, ctx, store, "main", manifest.Segments[0].SegmentId)
	require.Len(t, mutations, 5)

	cancel()
	require.NoError(t, <-flusherErrCh)
}
