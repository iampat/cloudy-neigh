package ingestion

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/iampat/cloudy-neigh/document"
)

func TestDrivers(t *testing.T) {
	drivers := []Driver{
		&JSONLDriver{},
		&RecordIODriver{},
	}

	mutations := []*Mutation{
		NewPutMutation(&document.Document{
			Id:    "doc-1",
			Title: "Test Document 1",
			Url:   "https://example.com/1",
			Text:  "Hello world contents",
		}),
		NewDeleteMutation("doc-2"),
	}

	for _, d := range drivers {
		t.Run(d.Name(), func(t *testing.T) {
			data, err := d.MarshalBatch(mutations)
			if err != nil {
				t.Fatalf("MarshalBatch failed: %v", err)
			}

			parsed, err := d.UnmarshalBatch(data)
			if err != nil {
				t.Fatalf("UnmarshalBatch failed: %v", err)
			}

			if len(parsed) != len(mutations) {
				t.Fatalf("expected %d mutations, got %d", len(mutations), len(parsed))
			}

			if parsed[0].DocID != "doc-1" || parsed[0].Type != MutationPut || parsed[0].Document.Title != "Test Document 1" {
				t.Errorf("unexpected mutation 0: %+v", parsed[0])
			}
			if parsed[1].DocID != "doc-2" || parsed[1].Type != MutationDelete {
				t.Errorf("unexpected mutation 1: %+v", parsed[1])
			}
		})
	}
}

