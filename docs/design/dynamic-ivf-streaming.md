# Dynamic Voronoi Streaming Storage

**Status:** Proposed, 2026-09-17

## 1. Problem Statement

Holding all vectors in memory limits dataset scale to available server RAM.
For ten million to one billion vectors, RAM hardware costs become prohibitive.

Brute-force streaming uncompressed vectors from disk or cloud storage is also unviable:
- In-region cloud object storage (GCS) provides 4 to 8 GB/s over parallel connections with 10 to 15 ms latency.
- Local NVMe solid-state drives deliver 3 to 6 GB/s sequential read throughput.
- At one billion vectors (4 TB raw data), a full scan takes 800 seconds on cloud storage and 800 seconds on disk.
- Even at ten million vectors (40 GB), an unpruned scan takes 8 seconds, violating interactive search latency (<50 ms).

Static clustering algorithms (such as global k-means) do not fit an append-only cloud search engine:
- Re-clustering millions of vectors on incoming writes is computationally impossible.
- Centroid drift requires rewriting historical segments, breaking immutable storage and inflating write amplification.

We need a design that:
1. Keeps uncompressed vector data on cloud storage and local NVMe cache.
2. Dynamically partitions vectors into Voronoi cells, starting small and splitting buckets as data grows.
3. Prunes over 95% of data during search using centroid routing and metadata Bloom filters.
4. Preserves append-only ingestion durability without global re-clustering.

## 2. Proposed Solution

```text
┌─────────────────────────────────────────────────────────────────────────────┐
│                          Two-Tier Query Architecture                        │
│                                                                             │
│  Query Vector Q + Filter (e.g. tenant="corp")                               │
│    │                                                                        │
│    ├─▶ Tier 1: Clustered Historical Tier (Voronoi Pruning)                  │
│    │   1. Scan in-memory Centroid Table (SIMD Cosine)                       │
│    │   2. Pick top P closest Voronoi cells (e.g., P = 10 to 20)             │
│    │   3. Evaluate candidate file Bloom filters (drop cells if tag missing) │
│    │   4. Fetch surviving clusters via pread from NVMe or GCS Range Read    │
│    │   5. Score vectors using SIMD Cosine into bounded min-heap             │
│    │                                                                        │
│    ├─▶ Tier 2: Active Delta Tier (Unclustered)                              │
│    │   1. Brute-force scan active Memtable and recent delta segments        │
│    │   2. Score candidates using SIMD Cosine (< 10k vectors, < 1.5 ms)      │
│    │   3. Insert hits into bounded min-heap                                 │
│    │                                                                        │
│    └─▶ Merge: Top-K min-heap yields global winners                          │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 2.1 Dynamic Voronoi Partitioning

Voronoi cells partition vector space using hyperplanes equidistant between centroids.
Every vector belongs to exactly one Voronoi cell. The cells never overlap.

The system starts with a small number of centroids (e.g., 10 to 100).
As vectors accumulate, buckets that exceed capacity split into two child cells via local 2-means.
This avoids over-partitioning a small collection and dynamically scales to millions of cells.

### 2.2 Columnar Storage Layout and Self-Contained Footers

Vector data resides in standalone `.vec` files, separated from document attributes (`.doc`).

```text
┌─────────────────────────────────────────────────────────────────────────────┐
│ Contiguous Float32 Vectors: [v0, v1, v2, ... vN]  (N * dim * 4 bytes)       │
├─────────────────────────────────────────────────────────────────────────────┤
│ Document ID Map:            [doc_id_0, doc_id_1, ... doc_id_N]              │
├─────────────────────────────────────────────────────────────────────────────┤
│ Split-Block Bloom Filter:   Metadata tags for scalar pre-filtering          │
├─────────────────────────────────────────────────────────────────────────────┤
│ File Footer:                Vector count, dimensions, metric, checksums     │
└─────────────────────────────────────────────────────────────────────────────┘
```

- Each file carries its own Bloom filter.
- Rebuilding a filter during a bucket split requires zero coordination across files.
- Query nodes read the footer with a single 4 KiB to 16 KiB range read from the end of the file.
- The target bucket size is configurable, defaulting to 10,000 vectors (~40 MB float32).

### 2.3 Local Split-on-Full Protocol

When a bucket exceeds twice the target capacity (e.g., 20,000 vectors / 80 MB):

```text
Compactor detects Bucket B exceeds threshold
  │
  ├─▶ 1. Read Bucket B vectors into memory
  ├─▶ 2. Run local 2-means (k=2) to produce child centroids: c1 and c2
  ├─▶ 3. Partition vectors into Bucket B1 and Bucket B2 (~10,000 vectors each)
  ├─▶ 4. Build Block Bloom filters for B1 and B2 from document attributes
  ├─▶ 5. Write segments/B1.vec and segments/B2.vec (Condition: Absent=true)
  └─▶ 6. Commit atomic CAS update on refs/heads/<branch>:
         - Remove centroid c
         - Add centroids c1 and c2
         - Mark bucket B superseded (retained for reader lease TTL)
