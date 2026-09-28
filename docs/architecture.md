# System Architecture

Cloudy-neigh is a cloud-native vector search engine in Go. It decouples compute
from storage and uses cloud object storage as the single source of truth.
Stateless ingest and query nodes persist write-ahead logs, immutable segments,
and branch manifests to object storage.

---

## 1. Overview and High-Level Topology

The system relies on cloud object storage for durability and state
coordination. No local disks, Raft clusters, or coordination daemons exist.

Core architectural tenets:
- **Decoupled compute and storage**: Ingest and query nodes run as independent,
  stateless processes.
- **Object storage as truth**: Cloud object storage stores all logs, segments,
  and branch manifests.
- **Multi-tenant key isolation**: Storage prefixes isolate tenant namespaces.
  Each tenant root contains independent catalogs and data paths.
- **Stateless execution**: Nodes can crash or restart without data loss.

```text
                      ┌────────────────────────┐
                      │      gRPC Client       │
                      └───────────┬────────────┘
                                  │
                 ┌────────────────┴────────────────┐
                 │ Upsert / Fork / Delete          │ Query
                 ▼                                 ▼
   ┌───────────────────────────┐     ┌───────────────────────────┐
   │        Ingest Node        │     │        Query Node         │
   │                           │     │                           │
   │  ┌─────────────────────┐  │     │  ┌─────────────────────┐  │
   │  │  grpcapi.IngestSvc  │  │     │  │  grpcapi.QuerySvc   │  │
   │  └──────────┬──────────┘  │     │  └──────────┬──────────┘  │
   │             ▼             │     │             ▼             │
   │  ┌─────────────────────┐  │     │  ┌─────────────────────┐  │
   │  │   ingest.Ingester   │  │     │  │    query.Engine     │  │
   │  └──────────┬──────────┘  │     │  └──────────┬──────────┘  │
   │             ▼             │     │             ▼             │
   │  ┌─────────────────────┐  │     │  ┌─────────────────────┐  │
   │  │   ingest.Flusher    │  │     │  │  query.Table / Dist │  │
   │  └──────────┬──────────┘  │     │  └──────────┬──────────┘  │
   └─────────────┼─────────────┘     └─────────────┼─────────────┘
                 │                                 │
                 ▼                                 ▼
   ┌─────────────────────────────────────────────────────────────┐
   │                    Cloud Object Storage                     │
   │            <tenant-storage-root>/ns/<namespace>/            │
   │  wal/             segments/          branches.json  refs/   │
   │  <seq>.recordio   <sha256>.recordio                 heads/  │
   └─────────────────────────────────────────────────────────────┘
```

---

## 2. Process Boundaries and Component Layering

The command-line interface executable `cloudy` provides two primary daemons:
- `cloudy ingest`: Runs the Remote Procedure Call (gRPC) ingestion service and
  background flusher.
- `cloudy query`: Runs the gRPC query service and incremental table sync loop.

The system implements a three-process operational model:
1. **Ingest Engine (Process 1)**: Accepts document writes over gRPC. Appends
   batches directly to the Write-Ahead Log (WAL).
2. **Flusher (Process 2)**: Tails the WAL across branches. Materializes
   immutable segments and updates branch manifests via Compare-And-Swap (CAS).
3. **Query Engine (Process 3)**: Polls manifests from object storage. Streams
   new segments into flat in-memory tables to serve searches.

