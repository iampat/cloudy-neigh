package kvfs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"strconv"
	"sync"
	"time"

	"github.com/iampat/cloudy-neigh/logstream"
	"github.com/iampat/cloudy-neigh/objectstore"
	kvfspb "github.com/iampat/cloudy-neigh/proto/kvfs/v1"
	"golang.org/x/sync/singleflight"
	"google.golang.org/protobuf/proto"
)

var (
	ErrInvalidKey  = errors.New("kvfs: empty key")
	ErrNilStore    = errors.New("kvfs: nil objectstore")
	ErrNilReader   = errors.New("kvfs: nil reader")
	ErrBatchClosed = errors.New("kvfs: batch closed")
)

type WriteOptions struct {
	Sync bool
}

var (
	Sync   = &WriteOptions{Sync: true}
	NoSync = &WriteOptions{Sync: false}
)

var defaultOptions = Options{
	WALFlushInterval: 500 * time.Millisecond,
	ManifestLeaseTTL: 500 * time.Millisecond,
}

type Options struct {
	WALFlushInterval time.Duration
	ManifestLeaseTTL time.Duration
}

type Value struct {
	Data io.ReadCloser
	Size int64
}

type Store interface {
	Branch() string
	Get(ctx context.Context, key string) (Value, error)
	Set(ctx context.Context, key string, r io.Reader, opts *WriteOptions) error
	Delete(ctx context.Context, key string, opts *WriteOptions) error
	Flush(ctx context.Context) error
	Fork(ctx context.Context, newBranch string) (Store, error)
	NewBatch() *Batch
	Close() error
}

type cachedManifest struct {
	manifestHash string
	generation   string
	manifest     *kvfspb.Manifest
	expiresAt    time.Time
}

type client struct {
	store            objectstore.Store
	branch           string
	walFlushInterval time.Duration
	manifestLeaseTTL time.Duration
	log              *logstream.Log

	mu             sync.Mutex
	closed         bool
	cachedManifest *cachedManifest

	flushGroup singleflight.Group
	readGroup  singleflight.Group
	stopCh     chan struct{}
	wg         sync.WaitGroup
}

func Open(ctx context.Context, store objectstore.Store, branch string, opts Options) (Store, error) {
	if store == nil {
		return nil, ErrNilStore
	}
	if err := validateBranch(branch); err != nil {
		return nil, err
	}

	if branch != "main" {
		if _, _, err := resolveBranch(ctx, store, branch); err != nil {
			return nil, err
		}
	}

	if opts.WALFlushInterval <= 0 {
		opts.WALFlushInterval = defaultOptions.WALFlushInterval
	}
	if opts.ManifestLeaseTTL <= 0 {
		opts.ManifestLeaseTTL = defaultOptions.ManifestLeaseTTL
	}

	l, err := logstream.New(store, "wal/"+branch)
	if err != nil {
		return nil, err
	}

	c := &client{
		store:            store,
		branch:           branch,
		walFlushInterval: opts.WALFlushInterval,
		manifestLeaseTTL: opts.ManifestLeaseTTL,
		log:              l,
		stopCh:           make(chan struct{}),
	}

	c.wg.Add(1)
	go c.backgroundFlusher()

	return c, nil
}

func (c *client) Branch() string {
	return c.branch
}

func (c *client) backgroundFlusher() {
	defer c.wg.Done()
	ticker := time.NewTicker(c.walFlushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopCh:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := c.Flush(ctx); err != nil {
				slog.Error("kvfs: background wal flush failed", "branch", c.branch, "err", err)
			}
			cancel()
		}
	}
}

