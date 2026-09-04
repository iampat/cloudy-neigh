# Storage and Query Simplification

**Status:** Accepted, 2026-09-03, v1

## Problem

The current storage design has three limitations that degrade read latency on
Google Cloud Storage (GCS):

1. Cold reads execute three sequential GCS round trips (`refs/heads` to
   `manifests/` to `cas/`), causing 60 to 90 ms latency.
2. Reads execute a network check on `refs/heads` on every call, preventing
   zero-network query execution.
3. The manifest stores a flat key map that grows to 106 MB for one million
   documents, causing high CPU unmarshaling and memory pressure.

This document specifies five architectural simplifications to achieve sub-2 ms
query latency on Google infrastructure backed by GCS without external
coordination services.

## Model

```text
Current Architecture (3 GCS Hops, Multi-WAL, Monolithic Manifest):
Query ──▶ refs/heads (25ms) ──▶ manifests/ CAS (25ms) ──▶ cas/ Blob (25ms)
Ingestion ──▶ KVFS Store ──▶ Per-Branch WAL ──▶ Flush to CAS ──▶ Map Manifest

Target Architecture (Zero-RTT Hot Path, 1 Metadata Hop, Global WAL):
Query ──▶ Pinned Snapshot Pointer ──▶ In-Memory Memtable (<1ms, 0 GCS Hops)
                                  ──▶ Local NVMe SSD Cache (<100µs, 0 GCS Hops)
Cold Miss ──▶ refs/heads (<=14KB Manifest) (25ms) ──▶ Range Read (25ms)
Ingestion ──▶ Single Global WAL ──▶ Memtable ──▶ Columnar Files (.vec/.post/.doc)
```

## 1. Collapse 3-Hop Read Chain into Branch Reference

### Problem

`kvfs/store.go` executes three sequential round trips for cold reads:
1. `store.Get` on `refs/heads/<branch>` to resolve the manifest hash.
2. `store.Get` on `manifests/<hash>` to fetch the manifest protobuf.
3. `store.Get` on `cas/<hash>` to fetch the payload blob.

On GCS, intra-region time to first byte (TTFB) is 20 to 30 ms. Three round trips
require 60 to 90 ms.

### Design

Write the segment descriptor list directly into `refs/heads/<branch>`.
Eliminate the intermediate `manifests/` content-addressed storage (CAS) layer.
One GCS read returns both the branch generation and the segment list.

Retain three columnar files per segment:
1. `segments/<id>.vec`: dense float vectors.
2. `segments/<id>.post`: inverted index postings and term dictionaries.
3. `segments/<id>.doc`: document attributes and text payloads.

Separate files preserve cache isolation. Vector searches download `.vec` files
without polluting local memory or NVMe cache with document text.

```proto
syntax = "proto3";

package cloudyneigh.storage.v1;

option go_package = "github.com/iampat/cloudy-neigh/proto/storage/v1;storagepb";

message SegmentRef {
  string segment_id = 1;
  uint64 min_doc_id = 2;
  uint64 max_doc_id = 3;
  uint64 doc_count = 4;
  uint32 level = 5;
  int64 vectors_size = 6;
  int64 postings_size = 7;
  int64 docs_size = 8;
}

message BranchManifest {
  uint64 checkpoint_seq = 1;
  uint64 schema_version = 2;
  repeated SegmentRef segments = 3;
}
```

Bound manifests to 14 KiB (about 150 segments). This fits in the TCP initial
window (IW10) of 10 packets, ensuring single round-trip transfers on cold
connections.

## 2. Zero-RTT Hot Reads via Global WAL Tailing

### Problem

`kvfs/store.go` checks `refs/heads/<branch>` on every read call. Even when a
manifest is cached in memory, the read incurs a 20 to 30 ms network check.
Writes committed to the write-ahead log (WAL) remain invisible until flushes
commit to GCS.

### Design

Query nodes continuously tail the global WAL in the background and update an
in-memory `activeMemtable`.

Query nodes maintain a single atomic pointer to a composite view:

```go
type EngineView struct {
	Snap *Snapshot
	Mem  *Memtable
}

type Engine struct {
	view    atomic.Pointer[EngineView]
	lastSeq atomic.Uint64
}
```

Updating `Snap` and `Mem` atomically prevents split-state race conditions during
flushes.

A query pins the snapshot using a reader lease:

```go
type ReaderLease struct {
	snap     *Snapshot
	released atomic.Bool
}

func (s *Snapshot) AcquireWithLease() *ReaderLease {
	s.refCount.Add(1)
	return &ReaderLease{snap: s}
}

func (l *ReaderLease) Release() {
	if l.released.CompareAndSwap(false, true) {
		l.snap.refCount.Add(-1)
	}
}
```

Queries execute across the in-memory `activeMemtable` and immutable local
segments. When the client supplies `min_seq`, the node executes immediately if
`lastSeq >= min_seq`. Read latency drops to under 2 ms with zero network calls.

Compaction sets a tombstone with timestamp `dropped_at` on replaced segments.
Segments remain protected by a 2-hour retention time to live (TTL). Segments are
deleted only when `refCount == 0` and the retention period has expired.

## 3. Bounded Segment Descriptors and Footer Metadata

### Problem