```text
┌─────────────────────────────────────────────────────────────────────────────┐
│ Layer 2: Segments and Manifests (segment/, manifest/, namespace/, ingest/)  │
│ • Immutable RecordIO segments: ns/<ns>/segments/<sha256>.recordio           │
│ • Branch manifests: ns/<ns>/refs/heads/<branch>.json in protojson           │
│ • Branch names: ns/<ns>/branches.json                                       │
│ • Zero-copy branch forks with Compare-And-Swap manifest updates             │
└──────────────────────────────────────┬──────────────────────────────────────┘
                                       │
                                       ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│ Layer 1: Global Write-Ahead Log (logstream/)                                │
│ • One log per namespace: ns/<ns>/wal/<020d_seq>.recordio                    │
│ • Conditional object creation: If-Generation-Match=0 (Absent: true)         │
│ • Log-scale tail discovery via exponential probe and binary search          │
└──────────────────────────────────────┬──────────────────────────────────────┘
                                       │
                                       ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│ Layer 0: Object Store Adapter (objectstore/)                                │
│ • Drivers: GCS, local disk, and memory                                      │
│ • Atomic preconditions: Absent (create-if-not-exists) and GenerationMatch   │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Component Package Index

| Package | Layer | Role |
| --- | --- | --- |
| `cmd/cloudy` | CLI | Subcommands for `ingest` and `query` daemons |
| `grpcapi` | Service | gRPC IngestService and QueryService endpoints |
| `namespace` | Catalog | Namespace validation, default fallbacks, keys |
| `ingest` | Pipeline | Direct batch ingestion and background flusher |
| `logstream` | WAL | Monotonic append-only log over RecordIO |
| `recordio` | Framing | Framed binary reader, writer, and scanner |
| `query` | Engine | Columnar in-memory table and manifest loader |
| `vector` | Math | Distance kernels for pure Go and SIMD variants |
| `segment` | Storage | Segment encoding and decoding over RecordIO |
| `manifest` | Storage | Branch manifest read and CAS write |
| `objectstore` | Storage | Storage drivers for GCS, local disk, memory |

---

## 3. Storage Hierarchy and Invariants

Cloud object storage isolates data through path prefixes:

```text
<tenant-storage-root>/
├── ns.json
└── ns/
    └── <namespace>/
        ├── wal/
        │   ├── 00000000000000000001.recordio
        │   ├── 00000000000000000002.recordio
        │   └── 00000000000000000003.recordio
        ├── segments/
        │   ├── <sha256-a>.recordio
        │   └── <sha256-b>.recordio
        ├── branches.json
        └── refs/
            └── heads/
                ├── main.json
                └── <branch>.json
```

Directory roles:
- `ns.json`: Tenant catalog tracking active and deleted namespaces.
- `ns/<namespace>/wal/`: Append-only WAL files named by sequence.
- `ns/<namespace>/segments/`: Immutable segment files named by SHA-256 hash.
- `ns/<namespace>/branches.json`: Branch catalog mapping names to metadata.
- `ns/<namespace>/refs/heads/<branch>.json`: Branch manifest in protojson.

### Branch Manifest Schema

The manifest records the checkpoint sequence and segment references:

```json
{
  "checkpoint_seq": "996",
  "schema_version": "1",
  "segments": [
    {
      "segment_id": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4",
      "doc_count": "1000",
      "docs_size": "5242880"
    }
  ]
}
```

### Core Storage Invariants

1. **WAL records are immutable**: Once written, a WAL sequence file is never
   modified or overwritten.
2. **Segment blobs are content-addressed**: Segment files hold immutable
   mutation batches. The segment key uses the SHA-256 hash of its bytes.
   Replaying an existing segment key succeeds idempotently.
3. **Manifests are ordered lists**: A manifest contains an ordered list of
   segment references. The flusher appends entries. The query loader applies
   entries in order.
4. **Branch heads advance monotonically**: Manifest commits update
   `refs/heads/<branch>.json` via atomic CAS generation checks.
5. **Branch catalog authority**: Active branches are cataloged in
   `branches.json`. The catalog stores branch names, not storage keys.
6. **Namespace owns key layout**: Package `namespace` builds all storage keys.
   No component parses storage paths to extract namespaces or branches.

---

## 4. Binary Container Framing (`recordio`)

Every persistent blob in the WAL and segment store uses the RecordIO container
format. Each frame provides 16 bytes of framing overhead.

```text
┌─────────────────┬──────────────────┬───────────────────┬──────────────────┐
│ Length (8B LE)  │ Header CRC (4B)  │  Payload (NB)     │ Data CRC (4B)    │
└─────────────────┴──────────────────┴───────────────────┴──────────────────┘
│◀──────── 12-byte Header ──────────▶│                   │◀─ 4-byte Footer ─▶│
```

Header layout:
- 8 bytes: Payload length encoded as little-endian uint64.
- 4 bytes: Masked Castagnoli CRC-32C computed over the 8 length bytes.

Payload layout:
- $N$ raw bytes of serialized Protocol Buffers.

Footer layout:
- 4 bytes: Masked Castagnoli CRC-32C computed over payload bytes.

Checksum masking rotates bits and adds a constant. This prevents nested CRC
collisions:

```go
func computeMaskedCRC(data []byte) uint32 {
	crc := crc32.Checksum(data, castagnoliTable)
	return ((crc >> 15) | (crc << 17)) + 0xa282ead8
}
```

Scanner corruption taxonomy:
- Header CRC mismatch returns `ErrHeaderCorrupted`.
- Footer CRC mismatch returns `ErrDataCorrupted`.
- Incomplete payload at file tail returns `ErrTornWrite`.
- Clean end of file at a 16-byte boundary returns `io.EOF`.

---

## 5. Ingestion Pipeline (Write Path)

Clients write document mutations and branch lifecycle events over gRPC.

```text
┌─────────────┐
│ gRPC Client │
└──────┬──────┘
       │ Upsert(doc) / Delete(doc) / Fork(branch)
       ▼