func (c *client) Flush(ctx context.Context) error {
	tail, err := c.log.Tail(ctx)
	if err != nil {
		return err
	}
	if tail == 0 {
		return nil
	}
	ch := c.flushGroup.DoChan(strconv.FormatUint(tail, 10), func() (any, error) {
		return nil, c.doFlush(context.Background(), tail)
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case res := <-ch:
		return res.Err
	}
}

func (c *client) doFlush(ctx context.Context, targetTail uint64) error {
	for {
		manifestHash, gen, err := resolveBranch(ctx, c.store, c.branch)
		if err != nil && !errors.Is(err, objectstore.ErrNotFound) {
			return err
		}

		var (
			parentManifest *kvfspb.Manifest
			lastWalSeq     uint64
		)

		if errors.Is(err, objectstore.ErrNotFound) {
			parentManifest = &kvfspb.Manifest{Entries: make(map[string]*kvfspb.ManifestEntry)}
			lastWalSeq = 0
		} else {
			parentManifest, err = getManifest(ctx, c.store, manifestHash)
			if err != nil {
				return err
			}
			lastWalSeq = parentManifest.LastWalSeq
		}

		if targetTail <= lastWalSeq {
			return nil
		}

		newEntries := make(map[string]*kvfspb.ManifestEntry, len(parentManifest.Entries))
		maps.Copy(newEntries, parentManifest.Entries)

		for seq := lastWalSeq + 1; seq <= targetTail; seq++ {
			records, err := c.log.Read(ctx, seq)
			if err != nil {
				if errors.Is(err, logstream.ErrEndOfStream) {
					break
				}
				return err
			}
			for _, rec := range records {
				var mut kvfspb.Mutation
				if err := proto.Unmarshal(rec, &mut); err != nil {
					return err
				}
				if mut.Tombstone {
					delete(newEntries, mut.Key)
				} else {
					newEntries[mut.Key] = &kvfspb.ManifestEntry{
						CasHash:   mut.CasHash,
						SizeBytes: mut.SizeBytes,
					}
				}
			}
		}

		newManifest := &kvfspb.Manifest{
			LastWalSeq: targetTail,
			Entries:    newEntries,
		}

		newManifestHash, err := putManifest(ctx, c.store, newManifest)
		if err != nil {
			return err
		}

		newGen, err := updateBranch(ctx, c.store, c.branch, newManifestHash, gen)
		if err == nil {
			c.mu.Lock()
			if c.cachedManifest == nil || newManifest.LastWalSeq >= c.cachedManifest.manifest.LastWalSeq {
				c.cachedManifest = &cachedManifest{
					manifestHash: newManifestHash,
					generation:   newGen,
					manifest:     newManifest,
					expiresAt:    time.Now().Add(c.manifestLeaseTTL),
				}
			}
			c.mu.Unlock()
			return nil
		}
		if !errors.Is(err, objectstore.ErrPreconditionFailed) {
			return err
		}
	}
}

func (c *client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	close(c.stopCh)
	c.mu.Unlock()

	c.wg.Wait()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.Flush(ctx)
}

func (c *client) Fork(ctx context.Context, newBranch string) (Store, error) {
	if err := c.Flush(ctx); err != nil {
		return nil, err
	}
	if _, _, err := createBranch(ctx, c.store, newBranch, c.branch); err != nil {
		return nil, err
	}
	return Open(ctx, c.store, newBranch, Options{
		WALFlushInterval: c.walFlushInterval,
		ManifestLeaseTTL: c.manifestLeaseTTL,
	})
}

func (c *client) loadManifest(ctx context.Context) (*kvfspb.Manifest, error) {
	c.mu.Lock()
	if c.cachedManifest != nil && time.Now().Before(c.cachedManifest.expiresAt) {
		m := c.cachedManifest.manifest
		c.mu.Unlock()
		return m, nil
	}
	c.mu.Unlock()

	ch := c.readGroup.DoChan("manifest", func() (any, error) {
		c.mu.Lock()
		if c.cachedManifest != nil && time.Now().Before(c.cachedManifest.expiresAt) {
			m := c.cachedManifest.manifest
			c.mu.Unlock()
			return m, nil
		}
		c.mu.Unlock()

		manifestHash, gen, err := resolveBranch(context.Background(), c.store, c.branch)
		if err != nil {
			return nil, err
		}

		c.mu.Lock()
		if c.cachedManifest != nil && c.cachedManifest.generation == gen && c.cachedManifest.manifestHash == manifestHash {
			c.cachedManifest.expiresAt = time.Now().Add(c.manifestLeaseTTL)
			m := c.cachedManifest.manifest
			c.mu.Unlock()
			return m, nil
		}
		c.mu.Unlock()

		manifest, err := getManifest(context.Background(), c.store, manifestHash)
		if err != nil {
			return nil, err
		}

		c.mu.Lock()
		if c.cachedManifest == nil || manifest.LastWalSeq >= c.cachedManifest.manifest.LastWalSeq {
			c.cachedManifest = &cachedManifest{
				manifestHash: manifestHash,
				generation:   gen,
				manifest:     manifest,
				expiresAt:    time.Now().Add(c.manifestLeaseTTL),
			}
		}
		c.mu.Unlock()
		return manifest, nil
	})

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
		return res.Val.(*kvfspb.Manifest), nil
	}
}

