package kvfs

import (
	"context"
	"errors"
	"io"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/iampat/cloudy-neigh/logstream"
	"github.com/iampat/cloudy-neigh/objectstore"
	kvfspb "github.com/iampat/cloudy-neigh/proto/kvfs/v1"
	"golang.org/x/sync/errgroup"
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

type Options struct {
	WALFlushInterval time.Duration
}

const defaultWALFlushInterval = 500 * time.Millisecond

type Value struct {
	Data io.ReadCloser
	Size int64
}

type Store interface {
	Get(ctx context.Context, branch, key string) (Value, error)
	Set(ctx context.Context, branch, key string, r io.Reader, opts *WriteOptions) error
	Delete(ctx context.Context, branch, key string, opts *WriteOptions) error
	Branch(ctx context.Context, newBranch, parentBranch string) error
	Close() error
}

type client struct {
	store            objectstore.Store
	walFlushInterval time.Duration

	mu             sync.Mutex
	logs           map[string]*logstream.Log
	activeBranches map[string]struct{}
	closed         bool

	flushGroup singleflight.Group
	stopCh     chan struct{}
	wg         sync.WaitGroup
}

func Open(store objectstore.Store, opts *Options) (Store, error) {
	if store == nil {
		return nil, ErrNilStore
	}

	flushInterval := defaultWALFlushInterval
	if opts != nil && opts.WALFlushInterval > 0 {
		flushInterval = opts.WALFlushInterval
	}

	c := &client{
		store:            store,
		walFlushInterval: flushInterval,
		logs:             make(map[string]*logstream.Log),
		activeBranches:   make(map[string]struct{}),
		stopCh:           make(chan struct{}),
	}

	c.wg.Add(1)
	go c.backgroundFlusher()

	return c, nil
}

func walStream(branch string) string {
	return strings.ReplaceAll(branch, "/", "--")
}

func (c *client) getOrCreateLog(branch string) (*logstream.Log, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	stream := walStream(branch)
	if l, ok := c.logs[stream]; ok {
		return l, nil
	}

	l, err := logstream.New(c.store, stream, logstream.WithPrefix("wal"))
	if err != nil {
		return nil, err
	}
	c.logs[stream] = l
	return l, nil
}

func (c *client) markBranchActive(branch string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.activeBranches[branch] = struct{}{}
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
			c.flushActiveBranches()
		}
	}
}

func (c *client) flushActiveBranches() {
	c.mu.Lock()
	branches := make([]string, 0, len(c.activeBranches))
	for b := range c.activeBranches {
		branches = append(branches, b)
	}
	c.mu.Unlock()

	var g errgroup.Group
	for _, branch := range branches {
		g.Go(func() error {
			return c.flushBranch(branch)
		})
	}
	_ = g.Wait()
}

func (c *client) flushBranch(branch string) error {
	_, err, _ := c.flushGroup.Do(branch, func() (any, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return nil, c.doFlushBranch(ctx, branch)
	})
	return err
}