┌─────────────────────────┐
│  grpcapi.IngestServer   │ Validates namespace and branch defaults
└──────┬──────────────────┘
       │ Direct batch append
       ▼
┌─────────────────────────┐
│     ingest.Ingester     │ Enforces linearizability and branch state
└──────┬──────────────────┘
       │ Append(record)
       ▼
┌─────────────────────────┐
│      logstream.Log      │ Append-only WAL over RecordIO frames
└──────┬──────────────────┘
       │ wal/<seq>.recordio
       ▼
┌─────────────────────────┐
│    objectstore.Store    │ Google Cloud Storage, local disk, or memory
└─────────────────────────┘
       ▲
       │ Tail WAL (seq >= checkpoint_seq + 1)
┌──────┴──────────────────┐
│     ingest.Flusher      │ Buffers mutations per sequence in memory
└──────┬──────────────────┘
       │ Flush on sequence commit or shutdown
       ├──▶ Writes segments/<sha256>.recordio with segment.Writer
       ├──▶ Updates refs/heads/<branch>.json manifest with manifest.Write
       └──▶ Registers branch in branches.json with Scope.AddBranch
```

### Direct Batch Ingestion

Ingestion avoids intermediate buffering or memtable queuing:
1. The client sends a batch of document records to `IngestService.Upsert`.
2. `IngestServer` validates namespace, branch, and document identifiers.
3. The server ensures the namespace exists in `ns.json`.
4. `ingest.Ingester` marshals records into WAL envelopes.
5. `logstream.Log` appends the batch directly to object storage.
6. The client receives confirmation only after the WAL sequence commits.

### WAL Append and Tail Discovery

WAL objects use 20-digit zero-padded sequence names:
`wal/<020d_seq>.recordio`. Lexicographical sort order matches sequence order.

```text
Writer                          Object Store
  │                                   │
  ├────── Put(seq=4, Absent=true) ───▶│
  │◀───── 200 OK ─────────────────────┤ (Committed atomically)
  │                                   │
  ├────── Put(seq=5, Absent=true) ───▶│
  │◀───── 412 Precondition Failed ───┤ (Collision with another writer)
  │                                   │
  ├────── head(start=5) ─────────────▶│ (List up to 1000 objects)
  │◀───── [seq 5, seq 6] ─────────────┤
  │                                   │
  ├────── Put(seq=7, Absent=true) ───▶│
  │◀───── 200 OK ─────────────────────┤ (Success)
