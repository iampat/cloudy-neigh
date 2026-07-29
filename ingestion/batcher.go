package ingestion

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

var ErrBatcherClosed = errors.New("batcher is closed")

// Config specifies batching limits and triggers.
type Config struct {
	MaxBatchSize  int           // Max documents per batch (N)
	MaxBatchBytes int           // Max batch size in bytes (B)
	FlushInterval time.Duration // Max time interval before flushing (T)
	WALPrefix     string        // Storage path prefix e.g. "wal"
}

// DefaultConfig provides reasonable prototype defaults.
func DefaultConfig() Config {
	return Config{
		MaxBatchSize:  1000,
		MaxBatchBytes: 4 * 1024 * 1024, // 4MB
		FlushInterval: 500 * time.Millisecond,
		WALPrefix:     "wal",
	}
}

type ingestRequest struct {
	mutation *Mutation
	done     chan error
}

// Batcher aggregates mutations into WAL batches and flushes them based on triggers.
type Batcher struct {
	config    Config
	walWriter *WALWriter
	logger    *slog.Logger

	reqChan chan ingestRequest
	flushCh chan chan error
	closeCh chan struct{}
	wg      sync.WaitGroup

	closed atomic.Bool
}

// NewBatcher creates a Batcher and starts its background processing goroutine.
func NewBatcher(config Config, walWriter *WALWriter, logger *slog.Logger) *Batcher {
	if logger == nil {
		logger = slog.Default()
	}
	if config.MaxBatchSize <= 0 {
		config.MaxBatchSize = 1000
	}
	if config.MaxBatchBytes <= 0 {
		config.MaxBatchBytes = 4 * 1024 * 1024
	}
	if config.FlushInterval <= 0 {
		config.FlushInterval = 500 * time.Millisecond
	}

	b := &Batcher{
		config:    config,
		walWriter: walWriter,
		logger:    logger,
		reqChan:   make(chan ingestRequest, config.MaxBatchSize*2),
		flushCh:   make(chan chan error),
		closeCh:   make(chan struct{}),
	}

	b.wg.Add(1)
	go b.loop()

	b.logger.Info("started Batcher",
		slog.Int("max_batch_size", config.MaxBatchSize),
		slog.Int("max_batch_bytes", config.MaxBatchBytes),
		slog.Duration("flush_interval", config.FlushInterval),
	)
	return b
}

// Ingest submits a mutation and blocks until the batch is committed to WAL (WAL Durability).
func (b *Batcher) Ingest(ctx context.Context, mutation *Mutation) error {
	if b.closed.Load() {
		return ErrBatcherClosed
	}

	req := ingestRequest{
		mutation: mutation,
		done:     make(chan error, 1),
	}

	select {
	case b.reqChan <- req:
	case <-ctx.Done():
		return ctx.Err()
	case <-b.closeCh:
		return ErrBatcherClosed
	}

	select {
	case err := <-req.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Flush explicitly flushes any pending batch to WAL and blocks until completed.
func (b *Batcher) Flush(ctx context.Context) error {
	if b.closed.Load() {
		return ErrBatcherClosed
	}
	errCh := make(chan error, 1)
	select {
	case b.flushCh <- errCh:
	case <-ctx.Done():
		return ctx.Err()
	case <-b.closeCh:
		return ErrBatcherClosed
	}

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close flushes remaining items and shuts down the batcher.
func (b *Batcher) Close() error {
	if b.closed.Swap(true) {
		return nil
	}
	close(b.closeCh)
	b.wg.Wait()
	return nil
}

func (b *Batcher) loop() {
	defer b.wg.Done()

	var pending []ingestRequest
	var currentBytes int
	ticker := time.NewTicker(b.config.FlushInterval)
	defer ticker.Stop()

	flushPending := func(triggerReason string) {
		if len(pending) == 0 {
			return
		}
		mutations := make([]*Mutation, len(pending))
		for i, req := range pending {
			mutations[i] = req.mutation
		}

		b.logger.Debug("flushing batch",
			slog.String("trigger", triggerReason),
			slog.Int("count", len(mutations)),
			slog.Int("est_bytes", currentBytes),
		)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		seq, path, err := b.walWriter.WriteBatch(ctx, mutations)
		cancel()

		if err != nil {
			b.logger.Error("batch flush failed", slog.String("error", err.Error()))
		} else {
			b.logger.Info("batch flush completed",
				slog.Uint64("seq", seq),
				slog.String("path", path),
				slog.Int("count", len(mutations)),
				slog.String("trigger", triggerReason),
			)
		}

		for _, req := range pending {
			req.done <- err
			close(req.done)
		}

		pending = nil
		currentBytes = 0
	}

	for {
		select {
		case req := <-b.reqChan:
			// Rough estimate of mutation bytes for size trigger
			estSize := len(req.mutation.DocID) + 64
			if req.mutation.Document != nil {
				estSize += len(req.mutation.Document.Title) + len(req.mutation.Document.Text) + len(req.mutation.Document.Url)
			}
			pending = append(pending, req)
			currentBytes += estSize

			if len(pending) >= b.config.MaxBatchSize {
				flushPending("count_trigger")
				ticker.Reset(b.config.FlushInterval)
			} else if currentBytes >= b.config.MaxBatchBytes {
				flushPending("byte_size_trigger")
				ticker.Reset(b.config.FlushInterval)
			}

		case errCh := <-b.flushCh:
			flushPending("explicit_flush")
			errCh <- nil
			ticker.Reset(b.config.FlushInterval)

		case <-ticker.C:
			flushPending("timer_trigger")

		case <-b.closeCh:
			// Flush any remaining items before exiting
			// Drain remaining in reqChan
			for {
				select {
				case req := <-b.reqChan:
					pending = append(pending, req)
				default:
					goto DRAIN_DONE
				}
			}
		DRAIN_DONE:
			flushPending("close_flush")
			return
		}
	}
}
