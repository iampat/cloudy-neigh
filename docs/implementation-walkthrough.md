# cloudy-neigh Implementation Walkthrough

cloudy-neigh is a distributed, cloud-native vector search engine written in Go.
It runs entirely on cloud object storage with no local disks, Raft clusters, or coordination services.

```
Client ──gRPC Upsert──▶ Ingest Engine ──batch──▶ WAL (LogStream on GCS)
                            [Process 1]                      │
                                                             │ tails WAL
                                                             ▼
                                                         Memtable
                                                             │
                                                             │ flush
                                                             ▼
                                                      segment.Writer
                                                             │ [Process 2]
                                                             ▼
                                              Segment on GCS + Manifest CAS
                                                             │
                                                             │ polls manifest
                                                             ▼
Client ──gRPC Query────▶ Query Engine ◀──loads─────── segment.Reader
                            [Process 3]
```

## 1. Storage Primitives and Layering

The engine relies on cloud object storage as its single source of truth.
No external database or consensus coordinator exists.

```
┌─────────────────────────────────────────────────────────────────────────────┐
│ Layer 2: Ingestion & Columnar Segments (segment/, ingest/, kvfs/)           │
│ • Immutable RecordIO segment files: segments/<branch>/<segID>.recordio      │
│ • Mutable branch heads: refs/heads/<branch> storing BranchManifest          │
│ • Zero-copy branch forks with Compare-And-Swap manifest updates             │
└──────────────────────────────────────┬──────────────────────────────────────┘
                                       │
                                       ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│ Layer 1: Global Write-Ahead Log (logstream/)                                │
│ • Append-only sequenced keys: wal/<020d_seq>.recordio                       │
│ • Conditional object creation: If-Generation-Match=0 (Absent: true)         │
│ • Log-scale tail discovery via exponential probe and binary search          │
└──────────────────────────────────────┬──────────────────────────────────────┘
                                       │
                                       ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│ Layer 0: Object Store Adapter (objectstore/)                                │
│ • Unified driver: GCS, AWS S3, and local filesystem                         │
│ • Atomic preconditions: Absent (create-if-not-exists) and GenerationMatch   │
└─────────────────────────────────────────────────────────────────────────────┘
```

The system uses two atomic object storage primitives:
1. `Absent: true`. Maps to `if-generation-match=0` on GCS and `If-None-Match: *` on S3.
2. `GenerationMatch: <gen>`. Maps to `if-generation-match=<gen>` on GCS for Compare-And-Swap.

## 2. Framing and Checksums: `recordio`

Every persistent blob in the WAL and segment store uses the RecordIO container format.
A frame embeds CRC-32C checksums at both ends to detect partial writes and bit rot.

```
┌─────────────────┬──────────────────┬───────────────────┬──────────────────┐
│ Length (8B LE)  │ Header CRC (4B)  │  Payload (NB)     │ Data CRC (4B)    │
└─────────────────┴──────────────────┴───────────────────┴──────────────────┘
│◀──────── 12-byte Header ──────────▶│                   │◀─ 4-byte Footer ─▶│
```

Header layout:
- 8 bytes: payload length encoded as little-endian `uint64`.
- 4 bytes: masked CRC-32C computed over the 8 length bytes.

Payload:
- $N$ raw bytes of serialized protobuf.

Footer layout:
- 4 bytes: masked CRC-32C computed over the payload bytes.

Checksum masking prevents nested CRC collisions:
```go
func computeMaskedCRC(data []byte) uint32 {
	crc := crc32.Checksum(data, castagnoliTable)
	return ((crc >> 15) | (crc << 17)) + 0xa282ead8
}
```

Scanner behavior on corruption:
- Corrupted header CRC returns `ErrHeaderCorrupted`.
- Corrupted footer CRC returns `ErrDataCorrupted`.
- Premature EOF mid-frame returns `ErrTornWrite`.
- Clean EOF at a 16-byte boundary returns `io.EOF`.

## 3. The Global Write-Ahead Log: `logstream`

`logstream.Log` provides a distributed, append-only log without a sequencer daemon.

```
wal/
├── 00000000000000000001.recordio
├── 00000000000000000002.recordio
└── 00000000000000000003.recordio   <-- head (seq 3)
```

Key format:
- `wal/<020d_seq>.recordio`.
- 20-digit zero-padded decimal string.
- Lexicographical sort order matches numeric sequence order.

### Append Protocol

```
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

1. The writer packs all records into one RecordIO segment buffer.
2. The writer attempts `store.Put(key, data, Condition{Absent: true})` at `seq = lastKnown + 1`.
3. On HTTP 200, the write commits.
4. On HTTP 412 Precondition Failed, a concurrent writer claimed `seq`.
5. The writer finds the new tail via `head()` and retries at `newHead + 1`.

### Head Discovery via Exponential Probing

If `List` returns 1,000 items, the writer executes an exponential jump probe.

```
lo ──▶ lo+1 ──▶ lo+2 ──▶ lo+4 ──▶ lo+8 ──▶ ... ──▶ high (Exists = false)
                                                     │
                             ┌───────────────────────┘
                             ▼
              Binary search in [low, high]