```

The writer issues `store.Put` with precondition `Absent: true`. If a concurrent
writer commits first, the store returns HTTP 412 Precondition Failed.

When `List` returns 1,000 objects, the writer runs exponential jump probing:

```text
lo ──▶ lo+1 ──▶ lo+2 ──▶ lo+4 ──▶ lo+8 ──▶ ... ──▶ high (Exists = false)
                                                     │
                             ┌───────────────────────┘
                             ▼
              Binary search in [low, high]
```

1. Probe `lo + 1`. If absent, sequence `lo` is the tail.
2. Double the step size on each iteration: `high += step * 2`.
3. Check `store.Exists` until finding an absent key.
4. Binary search the interval `[low, high]` to locate the head in $O(\log N)$
   calls.

### Ingestion Execution Call Tree

```text
grpcapi.IngestServer.Upsert
  resolveNamespace
  resolveBranch
  ingest.Ingester.Upsert (namespace, branch)
    logstream.Log.Append
      recordio.Writer.WriteRecord
      objectstore.Store.Put (wal/<020d_seq>.recordio)
grpcapi.IngestServer.Fork
  resolveNamespace, validate source and target branches
  ingest.Ingester.Fork (namespace, source, target)
    manifest.Read (Scope.ManifestKey(source))
    manifest.Write (Scope.ManifestKey(target), Absent: true)
    Scope.AddBranch (branches.json)
    logstream.Log.Append (BranchLifecycleEvent_FORK)
    on failure: delete target manifest and Scope.RemoveBranch
ingest.Flusher.Run (Background goroutine)
  logstream.Log.Read
  ingest.Flusher.processRecords
  ingest.Flusher.flushBranch
    segment.Writer.Write (segments/<sha256>.recordio)
    manifest.Write (refs/heads/<branch>.json, CAS generation)
    Scope.AddBranch (branches.json)
```

---

## 6. Materialization and Branch Lifecycle

The `Flusher` tails the WAL and materializes immutable segments.

### Checkpoint Initialization

On startup, the flusher stream initializes checkpoint state:
1. `Scope.ListBranches` retrieves branch names from `branches.json`.
2. `manifest.Read` retrieves manifests from `refs/heads/<branch>.json`.
3. The stream finds `minCheckpoint = min(manifest.CheckpointSeq)`.
4. WAL tailing begins at sequence `minCheckpoint + 1`.

Materialization occurs immediately for each WAL sequence.

```text
Flusher                           Object Store
   │                                   │
   ├─── 1. Write RecordIO segment      │
   │       to memory buffer            │
   │                                   │
   ├─── 2. Generate segment ID:        │
   │       sha256(segment bytes)       │
   │                                   │
   ├─── 3. Put segment blob ──────────▶│ segments/<sha256>.recordio
   │       (Condition: Absent=true)    │
   │                                   │
   │    ┌─── CAS Commit Loop ──────────┤
   │    │                              │
   ├───┼─── 4. manifest.Read ─────────▶│ refs/heads/<branch>.json
   │   │◀── Manifest + Generation ─────┤
   │   │                               │
   │   ├─── 5. Append SegmentRef       │
   │   │       Set CheckpointSeq       │
   │   │                               │
   │   ├─── 6. manifest.Write ────────▶│ refs/heads/<branch>.json
   │   │       (GenMatch = Generation) │
   │   │                               │
   │   │   [If 412: Retry CAS loop]    │
   │   └───[If 200: Break] ────────────│
   │                                   │
   ├─── 7. Scope.AddBranch ───────────▶│ branches.json
   │                                   │
   └─── Update in-memory checkpoint    │
