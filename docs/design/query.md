# Query Subsystem Specification

**Status:** Accepted, 2026-09-28 [#157, #158]

The query engine executes vector searches against in-memory snapshots.
It uses flat columnar memory tables, Single Instruction Multiple Data (SIMD)
kernels, and incremental manifest synchronization.
Queries never perform remote storage operations.

## 1. Query Engine Architecture

Query execution runs entirely in memory without network hops during search.
The query engine isolates client traffic from remote object storage.
Queries read immutable snapshot tables in local memory.
Network access occurs only during background synchronization.

The engine exposes the `QueryService.Query` Remote Procedure Call (RPC)
endpoint.
Clients submit a `QueryRequest` containing namespace, branch, vector column,
query vector, candidate count `top_k`, and optional filters.
The server returns a `QueryResponse` holding ordered scored records.

The server extracts the tenant identifier from the `x-tenant-id` context
metadata.
It locates the dedicated in-memory engine instance for that tenant.
Requests with unknown tenants fail immediately with status code `NotFound`.
Empty query vectors and non-positive `top_k` values return `InvalidArgument`.
Invalid namespace strings return `InvalidArgument`.

The server tracks latency across four distinct execution phases: validation,
search, scan, and materialization.
It attaches total server processing time in microseconds to the `server-time-us`
response trailer.

```text
┌──────────────────────────────────────────────────────────────┐
│                        Client Request                        │
│         (x-tenant-id, namespace, branch, query vector)       │
└──────────────────────────────┬───────────────────────────────┘
                               │
                               ▼
┌──────────────────────────────────────────────────────────────┐
│                      QueryServer.Query                       │
│   • Validates tenant, namespace, branch, and vector          │
│   • Measures latency phases                                  │
│   • Appends server-time-us response trailer                  │
└──────────────────────────────┬───────────────────────────────┘
                               │
                               ▼
┌──────────────────────────────────────────────────────────────┐
│                      query.Engine.Query                      │
│   • Resolves branch loader                                   │
│   • Loads atomic Table pointer snapshot                      │
└──────────────────────────────┬───────────────────────────────┘
                               │
                               ▼
┌──────────────────────────────────────────────────────────────┐
│                         Table.Search                         │
│   • Evaluates scalar filters                                 │
│   • Computes SIMD distance scores against in-memory rows     │
│   • Prunes candidates via bounded top-k heap                 │
└──────────────────────────────────────────────────────────────┘
```

## 2. Incremental Manifest Polling & Synchronization

The query engine synchronizes tables from storage through a background loop.
`query.Engine.Run` launches a background ticker running at `syncInterval`.
Each tick calls `SyncOnce` across all discovered namespaces and branches.

`SyncOnce` polls the tenant catalog `ns.json` to find active namespaces.
It queries `branches.json` in each namespace to enumerate active branches.
For each branch, the engine maintains a dedicated `query.Loader` instance and an
`atomic.Pointer[Table]`.
A mutex protects the branch loader map during registration.

The loader reads the branch manifest at:
`ns/<namespace>/refs/heads/<branch>.json`.
Object storage returns the manifest content and an object generation string.
The loader compares the generation string against `lastGen`.
If the generation matches, the manifest has not changed.
The loader exits immediately and performs zero segment reads.

When the generation changes, the loader checks the manifest segment count.
If the applied segment count exceeds the manifest segment count, the manifest
shrank.
The loader halts and returns `ErrManifestTruncated`.
This guard prevents corrupted or truncated index states.

The loader applies new segments using Copy-on-Write (CoW) table cloning.
It clones the existing table snapshot by calling `l.table.Load().Clone()`.
Cloning duplicates metadata maps and slice headers without mutating the active
table.
The loader reads new content-addressed segment files
(`segments/<sha256>.recordio`) sequentially.
It scans mutations using `segment.Reader`.
For `PUT` mutations, it unmarshals the record protobuf and calls
`Table.UpsertRecord`.
For `DELETE` mutations, it calls `Table.Delete`.
Once all new segments apply, the loader commits the new table using
`l.table.Store(t)`.
Readers continue searching the prior snapshot with zero lock contention until
the pointer swap occurs.

```text
┌──────────────────────────────────────────────────────────────┐
│                   Incremental Sync Loop                      │
│                                                              │
│  1. Read branch manifest at refs/heads/<branch>.json         │
│  2. Compare object generation with lastGen                   │
│     ├── Match: return 0 (zero segment I/O)                   │
│     └── Mismatch: proceed to segment ingestion               │
│  3. Verify applied <= len(manifest.Segments)                 │
│     └── applied > len(manifest.Segments): truncate error     │
│  4. Clone in-memory Table: t = table.Load().Clone()          │
│  5. Stream segments[applied:]:                               │
│     ├── Read segment/<sha256>.recordio                       │
│     ├── Unmarshal PUT records and call UpsertRecord          │
│     └── Apply DELETE tombstones                              │
│  6. Swap atomic pointer: table.Store(t)                      │
└──────────────────────────────────────────────────────────────┘
```

## 3. Flat Memory Table Layout

The query table uses a columnar memory layout designed for Level 3 (L3) cache
bandwidth.
Pointer chasing stalls vector scans.
Contiguous parallel slices maximize streaming memory bandwidth and hardware
prefetching.

The `Table` structure organizes state across parallel slices indexed by integer
row identifiers:
- `numRows int`: total allocated row count.
- `docIDs []string`: maps row index to external string document identifier.
- `index map[string]int`: maps external document identifier to row index.
- `tombstones []bool`: bitmap indicating deleted rows.
- `attrs map[string][]*cloudyneighpb.AttributeValue`: column-oriented attribute
  slices.
- `vectors map[string]*flatVectorCol`: column-oriented vector store.

The `flatVectorCol` struct stores dense vector data:
- `dim int`: vector dimensionality.
- `data []float32`: contiguous 32-bit floating point array of size
  `numRows * dim`.
- `data16 []uint16`: contiguous 16-bit half-precision array of size
  `numRows * dim`.
- `hasVec []bool`: validity bitmap per row.
- `invNorms []float32`: precomputed inverse Euclidean norms `1 / |v|` per row.

Either `data` or `data16` is populated, matching the configured distance
variant.
The unused slice remains nil.

`Upsert` enforces atomic row mutation semantics.
Before modifying table state, it validates all column dimensions.
It computes inverse Euclidean norms for all incoming vectors.
For 16-bit tables, it quantizes inputs using `vector.EncodeFP16`.
If any vector overflows float16 limits, `Upsert` returns an error before
touching table storage.

For existing document identifiers, `Upsert` updates vector data and attribute
values in place.
It resets the tombstone flag to false.
If the row was previously deleted, unsupplied columns are cleared.
For new document identifiers, `Upsert` assigns row index `numRows`.
It appends values to `docIDs`, `tombstones`, vector columns, and attribute
columns.
It increments `numRows` upon completion.
Calling `Delete` sets the tombstone flag for that row index.

```text
Table Layout:
┌───────┬────────────┬───────────┬───────────────────┬─────────┐
│ Row   │ docIDs     │ Tombstone │ Vectors (Dim=4)   │ invNorm │
├───────┼────────────┼───────────┼───────────────────┼─────────┤
│ 0     │ "doc-101"  │ false     │ [1.0, 0.0, 0.5, …]│ 0.8944  │
│ 1     │ "doc-102"  │ true      │ [0.0, 0.2, 0.1, …]│ 0.0000  │
│ 2     │ "doc-103"  │ false     │ [0.3, 0.7, 0.2, …]│ 1.2649  │
└───────┴────────────┴───────────┴───────────────────┴─────────┘
                       Contiguous Buffer: data or data16
```

## 4. Distance Metrics & Scoring Algorithms

The table supports three distance metrics: Cosine, Euclidean Squared, and Dot
Product.
The search request selects the metric via `proto.DistanceMetric`.

Cosine distance computes angular difference between normalized vectors.
Similarity ranges between -1.0 and 1.0.
Distance is defined as `1.0 - similarity`.
A distance of 0.0 represents identical vector orientations.

Euclidean Squared distance calculates the sum of squared differences across all
dimensions.
Smaller values represent closer geometric proximity.

Dot Product computes the inner product between vectors.
Search treats dot product as a similarity metric, ranking higher values first.

The query engine uses a one-stream Cosine optimization.
Conventional cosine kernels read three memory streams: vector `a`, vector `b`,
and their squared norms.
Three-stream cosine kernels cost 1.9x to 2.3x more CPU cycles than dot product
kernels.
Cloudy-neigh moves vector normalization into the ingestion path.
During upsert, the table precomputes and stores `invNorms[row] = 1 / |v|` from
raw 32-bit floats.
At search time, the query engine computes query inverse norm `invQ = 1 / |q|`
once.
Each row requires only a single dot product kernel execution:

```go
sim := dot(query, row) * invQ * invNorms[row]
sim = clamp(sim, -1.0, 1.0)
score := 1.0 - sim
```

This optimization eliminates two vector magnitude streams during scans.
It requires only one extra float32 read per row.

Zero vectors receive explicit handling.
A query vector with zero norm returns `vector.ErrZeroVector`.
Rows with zero norm have `invNorms[row] == 0`.
The engine skips zero-norm rows during cosine search.
Euclidean squared and dot product metrics evaluate zero-norm rows normally.

## 5. SIMD Kernel Dispatch & Hardware Acceleration

The query server configures hardware acceleration via the
`-distance=<pure|simd|fp16>` Command Line Interface (CLI) flag.
The flag parses into a `vector.Variant` integer enumeration.
The zero value is `pure`, ensuring safe execution on any CPU architecture.

`Variant.Kernels` constructs a `vector.Kernels` dispatch structure:
- `Name string`: kernel identifier (`pure`, `portable`, `avx512`).
- `Dot func(a, b []float32) float32`: 32-bit float dot product.
- `L2 func(a, b []float32) float32`: 32-bit float squared Euclidean distance.
- `Dot16 func(q []float32, row []uint16) float32`: mixed-precision 16-bit row
  dot product.
- `L216 func(q []float32, row []uint16) float32`: mixed-precision 16-bit
  squared Euclidean distance.
- `Decode16 func(dst []float32, src []uint16)`: mixed-precision vector
  decompression.

Feature gating selects implementations based on host processor capabilities.
The `pure` variant uses portable scalar Go loops.
The `simd` variant inspects CPU flags via `archsimd.X86.AVX512()` on x86-64
(amd64).
If Advanced Vector Extensions 512 (AVX-512F) is present, it binds AVX-512
kernels.
Otherwise, it selects portable SIMD kernels.
The `fp16` variant requires AVX-512F hardware.
If AVX-512F is absent, `fp16` fails startup immediately.
The server never falls back to another row format silently.
Silent fallbacks obscure recall and latency regressions behind healthy status
logs.

AVX-512 kernels achieve peak throughput by eliminating Go bounds checks.
Standard index loops generate 27 bounds checks in Go 1.27.
The compiler prover fails to propagate slice boundary proofs across iteration
steps.
Kernels advance slices using reslicing idioms:

```go
a, b = a[128:], b[128:]
```

Guarding both `len(a) >= 128` and `len(b) >= 128` allows the compiler to
eliminate all bounds checks.
The kernel unrolls across eight ZMM accumulators, processing 128 floats per
iteration.
Subsequent loops process 64 floats with four accumulators and 16 floats with one
accumulator.
Masked loads handle the remaining tail elements.

Every AVX-512 kernel maintains the upper-state register invariant.
Modifying 512-bit ZMM registers leaves dirty upper bits.
Subsequent legacy Streaming SIMD Extensions (SSE) instructions trigger a 180 ns
hardware state transition penalty.
Go compiler function epilogues emit SSE instructions.
To prevent this penalty, kernels store reduced vector outputs to a stack array
before clearing state.
Assembly and intrinsics invoke `archsimd.ClearAVXUpperBits()` or `VZEROUPPER`
before returning control to scalar Go.

```text
┌──────────────────────────────────────────────────────────────┐
│             CLI Flag: -distance=<pure|simd|fp16>             │
└──────────────────────────────┬───────────────────────────────┘
                               │
                               ▼
┌──────────────────────────────────────────────────────────────┐
│                      Variant.Kernels()                       │
├───────────────┬──────────────────────────────┬───────────────┤
│ pure          │ simd                         │ fp16          │
│ • Scalar Go   │ • AVX-512F: avx512           │ • Requires    │
│ • Universal   │ • Other: portable SIMD       │   AVX-512F    │
└───────────────┴──────────────────────────────┴───────────────┘
```

## 6. FP16 Row Storage & Quantization

The `fp16` variant compresses stored vectors using Institute of Electrical and
Electronics Engineers (IEEE) 754 binary16 encoding.
Each vector element occupies 16 bits: 1 sign bit, 5 exponent bits, and 10
mantissa bits.
Conversion uses round-to-nearest-ties-even rounding.

FP16 halves memory bus traffic during scans.
Scanning 10,000 vectors of dimension 1024 requires streaming 41 Megabytes (MB)
in float32.
The same scan streams only 20 MB in fp16.
Because vector scanning is memory bound, bandwidth reduction directly cuts scan
latency.
On Intel Emerald Rapids, 10,000-row scan latency drops from 146 ns to 72 ns per
row.
On Intel Granite Rapids, latency drops from 139 ns to 71 ns per row.
Precision remains high.
Tests on 2,000 random unit vectors at dimension 64 achieve 1.000 recall at top
10 against exact float32 results.

The FP16 format restricts numerical dynamic range.
The maximum representable finite value is 65504.
Values exceeding 65504 cause `vector.EncodeFP16` to return `ErrFloat16Overflow`.
Non-finite inputs (NaN and infinity) return errors.
Upsert validates and encodes all vectors into temporary buffers before altering
table rows.
Failed encodings leave table state completely untouched.

The query vector remains 32-bit float throughout execution.
At 1024 dimensions, a query vector occupies only 4 Kilobytes (KB) and fits
within L1 cache.
Only stored rows utilize 16-bit encoding.

Hardware assembly accelerates row decompression during document retrieval.
`Kernels.Decode16` expands 16-bit rows into 32-bit floats using the AVX-512
`VCVTPH2PS` instruction.
This hardware decoding avoids a 25 microsecond scalar decoding overhead per
query.

```text
IEEE 754 Binary16 Bit Layout:
┌───┬─────────────┬────────────────────────────────┐
│ S │  Exponent   │           Fraction             │
│ 1 │   5 bits    │           10 bits              │
└───┴─────────────┴────────────────────────────────┘
 15  14         10 9                              0
```

## 7. Search Execution & Candidate Ranking

Search scans in-memory rows using a bounded priority queue.
`Table.Search` allocates a `hitHeap` bounded to `top_k` capacity.
The heap maintains candidate records using `container/heap`.

For ascending distance metrics (Cosine and Euclidean Squared), `hitHeap`
functions as a max-heap.
The candidate with the largest distance sits at heap root (`h.hits[0]`).
For descending metrics (Dot Product), `hitHeap` functions as a min-heap.
The candidate with the lowest score sits at the root.

The scan loop prunes non-competitive candidates without heap mutations.
When the heap reaches `top_k` elements, the scanner inspects candidate score
against the root score.
For ascending metrics, candidates with scores greater than root are discarded
immediately.
For descending metrics, candidates with scores less than root are discarded.
Discarded candidates bypass heap insertion and restructuring overhead.

When a candidate beats the root score in a full heap, the scanner updates the
root in place:

```go
h.hits[0] = cand
heap.Fix(&h, 0)
```

This avoids separate heap pop and push operations.

Score ties break deterministically using document identifiers.
Comparison functions `cmpAsc` and `cmpDesc` apply secondary lexicographical
sorting on `docIDs`:

```go
if a.score != b.score {
	return cmp.Compare(a.score, b.score)
}
return strings.Compare(a.id, b.id)
```

Identical scores yield stable, deterministic result orders across queries.

Scalar pre-filtering executes prior to vector distance math.
If a query specifies an `EqualityFilter`, the scanner checks row attribute
values before computing distances.
Rows failing filter matching skip vector dot products and heap evaluation
entirely.

Result materialization finalizes the top candidates.
`slices.SortFunc` sorts heap candidates into final rank order.
The engine materializes full `cloudyneighpb.Record` structures for surviving
candidates.
If rows use 16-bit storage, `Kernels.Decode16` expands vectors into float32
slices.
Search returns scored records alongside scan and materialization timing
metrics.

```text
┌──────────────────────────────────────────────────────────────┐
│                     Table Row Candidate                      │
└──────────────────────────────┬───────────────────────────────┘
                               │
                               ▼
                 ┌───────────────────────────┐
                 │  Matches EqualityFilter?  │
                 └─────────────┬─────────────┘
                               │
                      Yes      │      No (Skip row)
               ┌───────────────┴───────────────┐
               │                               ▼
               ▼                      [Discard candidate]
  ┌───────────────────────────┐
  │ Compute SIMD Vector Score │
  └────────────┬──────────────┘
               │
               ▼
  ┌───────────────────────────┐
  │  Heap full & beats root?  │
  └────────────┬──────────────┘
               │
      Yes      │      No (Worse than root)
    ┌──────────┴──────────┐
    │                     ▼
    ▼            [Discard candidate]
┌───────────────────────────┐
│ Replace root & heap.Fix() │
└───────────────────────────┘
```

## Open Questions

`CONSIDER(ali):` Evaluate 8-bit integer (int8) quantization for row vectors.
Int8 halves memory bandwidth again to 10 MB per query.
It requires per-row or per-dimension scaling factors and empirical recall
benchmarking.
