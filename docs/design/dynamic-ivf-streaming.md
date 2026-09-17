# Dynamic Voronoi-Clustered Storage and Streaming

**Status:** Proposed, 2026-09-17

## 1. Problem Statement

Holding all vectors in memory limits dataset scale to available RAM.
Brute-force streaming uncompressed vectors from disk or cloud storage takes 800 ms to 80 seconds.
This latency violates interactive search requirements.

We need a storage and retrieval engine that:
1. Keeps uncompressed vector data in immutable columnar files on cloud storage and local NVMe cache.
2. Dynamically partitions vectors into Voronoi cells, splitting buckets as they grow.
3. Prunes over 95% of data during search using centroid routing and file-level Bloom filters.
4. Preserves append-only ingestion durability without global re-clustering jobs.

## 2. Storage Layout and Bucket Format

```text
[Bucket Root]
├── refs/heads/<branch>       --> BranchManifest (checkpoint_seq, centroids[], segments[])
└── segments/
    ├── <cluster_id>.vec      --> Dense vectors and Footer (Bloom filter, block index)
    ├── <cluster_id>.doc      --> Document attributes and text payload
    └── delta_<seq>.recordio  --> Uncompacted incoming WAL flush
```

### Segment File Layout (`.vec`)

Each cluster file holds a contiguous array of dense float32 vectors followed by a binary footer.

```text
┌─────────────────────────────────────────────────────────────────────────────┐
│ Contiguous Vectors: [v0, v1, v2, ... vN]  (N * dim * 4 bytes)               │
├─────────────────────────────────────────────────────────────────────────────┤
│ Document ID Map:    [doc_id_0, doc_id_1, ... doc_id_N]                      │
├─────────────────────────────────────────────────────────────────────────────┤
│ Split-Block Bloom Filter: 10 bits/key for metadata tags (tenant, lang)       │
├─────────────────────────────────────────────────────────────────────────────┤
│ Footer Header: doc_count (4B), dim (2B), metric (1B), bloom_bytes (4B), CRC │
└─────────────────────────────────────────────────────────────────────────────┘
```

- Each file carries its own Bloom filter.
- Rebuilding a filter during a split requires zero cross-file coordination.
- Reading the footer takes one 4 KiB to 16 KiB `pread` from the end of the file.
- The default bucket capacity target is 10,000 vectors (~40 MB float32).
- The threshold is configurable via `max_bucket_vectors`.

## 3. Two-Tier Retrieval Architecture

```text
Query Vector Q + Filter (e.g. tenant="corp")
  │
  ├─▶ Tier 1: Clustered Historical Storage
  │   1. Scan in-memory Centroid Table (SIMD Cosine)
  │   2. Pick top P closest centroids (e.g., P = 10 to 20)
  │   3. Evaluate each candidate file's Bloom filter
  │      └─ Skip file if tenant="corp" is absent (0 vector I/O)
  │   4. Fetch surviving clusters via pread from NVMe (or GCS Range Read)
  │   5. Score vectors using SIMD Cosine, insert into bounded top-K heap
  │
  ├─▶ Tier 2: Active Delta Tier (Unclustered)
  │   1. Brute-force scan active Memtable and recent delta segments
  │   2. Score candidates using SIMD Cosine (< 10k vectors, < 1.5 ms)
  │   3. Insert into bounded top-K heap
  │
  └─▶ Merge: Top-K min-heap outputs global winners
```

## 4. Local Bucket Split Protocol

A bucket split occurs when a segment exceeds twice the target capacity (e.g., 20,000 vectors).

```text
Compactor detects Bucket B exceeds threshold (e.g. 20,000 vectors / 80 MB)
  │
  ├─▶ 1. Read Bucket B vectors into memory
  │
  ├─▶ 2. Run local 2-means (k=2) to produce child centroids: c1 and c2
  │
  ├─▶ 3. Partition vectors into Bucket B1 and Bucket B2 (~10,000 vectors each)
  │
  ├─▶ 4. Construct Block Bloom filters for B1 and B2 from respective doc metadata
  │
  ├─▶ 5. Write segments/B1.vec and segments/B2.vec to GCS (Condition: Absent=true)
  │
  └─▶ 6. Commit CAS update on refs/heads/<branch>:
         - Remove centroid c
         - Add centroids c1, c2
         - Mark bucket B superseded (protected by reader lease TTL)
```