```

### Zero-Copy Branch Forks

A branch fork creates an isolated branch without copying vector data:
1. `manifest.Read` fetches the source branch manifest.
2. `manifest.Write` writes the manifest to `refs/heads/<target>.json` with
   precondition `Absent: true`.
3. `Scope.AddBranch` registers the target branch in `branches.json`.
4. `logstream.Log.Append` records a `FORK` lifecycle event.
5. If catalog registration or log append fails, rollback logic removes the
   target manifest and branch entry.

---

## 7. Query Pipeline (Read Path)

Query nodes read immutable segments referenced by branch manifests.

```text
Background Ticker (2s)                   Query Worker
         │                                    │
         ▼                                    ▼
   SyncOnce(ctx)                        Query(ctx, req)
         │                                    │
         ├─ ActiveNamespaces (ns.json)        ├─ atomic.Pointer.Load()
         ├─ ListBranches (branches.json)      ├─ Table.Search()
         ├─ manifest.Read(branch)             │   ├─ Filter check
         ├─ gen == lastGen? Skip              │   ├─ Variant kernel dot
         │                                    │   └─ Top-K min-heap culling
         ├─ New segments?                     │
         │   └─ Table.Clone()                 └─ Materialize top hits
         │   └─ Stream segment.Reader
         │   └─ Apply to the clone
         └─ atomic.Pointer.Store(clone)
```

### Manifest Polling and Incremental Loading

`query.Engine.SyncOnce` runs on a 2-second background ticker:
1. Polls active namespaces and branches from `ns.json` and `branches.json`.
2. Reads `refs/heads/<branch>.json` to get the latest manifest generation.
3. If the generation is unchanged, sync skips the branch immediately.
4. Streams newly added segments past the `applied` index.
5. Clones the existing in-memory `Table` with `slices.Clone`.
6. Replays mutations into the cloned table.
7. Swaps the table pointer with `atomic.Pointer[Table].Store`. Reader queries
   continue without lock contention.

### In-Memory Table Structure

`query.Table` stores data in contiguous row-indexed slices:

```text
Table
├── docIDs:      []string
├── index:       map[string]int          doc ID to row
├── tombstones:  []bool
├── vectors:
│   └── "default" ──▶ flatVectorCol
│                     ├── data:     []float32  (float32 variants)
│                     ├── data16:   []uint16   (fp16 variant)
│                     ├── hasVec:   []bool
│                     └── invNorms: []float32
└── attrs:
    └── "lang"    ──▶ []*AttributeValue
```

### Search Execution Pipeline

```text
Query Vector + Filter
        │
        ▼
   Iterate rows
        │
        ├── 1. Check tombstone ──▶ If true, continue
        │
        ├── 2. Evaluate filter on attribute column ──▶ If mismatch, continue
        │
        ├── 3. Compute cosine with the variant kernel:
        │      dot(query, row) * invNorm(query) * invNorm(row)
        │
        └── 4. Top-K Min-Heap Culling:
               ├─ Heap size < topK ──▶ Push candidate
               ├─ Score better than heap root ──▶ Replace root & heap.Fix()
               └─ Score worse than heap root ──▶ Discard candidate immediately
        │
        ▼
   Sort heap winners
        │
        ▼
   Materialize Record protobuf only for top-K results
