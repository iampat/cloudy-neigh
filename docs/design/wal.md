# LogStream

## 1. Overview
LogStream is Layer 1 of the storage architecture. It provides an append-only, sequentially numbered log primitive on top of Cloud Object Storage without an external coordination service.

LogStream operates directly on sequential object keys. It does not use Content-Addressed Storage (CAS).

```text
wal/
├── main/
│   ├── 00000000000000000001.recordio
│   ├── 00000000000000000002.recordio
│   └── 00000000000000000003.recordio   <-- head (seq 3)
└── _meta/
    └── 00000000000000000001.recordio   <-- branch lifecycle events
```

## 2. Object Layout & Key Scheme
- Segment key format: `wal/<stream>/<020d_seq>.recordio`.
- `<seq>` is a fixed-width 20-digit zero-padded decimal integer (supporting up to $2^{64}-1$).
- Lexicographical string sorting matches numeric sequence order.
- Each segment file is an immutable RecordIO container storing $N$ binary records with CRC32C integrity checksums.

## 3. Append Protocol (Atomic Conditional Write)
To append records without coordination:
1. The writer determines the target sequence: `seq = last_known_seq + 1`.
2. The writer issues a conditional create:
   - GCS: `Put(key, if-generation-match=0)`
   - AWS S3: `Put(key, If-None-Match="*")`
3. If the write succeeds (`200 OK`), the sequence number is committed.
4. If a conflict occurs (`412 Precondition Failed`), another writer claimed `seq`. The writer increments `seq` and retries.

## 4. Tail Finding & Recovery (Exponential Jump Search)
When a writer starts cold or falls behind:
1. `Tail(stream)` issues a `List` call with limit 1000.
2. If segments exceed 1000, it performs an exponential probing search (`jump`) to find the head in $O(\log N)$ round-trips.

## 5. Delivery Guarantees & Semantics
- **At-Least-Once Delivery**: A lost acknowledgment during retry may cause identical batches across adjacent segments. Consumers must handle deduplication or apply idempotent state mutations.
- **Contiguity Invariant**: Live sequence numbers form a contiguous range `1..N` with no gaps.
