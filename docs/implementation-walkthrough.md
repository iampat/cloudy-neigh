# cloudy-neigh Implementation Walkthrough

cloudy-neigh is a distributed, cloud-native vector search engine written in Go.
It runs entirely on cloud object storage with no local disks, Raft clusters, or coordination services.

```
Client ──gRPC Upsert──▶ Ingest Engine ──batch──▶ WAL (LogStream on GCS)
                            [Process 1]                      │
                                                             │ tails WAL
                                                             ▼
                                                          Flusher
                                                             │
                                                             │ per WAL entry
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
│ Layer 2: Segments and Manifests (segment/, manifest/, namespace/, ingest/)  │
│ • Immutable RecordIO segments: ns/<ns>/segments/<020d_seq>.recordio         │
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

The system uses two atomic object storage primitives:
1. `Absent: true`. Maps to `if-generation-match=0` on GCS.
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
Each namespace has one log under `ns/<namespace>/wal/`.

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
	Upsert(ctx context.Context, ns, branch string, records []*cloudyneighpb.Record) error
	Delete(ctx context.Context, ns, branch string, ids []string) error
	Fork(ctx context.Context, ns, source, target string) error
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
2. `IngestServer` validates the namespace, the branch, and the record IDs. It passes the namespace and the branch as separate parameters to `ingester.Upsert`.
3. On the first write to a namespace, the ingester adds it to `ns.json`.
4. `ingest.Ingester` marshals the batch into `WalRecord` envelopes and appends them directly to `logstream.Log`.
5. The client call returns success once the batch commits to object storage.
6. If the WAL write fails, the client immediately receives the error. Client requests receive success only after durability is guaranteed.

## 5. Materialization and Flusher: `ingest.Flusher`

`Flusher` reads the active namespaces from `ns.json` at each poll interval. It
runs one stream per namespace. A stream tails the namespace WAL and flushes one
segment per branch in each WAL entry. There is no memtable.

### Checkpoint Initialization

On startup, each stream does these steps:
1. `Scope.ListBranches` reads the branch names from `branches.json`.
2. `manifest.Read` reads `refs/heads/<branch>.json` for each branch.
3. `minCheckpoint = min(manifest.CheckpointSeq)` across all branches.
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
    if walRec is FORK:
      flushBranch(parent, branchMutations[parent], seq)
      branchCheckpoints[child] = seq
      continue
    mut = walRec.GetMutation()
    if seq <= branchCheckpoints[mut.Branch]:
      continue // Skip already-materialized mutation
    branchMutations[mut.Branch].append(mut)

  for branch, muts in branchMutations:
    flushBranch(ctx, branch, muts, seq)
  seq++
```

Segment flush triggers:
- Materialization occurs immediately per WAL entry.
- Batching occurs at the client request boundary, maximizing object storage write efficiency.
- Graceful shutdown initiates: drains WAL to tail and flushes all pending sequences.

### Segment Flush and Atomic CAS Commit

```
Flusher                           Object Store
   │                                   │
   ├─── 1. Write RecordIO segment      │
   │       to memory buffer            │
   │                                   │
   ├─── 2. Segment ID = WAL seq        │
   │       (20-digit, zero-padded)     │
   │                                   │
   ├─── 3. Put segment blob ──────────▶│ segments/<020d_seq>.recordio
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

A WAL entry holds the mutations of one branch. Thus the WAL sequence number
alone names the segment. The name holds no branch, so forks share segments.

## 6. Branch Management: `manifest` and `namespace`

The branch manifest lives at `ns/<namespace>/refs/heads/<branch>.json`. The
object body is a `BranchManifest` in protojson. `branches.json` holds a
`BranchCatalog` with the branch names, e.g. `{"branches":{"main":{}}}`.

```proto
message BranchManifest {
  uint64 checkpoint_seq = 1;
  uint64 schema_version = 2;
  repeated SegmentRef segments = 3;
}
```

Operations:
- `manifest.Read`: reads the manifest and returns the storage generation.
- `manifest.Write`: writes the manifest with `Condition{GenerationMatch: gen}`, or with `Condition{Absent: true}` when `gen` is empty.
- `Ingester.Fork`: copies the source manifest to `refs/heads/<target>.json` with `Condition{Absent: true}`. It adds the target to `branches.json` and appends a `FORK` event to the WAL.
- `Scope.ListBranches`, `Scope.AddBranch`, `Scope.RemoveBranch`: read and change `branches.json` with CAS.
- Zero-copy forks require no segment duplication. Segments remain immutable and shared.

Callers pass the namespace and the branch as separate parameters. Package
`namespace` builds every key. No code parses a key to find a namespace or a
branch.

## 7. Query Engine and Incremental Loader: `query`

The query engine decouples read serving from ingestion.
It keeps one in-memory `Table` and one `Loader` per namespace and branch.

```
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

