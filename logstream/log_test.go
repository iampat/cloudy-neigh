package logstream_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/iampat/cloudy-neigh/logstream"
	"github.com/iampat/cloudy-neigh/objectstore"
	"github.com/iampat/cloudy-neigh/recordio"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func forEachBackend(t *testing.T, fn func(t *testing.T, s objectstore.Store)) {
	t.Helper()
	t.Run("mem", func(t *testing.T) {
		s, err := objectstore.Open(context.Background(), "mem://")
		require.NoError(t, err)
		t.Cleanup(func() { s.Close() })
		fn(t, s)
	})
	t.Run("file", func(t *testing.T) {
		dir := t.TempDir()
		s, err := objectstore.Open(context.Background(), "file://"+dir+"?create_dir=true")
		require.NoError(t, err)
		t.Cleanup(func() { s.Close() })
		fn(t, s)
	})
}

func newLog(t *testing.T, s objectstore.Store, prefix string) *logstream.Log {
	t.Helper()
	l, err := logstream.New(s, prefix)
	require.NoError(t, err)
	return l
}

func TestNewNilStore(t *testing.T) {
	_, err := logstream.New(nil, "main")
	assert.Error(t, err)
}

func TestPrefixValidation(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		tests := []struct {
			name    string
			prefix  string
			wantErr bool
		}{
			{name: "valid simple", prefix: "main", wantErr: false},
			{name: "valid nested slash", prefix: "wal/main", wantErr: false},
			{name: "valid multiple slashes", prefix: "wal/feature/search", wantErr: false},
			{name: "valid hyphen underscore", prefix: "orders-topic_1", wantErr: false},
			{name: "valid single char", prefix: "a", wantErr: false},
			{name: "valid digits", prefix: "0123", wantErr: false},
			{name: "empty", prefix: "", wantErr: true},
			{name: "double slash", prefix: "wal//main", wantErr: true},
			{name: "leading slash", prefix: "/wal/main", wantErr: true},
			{name: "trailing slash", prefix: "wal/main/", wantErr: true},
			{name: "dot", prefix: ".", wantErr: true},
			{name: "dot dot", prefix: "..", wantErr: true},
			{name: "space", prefix: "a b", wantErr: true},
			{name: "special char", prefix: "a@b", wantErr: true},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				log, err := logstream.New(s, tc.prefix)
				if tc.wantErr {
					assert.Error(t, err)
					return
				}
				require.NoError(t, err)
				_, err = log.Append(context.Background(), []logstream.Record{[]byte("val")})
				assert.NoError(t, err)
			})
		}
	})
}

func TestAppendAndRead(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		prefix := "wal/test-stream"
		log := newLog(t, s, prefix)

		tail, err := log.Tail(ctx)
		assert.NoError(t, err)
		assert.Zero(t, tail)

		b1 := []logstream.Record{[]byte("r1"), []byte("r2")}
		seq1, err := log.Append(ctx, b1)
		require.NoError(t, err)
		assert.Equal(t, uint64(1), seq1)

		b2 := []logstream.Record{[]byte("r3")}
		seq2, err := log.Append(ctx, b2)
		require.NoError(t, err)
		assert.Equal(t, uint64(2), seq2)

		tail, err = log.Tail(ctx)
		assert.NoError(t, err)
		assert.Equal(t, uint64(2), tail)

		read1, err := log.Read(ctx, 1)
		require.NoError(t, err)
		assert.Equal(t, []logstream.Record{[]byte("r1"), []byte("r2")}, read1)

		read2, err := log.Read(ctx, 2)
		require.NoError(t, err)
		assert.Equal(t, []logstream.Record{[]byte("r3")}, read2)

		_, err = log.Read(ctx, 3)
		assert.ErrorIs(t, err, logstream.ErrEndOfStream)

		_, err = log.Read(ctx, 0)
		assert.Error(t, err)
	})
}

func TestAppendValidation(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		prefix := "wal/validation-stream"
		log := newLog(t, s, prefix)

		_, err := log.Append(ctx, nil)
		assert.Error(t, err)

		_, err = log.Append(ctx, []logstream.Record{})
		assert.Error(t, err)

		canceledCtx, cancel := context.WithCancel(ctx)
		cancel()
		_, err = log.Append(canceledCtx, []logstream.Record{[]byte("ok")})
		assert.ErrorIs(t, err, context.Canceled)
	})
}

func TestTailJumpColdStart(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		prefix := "wal/cold-jump"
		log := newLog(t, s, prefix)

		total := 1005
		for i := 1; i <= total; i++ {
			seq, err := log.Append(ctx, []logstream.Record{[]byte(fmt.Sprintf("rec-%d", i))})
			require.NoError(t, err)
			assert.Equal(t, uint64(i), seq)
		}

		coldLog := newLog(t, s, prefix)
		tail, err := coldLog.Tail(ctx)
		require.NoError(t, err)
		assert.Equal(t, uint64(total), tail)
	})
}

