package ingestion

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// WALWriter handles coordinator-free sequence generation and conditional writes.
type WALWriter struct {
	store     Store
	driver    Driver
	walPrefix string
	lastSeq   atomic.Uint64
	mu        sync.Mutex
	logger    *slog.Logger
}

// NewWALWriter initializes a WALWriter, probing the store for the current highest sequence.
func NewWALWriter(ctx context.Context, store Store, driver Driver, walPrefix string, logger *slog.Logger) (*WALWriter, error) {
	if logger == nil {
		logger = slog.Default()
	}
	w := &WALWriter{
		store:     store,
		driver:    driver,
		walPrefix: walPrefix,
		logger:    logger,
	}

	highestSeq, err := w.discoverHighestSeq(ctx, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to discover highest WAL sequence: %w", err)
	}
	w.lastSeq.Store(highestSeq)

	w.logger.Info("initialized WALWriter",
		slog.Uint64("initial_last_seq", highestSeq),
		slog.String("wal_prefix", walPrefix),
		slog.String("driver", driver.Name()),
	)
	return w, nil
}

// discoverHighestSeq lists keys after lastSeenSeq using lexicographical startAfter.
func (w *WALWriter) discoverHighestSeq(ctx context.Context, lastSeenSeq uint64) (uint64, error) {
	var startAfter string
	if lastSeenSeq > 0 {
		startAfter = w.formatPath(lastSeenSeq)
	}

	keys, err := w.store.List(ctx, w.walPrefix, startAfter)
	if err != nil {
		return 0, err
	}

	highest := lastSeenSeq
	for _, key := range keys {
		seq, ok := w.parsePathSeq(key)
		if ok && seq > highest {
			highest = seq
		}
	}
	return highest, nil
}

// formatPath returns formatted key e.g. "wal/000000000123.jsonl"
func (w *WALWriter) formatPath(seq uint64) string {
	ext := w.driver.Extension()
	return fmt.Sprintf("%s/%012d%s", strings.TrimSuffix(w.walPrefix, "/"), seq, ext)
}

// parsePathSeq extracts sequence number from key like "wal/000000000123.jsonl"
func (w *WALWriter) parsePathSeq(path string) (uint64, bool) {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	numStr := strings.TrimSuffix(base, ext)
	seq, err := strconv.ParseUint(numStr, 10, 64)
	if err != nil {
		return 0, false
	}
	return seq, true
}

// WriteBatch writes a batch of mutations to the WAL using WriteIfNotExist with CAS retries.
func (w *WALWriter) WriteBatch(ctx context.Context, mutations []*Mutation) (uint64, string, error) {
	if len(mutations) == 0 {
		return 0, "", nil
	}

	startTime := time.Now()
	data, err := w.driver.MarshalBatch(mutations)
	if err != nil {
		return 0, "", fmt.Errorf("failed to marshal batch: %w", err)
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	retries := 0
	for {
		candidateSeq := w.lastSeq.Load() + 1
		path := w.formatPath(candidateSeq)

		err := w.store.WriteIfNotExist(ctx, path, data)
		if err == nil {
			w.lastSeq.Store(candidateSeq)
			duration := time.Since(startTime)

			w.logger.Info("wrote WAL bin batch",
				slog.Uint64("seq", candidateSeq),
				slog.String("path", path),
				slog.Int("mutation_count", len(mutations)),
				slog.Int("bytes", len(data)),
				slog.Int("cas_retries", retries),
				slog.Duration("duration", duration),
			)
			return candidateSeq, path, nil
		}

		if err == ErrExists {
			retries++
			w.logger.Warn("CAS collision writing WAL bin, re-probing head",
				slog.Uint64("attempted_seq", candidateSeq),
				slog.Int("retry_count", retries),
			)

			// Re-discover highest sequence using startAfter range query
			highest, discoverErr := w.discoverHighestSeq(ctx, candidateSeq)
			if discoverErr != nil {
				return 0, "", fmt.Errorf("failed to re-discover highest seq after CAS collision: %w", discoverErr)
			}
			w.lastSeq.Store(highest)
			continue
		}

		w.logger.Error("failed to write WAL bin",
			slog.Uint64("seq", candidateSeq),
			slog.String("path", path),
			slog.String("error", err.Error()),
		)
		return 0, "", err
	}
}