The split blast radius is strictly local.
No other bucket or centroid is modified.

## 5. Multi-Step Implementation Plan

### PR 1: Columnar Vector Segment Layout (`.vec`) and Block Bloom Filter
- Components:
  - `segment/vector.go`: `VectorWriter` and `VectorReader`.
  - Binary framing with 4-byte CRC-32C validation and trailing metadata footer.
  - Split-Block Bloom filter encoder and decoder in `segment/bloom.go`.
- Validation:
  - Unit tests for write and read round-trip with 1024-dim vectors.
  - Verification of `VectorReader.ReadRange` and `VectorReader.ReadFooter`.
  - Negative and positive Bloom filter accuracy tests.
  - Torn footer and checksum corruption test cases.

### PR 2: Local 2-Means Vector Splitter
- Components:
  - `query/cluster/kmeans.go`: local 2-means clustering implementation.
  - Pure Go with SIMD distance acceleration.
  - Balanced partitioning fallback if clusters are degenerate.
- Validation:
  - Synthetic dataset cluster discovery tests.
  - Degenerate collinear data handling.
  - Vector partition index verification.
  - Benchmark measuring 2-means execution time across 20,000 1024-dim vectors.

### PR 3: Centroid Table and Voronoi Multi-Probe Router
- Components:
  - `query/cluster/table.go`: `CentroidTable`.
  - Flat contiguous float32 storage of all active centroids.
  - Thread-safe centroid swaps via `atomic.Pointer`.
  - `Route(query []float32, topP int) []string`: returns top $P$ closest bucket IDs.
- Validation:
  - Equivalence tests against linear scan baseline.
  - Concurrent `Route` calls during centroid split updates under race detector.
  - Multi-threaded SIMD benchmark for routing throughput.

### PR 4: Tiered Query Engine with Local NVMe Streaming
- Components:
  - `query/engine.go`: integrated two-tier query executor.
  - Connects in-memory `activeMemtable` with `CentroidTable` and `VectorReader`.
  - Pre-evaluates Bloom filters to prune I/O.
  - Streams surviving blocks via `pread` on NVMe, falling back to GCS range reads.
- Validation:
  - End-to-end integration test: ingest, split, and query.
  - Recall validation: compare top-10 exact brute force vs. multi-probe IVF recall.
  - Metadata pruning test: ensure Bloom filter drops zero-match files before disk I/O.

### PR 5: Background Compactor and Dynamic Split Worker
- Components:
  - `ingest/compactor.go`: background compaction loop.
  - Ingests unclustered delta segments, routes vectors to target buckets.
  - Triggers PR 2 splitter when bucket exceeds `MaxBucketVectors` (default: 10,000).
  - Atomically commits updated centroids and segment references via KVFS branch CAS.
- Validation:
  - End-to-end continuous ingestion test with live queries running concurrently.
  - Simulation of CAS conflicts (HTTP 412) with exponential backoff and retry.
  - Reader lease safety test: confirm superseded files remain accessible during active reads.

## 6. Failure Modes and Recovery Invariants

| Failure Event | Recovery Mechanism |
| :--- | :--- |
| **Crash during bucket split write** | Uncommitted `.vec` files on GCS remain unreferenced garbage. Compactor retries on restart. Zero data loss. |
| **Manifest CAS collision (HTTP 412)** | Compactor refreshes branch head, re-checks bucket thresholds, and retries commit. |
| **Active queries reading old bucket** | Replaced segments retain reader leases and a 2-hour deletion TTL. Readers never receive file-not-found errors. |
| **Compaction falls behind ingestion** | Ingestion throttles if uncompacted delta vectors exceed 50,000. Protects query latency. |
