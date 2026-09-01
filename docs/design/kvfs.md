# Branching Key-Value Store

## Problem

A cloud-native search engine requires durable, versioned key-value storage
directly on object storage without a dedicated database cluster. It needs
Git-like branching to support isolated index experiments and zero-copy forks.

Object storage lacks append operations and charges latency for every round trip.
Naive single-file manifests cause large write amplification when key counts
grow. Direct client writes also encounter write-rate limits on single object
keys.

This document defines the storage layout, protobuf wire formats, caching
topology, and Go API for the Key-Value Filesystem (KVFS). KVFS is Layer 2 of
[the storage architecture](storage.md), built on top of [LogStream](wal.md).

## Goals

- Zero-copy, constant-time branch creation from existing snapshots.
- Durable mutation logging through LogStream using RecordIO containers.
- Content-addressed storage for values larger than 4 KB to prevent manifest bloat.
- Multi-tier cache support (in-memory L1 and local disk L2) to eliminate cloud RTTs.
- Protection against thundering herds on concurrent cache misses.
- Both synchronous and asynchronous commit pipelines.

## Non-goals

- Ordered range scans and prefix iteration in version 1.
- Automatic compaction of multi-level SSTables in version 1.
- In-place mutation of stored blobs. Every object in CAS is immutable.

## Future work

- Pebble-backed SSTable range partitions for scaling past 100,000 keys.
- Sorted range scans and prefix iterators across branch snapshots.
- Garbage collection of unreachable CAS blobs.

## Model

A **branch** is an isolated, named mutation stream identified by a reference
under `refs/heads/<branch>`.

A **manifest** is an immutable, content-addressed snapshot of all active keys on
a branch at a specific sequence number.

A **mutation** is an atomic write or delete record appended to the branch log.

A **blob** is an immutable raw payload stored under `objects/` and addressed by
its SHA-256 hash.

```text
┌─────────────────────────────────────────────────────────────┐
│ Layer 2: Branching KV Store (KVFS)                          │
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
│ Layer 1: LogStream (wal/<branch>/<020d_seq>.recordio)       │
└─────────────────────────────────────────────────────────────┘
```

## Storage layout

```text
[Bucket Root]
├── refs/heads/
│   ├── main                     --> "1a2beff8..." (Manifest SHA-256 Hash)
│   └── feature-1                --> "3c4d5e6f..."
│
├── manifests/
│   └── 1a/2b/1a2beff890...      --> Protobuf Manifest binary
│
├── objects/                     <-- Content-Addressed Payloads (> 4 KB)
│   └── a3/f1/a3f1c8901b...      --> Raw binary payload (Docs, Media)
│
└── wal/                         <-- Layer 1 LogStream
    └── main/
        └── 00000000000000000001.recordio
```

## Schema

All persistent metadata records use Protocol Buffers.

```proto
syntax = "proto3";

package kvfs.v1;

option go_package = "github.com/iampat/cloudy-neigh/proto/kvfs/v1;kvfspb";

message KVMutation {
  string key = 1;
  uint64 size_bytes = 2;

  oneof value {
    bytes inline_value = 3;
    string blob_hash = 4;
    bool tombstone = 5;
  }
}

message ManifestEntry {
  uint64 size_bytes = 1;

  oneof value {
    bytes inline_value = 2;
    string blob_hash = 3;
  }
}

message Manifest {
  uint64 last_wal_seq = 1;
  map<string, ManifestEntry> entries = 2;
}
```

## Value separation

Values up to 4 KB are inlined directly into `KVMutation` and `ManifestEntry`.

Values larger than 4 KB are stored as standalone SHA-256 objects in `objects/`.
The manifest stores only the 32-byte content hash. This separation limits
manifest growth during large document and vector writes.

Embeddings with multiple dense vectors store the entire vector array as one
contiguous binary payload in `objects/`.

## Write path

A write operation proceeds as follows:

1. If value size is larger than 4 KB, compute the SHA-256 hash of the payload.
2. Upload the payload to `objects/<h0>/<h1>/<hash>` with precondition `Absent: true`.
3. Construct a `KVMutation` with the inline bytes or the content hash.
4. Serialize the mutation to protobuf and append it to `wal/<branch>/` through LogStream.
5. In asynchronous mode, the call returns once LogStream acknowledges the write.
6. In synchronous mode, the writer flushes a new manifest snapshot and advances `refs/heads/<branch>` before returning.

## Committer and snapshot isolation

An asynchronous committer process materializes state from LogStream:

1. Read the current branch manifest hash from `refs/heads/<branch>`.
2. Read mutations sequentially from `wal/<branch>/` starting after `Manifest.last_wal_seq`.
3. Apply mutations in memory to update the key map.
4. Serialize the updated `Manifest` to protobuf.
5. Compute the manifest SHA-256 hash and upload it to `manifests/<m0>/<m1>/<hash>`.
6. Conditionally update `refs/heads/<branch>` with the new manifest hash.

## Read path and multi-layer caching

Reads check caches before querying cloud object storage:

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
          ├── If inline, extract value
          ├── If blob, fetch objects/<hash>
          └── Populate L2 Disk and L1 Memory caches
```

Concurrent requests for the same missing key join the in-flight fetch through
`singleflight.Group`. This prevents thundering herds on cold starts.

## Branching

Branch creation is an atomic metadata operation:

1. Read `refs/heads/<parent_branch>` to get parent manifest hash `M`.
2. Write `refs/heads/<new_branch>` with value `M` using precondition `Absent: true`.
3. The new branch immediately shares all parent objects and manifests.
4. The operation completes in one round trip without data copying.

## API

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

type WriteOption func(*WriteOptions)

type WriteOptions struct {
	Sync bool
}

func WithSync(sync bool) WriteOption {
	return func(o *WriteOptions) {
		o.Sync = sync
	}
}

type Store interface {
	Get(ctx context.Context, branch, key string) (Value, error)
	Put(ctx context.Context, branch, key string, r io.Reader, size int64, opts ...WriteOption) error
	Delete(ctx context.Context, branch, key string, opts ...WriteOption) error
	Branch(ctx context.Context, newBranch, parentBranch string) error
	Flush(ctx context.Context, branch string) (string, error)
	Close() error
}

func New(store objectstore.Store, opts ...Option) (Store, error)
```
