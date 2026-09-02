# LogStream

## Problem

Distributed writers require an append-only, sequentially numbered log
primitive on cloud object storage without running a coordination service.

## Model

LogStream is Layer 1 of the storage architecture. It provides an append-only,
sequentially numbered log primitive on top of Cloud Object Storage without an
external coordination service.

LogStream operates directly on sequential object keys. It does not use
Content-Addressed Storage (CAS).

```text
wal/
├── main/
│   ├── 00000000000000000001.recordio
│   ├── 00000000000000000002.recordio
│   └── 00000000000000000003.recordio   <-- head (seq 3)
└── _meta/
    └── 00000000000000000001.recordio   <-- branch lifecycle events
```

## Storage layout

- Segment key format: `wal/<stream>/<020d_seq>.recordio`.
- `<seq>` is a fixed-width 20-digit zero-padded decimal integer (supporting up to 2^64 - 1).
- Lexicographical string sorting matches numeric sequence order.
- Each segment file is an immutable RecordIO container storing one or more binary records with CRC32C integrity checksums.

## Append protocol

Writers append records without central coordination:
1. The writer determines the target sequence: `seq = last_known_seq + 1`.
2. The writer encodes one or more records into a single RecordIO segment buffer.
3. The writer issues a conditional create:
   - GCS: `Put(key, if-generation-match=0)`
   - AWS S3: `Put(key, If-None-Match="*")`
4. If the write succeeds (`200 OK`), all records in the segment commit atomically under sequence `seq`.
5. If a conflict occurs (`412 Precondition Failed`), another writer claimed `seq`. The writer increments `seq` and retries.

### Multi-record batching

Callers can append multiple records in a single call:
`Append(ctx, records ...Record) (uint64, error)`

LogStream packs all records into a single `.recordio` segment. This commits the
entire batch in one cloud round-trip, dividing write costs across all records in
the batch.

## Tail finding and recovery

When a writer starts cold or falls behind:
1. `Tail(stream)` issues a `List` call with limit 1000.
2. If segments exceed 1000, it performs an exponential probing search (`jump`) to find the head in O(log N) round-trips.

## Delivery guarantees

- **At-Least-Once Delivery**: A lost acknowledgment during retry may cause identical batches across adjacent segments. Consumers must handle deduplication or apply idempotent state mutations.
- **Contiguity Invariant**: Live sequence numbers form a contiguous range `1..N` with no gaps.
