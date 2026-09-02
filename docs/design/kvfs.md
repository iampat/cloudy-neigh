# Branching Key-Value Store

## Problem

Modern distributed applications require versioned key-value storage directly on
cloud object storage without running a dedicated database cluster. They need
Git-like branching to support isolated workspaces, reproducible experiments, and
zero-copy dataset forks.

Cloud object storage (GCS, AWS S3) presents fundamental constraints:
1. Object storage supports only full-object writes and immutable reads. It
   provides no append operations, partial updates, or multi-key transactions.
2. Latency is high: a single read or write costs 20 ms to 40 ms of round-trip
   time.
3. Mutations on any single object key are rate-limited to approximately 1 write
   per second before HTTP 429 and contention errors occur.

A naive key-value design stores the full database state in a single manifest
file and rewrites that file on every mutation. This naive approach fails:
- **Write amplification grows with database size:** Writing a 100-byte value in
  a 100,000-key table rewrites a 7 MB manifest on every put.
- **Concurrency collapses:** Multiple concurrent writers on the same branch
  encounter continuous compare-and-swap conflicts on the root key.

This document defines the architecture, protobuf schema, batching semantics,
multi-layer caching, and Go API for the Key-Value Filesystem (KVFS).

## Goals

- Atomic multi-key batch writes with single-round-trip commits.
- Zero-copy, constant-time branch creation from existing snapshots.
- Sub-microsecond read latency for cached working sets.
- Content-addressed deduplication of values across branches and keys.
- Protection against thundering herds on concurrent cache misses.
- Both synchronous direct commits and asynchronous write-ahead log ingestion.

## Non-goals

- Ordered range scans and prefix iteration in version 1.
- Inlined values in the manifest in version 1. Every value is a content-addressed blob.
- Automatic compaction of multi-level SSTables in version 1.

## Future work

- Pebble-backed local SSTable engines for datasets exceeding 100,000 keys.
- Range scan iterators across branch snapshots.
- Value inlining in manifests for high-density small-key tables (see Appendix A).
- Garbage collection of orphaned blobs.

## Model

A **branch** is an isolated, named line of history identified by a reference
under `refs/heads/<branch>`.

A **manifest** is an immutable, content-addressed snapshot mapping keys to
payload content hashes.

A **blob** is an immutable raw payload stored under `objects/` and named by its
SHA-256 hash.

A **batch** is a staged set of mutations applied atomically to a branch.

```text
┌─────────────────────────────────────────────────────────────┐
│ Layer 2: Branching Key-Value Store (KVFS)                   │
│                                                             │
│   ┌───────────────┐     ┌───────────────┐     ┌──────────┐  │
│   │ Memory Cache  │ ──► │  Disk Cache   │ ──► │ GCS / S3 │  │
│   │ (L1 In-Memory)│     │ (L2 SSD/Local)│     │ (L0 CAS) │  │
│   └───────────────┘     └───────────────┘     └──────────┘  │
│           │                     │                   │       │
│           └──────────────┬──────┴───────────────────┘       │
│                          ▼                                  │
│                 singleflight.Group                          │
│             (Thundering Herd Defense)                       │
└──────────────────────────────┬──────────────────────────────┘
                               │
                               ▼
┌─────────────────────────────────────────────────────────────┐
│ Layer 0 / Layer 1: ObjectStore / LogStream                  │
└─────────────────────────────────────────────────────────────┘
```

## Storage layout

All data and metadata objects are immutable, content-addressed files except the
branch pointer:

```text
[Bucket Root]
├── refs/heads/                  <-- Mutable branch heads (conditional CAS)
│   ├── main                     --> "1a2beff8..." (Manifest SHA-256 Hash)
│   └── feature-1                --> "3c4d5e6f..."
│
├── manifests/                   <-- Immutable Content-Addressed Manifests
│   └── 1a/2b/1a2beff890...      --> Protobuf Manifest binary
│
└── objects/                     <-- Immutable Content-Addressed Payloads
    └── a3/f1/a3f1c8901b...      --> Raw binary payload (Docs, Media, Vectors)
```

## Schema

Persistent metadata uses Protocol Buffers.

```proto
syntax = "proto3";

package kvfs.v1;

option go_package = "github.com/iampat/cloudy-neigh/proto/kvfs/v1;kvfspb";

message ManifestEntry {
  string blob_hash = 1;
  uint64 size_bytes = 2;
}

message Manifest {
  uint64 last_wal_seq = 1;
  map<string, ManifestEntry> entries = 2;
}

message Mutation {
  string key = 1;
  string blob_hash = 2;
  uint64 size_bytes = 3;
  bool tombstone = 4;
}
```

## Write path and batching

KVFS provides two write modes: Direct Synchronous Batching and Asynchronous WAL
Streaming.

### Direct synchronous batching

Direct mode commits mutations atomically without an intermediary write-ahead log:

