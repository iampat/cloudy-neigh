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
* **Problem:** Parquet and LevelDB place metadata in footers because streaming writers to append-only filesystems cannot seek backward. On GCS, reading a footer requires guessing footer size or issuing extra range reads.
* **Mechanics:** Flushes write whole blobs to GCS from an in-memory Memtable. All block offsets, sizes, and statistics are known before upload starts.
* **Layout:**
  - `[0..3]`: Magic bytes `0x434C4E53` ("CLNS").
  - `[4..7]`: Header length uint32 LE.
  - `[8..N]`: Protobuf header containing `repeated BlockEntry` and Bloom filters.
  - `[N+1..EOF]`: Compressed data blocks (128 KiB docs, 256 KiB vectors).
* **Latency Trade-off:** An initial `ReadRange(0, 1 MiB)` fetches the header and first data blocks in one 25 ms network hop. Queries hitting early blocks need no second read. Discarding 1 MiB on distant block lookups costs under 2 ms transfer time.
* **Verdict:** Header index adopted for cloudy-neigh segment files.
* **Status:** Adopted for segment design.

## 2. Skiplist vs Flat Block Array for RecordIO
* **Date:** 2026-09-04
* **Topic:** Random access point lookups in stored RecordIO files.
* **Options Considered:** On-disk/in-memory skiplist index vs flat contiguous block array.
* **Storage Analysis:** Traversing skiplist pointers on GCS requires O(log N) sequential network reads. Ten pointer dereferences take 250 ms.
* **Memory Analysis:** Stored RecordIO segments are immutable once written. Dynamic balancing is unneeded. Skiplist nodes allocate `next []*Node` slices, consuming 4x memory with poor CPU cache locality.
* **Verdict:** Flat block array adopted. Store `[]BlockEntry` in the segment header. Readers binary search contiguous memory via `slices.BinarySearchFunc` in CPU L1/L2 cache, then issue one range read to the data block offset.
* **Status:** Adopted for block indexing.

## 3. Memcomparable Binary Keys
* **Date:** 2026-09-04
* **Topic:** Primary key and range filter encoding.
* **Reference:** [luci-go cmpbin](https://chromium.googlesource.com/infra/luci/luci-go/+/refs/heads/main/common/data/cmpbin/)
* **Mechanics:** Encodes signed and unsigned integers with inverted bits for negatives. Transforms IEEE-754 floats via Herf radix algorithm, preserving order across negative numbers, NaN, and Inf. Encodes strings with 7 data bits and 1 stop bit per byte.
* **Application:** Columnar zone maps, sort keys, and min/max block statistics in `BlockEntry`. Allows byte-level sorting via `bytes.Compare` without deserializing.
* **Verdict:** Adopt for segment block statistics and composite keys.
* **Status:** Adopt for segment block statistics.

## 4. Threshold Dispatcher Batching
* **Date:** 2026-09-04
* **Topic:** Memtable flush triggers.
* **Reference:** [luci-go dispatcher](https://chromium.googlesource.com/infra/luci/luci-go/+/refs/heads/main/common/sync/dispatcher/)
* **Mechanics:** Asynchronous input channel feeds a worker loop backed by a min-heap buffer. Triggers flushes on three bounds: item count (`BatchItemsMax`), total byte size (`BatchSizeMax`), or elapsed age (`BatchAgeMax`).
* **Application:** Memtable flush engine. Flushes idle branches after time limits (e.g. 1 minute) and high-throughput branches when size limits are reached (e.g. 64 MB).
* **Verdict:** Adopt pattern for ingestion flush worker.
* **Status:** Adopt for ingestion pipeline.

## 5. DNS Multi-IP Dialing Transport
* **Date:** 2026-09-04
* **Topic:** GCS connection pool throttling.
* **Reference:** [grailbio s3transport](https://github.com/grailbio/base/blob/master/file/s3file/s3transport/transport.go)
* **Mechanics:** Go `http.DefaultTransport` pins connections to a single resolved IP address. High concurrent read traffic saturates that IP and triggers HTTP 503 SlowDown errors. The transport resolves all DNS A/AAAA records, maintains an IP pool, and distributes requests round-robin with TLS SNI rewriting.
* **Application:** GCS HTTP client transport under high query load.
* **Verdict:** Postpone. Relevant when concurrent query throughput exceeds single IP bandwidth.
* **Status:** Postpone until load testing.

## 6. Ordered Completion Queue
* **Date:** 2026-09-04
* **Topic:** Ingestion worker concurrency.
* **Reference:** [grailbio syncqueue](https://github.com/grailbio/base/blob/master/syncqueue/ordered_queue.go)
* **Mechanics:** Worker goroutines parse, validate, and serialize document mutations concurrently out of order. An ordered queue buffers completed tasks and delivers them in strict monotonic sequence order without worker stalling.
* **Application:** Feeding serialized mutations into `logstream.Log`.
* **Verdict:** Postpone. Adopt when document parsing CPU time limits ingestion throughput.
* **Status:** Postpone until needed.

## 7. K-Way Min-Heap Merge and Lineage
* **Date:** 2026-09-04
* **Topic:** Multi-segment compaction and postings union.
* **Reference:** [go-sstables priority queue and merger](https://github.com/thomasjungblut/go-sstables)
* **Mechanics:** Generic 1-based min-heap streams sorted entries across multiple segment readers with pluggable conflict reducers. Pairs with a `floodFill` algorithm that mandates contiguous inclusion of intermediate segments between sequence bounds.
* **Application:** Segment compaction. Preserves snapshot isolation and prevents deleted tombstones from resurrecting older records.
* **Verdict:** Adopt for segment compaction and postings list unions.
* **Status:** Adopt when building compaction.

## 8. Bitset Nonzero Word Scanning
* **Date:** 2026-09-04
* **Topic:** Document filter scans.
* **Reference:** [grailbio bitset](https://github.com/grailbio/base/blob/master/bitset/bitset.go)
* **Mechanics:** Word-aligned `[]uintptr` bitset. Scans set bits using hardware trailing zero counts (`bits.TrailingZeros64`) and clears lowest bits with `w & (w - 1)`. Skips entire 64-bit zero words in one branch.
* **Application:** Iterating candidate document IDs and tombstone bitsets during vector search.
* **Verdict:** Adopt for candidate scoring filters.
* **Status:** Adopt for vector filter pass.

## 9. Lazy Slot Stale-While-Revalidate
* **Date:** 2026-09-04
* **Topic:** Branch manifest caching.
* **Reference:** [luci-go lazyslot](https://chromium.googlesource.com/infra/luci/luci-go/+/refs/heads/main/common/data/caching/lazyslot/)
* **Mechanics:** Cache hit returns cached data instantly. Expired cache triggers exactly one background refresh while concurrent readers continue receiving the stale copy without blocking.
* **Application:** Caching `refs/heads/<branch>` manifests on query nodes.
* **Verdict:** Adopt when building query nodes. Eliminates 25 ms GCS network checks on hot read paths.
* **Status:** Adopt for query engine.
