package ingest_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/iampat/cloudy-neigh/ingest"
	"github.com/iampat/cloudy-neigh/logstream"
	"github.com/iampat/cloudy-neigh/manifest"
	"github.com/iampat/cloudy-neigh/namespace"
	"github.com/iampat/cloudy-neigh/objectstore"
	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	storagepb "github.com/iampat/cloudy-neigh/proto/storage/v1"
	"github.com/iampat/cloudy-neigh/segment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func defaultKey(branch string) string {
	return namespace.Scope{Namespace: namespace.DefaultNamespace}.ManifestKey(branch)
}

const defaultWAL = "ns/default/wal"

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
	timeout := time.After(5 * time.Second)

	for {
		select {
		case <-ctx.Done():
			t.Fatalf("context canceled waiting for branch %s manifest: %v", branch, ctx.Err())
			return nil
		case <-timeout:
			t.Fatalf("timed out waiting for branch %s manifest", branch)
			return nil
		case <-ticker.C:
			m, _, err := manifest.Read(ctx, store, branch)
			if err == nil && cond(m) {
				return m
			}
		}
	}
}

func readSegmentMutations(t *testing.T, ctx context.Context, store objectstore.Store, branch, segID string) []*storagepb.DocumentMutation {
	t.Helper()
	scope := namespace.Scope{Namespace: namespace.DefaultNamespace}
	key := scope.SegmentKey(segID)
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

func TestBatchFlush(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	defer store.Close()

	log, err := logstream.New(store, defaultWAL)
	require.NoError(t, err)

	br := "main"
	var records []logstream.Record
	for i := 1; i <= 3; i++ {
		walRec := &storagepb.WalRecord{
			Record: &storagepb.WalRecord_Mutation{
				Mutation: &storagepb.DocumentMutation{
					Branch:  br,
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

	flusher, err := ingest.NewFlusher(store, ingest.Config{
		PollInterval: 10 * time.Millisecond,
	})
	require.NoError(t, err)

	flusherErrCh := make(chan error, 1)
	go func() {
		flusherErrCh <- flusher.Run(ctx)
	}()

	manifest := waitForManifest(t, ctx, store, defaultKey(br), func(m *storagepb.BranchManifest) bool {
		return len(m.Segments) == 1 && m.CheckpointSeq == 1
	})
	require.NotNil(t, manifest)
	assert.Equal(t, uint64(1), manifest.CheckpointSeq)
	require.Len(t, manifest.Segments, 1)
	assert.Equal(t, uint64(3), manifest.Segments[0].DocCount)

	mutations := readSegmentMutations(t, ctx, store, br, manifest.Segments[0].SegmentId)
	require.Len(t, mutations, 3)
	for i, m := range mutations {
		assert.Equal(t, fmt.Sprintf("doc-%d", i+1), m.DocId)
		assert.Equal(t, []byte(fmt.Sprintf("val-%d", i+1)), m.Payload)
	}

	cancel()
	err = <-flusherErrCh
	require.NoError(t, err)
}

func TestMultiBranchFlush(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	defer store.Close()

	log, err := logstream.New(store, defaultWAL)
	require.NoError(t, err)

	appendDoc(t, ctx, log, "main", "doc-1", []byte("val-1"))
	appendDoc(t, ctx, log, "dev", "doc-2", []byte("val-2"))
	appendDoc(t, ctx, log, "main", "doc-3", []byte("val-3"))

	flusher, err := ingest.NewFlusher(store, ingest.Config{
		PollInterval: 10 * time.Millisecond,
	})
	require.NoError(t, err)

	flusherErrCh := make(chan error, 1)
	go func() {
		flusherErrCh <- flusher.Run(ctx)
	}()

	mainManifest := waitForManifest(t, ctx, store, defaultKey("main"), func(m *storagepb.BranchManifest) bool {
		return len(m.Segments) == 2 && m.CheckpointSeq == 3
	})
	assert.Equal(t, "00000000000000000001", mainManifest.Segments[0].SegmentId)
	assert.Equal(t, "00000000000000000003", mainManifest.Segments[1].SegmentId)
	exists, err := store.Exists(ctx, namespace.Scope{Namespace: namespace.DefaultNamespace}.SegmentKey(mainManifest.Segments[1].SegmentId))
	require.NoError(t, err)
	assert.True(t, exists)

	devManifest := waitForManifest(t, ctx, store, defaultKey("dev"), func(m *storagepb.BranchManifest) bool {
		return len(m.Segments) == 1 && m.CheckpointSeq == 2
	})
	assert.Equal(t, "00000000000000000002", devManifest.Segments[0].SegmentId)

	cancel()
	err = <-flusherErrCh
	require.NoError(t, err)
}

func TestFlusher_RejectsEntryWithTwoBranches(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	defer store.Close()

	log, err := logstream.New(store, defaultWAL)
	require.NoError(t, err)

	var records []logstream.Record
	for _, branch := range []string{"main", "dev"} {
		data, err := proto.Marshal(&storagepb.WalRecord{
			Record: &storagepb.WalRecord_Mutation{
				Mutation: &storagepb.DocumentMutation{Branch: branch, DocId: "doc-" + branch, Op: storagepb.MutationOp_PUT},
			},
		})
		require.NoError(t, err)
		records = append(records, data)
	}
	_, err = log.Append(ctx, records)
	require.NoError(t, err)

	flusher, err := ingest.NewFlusher(store, ingest.Config{PollInterval: 10 * time.Millisecond})
	require.NoError(t, err)

	flusherErrCh := make(chan error, 1)
	go func() {
		flusherErrCh <- flusher.Run(ctx)
	}()
	cancel()

	err = <-flusherErrCh
	require.ErrorContains(t, err, `wal entry 1 holds mutations for branches "main" and "dev"`)
	for _, branch := range []string{"main", "dev"} {
		exists, err := store.Exists(context.Background(), defaultKey(branch))
		require.NoError(t, err)
		assert.False(t, exists, branch)
	}
}

func TestRestartResume(t *testing.T) {
	ctx := context.Background()

	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	defer store.Close()

	log, err := logstream.New(store, defaultWAL)
	require.NoError(t, err)

	mainBr := "main"
	for i := 1; i <= 2; i++ {
		appendDoc(t, ctx, log, mainBr, fmt.Sprintf("doc-%d", i), []byte(fmt.Sprintf("val-%d", i)))
	}

	flusher1Ctx, cancel1 := context.WithCancel(ctx)
	flusher1, err := ingest.NewFlusher(store, ingest.Config{
		PollInterval: 10 * time.Millisecond,
	})
	require.NoError(t, err)

	flusher1ErrCh := make(chan error, 1)
	go func() {
		flusher1ErrCh <- flusher1.Run(flusher1Ctx)
	}()

	manifest1 := waitForManifest(t, ctx, store, defaultKey(mainBr), func(m *storagepb.BranchManifest) bool {
		return len(m.Segments) == 2 && m.CheckpointSeq == 2
	})
	require.NotNil(t, manifest1)

	cancel1()
	require.NoError(t, <-flusher1ErrCh)

	appendDoc(t, ctx, log, mainBr, "doc-3", []byte("val-3"))

	flusher2Ctx, cancel2 := context.WithCancel(ctx)
	flusher2, err := ingest.NewFlusher(store, ingest.Config{
		PollInterval: 10 * time.Millisecond,
	})
	require.NoError(t, err)

	flusher2ErrCh := make(chan error, 1)
	go func() {
		flusher2ErrCh <- flusher2.Run(flusher2Ctx)
	}()

	manifest2 := waitForManifest(t, ctx, store, defaultKey(mainBr), func(m *storagepb.BranchManifest) bool {
		return len(m.Segments) == 3 && m.CheckpointSeq == 3
	})
	require.NotNil(t, manifest2)
	assert.Equal(t, uint64(3), manifest2.CheckpointSeq)
	require.Len(t, manifest2.Segments, 3)

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
		data, err := protojson.Marshal(concurrentManifest)
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

	br := "main"
	store := &casConflictStore{
		Store:      memStore,
		conflictOn: defaultKey(br),
	}

	log, err := logstream.New(store, defaultWAL)
	require.NoError(t, err)

	appendDoc(t, ctx, log, br, "doc-1", []byte("val-1"))

	flusher, err := ingest.NewFlusher(store, ingest.Config{
		PollInterval: 10 * time.Millisecond,
	})
	require.NoError(t, err)

	flusherErrCh := make(chan error, 1)
	go func() {
		flusherErrCh <- flusher.Run(ctx)
	}()

	manifest := waitForManifest(t, ctx, store, defaultKey(br), func(m *storagepb.BranchManifest) bool {
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

	log, err := logstream.New(store, defaultWAL)
	require.NoError(t, err)

	br := "main"
	appendDoc(t, ctx, log, br, "doc-1", []byte("val-1"))
	appendDoc(t, ctx, log, br, "doc-2", []byte("val-2"))

	flusher, err := ingest.NewFlusher(store, ingest.Config{
		PollInterval: 10 * time.Millisecond,
	})
	require.NoError(t, err)

	flusherErrCh := make(chan error, 1)
	go func() {
		flusherErrCh <- flusher.Run(ctx)
	}()

	cancel()

	err = <-flusherErrCh
	require.NoError(t, err)

	drainCtx := context.Background()
	m, _, err := manifest.Read(drainCtx, store, defaultKey(br))
	require.NoError(t, err)
	assert.Equal(t, uint64(2), m.CheckpointSeq)
	require.Len(t, m.Segments, 2)
}

func TestForkEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	defer store.Close()

	b, err := ingest.NewIngester(store)
	require.NoError(t, err)

	parentBr := "parent"
	childBr := "child"

	_, err = manifest.Write(ctx, store, defaultKey(parentBr), &storagepb.BranchManifest{CheckpointSeq: 0}, "")
	require.NoError(t, err)

	ns := namespace.DefaultNamespace
	require.NoError(t, b.Upsert(ctx, ns, parentBr, []*cloudyneighpb.Record{{Id: "p-doc-1"}}))
	require.NoError(t, b.Fork(ctx, ns, parentBr, childBr))
	require.NoError(t, b.Upsert(ctx, ns, childBr, []*cloudyneighpb.Record{{Id: "c-doc-1"}}))

	flusher, err := ingest.NewFlusher(store, ingest.Config{
		PollInterval: 10 * time.Millisecond,
	})
	require.NoError(t, err)

	flusherErrCh := make(chan error, 1)
	go func() {
		flusherErrCh <- flusher.Run(ctx)
	}()

	childManifest := waitForManifest(t, ctx, store, defaultKey(childBr), func(m *storagepb.BranchManifest) bool {
		return len(m.Segments) >= 1
	})
	require.NotNil(t, childManifest)

	cancel()
	err = <-flusherErrCh
	require.NoError(t, err)
}

type cancelOnBranchStore struct {
	objectstore.Store
	mu        sync.Mutex
	cancel    context.CancelFunc
	keys      []string
	armed     bool
	doneFirst bool
}

func (s *cancelOnBranchStore) Arm() {
	s.mu.Lock()
	s.armed = true
	s.mu.Unlock()
}

func (s *cancelOnBranchStore) Put(ctx context.Context, key string, r io.Reader, cond objectstore.Condition) (string, error) {
	s.mu.Lock()
	if s.armed && slices.Contains(s.keys, key) {
		if !s.doneFirst {
			s.doneFirst = true
			s.mu.Unlock()
			gen, err := s.Store.Put(ctx, key, r, cond)
			s.cancel()
			return gen, err
		}
	}
	s.mu.Unlock()
	return s.Store.Put(ctx, key, r, cond)
}

func TestShutdownFlushDrainsLog(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	memStore, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	defer memStore.Close()

	b1 := "branch-1"
	b2 := "branch-2"
	store := &cancelOnBranchStore{
		Store:  memStore,
		cancel: cancel,
		keys:   []string{defaultKey(b1), defaultKey(b2)},
	}

	log, err := logstream.New(store, defaultWAL)
	require.NoError(t, err)

	_, err = manifest.Write(ctx, store, defaultKey(b1), &storagepb.BranchManifest{CheckpointSeq: 0}, "")
	require.NoError(t, err)
	_, err = manifest.Write(ctx, store, defaultKey(b2), &storagepb.BranchManifest{CheckpointSeq: 0}, "")
	require.NoError(t, err)

	appendDoc(t, ctx, log, b1, "doc-1", nil)
	appendDoc(t, ctx, log, b2, "doc-2", nil)

	flusher, err := ingest.NewFlusher(store, ingest.Config{
		PollInterval: 10 * time.Millisecond,
	})
	require.NoError(t, err)

	store.Arm()
	err = flusher.Run(ctx)
	require.NoError(t, err)

	drainCtx := context.Background()
	m1, _, err := manifest.Read(drainCtx, store, defaultKey(b1))
	require.NoError(t, err)
	m2, _, err := manifest.Read(drainCtx, store, defaultKey(b2))
	require.NoError(t, err)

	assert.Equal(t, uint64(1), m1.CheckpointSeq)
	assert.Len(t, m1.Segments, 1)
	assert.Equal(t, uint64(2), m2.CheckpointSeq)
	assert.Len(t, m2.Segments, 1)
}

func TestFlusher_MultiNamespaceDiscoveryAndFlush(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	defer store.Close()

	in, err := ingest.NewIngester(store)
	require.NoError(t, err)

	brA := namespace.Scope{Namespace: "ns-a"}.ManifestKey("main")
	brB := namespace.Scope{Namespace: "ns-b"}.ManifestKey("main")

	require.NoError(t, in.Upsert(ctx, "ns-a", "main", []*cloudyneighpb.Record{{Id: "doc-a1"}, {Id: "doc-a2"}}))
	require.NoError(t, in.Upsert(ctx, "ns-b", "main", []*cloudyneighpb.Record{{Id: "doc-b1"}}))

	flusher, err := ingest.NewFlusher(store, ingest.Config{
		PollInterval: 10 * time.Millisecond,
	})
	require.NoError(t, err)

	flusherErrCh := make(chan error, 1)
	go func() {
		flusherErrCh <- flusher.Run(ctx)
	}()

	manifestA := waitForManifest(t, ctx, store, brA, func(m *storagepb.BranchManifest) bool {
		return len(m.Segments) == 1 && m.CheckpointSeq == 1
	})
	require.NotNil(t, manifestA)
	assert.Equal(t, uint64(2), manifestA.Segments[0].DocCount)

	manifestB := waitForManifest(t, ctx, store, brB, func(m *storagepb.BranchManifest) bool {
		return len(m.Segments) == 1 && m.CheckpointSeq == 1
	})
	require.NotNil(t, manifestB)
	assert.Equal(t, uint64(1), manifestB.Segments[0].DocCount)

	require.NoError(t, in.Upsert(ctx, "ns-b", "main", []*cloudyneighpb.Record{{Id: "doc-b2"}}))
	manifestB2 := waitForManifest(t, ctx, store, brB, func(m *storagepb.BranchManifest) bool {
		return len(m.Segments) == 2 && m.CheckpointSeq == 2
	})
	require.NotNil(t, manifestB2)

	cancel()
	err = <-flusherErrCh
	require.NoError(t, err)
}

var errCrash = errors.New("crash before manifest write")

type crashOnPutStore struct {
	objectstore.Store
	mu      sync.Mutex
	crashOn string
	crashed chan struct{}
}

func (s *crashOnPutStore) Put(ctx context.Context, key string, r io.Reader, cond objectstore.Condition) (string, error) {
	s.mu.Lock()
	if key == s.crashOn {
		select {
		case <-s.crashed:
		default:
			close(s.crashed)
			s.mu.Unlock()
			return "", errCrash
		}
	}
	s.mu.Unlock()
	return s.Store.Put(ctx, key, r, cond)
}

func TestFlusher_RecoversAfterCrashBetweenUploadAndManifest(t *testing.T) {
	ctx := context.Background()

	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	defer store.Close()

	log, err := logstream.New(store, defaultWAL)
	require.NoError(t, err)
	seq1 := appendDoc(t, ctx, log, "main", "doc-1", []byte("val-1"))

	crashing := &crashOnPutStore{Store: store, crashOn: defaultKey("main"), crashed: make(chan struct{})}
	flusher1, err := ingest.NewFlusher(crashing, ingest.Config{PollInterval: 10 * time.Millisecond})
	require.NoError(t, err)
	ctx1, cancel1 := context.WithCancel(ctx)
	errCh1 := make(chan error, 1)
	go func() {
		errCh1 <- flusher1.Run(ctx1)
	}()
	select {
	case <-crashing.crashed:
	case <-time.After(5 * time.Second):
		t.Fatal("flusher never reached the manifest write")
	}
	cancel1()
	<-errCh1

	flusher2, err := ingest.NewFlusher(store, ingest.Config{PollInterval: 10 * time.Millisecond})
	require.NoError(t, err)
	ctx2, cancel2 := context.WithCancel(ctx)
	errCh2 := make(chan error, 1)
	go func() {
		errCh2 <- flusher2.Run(ctx2)
	}()

	waitForManifest(t, ctx, store, defaultKey("main"), func(m *storagepb.BranchManifest) bool {
		return m.CheckpointSeq == seq1 && len(m.Segments) == 1
	})

	seq2 := appendDoc(t, ctx, log, "main", "doc-2", []byte("val-2"))
	waitForManifest(t, ctx, store, defaultKey("main"), func(m *storagepb.BranchManifest) bool {
		return m.CheckpointSeq == seq2 && len(m.Segments) == 2
	})

	cancel2()
	require.NoError(t, <-errCh2)
}