```text
Client -> Batch.Set(k1, v1) -> Upload objects/<hash1> (parallel)
       -> Batch.Set(k2, v2) -> Upload objects/<hash2> (parallel)
       -> Batch.Commit()    -> Download parent manifest
                            -> Apply staged changes in memory
                            -> Upload new manifest manifests/<new_hash>
                            -> Put(refs/heads/<branch>, if-match=old_gen)
```

1. The client stages mutations into a `Batch`.
2. Payloads are uploaded directly to `objects/<h0>/<h1>/<hash>` concurrently with
   precondition `Absent: true`.
3. On `Commit`, the writer downloads the latest manifest for the branch.
4. The writer constructs a new `Manifest` protobuf in memory.
5. The writer uploads the new manifest to `manifests/<hash>`.
6. The writer atomically updates `refs/heads/<branch>` with a generation precondition.
7. If a conflict occurs (HTTP 412), the batch reloads the new manifest and retries.

### Asynchronous log streaming

For high-concurrency ingestion where multiple writers exceed 3 writes per second,
mutations append to LogStream at `wal/<branch>/`.

When committing an asynchronous batch:
1. The batch uploads all staged payloads to `objects/` concurrently.
2. The batch serializes each staged mutation into a `kvfs.v1.Mutation` protobuf record.
3. The batch passes all serialized records to `LogStream.Append(ctx, records...)` in a single call.
4. LogStream writes all records into one `.recordio` WAL segment in one round-trip.
5. A background committer periodically reads new WAL segments and applies them to new manifest snapshots.

## Read path and multi-layer caching

Reads check local caches in sequence before making cloud requests:

```text
Read Key
   │
   ├─► 1. L1 Memory Cache (Hit ──► Return value)
   │
   ├─► 2. L2 Local Disk Cache (Hit ──► Populate L1 ──► Return value)
   │
   └─► 3. Singleflight Coalescer
          │
          ├── Resolve branch pointer from refs/heads/<branch>
          ├── Fetch manifest from manifests/<hash>
          ├── Fetch blob from objects/<hash>
          └── Populate L2 Disk and L1 Memory caches
```

Concurrent cache misses for the same key or manifest join a single in-flight
fetch via `singleflight.Group`. This prevents thundering herds on cold starts.

## Cache invalidation

Manifests and data blobs are content-addressed and immutable. They never become
stale and can remain cached indefinitely.

Cache freshness follows three rules:

1. **Local write-through:** When a local process commits a write, it immediately
   updates its in-memory and local disk cache with the new manifest.
2. **Lease TTL:** A cached manifest is considered fresh for a configurable TTL
   (such as 500 ms). Reads during this window require zero network requests.
3. **Header validation on expiry:** When the lease expires, the reader executes
   `Store.Stat("refs/heads/<branch>")`. If the generation matches, the lease
   renews without downloading any data. If the generation changed, the reader
   fetches the new manifest.

## Branching

Branch creation is an atomic, zero-copy metadata operation:

1. Read `refs/heads/<parent_branch>` to obtain current manifest hash `M`.
2. Write `refs/heads/<new_branch>` with value `M` using precondition `Absent: true`.
3. The child branch immediately shares all existing manifests and blobs.
4. The operation completes in one round trip with zero data copying.

## API

The API mirrors standard key-value conventions:

```go
package kvstore

import (
	"context"
	"io"

	"github.com/iampat/cloudy-neigh/objectstore"
)

type Value struct {
	Data io.ReadCloser
	Size int64
}

type Batch interface {
	// Set stages a key-value write in the batch.
	Set(key string, r io.Reader, size int64) error

	// Delete stages a key deletion in the batch.
	Delete(key string)

	// Commit atomically applies all staged mutations to the branch.
	Commit(ctx context.Context) error

	// Close discards uncommitted mutations and releases resources.
	Close() error
}

type Store interface {
	// Get retrieves a key's payload from a branch snapshot.
	Get(ctx context.Context, branch, key string) (Value, error)

	// Set writes a single key-value pair synchronously.
	Set(ctx context.Context, branch, key string, r io.Reader, size int64) error

	// Delete removes a single key synchronously.
	Delete(ctx context.Context, branch, key string) error

	// NewBatch allocates an atomic mutation batch for a branch.
	NewBatch(branch string) Batch

	// Branch creates a new branch pointer from a parent in O(1) time.
	Branch(ctx context.Context, newBranch, parentBranch string) error

	// Close releases cache and background resources.
	Close() error
}

func Open(store objectstore.Store, opts ...Option) (Store, error)
```

## Appendix A: Inline value threshold analysis

Inlining small payloads directly into the manifest avoids a second round-trip
for blob fetches. But inlining increases manifest size:

- A pure content-addressed manifest of 100,000 keys requires approximately 7 MB.
- Inlining 4 KB values across 100,000 keys expands the manifest to 400 MB.

Downloading 400 MB on cold starts adds seconds of latency and burns memory.
Version 1 stores all payloads in `objects/`.

Inlining will be revisited in future versions when manifest range partitioning
is introduced.