```

1. Probe `lo + 1`. If absent, head is `lo`.
2. Double the step size on each iteration: `high += step * 2`.
3. Check `store.Exists(ctx, segmentKey(high))` until finding an absent key.
4. Run binary search across `[low, high]` to locate the head in $O(\log N)$ calls.

## 4. Ingestion Server: `grpcapi.IngestServer` and `ingest.Ingester`

`IngestServer` is decoupled from storage through the `Ingester` interface.
No serialization or WAL implementation details reside inside `grpcapi`.

```go
type Ingester interface {
	Upsert(ctx context.Context, namespace string, records []*cloudyneighpb.Record) error
	Delete(ctx context.Context, namespace string, ids []string) error
	Fork(ctx context.Context, source, target string) error
}
```

### Ingestion Flow and Direct Batch Processing

```
Client (batch=200) ──▶ IngestServer ──▶ Ingester.Upsert()
                                             │
                                             │ synchronous append
                                             ▼
                                        logstream.Log.Append()
                                             │
                                             ▼ (200 OK after WAL commit)
```

1. Client sends batch requests (e.g. 200 records) to `IngestService.Upsert`.
2. `IngestServer` validates namespace and record IDs, then delegates to `ingester.Upsert`.
3. `ingest.Ingester` marshals the batch into `WalRecord` envelopes and appends them directly to `logstream.Log`.
4. The client call returns success once the batch commits to object storage.
5. If the WAL write fails, the client immediately receives the error. Client requests receive success only after durability is guaranteed.

## 5. Materialization and Flusher: `ingest.Flusher`

`Flusher` tails the global WAL, routes records to branch memtables, and flushes segments.

### Checkpoint Initialization

On startup, `Flusher.Run` scans all branches in `refs/heads/`:
1. `kvfs.ListBranches` returns all branch names.
2. `kvfs.ResolveBranch` reads each branch manifest.
3. `minCheckpoint = min(manifest.CheckpointSeq)` across all active branches.
4. The tail loop starts at `seq = minCheckpoint + 1`.

### Ingestion Loop and Routing

```
readLoop:
  records, err = log.Read(ctx, seq)
  if err == ErrEndOfStream:
    sleep(PollInterval) // 100ms
    continue

  for rec in records:
    walRec = unmarshal(rec)
    mut = walRec.GetMutation()
    if seq <= branchCheckpoints[mut.Branch]:
      continue // Skip already-materialized mutation
    branchMutations[mut.Branch].append(mut)

  for branch, muts in branchMutations:
    flushBranch(ctx, branch, muts, seq)
```

Segment flush triggers:
- Materialization occurs immediately per WAL sequence batch.
- Batching occurs at the client request boundary, maximizing object storage write efficiency.
- Graceful shutdown initiates: drains WAL to tail and flushes all pending sequences.

### Segment Flush and Atomic CAS Commit

```
Flusher                           Object Store
   │                                   │
   ├─── 1. Write RecordIO segment      │
   │       to memory buffer            │
   │                                   │
   ├─── 2. Generate segment ID:        │
   │       YYYYMMDDHHMMSS-micros-rand  │
   │                                   │
   ├─── 3. Put segment blob ──────────▶│ segments/<branch>/<id>.recordio
   │       (Condition: Absent=true)    │
   │                                   │
   │    ┌─── CAS Commit Loop ──────────┤
   │    │                              │
   ├───┼─── 4. ResolveBranch ─────────▶│ refs/heads/<branch>
   │   │◀── Manifest + Generation ─────┤
   │   │                               │
   │   ├─── 5. Append SegmentRef       │
   │   │       Set CheckpointSeq       │
   │   │                               │
   │   ├─── 6. Put manifest ──────────▶│ refs/heads/<branch>
   │   │       (GenMatch = Generation) │
   │   │                               │
   │   │   [If 412: Retry CAS loop]    │
   │   └───[If 200: Break] ────────────│
   │                                   │
   └─── Update in-memory checkpoint    │
