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
	ErrInvalidKey = errors.New("kvfs: empty key")
	ErrNilStore   = errors.New("kvfs: nil objectstore")
	ErrNilReader  = errors.New("kvfs: nil reader")
)

type WriteOptions struct {
	Sync bool
}

var (
	Sync   = &WriteOptions{Sync: true}
	NoSync = &WriteOptions{Sync: false}
)

const defaultWALFlushInterval = 500 * time.Millisecond

type Options struct {
	WALFlushInterval time.Duration
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
	Close() error
}

type client struct {
	store            objectstore.Store
	branch           string
	walFlushInterval time.Duration
	log              *logstream.Log

	mu     sync.Mutex
	closed bool

	flushGroup singleflight.Group
	stopCh     chan struct{}
	wg         sync.WaitGroup
}

func Open(ctx context.Context, store objectstore.Store, branch string, opts *Options) (Store, error) {
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

	flushInterval := defaultWALFlushInterval
	if opts != nil && opts.WALFlushInterval > 0 {
		flushInterval = opts.WALFlushInterval
	}

	l, err := logstream.New(store, "wal/"+branch)
	if err != nil {
		return nil, err
	}

	c := &client{
		store:            store,
		branch:           branch,
		walFlushInterval: flushInterval,
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
	_, err, _ = c.flushGroup.Do(strconv.FormatUint(tail, 10), func() (any, error) {
		return nil, c.doFlush(ctx, tail)
	})
	return err
}

func (c *client) doFlush(ctx context.Context, targetTail uint64) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

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

		_, err = updateBranch(ctx, c.store, c.branch, newManifestHash, gen)
		if err == nil {
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
	return Open(ctx, c.store, newBranch, &Options{WALFlushInterval: c.walFlushInterval})
}

func (c *client) Get(ctx context.Context, key string) (Value, error) {
	if key == "" {
		return Value{}, ErrInvalidKey
	}

	manifestHash, _, err := resolveBranch(ctx, c.store, c.branch)
	if err != nil {
		return Value{}, err
	}

	manifest, err := getManifest(ctx, c.store, manifestHash)
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

func (c *client) mutate(ctx context.Context, mut *kvfspb.Mutation, opts *WriteOptions) error {
	if mut.Key == "" {
		return ErrInvalidKey
	}

	data, err := proto.Marshal(mut)
	if err != nil {
		return err
	}
	if _, err := c.log.Append(ctx, []logstream.Record{data}); err != nil {
		return err
	}

	if opts == nil || opts.Sync {
		return c.Flush(ctx)
	}
	return nil
}

func (c *client) Set(ctx context.Context, key string, r io.Reader, opts *WriteOptions) error {
	if r == nil {
		return ErrNilReader
	}
	if key == "" {
		return ErrInvalidKey
	}

	casHash, size, err := putBlob(ctx, c.store, r)
	if err != nil {
		return err
	}

	return c.mutate(ctx, &kvfspb.Mutation{
		Key:       key,
		CasHash:   casHash,
		SizeBytes: uint64(size),
	}, opts)
}

func (c *client) Delete(ctx context.Context, key string, opts *WriteOptions) error {
	return c.mutate(ctx, &kvfspb.Mutation{
		Key:       key,
		Tombstone: true,
	}, opts)
}