func TestAppendDriftRecovery(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		prefix := "wal/drift-stream"

		writerA := newLog(t, s, prefix)
		writerB := newLog(t, s, prefix)

		seqA1, err := writerA.Append(ctx, []logstream.Record{[]byte("A1")})
		require.NoError(t, err)
		assert.Equal(t, uint64(1), seqA1)

		for i := 2; i <= 30; i++ {
			seqB, err := writerB.Append(ctx, []logstream.Record{[]byte(fmt.Sprintf("B%d", i))})
			require.NoError(t, err)
			assert.Equal(t, uint64(i), seqB)
		}

		seqA2, err := writerA.Append(ctx, []logstream.Record{[]byte("A2")})
		require.NoError(t, err)
		assert.Equal(t, uint64(31), seqA2)
	})
}

func TestConcurrentAppends(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		tests := []struct {
			name      string
			prefix    string
			writers   int
			perWriter int
			sharedLog bool
		}{
			{name: "single log", prefix: "wal/concurrent-single", writers: 8, perWriter: 5, sharedLog: true},
			{name: "multi log", prefix: "wal/concurrent-multi", writers: 4, perWriter: 4, sharedLog: false},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				ctx := context.Background()
				totalAppends := tc.writers * tc.perWriter

				var shared *logstream.Log
				if tc.sharedLog {
					shared = newLog(t, s, tc.prefix)
				}

				errCh := make(chan error, totalAppends)
				seqCh := make(chan uint64, totalAppends)

				var wg sync.WaitGroup
				for w := 0; w < tc.writers; w++ {
					wg.Add(1)
					go func(writerID int) {
						defer wg.Done()
						log := shared
						if log == nil {
							var err error
							log, err = logstream.New(s, tc.prefix)
							if err != nil {
								errCh <- err
								return
							}
						}
						for i := 0; i < tc.perWriter; i++ {
							rec := []byte(fmt.Sprintf("w%d-%d", writerID, i))
							seq, err := log.Append(ctx, []logstream.Record{rec})
							if err != nil {
								errCh <- fmt.Errorf("writer %d iter %d: %w", writerID, i, err)
								return
							}
							seqCh <- seq
						}
					}(w)
				}

				wg.Wait()
				close(errCh)
				close(seqCh)

				for err := range errCh {
					require.NoError(t, err)
				}

				seen := make(map[uint64]bool)
				for seq := range seqCh {
					assert.False(t, seen[seq], "duplicate sequence number: %d", seq)
					seen[seq] = true
				}

				assert.Len(t, seen, totalAppends)

				for i := 1; i <= totalAppends; i++ {
					assert.True(t, seen[uint64(i)], "missing sequence number %d", i)
				}

				readerLog := shared
				if readerLog == nil {
					readerLog = newLog(t, s, tc.prefix)
				}
				tail, err := readerLog.Tail(ctx)
				require.NoError(t, err)
				assert.Equal(t, uint64(totalAppends), tail)
			})
		}
	})
}

func TestCorruptedSegment(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		prefix := "wal/corrupt-stream"
		log := newLog(t, s, prefix)

		seq, err := log.Append(ctx, []logstream.Record{[]byte("valid payload")})
		require.NoError(t, err)

		key := fmt.Sprintf("%s/%020d.recordio", prefix, seq)
		rc, _, err := s.Get(ctx, key)
		require.NoError(t, err)
		validBytes, err := io.ReadAll(rc)
		rc.Close()
		require.NoError(t, err)

		corruptHeader := bytes.Clone(validBytes)
		corruptHeader[9] ^= 0xFF
		require.NoError(t, s.Delete(ctx, key))
		_, err = s.Put(ctx, key, bytes.NewReader(corruptHeader), objectstore.Condition{})
		require.NoError(t, err)

		_, err = log.Read(ctx, seq)
		assert.ErrorIs(t, err, recordio.ErrHeaderCorrupted)

		corruptData := bytes.Clone(validBytes)
		corruptData[len(corruptData)-2] ^= 0xFF
		require.NoError(t, s.Delete(ctx, key))
		_, err = s.Put(ctx, key, bytes.NewReader(corruptData), objectstore.Condition{})
		require.NoError(t, err)

		_, err = log.Read(ctx, seq)
		assert.ErrorIs(t, err, recordio.ErrDataCorrupted)

		truncated := validBytes[:len(validBytes)-2]
		require.NoError(t, s.Delete(ctx, key))
		_, err = s.Put(ctx, key, bytes.NewReader(truncated), objectstore.Condition{})
		require.NoError(t, err)

		_, err = log.Read(ctx, seq)
		assert.ErrorIs(t, err, recordio.ErrTornWrite)
	})
}

func BenchmarkAppend(b *testing.B) {
	s, err := objectstore.Open(context.Background(), "mem://")
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()
	log, err := logstream.New(s, "wal/bench")
	if err != nil {
		b.Fatal(err)
	}
	rec := []logstream.Record{[]byte("benchmark payload data 128 bytes of information for testing throughput of append operation in logstream package")}

	for b.Loop() {
		if _, err := log.Append(ctx, rec); err != nil {
			b.Fatal(err)
		}
	}
}
