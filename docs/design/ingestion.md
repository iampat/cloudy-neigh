# Ingestion Subsystem

The ingestion subsystem processes real-time document mutations across multiple
dataset branches without external coordination.
It ingests documents from client Remote Procedure Call (RPC) requests into
durable, content-addressed storage segments.
The cloudy ingest daemon hosts both the public gRPC ingest server and the
background flusher worker.

## 1. Ingestion Pipeline Architecture

The ingestion pipeline decouples fast client acknowledgment from background
segment materialization.
The diagram below shows the component boundaries and end-to-end data flow.

```text
┌─────────────────────────┐ Upsert, Delete, Fork
│ Client                  ├────────────────────────────┐
└─────────────────────────┘                            ▼
                            ┌──────────────────────────────────────┐
                            │ IngestServer (gRPC)                  │
                            └──────────────────┬───────────────────┘
                                               │ Ingester
                                               ▼
                            ┌──────────────────────────────────────┐
                            │ LogStream Write-Ahead Log (WAL)      │
                            │ ns/<namespace>/wal/<020d_seq>.recordio│
                            └──────────────────┬───────────────────┘
                                               │ Read(seq)
                                               ▼
                            ┌──────────────────────────────────────┐
                            │ Flusher (Background Worker)          │
                            └─────────┬──────────────────┬─────────┘
        Upload Segment (CAS)          │                  │ Update Manifest (CAS)
                                      ▼                  ▼
┌──────────────────────────────────────┐   ┌───────────────────────────────┐
│ Immutable Segments                   │   │ Branch Manifests and Catalogs │
│ ns/<namespace>/segments/<hash>.recordio  │ refs/heads/<branch>.json      │
└──────────────────────────────────────┘   │ branches.json                 │
                                           └───────────────────────────────┘
```

The pipeline executes through eleven discrete stages:

1. The client issues a write RPC to IngestServer.
2. IngestServer extracts tenant identity from the request context.
3. IngestServer validates document payloads, record identifiers, and branch
names.
4. Ingester wraps mutations into storage Write-Ahead Log (WAL) records.
5. Ingester appends the batch to LogStream as a single RecordIO object.
6. LogStream commits the object with an atomic conditional write.
7. The RPC acknowledges success to the client immediately after commit.
8. The background Flusher discovers active namespaces from ns.json.
9. The Flusher tails each LogStream sequentially starting from the lowest
checkpoint.
10. The Flusher writes batch mutations into content-addressed segment files.
11. The Flusher updates branch manifests using atomic Compare-And-Swap (CAS)
operations.

## 2. gRPC Ingest Service Endpoints

The IngestService exposes three RPC endpoints: Upsert, Delete, and Fork.
The Protocol Buffers (protobuf) schema lives in
proto/cloudyneigh/v1/index.proto.

```proto
syntax = "proto3";

package cloudyneigh.v1;

service IngestService {
  rpc Upsert(UpsertRequest) returns (UpsertResponse);
  rpc Delete(DeleteRequest) returns (DeleteResponse);
  rpc Fork(ForkRequest) returns (ForkResponse);
}
```

### Endpoint Schemas

Upsert ingests a batch of documents into a dataset branch.

```proto
message UpsertRequest {
  string namespace = 1;
  repeated Record records = 2;
  string branch = 3;
}

message UpsertResponse {
  uint32 upserted_count = 1;
}

message Record {
  string id = 1;
  map<string, Vector> vectors = 2;
  map<string, AttributeValue> attributes = 3;
}

message Vector {
  repeated float values = 1;
}

message AttributeValue {
  oneof value {
    string string_value = 1;
  }
}
```

Delete appends tombstones for a list of document identifiers.

```proto
message DeleteRequest {
  string namespace = 1;
  repeated string ids = 2;
  string branch = 3;
}

message DeleteResponse {
  uint32 deleted_count = 1;
}
```

Fork duplicates an existing branch manifest to create a new branch.

```proto
message ForkRequest {
  string namespace = 1;
  string source_branch = 2;
  string target_branch = 3;
}

message ForkResponse {}
```

### Validation Rules

The server normalizes empty namespace strings to "default".
The server normalizes empty branch strings to "main".
Valid namespace and branch names must start with an ASCII letter and contain
only ASCII alphanumeric characters, hyphens, or underscores
(`^[a-zA-Z][a-zA-Z0-9_-]*$`).
Upsert returns InvalidArgument if any record pointer is nil.
Upsert returns InvalidArgument if any record contains an empty id.
Delete returns InvalidArgument if any identifier string is empty.
Fork returns InvalidArgument when target_branch is empty.
Fork returns InvalidArgument when source_branch equals target_branch.

