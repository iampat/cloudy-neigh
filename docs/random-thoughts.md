# Random Thoughts, Algorithms, and Brainstorms

Catalog of architectural patterns, third-party algorithms, and brainstorming session outcomes.

## Index

1. [Header vs Footer Segment Index](#1-header-vs-footer-segment-index): Store segment block indexes at byte zero to eliminate reverse range lookups on GCS.
2. [Skiplist vs Flat Block Array for RecordIO](#2-skiplist-vs-flat-block-array-for-recordio): Use skiplist only for mutable in-memory Memtables. Use flat slice binary search for immutable RecordIO files.
3. [Memcomparable Binary Keys](#3-memcomparable-binary-keys): Encode numbers and strings so raw bytes sort in numerical order via `bytes.Compare`.
4. [Threshold Dispatcher Batching](#4-threshold-dispatcher-batching): Flush queues on three bounds: item count, total byte size, or elapsed age.
5. [DNS Multi-IP Dialing Transport](#5-dns-multi-ip-dialing-transport): Round-robin requests across cloud edge IPs to prevent HTTP 503 SlowDown errors.
6. [Ordered Completion Queue](#6-ordered-completion-queue): Execute document validation in parallel while enforcing strict sequence order into WAL.
7. [K-Way Min-Heap Merge and Lineage](#7-k-way-min-heap-merge-and-lineage): Merge sorted iterators and protect snapshot causality during segment compaction.
8. [Bitset Nonzero Word Scanning](#8-bitset-nonzero-word-scanning): Scan candidate document IDs skipping empty words with hardware trailing zeros count.
9. [Lazy Slot Stale-While-Revalidate](#9-lazy-slot-stale-while-revalidate): Cache branch manifests without blocking read queries on GCS network polls.

---

## 1. Header vs Footer Segment Index
* **Date:** 2026-09-04
* **Topic:** Segment file metadata placement on GCS.
* **Options Considered:** Footer index (Parquet style) vs Header index at byte zero.
* **Verdict:** Header index adopted. Flushes write whole blobs to GCS from memory. Reading byte zero returns the index and first data blocks in one 1 MiB range read.
* **Status:** Adopted for segment design.

## 2. Skiplist vs Flat Block Array for RecordIO
* **Date:** 2026-09-04
* **Topic:** Random access point lookups in stored RecordIO files.
* **Options Considered:** Skiplist index vs flat contiguous block index.
* **Verdict:** Flat block array adopted. Skiplists on disk cause O(log N) sequential GCS hops. For immutable files, a flat array binary search uses four times less RAM and hits CPU cache.
* **Status:** Adopted for block indexing.

## 3. Memcomparable Binary Keys
* **Date:** 2026-09-04
* **Topic:** Primary key and range filter encoding.
* **Reference:** [luci-go cmpbin](https://chromium.googlesource.com/infra/luci/luci-go/+/refs/heads/main/common/data/cmpbin/)
* **Verdict:** Adopt for segment block statistics and sort keys. Compare raw bytes without deserializing.
* **Status:** Adopt for segment block statistics.

## 4. Threshold Dispatcher Batching
* **Date:** 2026-09-04
* **Topic:** Memtable flush triggers.
* **Reference:** [luci-go dispatcher](https://chromium.googlesource.com/infra/luci/luci-go/+/refs/heads/main/common/sync/dispatcher/)
* **Verdict:** Adopt for ingestion flush worker. Flushes cleanly on size, item count, or time limits.
* **Status:** Adopt for ingestion pipeline.

## 5. DNS Multi-IP Dialing Transport
* **Date:** 2026-09-04
* **Topic:** GCS connection pool throttling.
* **Reference:** [grailbio s3transport](https://github.com/grailbio/base/blob/master/file/s3file/s3transport/transport.go)
* **Verdict:** Postpone. Useful when concurrent read traffic causes HTTP 503 SlowDown errors on single IPs.
* **Status:** Postpone until load testing.

## 6. Ordered Completion Queue
* **Date:** 2026-09-04
* **Topic:** Ingestion worker concurrency.
* **Reference:** [grailbio syncqueue](https://github.com/grailbio/base/blob/master/syncqueue/ordered_queue.go)
* **Verdict:** Postpone. Validate documents concurrently once CPU parsing becomes a bottleneck.
* **Status:** Postpone until needed.

## 7. K-Way Min-Heap Merge and Lineage
* **Date:** 2026-09-04
* **Topic:** Multi-segment compaction.
* **Reference:** [go-sstables priority queue](https://github.com/thomasjungblut/go-sstables)
* **Verdict:** Adopt for segment compaction. Merges sorted iterators and preserves snapshot lineage.
* **Status:** Adopt when building compaction.

## 8. Bitset Nonzero Word Scanning
* **Date:** 2026-09-04
* **Topic:** Document filter scans.
* **Reference:** [grailbio bitset](https://github.com/grailbio/base/blob/master/bitset/bitset.go)
* **Verdict:** Adopt for document filtering and tombstones. Uses hardware trailing zero counts to skip empty words.
* **Status:** Adopt for vector filter pass.

## 9. Lazy Slot Stale-While-Revalidate
* **Date:** 2026-09-04
* **Topic:** Branch manifest caching.
* **Reference:** [luci-go lazyslot](https://chromium.googlesource.com/infra/luci/luci-go/+/refs/heads/main/common/data/caching/lazyslot/)
* **Verdict:** Adopt when building query nodes. Prevents GCS manifest polling latency on read path.
* **Status:** Adopt for query engine.