func (c *client) Get(ctx context.Context, key string) (Value, error) {
	if key == "" {
		return Value{}, ErrInvalidKey
	}

	manifest, err := c.loadManifest(ctx)
	if err != nil {
		return Value{}, err
	}

	entry, ok := manifest.Entries[key]
	if !ok {
		return Value{}, objectstore.ErrNotFound
	}

	rc, size, err := getBlob(ctx, c.store, entry.CasHash)
	if err != nil {
		return Value{}, err
	}
	return Value{Data: rc, Size: size}, nil
}

type Batch struct {
	client *client
	mu     sync.Mutex
	muts   []*kvfspb.Mutation
	closed bool
}

func (c *client) NewBatch() *Batch {
	return &Batch{client: c}
}

func (b *Batch) Set(ctx context.Context, key string, r io.Reader) error {
	if r == nil {
		return ErrNilReader
	}
	if key == "" {
		return ErrInvalidKey
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ErrBatchClosed
	}
	b.mu.Unlock()

	casHash, size, err := putBlob(ctx, b.client.store, r)
	if err != nil {
		return err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return ErrBatchClosed
	}

	b.muts = append(b.muts, &kvfspb.Mutation{
		Key:       key,
		CasHash:   casHash,
		SizeBytes: uint64(size),
	})
	return nil
}

func (b *Batch) Delete(key string) error {
	if key == "" {
		return ErrInvalidKey
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return ErrBatchClosed
	}

	b.muts = append(b.muts, &kvfspb.Mutation{
		Key:       key,
		Tombstone: true,
	})
	return nil
}

func (b *Batch) Commit(ctx context.Context, opts *WriteOptions) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ErrBatchClosed
	}
	if len(b.muts) == 0 {
		b.closed = true
		b.mu.Unlock()
		return nil
	}
	muts := b.muts
	b.muts = nil
	b.closed = true
	b.mu.Unlock()

	records := make([]logstream.Record, len(muts))
	for i, mut := range muts {
		data, err := proto.Marshal(mut)
		if err != nil {
			return err
		}
		records[i] = data
	}

	if _, err := b.client.log.Append(ctx, records); err != nil {
		return err
	}

	if opts == nil || opts.Sync {
		return b.client.Flush(ctx)
	}
	return nil
}

func (b *Batch) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	b.muts = nil
	return nil
}

func (c *client) Set(ctx context.Context, key string, r io.Reader, opts *WriteOptions) error {
	b := c.NewBatch()
	if err := b.Set(ctx, key, r); err != nil {
		return err
	}
	return b.Commit(ctx, opts)
}

func (c *client) Delete(ctx context.Context, key string, opts *WriteOptions) error {
	b := c.NewBatch()
	if err := b.Delete(key); err != nil {
		return err
	}
	return b.Commit(ctx, opts)
}