func (c *client) doFlushBranch(ctx context.Context, branch string) error {
	log, err := c.getOrCreateLog(branch)
	if err != nil {
		return err
	}

	tail, err := log.Tail(ctx)
	if err != nil {
		return err
	}
	if tail == 0 {
		return nil
	}

	for {
		manifestHash, gen, err := resolveBranch(ctx, c.store, branch)
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

		if tail <= lastWalSeq {
			return nil
		}

		newEntries := make(map[string]*kvfspb.ManifestEntry, len(parentManifest.Entries))
		maps.Copy(newEntries, parentManifest.Entries)

		for seq := lastWalSeq + 1; seq <= tail; seq++ {
			records, err := log.Read(ctx, seq)
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
			LastWalSeq: tail,
			Entries:    newEntries,
		}

		newManifestHash, err := putManifest(ctx, c.store, newManifest)
		if err != nil {
			return err
		}

		_, err = updateBranch(ctx, c.store, branch, newManifestHash, gen)
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

	c.mu.Lock()
	branches := make([]string, 0, len(c.activeBranches))
	for b := range c.activeBranches {
		branches = append(branches, b)
	}
	c.mu.Unlock()

	var g errgroup.Group
	for _, branch := range branches {
		g.Go(func() error {
			return c.flushBranch(branch)
		})
	}
	_ = g.Wait()

	return c.store.Close()
}

func (c *client) Branch(ctx context.Context, newBranch, parentBranch string) error {
	_, _, err := createBranch(ctx, c.store, newBranch, parentBranch)
	return err
}

func (c *client) Get(ctx context.Context, branch, key string) (Value, error) {
	if key == "" {
		return Value{}, ErrInvalidKey
	}

	manifestHash, _, err := resolveBranch(ctx, c.store, branch)
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

func (c *client) Set(ctx context.Context, branch, key string, r io.Reader, opts *WriteOptions) error {
	if err := validateBranch(branch); err != nil {
		return err
	}
	if key == "" {
		return ErrInvalidKey
	}
	if r == nil {
		return ErrNilReader
	}

	casHash, size, err := putBlob(ctx, c.store, r)
	if err != nil {
		return err
	}

	syncWrite := true
	if opts != nil {
		syncWrite = opts.Sync
	}

	if !syncWrite {
		mut := &kvfspb.Mutation{
			Key:       key,
			CasHash:   casHash,
			SizeBytes: uint64(size),
		}
		data, err := proto.Marshal(mut)
		if err != nil {
			return err
		}
		log, err := c.getOrCreateLog(branch)
		if err != nil {
			return err
		}
		if _, err := log.Append(ctx, []logstream.Record{data}); err != nil {
			return err
		}
		c.markBranchActive(branch)
		return nil
	}

	entry := &kvfspb.ManifestEntry{
		CasHash:   casHash,
		SizeBytes: uint64(size),
	}

	for {
		manifestHash, gen, err := resolveBranch(ctx, c.store, branch)
		if err != nil && !errors.Is(err, objectstore.ErrNotFound) {
			return err
		}

		var (
			newEntries map[string]*kvfspb.ManifestEntry
			lastWalSeq uint64
		)

		if errors.Is(err, objectstore.ErrNotFound) {
			newEntries = map[string]*kvfspb.ManifestEntry{key: entry}
		} else {
			parentManifest, err := getManifest(ctx, c.store, manifestHash)
			if err != nil {
				return err
			}
			newEntries = make(map[string]*kvfspb.ManifestEntry, len(parentManifest.Entries)+1)
			maps.Copy(newEntries, parentManifest.Entries)
			newEntries[key] = entry
			lastWalSeq = parentManifest.LastWalSeq
		}

		newManifest := &kvfspb.Manifest{
			LastWalSeq: lastWalSeq,
			Entries:    newEntries,
		}

		newManifestHash, err := putManifest(ctx, c.store, newManifest)
		if err != nil {
			return err
		}

		_, err = updateBranch(ctx, c.store, branch, newManifestHash, gen)
		if err == nil {
			return nil
		}
		if !errors.Is(err, objectstore.ErrPreconditionFailed) {
			return err
		}
	}
}

func (c *client) Delete(ctx context.Context, branch, key string, opts *WriteOptions) error {
	if err := validateBranch(branch); err != nil {
		return err
	}
	if key == "" {
		return ErrInvalidKey
	}

	syncWrite := true
	if opts != nil {
		syncWrite = opts.Sync
	}

	if !syncWrite {
		mut := &kvfspb.Mutation{
			Key:       key,
			Tombstone: true,
		}
		data, err := proto.Marshal(mut)
		if err != nil {
			return err
		}
		log, err := c.getOrCreateLog(branch)
		if err != nil {
			return err
		}
		if _, err := log.Append(ctx, []logstream.Record{data}); err != nil {
			return err
		}
		c.markBranchActive(branch)
		return nil
	}

	for {
		manifestHash, gen, err := resolveBranch(ctx, c.store, branch)
		if err != nil {
			return err
		}

		parentManifest, err := getManifest(ctx, c.store, manifestHash)
		if err != nil {
			return err
		}

		if _, ok := parentManifest.Entries[key]; !ok {
			return objectstore.ErrNotFound
		}

		newEntries := make(map[string]*kvfspb.ManifestEntry, len(parentManifest.Entries)-1)
		for k, v := range parentManifest.Entries {
			if k != key {
				newEntries[k] = v
			}
		}

		newManifest := &kvfspb.Manifest{
			LastWalSeq: parentManifest.LastWalSeq,
			Entries:    newEntries,
		}

		newManifestHash, err := putManifest(ctx, c.store, newManifest)
		if err != nil {
			return err
		}

		_, err = updateBranch(ctx, c.store, branch, newManifestHash, gen)
		if err == nil {
			return nil
		}
		if !errors.Is(err, objectstore.ErrPreconditionFailed) {
			return err
		}
	}
}