### Multi-Tenant Routing

The gRPC interceptor extracts tenant identity from the x-tenant-id metadata
header.
The server maps tenant identities to dedicated in-memory Ingester instances.
Requests with missing tenant headers fail with Unauthenticated.
Requests with unrecognized tenant identities fail with NotFound.

### Status Code Mappings

The table below outlines gRPC status codes returned by the ingest service.

| Status Code | Condition | Description |
| --- | --- | --- |
| InvalidArgument | Bad request | Invalid name, nil record, or empty id. |
| Unauthenticated | Missing auth | Missing x-tenant-id metadata header. |
| NotFound | Missing item | Unrecognized tenant or missing source branch. |
| AlreadyExists | Target exists | Target manifest exists on fork. |
| Canceled | Canceled | Request context canceled early. |
| DeadlineExceeded | Timeout | Context deadline expired during write. |
| Internal | Storage error | Object store operation failed. |

### Batch Semantics

All records in an Upsert or Delete request commit atomically.
The Ingester serializes the entire batch into one WAL segment.
The server never commits partial batches to storage.
Clients can safely retry failed requests due to idempotent mutations.

## 3. LogStream Architecture & Sequencing

LogStream implements Layer 1 of the storage architecture.
It provides an append-only, sequentially numbered log on cloud object storage.
The log operates without an external coordination service.

### Storage Layout

Each namespace maintains its own log prefix under ns/<namespace>/wal/.
Individual log segment keys follow the pattern wal/<020d_seq>.recordio.
The sequence number is a 20-digit zero-padded decimal integer.
Lexicographical string sorting matches numeric sequence order.

```text
wal/
├── 00000000000000000001.recordio
├── 00000000000000000002.recordio
└── 00000000000000000003.recordio   <- head (seq 3)
```

### Contiguity Invariant

Committed sequence numbers form a contiguous range 1..N with zero gaps.
The reader halts iteration on the first missing sequence number.
The engine relies on this invariant for deterministic crash recovery.

### Conditional Append Protocol

Writers append records without central locking through cloud storage
preconditions.

1. The writer determines candidate sequence seq = last_known_seq + 1.
2. The writer serializes the batch into a RecordIO segment buffer.
3. The writer issues a conditional create operation to cloud storage.
On Google Cloud Storage (GCS), the writer sets if-generation-match=0.
On Amazon Web Services (AWS) S3, the writer sets If-None-Match="*".
4. If the write succeeds, the batch commits under sequence seq.
5. If the write returns HTTP 412 (Precondition Failed), another writer claimed
seq.
The writer locates the new head and retries at head + 1.

### Multi-Record Batching

LogStream packs all records in an Append call into a single segment.
This commits the entire batch in one cloud round-trip.
Batching amortizes network Round-Trip Time (RTT) and object storage PUT fees.

### Tail Discovery and Exponential Probing

When a writer starts cold, it discovers the log head.
Tail issues a List operation with a limit of 1000 keys.
If the list contains fewer than 1000 items, the last key is head.
If the list returns 1000 items, LogStream runs an exponential jump search.

The diagram below illustrates the exponential probe and binary search phases.

```text
Step 1: Exponential Probing (find upper bound)
Seq:   1000 ──▶ 1001 ──▶ 1003 ──▶ 1007 ──▶ 1015 (Absent)
              (Exists) (Exists) (Exists)

Step 2: Binary Search (locate exact head)
Range: [1007, 1015] ──▶ Mid: 1011 (Exists) ──▶ Range: [1011, 1015]
                     ──▶ Mid: 1013 (Absent) ──▶ Range: [1011, 1013]
                     ──▶ Mid: 1012 (Exists) ──▶ Head = 1012
```

The search doubles the jump step size until a segment key is absent.
It then executes a binary search between the bounds.
This locates the current stream head in O(log N) operations.

### Delivery Guarantees

LogStream provides at-least-once delivery semantics.
A network interruption during acknowledgment can cause a retry to write
duplicates.
Downstream consumers must handle deduplication or apply idempotent state
mutations.

## 4. RecordIO Binary Framing Format

RecordIO encapsulates variable-length binary payloads with 16 bytes of framing
overhead.
The wire layout guarantees corruption detection and supports zero-allocation
streaming.

### Wire Format Layout

Each frame consists of a 12-byte header, data payload, and 4-byte footer.

```text
┌───────────────────────┬───────────────────────┬──────────────┬───────────────┐
│ Length (uint64 LE)    │ LengthCRC (uint32 LE) │ Data Payload │ DataCRC       │
│ 8 Bytes               │ 4 Bytes               │ N Bytes      │ 4 Bytes       │
└───────────────────────┴───────────────────────┴──────────────┴───────────────┘
└────────────── 12-Byte Header ─────────────────┘              └─ 4-Byte Footer┘
```