func TestStoreWriteIfNotExistAndList(t *testing.T) {
	ctx := context.Background()
	tempDir, err := os.MkdirTemp("", "store_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	fileStore, err := NewFileStore(tempDir)
	if err != nil {
		t.Fatalf("failed to create FileStore: %v", err)
	}

	stores := map[string]Store{
		"MemoryStore": NewMemoryStore(),
		"FileStore":   fileStore,
	}

	for name, st := range stores {
		t.Run(name, func(t *testing.T) {
			path1 := "wal/000000000001.jsonl"
			path2 := "wal/000000000002.jsonl"

			// First write should succeed
			if err := st.WriteIfNotExist(ctx, path1, []byte("line1\n")); err != nil {
				t.Fatalf("WriteIfNotExist failed: %v", err)
			}

			// Second write to same path should return ErrExists
			if err := st.WriteIfNotExist(ctx, path1, []byte("line1_again\n")); err != ErrExists {
				t.Fatalf("expected ErrExists, got: %v", err)
			}

			// Write path2
			if err := st.WriteIfNotExist(ctx, path2, []byte("line2\n")); err != nil {
				t.Fatalf("WriteIfNotExist path2 failed: %v", err)
			}

			// List all
			keys, err := st.List(ctx, "wal/", "")
			if err != nil {
				t.Fatalf("List failed: %v", err)
			}
			if len(keys) != 2 || keys[0] != path1 || keys[1] != path2 {
				t.Fatalf("unexpected list keys: %v", keys)
			}

			// List with startAfter
			keysAfter, err := st.List(ctx, "wal/", path1)
			if err != nil {
				t.Fatalf("List startAfter failed: %v", err)
			}
			if len(keysAfter) != 1 || keysAfter[0] != path2 {
				t.Fatalf("unexpected list keysAfter: %v", keysAfter)
			}
		})
	}
}

func TestWALWriterSequentialAndDiscovery(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	driver := &JSONLDriver{}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	w1, err := NewWALWriter(ctx, store, driver, "wal", logger)
	if err != nil {
		t.Fatalf("NewWALWriter failed: %v", err)
	}

	mutations := []*Mutation{NewDeleteMutation("d1")}

	seq1, path1, err := w1.WriteBatch(ctx, mutations)
	if err != nil || seq1 != 1 || path1 != "wal/000000000001.jsonl" {
		t.Fatalf("unexpected write 1 result: seq=%d, path=%s, err=%v", seq1, path1, err)
	}

	seq2, path2, err := w1.WriteBatch(ctx, mutations)
	if err != nil || seq2 != 2 || path2 != "wal/000000000002.jsonl" {
		t.Fatalf("unexpected write 2 result: seq=%d, path=%s, err=%v", seq2, path2, err)
	}

	// Create new WALWriter on same store, should discover sequence 2 and write sequence 3
	w2, err := NewWALWriter(ctx, store, driver, "wal", logger)
	if err != nil {
		t.Fatalf("NewWALWriter 2 failed: %v", err)
	}

	seq3, path3, err := w2.WriteBatch(ctx, mutations)
	if err != nil || seq3 != 3 || path3 != "wal/000000000003.jsonl" {
		t.Fatalf("unexpected write 3 result: seq=%d, path=%s, err=%v", seq3, path3, err)
	}
}

func TestWALWriterConcurrentCASContention(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	driver := &JSONLDriver{}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	// Create 5 concurrent WALWriters against the same store
	numWriters := 5
	writers := make([]*WALWriter, numWriters)
	for i := 0; i < numWriters; i++ {
		w, err := NewWALWriter(ctx, store, driver, "wal", logger)
		if err != nil {
			t.Fatalf("failed to create writer: %v", err)
		}
		writers[i] = w
	}

	var wg sync.WaitGroup
	writesPerWorker := 10
	errChan := make(chan error, numWriters*writesPerWorker)

	for i := 0; i < numWriters; i++ {
		wg.Add(1)
		go func(w *WALWriter, workerID int) {
			defer wg.Done()
			for j := 0; j < writesPerWorker; j++ {
				muts := []*Mutation{NewDeleteMutation(fmt.Sprintf("doc-%d-%d", workerID, j))}
				_, _, err := w.WriteBatch(ctx, muts)
				if err != nil {
					errChan <- err
				}
			}
		}(writers[i], i)
	}

	wg.Wait()
	close(errChan)

	for err := range errChan {
		t.Fatalf("concurrent write failed: %v", err)
	}

	keys, err := store.List(ctx, "wal/", "")
	if err != nil {
		t.Fatalf("failed to list keys: %v", err)
	}

	expectedTotal := numWriters * writesPerWorker
	if len(keys) != expectedTotal {
		t.Fatalf("expected %d WAL bins, got %d", expectedTotal, len(keys))
	}
}

func TestBatcherTriggers(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	t.Run("CountTrigger", func(t *testing.T) {
		store := NewMemoryStore()
		driver := &JSONLDriver{}
		walWriter, _ := NewWALWriter(ctx, store, driver, "wal", logger)

		cfg := Config{
			MaxBatchSize:  5,
			FlushInterval: 10 * time.Second, // long interval to rely on count
		}
		batcher := NewBatcher(cfg, walWriter, logger)
		defer batcher.Close()

		var wg sync.WaitGroup
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				doc := &document.Document{Id: fmt.Sprintf("d-%d", idx)}
				if err := batcher.Ingest(ctx, NewPutMutation(doc)); err != nil {
					t.Errorf("Ingest failed: %v", err)
				}
			}(i)
		}
		wg.Wait()

		// Verify WAL bin was created
		keys, err := store.List(ctx, "wal/", "")
		if err != nil || len(keys) != 1 {
			t.Fatalf("expected 1 WAL bin after 5 ingested docs, got keys=%v, err=%v", keys, err)
		}
	})

	t.Run("TimeTrigger", func(t *testing.T) {
		store := NewMemoryStore()
		driver := &JSONLDriver{}
		walWriter, _ := NewWALWriter(ctx, store, driver, "wal", logger)

		cfg := Config{
			MaxBatchSize:  100,
			FlushInterval: 100 * time.Millisecond,
		}
		batcher := NewBatcher(cfg, walWriter, logger)
		defer batcher.Close()

		doc := &document.Document{Id: "d-timer"}
		start := time.Now()
		if err := batcher.Ingest(ctx, NewPutMutation(doc)); err != nil {
			t.Fatalf("Ingest failed: %v", err)
		}
		elapsed := time.Since(start)

		if elapsed < 80*time.Millisecond {
			t.Errorf("Ingest returned too quickly before timer flush, elapsed=%v", elapsed)
		}

		keys, err := store.List(ctx, "wal/", "")
		if err != nil || len(keys) != 1 {
			t.Fatalf("expected 1 WAL bin from timer trigger, got keys=%v", keys)
		}
	})
}