`proto/kvfs/v1/kvfs.proto` defines `Manifest` as `map<string, ManifestEntry>`.
At one million documents, the manifest reaches 106 MB. Unmarshaling takes 800
to 2200 ms of CPU time and 400 MB of heap memory.

### Design

Track segments instead of individual documents. A segment groups 50,000 to
200,000 documents. At 10 million documents, 100 segments produce a 15 KiB
manifest.

Store secondary block indexes and Split-Block Bloom filters in segment footers
(the last 4 to 16 KiB of the file). The root manifest tracks only segment
boundaries and column file locations.

```proto
syntax = "proto3";

package cloudyneigh.storage.v1;

message BlockEntry {
  uint64 min_doc_id = 1;
  uint64 max_doc_id = 2;
  uint64 file_offset = 3;
  uint32 compressed_length = 4;
  uint32 uncompressed_length = 5;
}

message SegmentFooter {
  uint32 bloom_filter_bytes = 1;
  repeated BlockEntry block_index = 2;
  uint64 tombstone_count = 3;
}
```

Use a 2-3 level Log-Structured Merge (LSM) compaction hierarchy to keep the
active segment count bounded to 20 or fewer segments. Track document deletions
with Roaring Bitsets. Purge tombstones during Level 2 compaction.

## 4. Google Cloud Storage Engine with Local NVMe SSD Cache

### Problem

`objectstore.Store` lacks range read support. Reading a 4 KiB footer or 128 KiB
chunk downloads the entire 64 MiB segment file, requiring 400 to 800 ms.

### Design

Add `ReadRange` to `objectstore.Store`:

```go
type Store interface {
	io.Closer
	Stat(ctx context.Context, key string) (Object, error)
	Get(ctx context.Context, key string) (io.ReadCloser, Object, error)
	ReadRange(ctx context.Context, key string, offset, length int64) (io.ReadCloser, Object, error)
	Put(ctx context.Context, key string, r io.Reader, cond Condition) (string, error)
	Delete(ctx context.Context, key string) error
	Exists(ctx context.Context, key string) (bool, error)
	List(ctx context.Context, prefix, startAfter string, limit int) ([]Object, error)
}
```

Store segment files uncompressed in GCS. Compress internal blocks individually
with Zstandard (Zstd) at 128 KiB (docs) and 256 KiB (vectors and postings).

Deploy an ephemeral local NVMe solid-state drive (SSD) cache on GCE instances:
1. Cache chunks at `<cache_dir>/chunks/<seg_id>/<col>.<offset>_<len>.chunk`.
2. Stage writes to `<cache_dir>/tmp/` on the same filesystem. Use atomic POSIX
   rename to prevent torn reads.
3. Coalesce concurrent cache misses using `singleflight.Group`.
4. Read local chunks using `os.File.ReadAt` (`pread`) guarded by a 256-token
   semaphore. Avoid `mmap` to prevent unnotified Go scheduler thread stalls on
   major page faults.
5. Apply dynamic tail latency hedging at the rolling p95 latency. Cap extra
   requests to a 5% budget. Pause hedging on HTTP 429 and 503 errors.

## 5. Merge KVFS into Ingestion with Single Global WAL

### Problem

`wal.md` and `kvfs/store.go` define per-branch WAL paths (`wal/<branch>/`), but
`ingestion.md` mandates a single global WAL (`wal/<020d_seq>.recordio`). Tailing
hundreds of branch prefixes causes high GCS List operation costs.

### Design

Delete `kvfs/store.go`. Standardize on a single global append-only log using
`logstream.Log`.

Retain branch compare-and-swap mechanics (`kvfs/branch.go`) using GCS generation
match on `refs/heads/<branch>`.

Ingestion workers tail the global WAL, route mutations to in-memory Memtables,
and flush columnar segments directly to GCS.

```text
Consolidated Layout:
[Bucket Root]
├── refs/heads/               <-- Inlined Manifests (<14 KiB)
│   ├── main                  --> {checkpoint_seq: 104, segments: [...]}
│   └── dev                   --> {checkpoint_seq: 82,  segments: [...]}
├── segments/                 <-- Columnar Files (128/256 KiB blocks)
│   ├── seg_001.vec           --> Dense float vectors
│   ├── seg_001.post          --> Inverted postings & dictionaries
│   └── seg_001.doc           --> Document attributes & footers
└── wal/                      <-- Single Global WAL
    ├── 00000000000000000001.recordio
    └── 00000000000000000002.recordio
```

To protect shared copy-on-write segments across branches, garbage collection
scans all active branch heads under `refs/heads/*` before deleting unreferenced
segments whose 2-hour retention period has expired.

## Latency Summary

| Query Path | Existing Latency | Proposed Latency | GCS Network Hops |
| :--- | :--- | :--- | :--- |
| **Hot Query (In-Memory Memtable)** | 20 to 60 ms | < 2 ms | **0 hops** |
| **Warm Query (Local NVMe SSD Hit)** | 20 to 60 ms | < 100 µs | **0 hops** |
| **Warm Query (Remote Column Chunk)** | 40 to 60 ms | 20 to 25 ms | **1 hop** |
| **Cold Query (Manifest + Column Chunk)** | 60 to 90 ms | 40 to 55 ms | **2 hops** |
| **Tail Latency (p99 Stragglers)** | 150+ ms | ~45 ms | **Dynamic Hedging** |