The header fields use Little-Endian (LE) byte order:
- Length (8 bytes): Unsigned 64-bit integer specifying payload size N.
- LengthCRC (4 bytes): Masked CRC32C checksum of the Length bytes.

The body holds the uninterpreted payload:
- Data (N bytes): Raw byte payload.

The footer protects the payload:
- DataCRC (4 bytes): Masked CRC32C checksum of the Data bytes.

### Castagnoli CRC32C Masking

Checksums use the hardware-accelerated Castagnoli polynomial (0x82F63B78).
The implementation applies bit rotation and constant addition masking:

```go
const maskDelta = 0xa282ead8

func mask(crc uint32) uint32 {
	return ((crc >> 15) | (crc << 17)) + maskDelta
}

func unmask(masked uint32) uint32 {
	rot := masked - maskDelta
	return (rot >> 17) | (rot << 15)
}
```

Masking prevents checksum collisions with file system signatures and all-zero
storage blocks.

### Memory Model

The RecordIO package enforces a zero-allocation model in steady-state streaming.

```text
               [ File or io.Reader ]
                         │
                         ▼
┌────────────────────────────────────────────────────────┐
│ Scanner Reusable Buffer                                │
│ ┌───────────────┬────────────────────────┬───────────┐ │
│ │  12B Header   │   Payload (N bytes)    │ 4B Footer │ │
│ │  (prealloc)   │                        │ (prealloc)│ │
│ └───────────────┴───────────┬────────────┴───────────┘ │
└─────────────────────────────┼──────────────────────────┘
                              │
              Borrowed slice: buf[12 : 12+N]
                              │
                              ▼
                 [ Caller or Deserializer ]
```

Writer memory behavior:
1. The writer uses preallocated byte arrays for the header and footer.
2. A default 64 KB buffer amortizes syscall and write overhead.
3. The writer tracks stream offset and returns start offsets for each record.

Scanner memory behavior:
1. Scanner allocates a single reusable internal buffer.
2. Record returns a borrowed slice valid until the next Scan call.
3. The scanner validates Length against MaxRecordSize before expanding memory.
Corrupt length headers fail before triggering large allocations.
4. Fast skip uses io.Seeker when supported to discard payloads without
allocation.

### Error Handling and Crash Recovery

RecordIO differentiates between recoverable crash truncations and
unrecoverable data corruption.

```go
var (
	ErrTornWrite = errors.New(
		"recordio: incomplete record at stream tail (torn write)")
	ErrHeaderCorrupted = errors.New(
		"recordio: header length CRC mismatch mid-stream")
	ErrDataCorrupted = errors.New(
		"recordio: payload data CRC mismatch mid-stream")
	ErrRecordTooLarge = errors.New(
		"recordio: record size exceeds max limit")
)
```

1. Torn write at tail (recoverable):
A process crash produces incomplete frames at the end of a stream.
Scanner encounters an unexpected End of File (EOF) and returns ErrTornWrite.
Recovery callers query LastValidOffset to truncate the file at the clean
boundary.

2. Mid-stream corruption (fatal):
A CRC mismatch in the middle of a stream indicates bit corruption.
The scanner halts immediately with ErrHeaderCorrupted or ErrDataCorrupted.
Iteration never skips over corrupted records during WAL replay.

3. Clean EOF:
When EOF aligns exactly with a frame boundary, Scan returns false and Err
returns nil.

## 5. WAL Record Schema

Mutations and lifecycle events serialize into RecordIO frames inside the WAL.
The schema lives in proto/storage/v1/storage.proto.

```proto
syntax = "proto3";

package cloudyneigh.storage.v1;

enum MutationOp {
  MUTATION_OP_UNSPECIFIED = 0;
  PUT = 1;
  DELETE = 2;
}

message DocumentMutation {
  string branch = 1;
  string doc_id = 2;
  MutationOp op = 3;
  bytes payload = 4;
}

message BranchLifecycleEvent {
  enum Type {
    UNSPECIFIED = 0;
    FORK = 1;
  }
  Type type = 1;
  string branch = 2;
  string parent_branch = 3;
}

message WalRecord {
  oneof record {
    DocumentMutation mutation = 1;
    BranchLifecycleEvent branch_event = 2;
  }
}
```

### Mutation Records

A DocumentMutation represents an update or removal of a single document.
- branch: Target dataset branch receiving the mutation.
- doc_id: Unique string identifier of the document.
- op: Operation type, either PUT or DELETE.
- payload: Marshaled cloudyneigh.v1.Record protobuf bytes for PUT operations.
For DELETE operations, payload remains empty.