```

Search stages:
1. **Scalar pre-filtering**: Evaluates attribute equality predicates before
   computing distances.
2. **Distance scoring**: Computes vector similarity with the selected kernel.
3. **Top-K min-heap culling**: Maintains top candidates in a bounded heap.
4. **Result materialization**: Allocates Protocol Buffer records only for the
   final winning hits.

### Distance Kernel Variants

The `-distance` command-line flag selects the row layout and SIMD kernel set:

| Variant | Storage Format | Hardware Target | Implementation |
| --- | --- | --- | --- |
| `pure` | float32 | All architectures | Scalar Go |
| `simd` | float32 | AVX-512F amd64 | `simd/archsimd` FMA |
| `simd` | float32 | Other amd64, arm64 | Portable `simd` |
| `fp16` | fp16 (uint16) | AVX-512F amd64 | Assembly `VCVTPH2PS` |

### Cosine Scoring and Upper-State Invariant

Cosine distance uses a single dot product per row. Inverse norms are stored at
ingestion:

```text
sim   = dot(query, row) * invQueryNorm * invNorms[row]
sim   = clamp(sim, -1, 1)
score = 1 - sim
```

Every AVX-512 kernel calls `ClearAVXUpperBits` or emits `VZEROUPPER` before
returning to Go scalar code. This clears upper register state and avoids
severe CPU transition penalties.

---

## 8. Failure Recovery Matrix

Stateless design ensures clean recovery across crash scenarios:

| Failure Scenario | Engine Recovery Behavior |
| --- | --- |
| Crash in `Append` | `Absent: true` avoids overwrite. Retries next sequence. |
| Ingest node crash | Stateless process. Requests fail over immediately. |
| Segment upload crash | `Absent: true` ensures unreferenced blob is harmless. |
| Crash before CAS | Flusher ignores replay 412 error and retries CAS. |
| Manifest race (412) | Flusher re-reads manifest, appends segment, retries. |
| Query node crash | Stateless process. Restarts and rebuilds memory tables. |
| Torn write | Scanner detects CRC mismatch and returns `ErrTornWrite`. |

---

## 9. End-to-End Concrete Dataflow Trace

Trace of 1,000 records from ingestion to query:

1. **Client Upsert**: Client invokes `IngestService.Upsert` with 1,000 records
   for namespace `default` and branch `main`.
2. **WAL Serialization**: `Ingester` validates identifiers and marshals records
   into `WalRecord` envelopes.
3. **Durable WAL Append**: `logstream.Log` uploads batch to
   `ns/default/wal/00000000000000000104.recordio` using `Absent: true`.
4. **Flusher Ingestion**: Flusher tails sequence 104, serializes mutations,
   and computes SHA-256 digest.
5. **Segment Upload**: Flusher uploads `ns/default/segments/<sha256>.recordio`
   using `Absent: true`.
6. **Manifest CAS Commit**: Flusher executes Compare-And-Swap on
   `ns/default/refs/heads/main.json`, advancing checkpoint sequence to 104.
7. **Query Table Sync**: Query engine ticker detects new generation, reads
   segment, clones table, and swaps pointer.
8. **Top-K Retrieval**: Query client sends search request. Query node scans
   table and returns 10 scored records.

---

## 10. Dataset Profile and Performance Baselines

Measurements reflect runs on Apple M3 Max hardware.

### Cohere Wikipedia Dataset Profile

- **Corpus**: `datasets/cohere-wikipedia`.
- **Document count**: 1,000,000 documents across 10 Parquet files.
- **Vector dimension**: 1024 float32 dimensions per document.
- **Segment sizing**: 10,000 documents per segment.
- **Storage footprint**: 100 segment files (~45 MB per segment, ~4.5 GB total).
- **Manifest**: 1 manifest at `ns/default/refs/heads/main.json` tracking 100
  segment references.

### Performance Measurements

Distance metric kernels (1024-dimension float32):

| Kernel | Implementation | Time / Op | Speedup |
| --- | --- | --- | --- |
| Cosine | Pure Go | 20,294 ns | 1.0x |
| Cosine | Portable SIMD | 298.8 ns | 68x faster |
| Cosine | Arch SIMD (FMA) | 273.6 ns | 74x faster |
| DotProduct | Arch SIMD | 277.3 ns | - |
| L2Squared | Arch SIMD | 289.3 ns | - |

Table ingestion and copy-on-write build:
- `BenchmarkTable_ApplyDelta`: 264.9 µs per delta batch.

Ingestion throughput:
- Sustained throughput: 1,377 documents per second across gRPC into WAL and
  segments.

Query latency (10,000 documents, 1024-dimension vectors):
- Vector search (`top_k=10`): p50 12.72 ms, p90 13.05 ms, p99 13.36 ms.
- Filtered search (`top_k=10`, `lang=en`): p50 52.68 ms, p90 53.73 ms,
  p99 55.80 ms.

---

## 11. Subsystem Design Specifications

Subsystem design specifications expand on each architectural domain:
- [Storage Subsystem (docs/design/storage.md)](design/storage.md):
  Storage layout, catalogs, manifests, and crash recovery.
- [Ingestion Subsystem (docs/design/ingestion.md)](design/ingestion.md):
  gRPC write path, WAL framing, and flusher commit loops.
- [Query Subsystem (docs/design/query.md)](design/query.md):
  Manifest polling, memory tables, SIMD kernels, and distance math.