`query.Engine.SyncOnce` runs every `syncInterval` (default: 2 seconds). It
lists the active namespaces and their branches. For each branch,
`query.Loader.Sync` does these steps:
1. Reads `refs/heads/<branch>.json` to get the latest manifest and object generation.
2. If `gen == lastGen`, the branch has not changed. The loader exits immediately.
3. For each `SegmentRef` in `manifest.Segments`:
   - Checks `loaded[seg.SegmentId]`. Skips previously loaded segments.
   - Clones the current table on the first new segment.
   - Downloads the object at `scope.SegmentKey(seg.SegmentId)`.
   - Scans records with `segment.Reader`.
   - PUT operations call `Table.UpsertRecord`.
   - DELETE operations call `Table.Delete`.
   - Marks `loaded[seg.SegmentId] = true`.
4. Swaps pointer via `atomic.Pointer[Table].Store(clone)`.
5. Active reader goroutines continue scanning previous table instances with zero lock contention.

## 8. In-Memory Table: `query.Table`

`query.Table` stores rows in flat, row-indexed slices.

```
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

An upsert of a known doc ID overwrites its row. A new doc ID appends a row. A
delete sets the tombstone. `Table.Clone` copies every slice, so a sync pass
costs memory in proportion to the table size.

The `-distance` flag selects the row format and the kernel set. See
`docs/design/distance-variants.md`.

### Search Execution Pipeline

```
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

1. Scalar pre-filtering runs before distance calculation.
2. The variant kernel computes the dot product. The table stores the inverse norm of each row.
3. Min-heap culling avoids sorting all candidates.
4. Protobuf materialization occurs only for the final top-$k$ hits.

## 9. Failure Modes and Resilience Invariants

| Failure Scenario | Engine Behavior and Recovery |
| :--- | :--- |
| Crash during `logstream.Append` | Conditional `Absent: true` prevents partial overwrite. Caller retries at next sequence. Contiguity preserved. |
| Ingest node crash | Stateless process. Incoming requests fail over to another ingest node immediately. |
| Crash during segment upload | Upload uses `Absent: true`. An unreferenced segment file never enters a manifest. |
| Crash between segment upload and CAS | On restart, Flusher re-tails WAL from `checkpoint_seq + 1`. The segment key exists, so the `Absent: true` Put fails and the stream stops with an error. CONSIDER(ali): accept an existing key on replay. |
| Race on manifest commit (HTTP 412) | Flusher catches `ErrPreconditionFailed`, re-resolves manifest, merges new segment into refreshed segment list, retries CAS. |
| Query node crash | Stateless process. On restart, polls manifest from GCS, downloads segments, rebuilds the in-memory table. |
| Torn write in segment or WAL | RecordIO reader checks length, header CRC, and footer CRC. Returns `ErrTornWrite` on truncation. |

## 10. End-to-End Concrete Dataflow Trace

```
1. Client calls Upsert (namespace "default", branch "main", 1000 records)
   │
2. Ingester packages records into WalRecord protobufs
   │
3. logstream.Log creates ns/default/wal/00000000000000000104.recordio
   │  Put with Condition{Absent: true} succeeds
   ▼
4. Flusher tails sequence 104
   │  Writes ns/default/segments/00000000000000000104.recordio
   │  Uploads with Condition{Absent: true}
   ▼
5. Flusher executes CAS on ns/default/refs/heads/main.json
   │  Updates CheckpointSeq = 104
   │  Appends new SegmentRef
   │  Put with Condition{GenerationMatch: gen} succeeds
   ▼
6. Query Engine background ticker fires
   │  Reads ns/default/refs/heads/main.json, detects new generation
   │  Streams ns/default/segments/00000000000000000104.recordio
   │  Clones the table, applies the records
   │  atomic.Pointer[Table].Store(clone)
   ▼
7. Client calls Query(vector, top_k=10, filter={lang: "en"})
   │  QueryServer loads active table snapshot
   │  Scans rows: pre-filters lang="en", computes cosine
   │  Top-10 min-heap returns scored results
   ▼
8. Query returns 10 ScoredRecord hits
```

## 11. Cohere Wikipedia Dataset and Segment Counts

- Corpus: `datasets/cohere-wikipedia`
- Total documents: 1,000,000 documents across 10 Parquet files.
- Vector dimension: 1024 float32 values per document.
- Batching threshold: Batch size of 10,000 docs per WAL sequence.
- Continuous streaming into namespace `default`, branch `main`, produces exactly:
  $$1,000,000 \text{ docs} / 10,000 \text{ docs/segment} = \mathbf{100} \text{ segment files}$$
- Storage footprints:
  - 100 segment files under `ns/default/segments/<020d_seq>.recordio` (~45 MB per segment, ~4.5 GB total).
  - 1 branch manifest at `ns/default/refs/heads/main.json` tracking 100 `SegmentRef` entries.

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