### Branch Lifecycle Records

A BranchLifecycleEvent captures dataset branch creation.
- type: Lifecycle operation type, set to FORK.
- branch: Target branch name created by the fork.
- parent_branch: Source branch from which state was copied.

### Ordering Invariants

Each WAL entry holds mutations for only one branch.
The Flusher rejects WAL entries containing mutations spanning multiple
branches.
The sequential WAL order establishes the authoritative timeline across all
branches in a namespace.

## 6. Flusher Commit Loops & Execution

The Flusher materializes WAL records into queryable, immutable segments and
manifests.
It executes as a background service inside the cloudy ingest daemon.

### Stream Discovery

Flusher polls the tenant catalog ns.json every 100 milliseconds.
It calls namespace.ActiveNamespaces to discover newly added namespaces.
Soft-deleted namespaces with non-zero deleted_at timestamps are ignored
automatically.
Flusher launches a persistent streamFlusher goroutine for each active
namespace.
An errgroup.Group manages worker lifecycles and propagates shutdown signals.

### Checkpoint Initialization

Before replaying a stream, streamFlusher recovers branch checkpoints.

1. The worker lists active branches from branches.json.
2. It reads each branch manifest under refs/heads/<branch>.json.
3. It records the CheckpointSeq from each manifest.
4. It sets the replay start sequence to min(CheckpointSeq) + 1.
If no branches exist, replay starts at sequence 1.

### Sequential Record Processing

The stream worker processes WAL entries sequentially by integer sequence
number.

1. The worker calls log.Read(ctx, seq).
2. If Read returns ErrEndOfStream, the worker sleeps for pollInterval before
retrying.
3. The worker parses each raw byte record into a WalRecord protobuf.
4. On a FORK event, the worker updates the child branch checkpoint.
5. On mutation records, the worker checks the target branch checkpoint.
If seq <= branchCheckpoints[branch], the flusher skips the mutation.
This prevents duplicate application during crash replay.

### Content-Addressed Segment Writing

The flusher writes unapplied mutations into immutable segment files.

1. Mutations are encoded into a RecordIO buffer using segment.NewWriter.
2. The worker computes the SHA-256 hash over the segment bytes.
3. The segment identifier is the hex-encoded SHA-256 digest.
4. The worker uploads the segment to segments/<sha256>.recordio with Absent:
true.
If upload fails with ErrPreconditionFailed (HTTP 412), the segment exists
already.
Because segment contents are identical, the flusher treats HTTP 412 as success.

### Manifest CAS Update Loop

After uploading the segment, the flusher links it to the branch manifest.
The update loop uses optimistic concurrency control:

```text
┌────────────────────────────────────────────────────────┐
│ 1. Read manifest refs/heads/<branch>.json + Generation │
└───────────────────────────┬────────────────────────────┘
                            │
                            ▼
┌────────────────────────────────────────────────────────┐
│ 2. Check: m.CheckpointSeq >= seq?                      │
│    YES ──▶ Branch up to date, AddBranch, return nil    │
└───────────────────────────┬────────────────────────────┘
                            │ NO
                            ▼
┌────────────────────────────────────────────────────────┐
│ 3. Append SegmentRef & Set m.CheckpointSeq = seq       │
└───────────────────────────┬────────────────────────────┘
                            │
                            ▼
┌────────────────────────────────────────────────────────┐
│ 4. Write manifest (Condition: if-generation-match)     │
└───────────────┬────────────────────────┬───────────────┘
                │ Success                │ HTTP 412
                ▼                        ▼
┌───────────────────────────┐  ┌─────────────────────────┐
│ AddBranch & return nil    │  │ Retry loop from Step 1  │
└───────────────────────────┘  └─────────────────────────┘
```

1. The worker reads the branch manifest and captures its generation string.
If the manifest does not exist, it initializes a new manifest.
2. If m.CheckpointSeq >= seq, another flush committed this sequence.
The worker registers the branch in branches.json and completes.
3. The worker appends a SegmentRef containing the segment ID, document count,
and byte size.
It updates m.CheckpointSeq to seq.
4. The worker writes the manifest with an if-generation-match precondition.
If the write succeeds, the worker registers the branch and completes.
If the write returns HTTP 412, concurrent flushes occurred.
The worker rereads the manifest and retries the loop.

### Graceful Shutdown Drain

When the flusher context receives a cancellation signal, workers drain the WAL.
Workers create a shutdown context with a 5-second deadline.
Each worker continues reading WAL entries until reaching ErrEndOfStream.
It flushes all pending mutations before terminating cleanly.
This prevents uncommitted mutations from lagging behind during planned service
restarts.