```

Segment ID format:
- Timestamp: `YYYYMMDDHHMMSS` in UTC.
- Microsecond suffix: ensures monotonic growth within the same second.
- 5 random bytes: avoids collisions across distributed flushers.

## 6. Branch Management: `kvfs`

Branch heads live under `refs/heads/<branch>`.
The object body contains a serialized `BranchManifest` protobuf.

```proto
message BranchManifest {
  uint64 checkpoint_seq = 1;
  uint64 schema_version = 2;
  repeated SegmentRef segments = 3;
}
```

Operations:
- `ResolveBranch`: reads `refs/heads/<branch>`, unmarshals manifest, returns storage generation.
- `UpdateBranch`: writes manifest using `Condition{GenerationMatch: gen}`.
- `CreateBranch`: copies parent manifest to `refs/heads/<new>` with `Condition{Absent: true}`.
- Zero-copy forks require no segment duplication. Segments remain immutable and shared.

## 7. Query Engine and Incremental Loader: `query`

The query engine decouples read serving from ingestion.
It maintains an in-memory, chunked columnar table per branch.

```
Background Ticker (2s)                   Query Worker
         │                                    │
         ▼                                    ▼
   SyncOnce(ctx)                        Query(ctx, req)
         │                                    │
         ├─ ResolveBranch(branch)             ├─ atomic.Pointer.Load()
         ├─ gen == lastGen? Skip              ├─ Table.SearchWithStats()
         │                                    │   ├─ Filter check
         ├─ New segments?                     │   ├─ SIMD Cosine distance
         │   └─ Stream segment.Reader         │   └─ Top-K min-heap culling
         │   └─ Apply to Builder              │
         │                                    └─ Materialize top hits
         └─ atomic.Pointer.Store(newTable)
```

### Manifest Polling and Incremental Loading

`query.Loader.Sync` executes every `syncInterval` (default: 2 seconds):
1. Resolves `refs/heads/<branch>` to obtain the latest manifest and object generation.
2. If `gen == lastGen`, the branch has not changed. The loader exits immediately.
3. For each `SegmentRef` in `manifest.Segments`:
   - Checks `loaded[seg.SegmentId]`. Skips previously loaded segments.
   - Downloads `segments/<branch>/<segID>.recordio`.
   - Scans records with `segment.Reader`.
   - PUT operations call `Builder.UpsertRecord`.
   - DELETE operations call `Builder.Delete`.
   - Marks `loaded[seg.SegmentId] = true`.
4. Calls `Builder.Build()` to create a new `*Table`.
5. Swaps pointer via `atomic.Pointer[Table].Store(newTable)`.
6. Active reader goroutines continue scanning previous table instances with zero lock contention.

## 8. In-Memory Chunked Columnar Table: `query.Table`

`query.Table` organizes records into 1024-row chunks to eliminate heap fragmentation.

```
Table (Chunk size = 1024)
├── docIDs:      [chunk 0: 1024 strings] [chunk 1: 1024 strings] ...
├── tombstones:  [chunk 0: 1024 bools]   [chunk 1: 1024 bools]   ...
├── index:       [map 0: base] [map 1: delta] ... [up to 16 maps]
├── vectors:
│   └── "default" ──▶ packedVectorCol
│                     ├── chunks:   [chunk 0: 1024 * dim float32] ...
│                     ├── vecToRow: [chunk 0: 1024 ints] ...
│                     └── rowToVec: [chunk 0: 1024 ints] ...
└── attrs:
    └── "lang"    ──▶ [chunk 0: 1024 *AttributeValue] ...
```

Fast bitwise index translation:
```go
const (
	chunkSize  = 1024
	chunkMask  = 1023
	chunkShift = 10
)

func chunkIndex(i int) (int, int) {
	return i >> chunkShift, i & chunkMask
}
```

### Copy-on-Write Structural Sharing

When `Builder` constructs a new table revision from an existing table:
1. It copies chunk pointer slices, setting `chunkShared[c] = true`.
2. When a row updates inside chunk $C$, only chunk $C$ is cloned via `slices.Clone`.
3. Unmodified chunks remain shared between old and new tables.
4. Memory allocation is proportional to batch size rather than table size.

### Packed Vector Storage

Vectors reside in dense, contiguous slices:
- Each vector chunk allocates `chunkSize * dim` contiguous `float32` values.
- No slice header or pointer indirection exists per vector.
- Vector row mapping:
  - `vecToRow[chunk][offset]` translates vector slot to table row.
  - `rowToVec[chunk][offset]` translates table row to vector slot.
  - Deletions set `rowToVec` to `-1` and set the tombstone flag.

### Search Execution Pipeline

```
Query Vector + Filter
        │
        ▼
   Iterate vector chunks (1024 vectors per chunk)
        │
        ├── 1. Check tombstone bit ──▶ If true, continue
        │
        ├── 2. Evaluate filter on attribute chunk ──▶ If mismatch, continue
        │
        ├── 3. Compute distance via SIMD kernel:
        │      Cosine(query, storedVector)
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

1. Scalar pre-filtering runs before distance calculation.
2. Distance kernels leverage SIMD via `simd.Float32s` and fused multiply-add.
3. Min-heap culling avoids sorting all candidates.
4. Protobuf materialization occurs only for the final top-$k$ hits.