```

The split blast radius is strictly local.
No other bucket, segment, or centroid is modified.

### 2.4 Two-Tier Ingestion and Compaction

To preserve write durability without blocking on clustering:
1. Incoming writes append to the write-ahead log and buffer in an in-memory Memtable.
2. The flusher periodically writes unclustered delta segments to cloud storage.
3. Live queries scan the unclustered delta tier using brute-force search alongside the clustered tier.
4. A background compactor routes delta vectors into target Voronoi buckets and triggers splits as needed.

## 3. Failure Modes and Invariants

| Failure Event | System Invariant & Recovery |
| :--- | :--- |
| **Crash during bucket split write** | Uncommitted `.vec` files on cloud storage remain unreferenced garbage. The compactor retries on restart. Zero data loss. |
| **Manifest CAS collision (HTTP 412)** | The compactor refreshes the branch head, re-checks bucket thresholds, and retries the commit. |
| **Active queries reading old bucket** | Superseded segments retain reader leases and a 2-hour deletion TTL. Readers never encounter missing files. |
| **Compaction falls behind ingestion** | Ingestion throttles when uncompacted delta vectors exceed 50,000. This preserves query latency bounds. |

## 4. Phased Delivery Roadmap

The architecture rolls out in five progressive phases.
Each phase delivers a functional, verifiable capability.

```text
Phase 1: Segment Format & Pruning Primitives
  │
  ▼
Phase 2: Centroid Routing & Local Splitting
  │
  ▼
Phase 3: Two-Tier Query Execution
  │
  ▼
Phase 4: Dynamic Compaction & Bucket Lifecycle
  │
  ▼
Phase 5: Quantization for Ultra-Large Scale (1B Vectors)
```

### Phase 1: Segment Format and Pruning Primitives
Establish the physical columnar vector container (`.vec`).
Implement binary framing with CRC-32C integrity validation, trailing metadata footers, and embedded Block Bloom filters.
Enable ranged file reads from local NVMe cache and cloud storage.

### Phase 2: Centroid Routing and Local Splitting
Build the in-memory centroid table and multi-probe routing logic.
Implement the local 2-means clustering algorithm to partition overflowing vector buffers into two balanced child cells.

### Phase 3: Two-Tier Query Execution
Integrate in-memory centroid routing with local NVMe chunk streaming.
Pre-evaluate Bloom filters to drop non-matching files before disk I/O.
Execute parallel queries across the historical clustered tier and the active delta tier, merging results in a top-K heap.

### Phase 4: Dynamic Compaction and Bucket Lifecycle
Deploy the background compaction worker.
Route unclustered delta records into matching Voronoi buckets.
Trigger bucket splits when files exceed capacity and atomically commit centroid and manifest updates via branch CAS.

### Phase 5: Quantization for Ultra-Large Scale (1B Vectors)
Introduce 8-bit Scalar Quantization (SQ8) and Product Quantization (PQ/ScaNN).
Compress vector files by 4x to 32x to allow billion-scale vector datasets to fit comfortably within local NVMe storage and server memory.