## 9. Failure Modes and Resilience Invariants

| Failure Scenario | Engine Behavior and Recovery |
| :--- | :--- |
| Crash during `logstream.Append` | Conditional `Absent: true` prevents partial overwrite. Caller retries at next sequence. Contiguity preserved. |
| Ingest node crash | Stateless process. Incoming requests fail over to another ingest node immediately. |
| Crash during segment upload | Upload uses random segment ID and `Absent: true`. Unreferenced segment file remains garbage, never enters manifest. |
| Crash between segment upload and CAS | Uncommitted segment is ignored. On restart, Flusher re-tails WAL from `checkpoint_seq + 1` and flushes a new segment. |
| Race on manifest commit (HTTP 412) | Flusher catches `ErrPreconditionFailed`, re-resolves manifest, merges new segment into refreshed segment list, retries CAS. |
| Query node crash | Stateless process. On restart, polls manifest from GCS, downloads segments, rebuilds columnar memory table. |
| Torn write in segment or WAL | RecordIO reader checks length, header CRC, and footer CRC. Returns `ErrTornWrite` on truncation. |

## 10. End-to-End Concrete Dataflow Trace

```
1. Client calls Upsert (1000 records)
   │
2. IngestServer packages records into WalRecord protobufs
   │
3. logstream.Log creates wal/00000000000000000104.recordio
   │  Put with Condition{Absent: true} succeeds
   ▼
4. Flusher tails sequence 104
   │  Decodes mutations, appends to branch memtable
   ▼
5. Memtable hits threshold (10,000 docs)
   │  Writes segments/main/20260914150000-000120ab12cd34ef.recordio
   │  Uploads with Condition{Absent: true}
   ▼
6. Flusher executes CAS on refs/heads/main
   │  Updates CheckpointSeq = 104
   │  Appends new SegmentRef
   │  Put with Condition{GenerationMatch: gen} succeeds
   ▼
7. Query Engine background ticker fires
   │  Resolves refs/heads/main, detects new generation
   │  Streams segments/main/20260914150000-000120ab12cd34ef.recordio
   │  Builder clones modified chunks, creates new Table
   │  atomic.Pointer[Table].Store(newTable)
   ▼
8. Client calls Query(vector, top_k=10, filter={lang: "en"})
   │  QueryServer loads active table snapshot
   │  Scans chunks: pre-filters lang="en", computes SIMD cosine
   │  Top-10 min-heap returns scored results
   ▼
9. Query returns 10 ScoredRecord hits
```

## 11. Cohere Wikipedia Dataset and Segment Counts

- Corpus: `datasets/cohere-wikipedia`
- Total documents: 1,000,000 documents across 10 Parquet files.
- Vector dimension: 1024 float32 values per document.
- Batching threshold: Batch size of 10,000 docs per WAL sequence.
- Continuous streaming into namespace `main` produces exactly:
  $$1,000,000 \text{ docs} / 10,000 \text{ docs/segment} = \mathbf{100} \text{ segment files}$$
- Storage footprints:
  - 100 segment files under `segments/main/<id>.recordio` (~45 MB per segment, ~4.5 GB total).
  - 1 branch manifest at `refs/heads/main` tracking 100 `SegmentRef` entries.

## 12. Benchmark Measurements (Apple M3 Max)

### Distance Metric Kernels (1024-dim Float32)

| Kernel | Implementation | Time / Op | Speedup over Pure Go |
| :--- | :--- | :--- | :--- |
| Cosine | Pure Go | 20,294 ns | 1.0x |
| Cosine | SIMD Portable (`simd.Float32s`) | 298.8 ns | 68x faster |
| Cosine | SIMD Arch (`archsimd.Float32x4` FMA) | 273.6 ns | **74x faster** |
| DotProduct | SIMD Arch | 277.3 ns | - |
| L2Squared | SIMD Arch | 289.3 ns | - |

### Table Ingestion & Copy-on-Write Build

- `BenchmarkTable_ApplyDelta`: 264.9 µs per delta batch.

### Ingestion Throughput (Batch Size = 200)

- Client: `scripts/demoload.py` with `--batch_size=200`
- Server: `cloudy ingest`
- Throughput: **1,377 documents / second** sustained across gRPC into WAL and segments.

### Query Latency (10,000 Documents, 1024-dim Vectors)

- Pure Vector Search (`top_k=10`):
  - Mean: **12.77 ms**
  - p50: **12.72 ms**
  - p90: **13.05 ms**
  - p99: **13.36 ms**
- Filtered Vector Search (`top_k=10, lang=en`):
  - Mean: **52.83 ms**
  - p50: **52.68 ms**
  - p90: **53.73 ms**
  - p99: **55.80 ms**

