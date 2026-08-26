# Design Specification: Branching Key-Value Filesystem & Generic LogStream on Object Storage

## 1. Problem Statement

### 1.1 Overview
Design and implement a modular, high-performance storage engine on top of cloud object storage, for example Google Cloud Storage or AWS S3. The engine needs no external coordination system, for example Redis, ZooKeeper, or RDBMS.

The system is structured in two decoupled, composable layers:
1. **Generic LogStream (WAL / Message Queue)**: An append-only, sequentially numbered, payload-agnostic log primitive backed by object storage.
2. **Branching KV Filesystem (KVFS)**: A Git-like, Copy-on-Write (CoW) key-value filesystem layered on top of the LogStream and ObjectStore primitives.

### 1.2 Functional Requirements
* **Generic LogStream Layer**:
  * Conditional appends of opaque binary records. An unconditional append would overwrite the segment of another writer.
  * Strictly contiguous 64-bit sequence numbers (`00000000000000000001.recordio`).
  * Self-delimiting RecordIO framing with masked CRC32C integrity checks. See [RecordIO](recordio.md).
  * At-least-once delivery. A caller that needs exactly-once makes its own operations idempotent. See Section 8.2.
  * Reusable independently as a distributed message queue, event sourcing log, or database WAL.
* **Key-Value Filesystem Layer (KVFS)**:
  * Atomic `PUT`, `GET`, and `DELETE` operations for arbitrary UTF-8 paths.
  * Zero-copy, $O(1)$ branching rooted at any branch snapshot.
  * Branch-isolated Copy-on-Write (CoW) mutations.
  * **Direct content-addressed storage (CAS) blob API**: Direct `GetBlob(blob_hash)` and `PutBlob(data)` access to bypass branch heads and manifests for internal systems and remote build caches that already possess the content hash ($1\text{ RTT}$).
* **Concurrency Control**:
  * Native storage conditional write preconditions (`if-generation-match=0` / `If-None-Match: *` / generation matching) to guarantee atomicity with zero external consensus servers.

### 1.3 Non-Goals
* **Garbage Collection (GC)**: Cleaning up unreferenced historical blobs, manifests, or log segments is out of scope for the initial implementation. Historical logs are retained permanently for deterministic replays, auditability, and index backfilling.

### 1.4 Future Work
* **Tiered Local Read Cache (Memory and Disk)**:
  * Cache immutable content-addressed objects and log segments in local memory and local disk.
  * Immutable objects never change, so local cached copies require no cache invalidation protocol or time-to-live expiration.
  * Local cache hits reduce read latency from $20\text{--}60\text{ ms}$ on cloud storage to under $0.5\text{ ms}$.
* **Single-Flight Request Coalescing**:
  * Deduplicate concurrent remote reads for the same cold blob or segment across worker threads.
  * Send one remote request to object storage when concurrent readers miss on the same object.
* **Asynchronous Read-Ahead**:
  * Prefetch the next log segment in the background during sequential log scans and replays.
  * Overlap network download with record processing to hide network latency.
* **Hierarchical Merkle Manifests**:
  * Partition large manifests into a tree of content-addressed chunk nodes when a branch exceeds 100,000 keys.
  * Download only the tree nodes that contain the requested keys instead of the full manifest.

---

## 2. Layered Architecture & Storage Layout

```text
┌─────────────────────────────────────────────────────────────────────────────┐
│                       Layer 2: Applications & Consumers                     │
│                                                                             │
│   ┌───────────────────────────────────┐   ┌─────────────────────────────┐   │
│   │    KVFS / Branching Filesystem    │   │    Message Queue / PubSub   │   │
│   │    (Encodes KVMutation -> bytes)  │   │    (Encodes Events -> bytes)│   │
│   └─────────────────┬─────────────────┘   └──────────────┬──────────────┘   │
└─────────────────────┼────────────────────────────────────┼──────────────────┘
                      ▼                                    ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│               Layer 1: Generic LogStream Primitive (WAL / Queue)            │
│               • Opaque binary records: (Headers map + Payload bytes)        │
│               • Contiguous sequential chunks: <020d_seq>.recordio           │
│               • Atomic conditional append via if-generation-match=0         │
└─────────────────────────────────────┬───────────────────────────────────────┘
                                      ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                       Layer 0: Cloud ObjectStore Adapter                    │
│                    (S3 / GCS Conditional Writes & Blobs)                    │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Storage Object Hierarchy

```text
[Bucket Root]
├── refs/heads/                  <-- Layer 2: Mutable Branch Heads (conditional write)
│   ├── main                     --> "1a2beff8..." (Generation: 17234001)
│   └── feature-1                --> "c94d0199..." (Generation: 17234002)
│
├── manifests/                   <-- Layer 2: Immutable Key-to-Hash Mappings (2-Byte Sharded)
│   ├── 1a/2b/1a2beff890...      --> Manifest { last_wal_seq: 10, entries: { "docs/a.txt": "a3f1c890..." } }
│   └── c9/4d/c94d0199aa...      --> Manifest { last_wal_seq: 12, entries: { ... } }
│
├── objects/                     <-- Layer 2: Immutable Raw Payloads (2-Byte Sharded)
│   ├── a3/f1/a3f1c8901b...      --> [raw binary data]
│   └── b7/0c/b70c34e91a...      --> [raw binary data]
│
└── wal/                         <-- Layer 1: Generic LogStream (Strictly Contiguous RecordIO)
    ├── main/
    │   ├── 00000000000000000001.recordio
    │   ├── 00000000000000000002.recordio
    │   └── 00000000000000000003.recordio
    └── orders-topic/
        ├── 00000000000000000001.recordio
        └── 00000000000000000002.recordio
```

### Component Breakdown
* **`wal/<stream>/<020d_seq>.recordio` (Layer 1)**: Opaque, sequentially numbered segment files. One file is one RecordIO stream that holds N records. No container message wraps them. Records are strictly append-only, immutable, and permanent.
* **`refs/heads/<branch>` (Layer 2)**: A reference object that stores the active manifest ID, for example `1a2beff8...`. This is the **only mutable pointer** in the KVFS layer. The storage system supplies the generation token, which the object content does not hold.
* **`manifests/<h0>/<h1>/<manifest_id>` (Layer 2)**: Immutable snapshot containing path-to-hash mappings and the committed `last_wal_seq` watermark. Partitioned across $65,536$ 2-byte prefixes (`manifests/1a/2b/...`) to eliminate cloud storage partition throttling.
* **`objects/<h0>/<h1>/<blob_hash>` (Layer 2)**: Raw, immutable file content addressed by `SHA-256(payload)`, partitioned across $65,536$ 2-byte prefixes (`objects/a3/f1/...`) for horizontal I/O distribution ($>200\text{M req/sec}$ theoretical bucket throughput).

### Architectural Decision: Elimination of the Standalone Commit Layer
In traditional Git implementations, a commit object acts as an intermediate bridge between branch references and manifests to track commit author, timestamp, commit messages, and parent lineage.

For this KV filesystem:
* **The Standalone Commit Object is Removed**: `refs/heads/<branch>` points directly to `manifest_id`.
* **Rationale**: Eliminates a sequential network round-trip ($30\text{ms}$ / $1\text{ RTT}$) on read operations, reducing cold `GET` latency from $4\text{ RTTs}$ to $3\text{ RTTs}$.
* **Lineage Preservation**: Parent manifest pointers (`"parent_manifest_ids": ["1a2beff8..."]`) and metadata are embedded directly within the immutable manifest schema without introducing a separate lookup layer.

---

## 3. End-to-End Operations (Direct Mode)

### 3.1 `PUT(branch, path, data)`
Atomically creates or overwrites a key on a given branch.

```text
Client                          GCS / S3 Storage
  │                                     │
  │── 1. Upload raw payload ───────────>│ Write objects/<h0>/<h1>/<blob_hash> (Immutable)
  │                                     │
  │── 2. Read current Ref ─────────────>│ Read refs/heads/<branch>
  │<─ Return (Manifest: M1, Gen: G1) ───│
  │                                     │
  │── 3. Fetch Manifest M1 ────────────>│ Read manifests/<m0>/<m1>/M1
  │<─ Return {path: hash, ...} ─────────│
  │                                     │
  │ [Client: Computes M2 = M1 + {path: blob_hash}]
  │                                     │
  │── 4. Write Manifest M2 ────────────>│ Write manifests/<m0>/<m1>/M2 (Immutable)
  │                                     │
  │── 5. Atomic Conditional Update ────>│ Write refs/heads/<branch> -> "M2"
  │      (if-generation-match=G1)       │   ├─ Match: SUCCESS
  │                                     │   └─ Mismatch: CONFLICT -> Retry step 2
```

#### Step-by-Step Logic:
1. Compute `blob_hash = SHA256(data)`. Upload to `objects/<h0>/<h1>/<blob_hash>` (where `h0=hash[0:2], h1=hash[2:4]`) if it does not already exist.
2. Read `refs/heads/<branch>` to get current manifest ID `M_old` and object generation `Gen_old`.
3. Fetch and parse `manifests/<m0>/<m1>/<M_old>`.
4. Update in-memory map: `M_new_entries = M_old_entries.set(path, blob_hash)`.
5. Compute `M_new_id` with the stable hash from Section 5.3. Write `manifests/<m0>/<m1>/<M_new_id>`.
6. Update `refs/heads/<branch>` to `M_new_id` using condition `if-generation-match = Gen_old`.
7. If precondition fails, back off and retry from Step 2.

---

### 3.2 `GET(branch, path)`
Fetches payload bytes for a key on a given branch.

```text
Client                          GCS / S3 Storage
  │                                     │
  │── 1. Resolve Branch Ref ───────────>│ Read refs/heads/<branch> -> returns "M2"
  │                                     │
  │── 2. Read Manifest ────────────────>│ Read manifests/<m0>/<m1>/M2 -> finds "hash_1"
  │                                     │
  │── 3. Read Payload ─────────────────>│ Read objects/<h0>/<h1>/hash_1
  │<─ Return file bytes ────────────────│
```

#### Step-by-Step Logic:
1. Read `refs/heads/<branch>` to get active manifest ID `M_curr`.
2. Read `manifests/<m0>/<m1>/<M_curr>`.
3. Look up `path` in manifest entries:
   * If key exists, retrieve `blob_hash`.
   * If key is absent, return `404 Not Found`.
4. Read and return data from `objects/<h0>/<h1>/<blob_hash>`.

---

### 3.3 `DELETE(branch, path)`
Removes a key from the branch snapshot without modifying historical objects.

```text
Client                          GCS / S3 Storage
  │                                     │
  │── 1. Read Ref & Manifest ──────────>│ Read refs/heads/<branch> (Gen: G1) & manifests/.../M1
  │                                     │
  │ [Client: Computes M2 = M1 - {path}] │
  │                                     │
  │── 2. Write New Manifest M2 ────────>│ Write manifests/<m0>/<m1>/M2 (Immutable)
  │                                     │
  │── 3. Atomic Conditional Update ────>│ Write refs/heads/<branch> -> "M2"
  │      (if-generation-match=G1)       │
```

---

### 3.4 `BRANCH(new_branch, parent_branch)`
Instant, zero-copy pointer creation.

```text
Client                          GCS / S3 Storage
  │                                     │
  │── 1. Read Parent Ref ──────────────>│ Read refs/heads/<parent_branch> -> "M1"
  │                                     │
  │── 2. Conditional Create Ref ───────>│ Write refs/heads/<new_branch> -> "M1"
  │      (if-generation-match=0)        │   ├─ Gen 0 ensures branch is brand new
  │                                     │   └─ Zero blobs copied ($O(1)$)
```

---

## 4. Latency & Concurrency Analysis

Assuming an average cloud storage round-trip time (RTT) of **~30ms**.

### 4.1 `PUT` Latency Gantt Chart (~90–120ms)
During a write, the client already holds the data payload in memory. It computes `SHA256(data)` locally and uploads the blob to `objects/<hash>` **concurrently** with resolving the current branch head and manifest.

```text
Time (ms):   0ms      30ms      60ms      90ms      120ms
             |---------|---------|---------|---------|
Upload Payload (objects/<hash>)
│██████████████████████████████│ (0 - 60ms, size-dependent)
│
Read Ref (refs/heads/<branch>)
│█████████████│                  (0 - 30ms)
│
Read Manifest (manifests/M_old)
              │█████████████│   (30 - 60ms)
              │
Write New Manifest (manifests/M_new)
                            │█████████████│ (60 - 90ms)
                                          │
Conditional Update Ref (refs/heads/<branch>)
                                          │█████████████│ (90 - 120ms)
                                                        │
Operation Complete ─────────────────────────────────────▼ ~90-120ms
```

### 4.2 `GET` Latency & Concurrency Constraints

* **Cold Uncached `GET` (Strict Dependency Chain — 3 RTTs / ~90ms)**:
  Because file keys are arbitrary UTF-8 paths and raw data is content-addressed (`objects/<blob_hash>`), the client **cannot fetch metadata and data concurrently on a cold read**. It must resolve `Ref -> Manifest` before it can determine which object hash to fetch.

```text
Time (ms):   0ms      30ms      60ms      90ms
             |---------|---------|---------|
1. Read Ref (refs/heads/<branch>)
│█████████████│                           (0 - 30ms)
              │
2. Read Manifest (manifests/<manifest_id>)
              │█████████████│             (30 - 60ms)
                            │
3. Read Blob (objects/<blob_hash>)
                            │█████████████│ (60 - 90ms)
                                          │
Complete ─────────────────────────────────▼ ~90ms
```

* **Warm Cached `GET` (Concurrent / Bypassed — 1 to 2 RTTs / ~30–60ms)**:
  * **Manifest Caching (2 RTTs)**: Manifests are immutable and content-addressed. Caching manifests in memory eliminates Step 2, reducing the lookup to `Read Ref -> Read Object`.
  * **Branch Ref Leases / Short TTL (1 RTT)**: A client that caches the branch Ref locally issues a single direct read to `objects/<blob_hash>`.

---

## 5. Protocol Buffer Data Schemas (`proto3`)

All core data structures and serialized messages are defined using language-agnostic Protocol Buffers.

### 5.1 Layer 1: LogStream Schemas (`logstream/v1/logstream.proto`)

```protobuf
syntax = "proto3";

package logstream.v1;

// LogRecord represents a single, self-contained record in the log stream.
message LogRecord {
  // Optional key-value metadata headers (e.g., "writer_id", "timestamp", "content_type").
  map<string, string> headers = 1;

  // Opaque application payload bytes (completely payload-agnostic).
  bytes payload = 2;
}
```

A `.recordio` file is the segment. The object key carries the sequence number,
and the prefix carries the stream name, so no container message repeats them.
The RecordIO framing delimits the N records inside the file.

Version 1 does not use this message. A frame holds the caller payload with no
envelope around it, which Section 8.2 states. `LogRecord` arrives as a new format
version, when a caller needs a header.

### 5.2 Layer 2: KVFS Schemas (`kvfs/v1/kvfs.proto`)

```protobuf
syntax = "proto3";

package kvfs.v1;

// OperationType defines the mutation operation.
enum OperationType {
  OPERATION_TYPE_UNSPECIFIED = 0;
  OPERATION_TYPE_PUT = 1;
  OPERATION_TYPE_DELETE = 2;
}

// KVMutation is the payload serialized into Layer 1 LogRecord.payload.
message KVMutation {
  OperationType op = 1;
  string path = 2;             // UTF-8 relative key path (e.g., "docs/a.txt")
  string blob_hash = 3;         // SHA-256 hash of object in objects/ (empty if DELETE)
}

// Manifest represents an immutable snapshot of branch state.
message Manifest {
  // High-watermark sequence number committed into this manifest snapshot.
  uint64 last_wal_seq = 1;

  // Full key-value index: UTF-8 Path -> Object Blob Hash.
  // Note: For branches scaling beyond ~100k keys, see Appendix A for the LSM SSTable chunking roadmap.
  map<string, string> entries = 2;

  // Optional parent lineage pointers for multi-parent merge or history tracking.
  repeated string parent_manifest_ids = 3;
}

// BranchHead is the client view of refs/heads/<branch>. The reference object
// stores manifest_id. The storage system supplies generation, an opaque
// token the client passes back unmodified.
message BranchHead {
  string manifest_id = 1;       // Content hash of active Manifest
  string generation = 2;        // Opaque precondition token, compared for equality
}
```

### 5.3 Manifest Identity

`manifest_id` must be a stable hash of the manifest content. The same manifest
must always produce the same id.

A hash over the protobuf serialization does not give that. proto3 leaves map
field order unspecified, and the Go implementation varies it on purpose.
`proto.MarshalOptions{Deterministic: true}` promises nothing across builds,
versions, or languages. The same manifest hashed twice can yield two ids, which
breaks content addressing and weakens the committer idempotency claim in
Section 10.

The manifest layer thus needs its own canonical encoding for the hash. The
encoding is not chosen yet.

---

## 6. End-to-End Operations

### 6.1 Layer 1: LogStream Append & Read

#### Append Algorithm (Atomic Reservation):
1. The writer determines target sequence `seq = last_known_seq + 1`.
2. The writer formats the path: `wal/<stream>/<020d_seq>.recordio`, for example `wal/main/00000000000000000005.recordio`.
3. The writer encodes the batch as a RecordIO stream, one frame per record. See [RecordIO](recordio.md) for the frame layout.
4. The writer issues a conditional create:
   * **GCS**: `Put(path, if-generation-match=0)`
   * **AWS S3**: `Put(path, If-None-Match="*")`
5. **Resolution**:
   * If `200 OK`: Append succeeded.
   * If `412 Conflict`: Another writer claimed `seq`. Increment `seq = seq + 1` and retry.

#### Read Algorithm (Direct Indexed Seek):
* Because sequences are strictly contiguous ($1, 2, 3\dots$), readers read `wal/<stream>/<020d_seq>.recordio` directly with `GetObject`.
* If the object returns `404 Not Found`, the reader has reached the head of the stream. Zero `ListPrefix` API calls are necessary on normal read loops.

---

### 6.2 Layer 2: KVFS Write Flow (WAL Staged Mode)

```text
[ Client ] 
    │
    │── 1. Upload payload bytes ───────────► objects/<h0>/<h1>/<blob_hash> (Immutable)
    │
    │── 2. Construct KVMutation(PUT, path, blob_hash)
    │── 3. Append to LogStream ────────────► wal/main/<020d_seq>.recordio (if-generation-match=0)
    │<─ ACK Write Complete (~30ms) ────────┘
    │
    ▼ (Async / Background Consolidation)
[ CommitProcessor ]
    │
    ├── 1. Read current Ref & Manifest ────► refs/heads/main -> Manifest M1 (last_wal_seq: 10)
    ├── 2. Direct GET Next WAL Segment ────► Get("wal/main/00000000000000000011.recordio")
    ├── 3. Decode LogRecords & KVMutations
    ├── 4. Fold mutations into map ────────► Manifest M2 (last_wal_seq: 11)
    ├── 5. Write new Manifest ─────────────► Write manifests/<m0>/<m1>/M2
    └── 6. Conditional Advance Branch Ref ─► Write refs/heads/main -> "M2" (if-generation-match=G1)
```

---

### 6.3 Layer 2: KVFS Read Flow (`GET(branch, path)` vs. Direct CAS)

* **Direct CAS Read (Bypass Manifest - 1 RTT / ~30ms)**:
  Internal systems, remote build caches (for example Bazel), or client layers that already possess the content hash (`blob_hash`) call `GetBlob(blob_hash)` directly. Reads `objects/<h0>/<h1>/<blob_hash>` in **1 single network round-trip ($30\text{ms}$)**, completely bypassing branch head resolution and manifest parsing.
* **Warm Cached Branch Read (1 RTT / ~30ms)**:
  A branch ref lease or a cached manifest resolves `blob_hash` locally. The client then reads `objects/<h0>/<h1>/<blob_hash>`.
* **Cold Branch Read (3 RTTs / ~90ms)**:
  1. `Read refs/heads/<branch>` $\to$ `manifest_id` (30ms).
  2. `Read manifests/<m0>/<m1>/<manifest_id>` $\to$ resolves `path` $\to$ `blob_hash` (30ms).
  3. `Read objects/<h0>/<h1>/<blob_hash>` $\to$ returns payload stream (30ms).

---

### 6.4 Layer 2: Zero-Copy Branching (`BRANCH(new_branch, parent_branch)`)

```text
Client                          Cloud Object Storage
  │                                     │
  │── 1. Read Parent Ref ──────────────>│ Read refs/heads/main -> "M2"
  │                                     │
  │── 2. Create Child Ref ─────────────>│ Write refs/heads/feature-1 -> "M2"
  │      (if-generation-match=0)        │   ├─ Gen 0 ensures branch is brand new
  │                                     │   └─ Zero blobs copied (O(1))
```

---

## 7. Language-Agnostic Service Definitions

```protobuf
syntax = "proto3";

package storage.v1;

import "logstream/v1/logstream.proto";
import "kvfs/v1/kvfs.proto";

// LogStreamService provides append and read primitives for the generic log layer.
service LogStreamService {
  rpc Append(AppendRequest) returns (AppendResponse);
  rpc Read(ReadRequest) returns (ReadResponse);
  rpc ReadBatch(ReadBatchRequest) returns (ReadBatchResponse);
}

message AppendRequest {
  string stream_name = 1;
  logstream.v1.LogRecord record = 2;
}

message AppendResponse {
  uint64 sequence_number = 1;
}

message ReadRequest {
  string stream_name = 1;
  uint64 sequence_number = 2;
}

message ReadResponse {
  logstream.v1.LogRecord record = 1;
}

message ReadBatchRequest {
  string stream_name = 1;
  uint64 start_sequence_number = 2;
  uint32 max_records = 3;
}

message ReadBatchResponse {
  repeated logstream.v1.LogRecord records = 1;
  uint64 next_sequence_number = 2;
}

// KVStoreService provides key-value filesystem and direct content-addressed storage operations.
service KVStoreService {
  // Branch & Path-based Operations
  rpc Put(KVPutRequest) returns (KVPutResponse);
  rpc Get(KVGetRequest) returns (KVGetResponse);
  rpc Delete(KVDeleteRequest) returns (KVDeleteResponse);
  rpc Branch(KVBranchRequest) returns (KVBranchResponse);

  // Direct CAS Operations (Bypasses Manifests)
  rpc GetBlob(GetBlobRequest) returns (GetBlobResponse);
  rpc PutBlob(PutBlobRequest) returns (PutBlobResponse);
}

message KVPutRequest {
  string branch = 1;
  string path = 2;
  bytes data = 3;
}

message KVPutResponse {
  uint64 wal_sequence_number = 1;
  string blob_hash = 2;
}

message KVGetRequest {
  string branch = 1;
  string path = 2;
}

message KVGetResponse {
  bytes data = 1;
  string blob_hash = 2;
}

message KVDeleteRequest {
  string branch = 1;
  string path = 2;
}

message KVDeleteResponse {
  uint64 wal_sequence_number = 1;
}

message KVBranchRequest {
  string parent_branch = 1;
  string new_branch = 2;
}

message KVBranchResponse {
  string manifest_id = 1;
}

message GetBlobRequest {
  string blob_hash = 1; // SHA-256 content hash
}

message GetBlobResponse {
  bytes data = 1;
}

message PutBlobRequest {
  bytes data = 1;
}

message PutBlobResponse {
  string blob_hash = 1; // SHA-256 content hash of the uploaded payload
}
```

`LogStreamService` disagrees with Section 8.2 on two points. `AppendRequest`
carries one record, and `Log.Append` takes a batch. The service also exposes no
`Tail` call, so a gateway client cannot find the head of a stream.

`CONSIDER(ali):` no server implements this service. Settle both points when one
does, because the answer depends on whether the gateway owns the sequence
counter.

---

## 8. Go Interfaces

### 8.1 Layer 0: ObjectStore (`objectstore`)

```go
package objectstore

import (
	"context"
	"errors"
	"io"
)

var (
	ErrNotFound           = errors.New("objectstore: not found")
	ErrPreconditionFailed = errors.New("objectstore: precondition failed")
)

type Object struct {
	Key        string
	Generation string
	Size       int64
}

type Store struct{ ... }

// Condition states what the live object must satisfy for a write to apply.
// The zero Condition, like a nil *Condition, writes unconditionally.
type Condition struct {
	Absent          bool
	GenerationMatch string
}

func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, string, error)
func (s *Store) Put(ctx context.Context, key string, r io.Reader, cond *Condition) (string, error)
func (s *Store) List(ctx context.Context, prefix, startAfter string, limit int) ([]Object, error)
func (s *Store) Delete(ctx context.Context, key string) error
func (s *Store) Close() error
```

`Store` is a struct, and one implementation holds the whole surface. An
unexported `bucket` interface holds the operations that a backend cannot share.

```go
type bucket interface {
	lock() (unlock func())
	writeOptions(ctx context.Context, key string, cond *Condition) (*blob.WriterOptions, func() (string, error), error)
	generation(r *blob.Reader) (string, error)
	listGeneration(o *blob.ListObject) string
}
```

`Get` always reports the generation of the bytes it returns, and `Put` takes
one `*Condition` rather than a method per precondition. Both keep the exported
surface to a single spelling of each operation, so a new precondition is a
field on `Condition` instead of another method on `Store`.

`Open` opens every scheme through one `blob.OpenBucket` call on the gocloud
URL mux, which the driver imports feed. One switch then pairs the scheme with
its `bucket` implementation. A scheme the switch does not name fails, so an
imported driver cannot silently route to the wrong conditional-write
semantics.

Every backend reaches storage through `gocloud.dev/blob`, which carries each
difference except the generation token. It offers a portable `IfNotExist` on a
write and no portable compare-and-swap. A `GenerationMatch` condition reaches
the native handle through `As`. That one hole defines the `bucket` interface,
and a new backend implements the four methods and adds its scheme to the
switch in `Open`.

The memory and disk backends share one implementation. Its generation derives
from `(ModTime, Size)`, which every reader and list entry carries. `Get` and
`List` take no lock on any backend -- the token rides the read itself, the
same shape as GCS. Mutations serialize on a lock to make each precondition
check atomic. The disk backend acquires an exclusive file lock (`flock`) on
the bucket directory descriptor during mutations, which coordinates writers
across processes. The memory backend uses an in-process mutex. The driver
`IfNotExist` option is not used, because `fileblob` checks existence with a
stat before a rename that interleaves.

Token uniqueness on the local backends assumes the wall clock advances
between successive writes to one key. A write costs more than the clock tick
on the supported filesystems (memory, APFS, ext4). The mutex serializes writers,
so the assumption holds there. Filesystems with second-granularity timestamps
are out of scope.

A token stored in blob metadata was considered and rejected. It would remove
the clock assumption, because `Put` could mint a unique token in the fileblob
attributes sidecar. But neither `blob.Reader` nor `blob.ListObject` carries
metadata, so `Get` and `List` would pay one extra attributes read per object.
The mutex also stays, because a precondition check against a stored token is
still a read before a write. The `(ModTime, Size)` token is the exact string
fileblob returns as its `ETag`, and it rides every read and list entry for
free.

`Close` takes no context because no implementation does network I/O in it.

`Generation` is an opaque token. A caller compares it only for equality and
passes it back unmodified to the store that minted it. Each backend picks its
own encoding: GCS renders its `int64` generation as decimal, and the local
backends render `(ModTime, Size)` as two hex fields -- ETag-shaped, the same
family a future S3 backend would use. Every `Put` returns the token of its
write. A conditional write changes the token, identical bytes included, on
every backend except S3. A `Condition` that sets both fields is a caller
bug and returns a plain error rather than `ErrPreconditionFailed`, so a caller
retrying on precondition failure cannot spin on it. The zero `Condition`
writes unconditionally, the same as nil. A `GenerationMatch`
token the backend could not have minted is the same kind of bug and errors the
same way on every backend.

`Condition{Absent: true}` is the one way to write a key that must be absent. It
maps to `if-generation-match=0` on GCS and to `If-None-Match: *` on S3, and
reports `ErrPreconditionFailed` when the key already exists. Both the LogStream
append and the `BRANCH` operation depend on it.

It also removes the `Exists` call from the blob upload path. A blob is
content-addressed, so a key that exists already holds the same bytes. The writer
treats `ErrPreconditionFailed` as success and pays one round trip, not two.

`List` returns objects in lexicographic key order, starting after `startAfter`.
It guarantees `Key`, `Size`, and `Generation` on every backend. Only the
cold-start tail search and operator tooling call it. The steady-state read,
append, and commit loops issue zero list calls.

### 8.2 Layer 1: LogStream (`logstream`)

```go
package logstream

import (
	"context"
	"errors"

	"github.com/iampat/cloudy-neigh/objectstore"
)

var ErrEndOfStream = errors.New("logstream: end of stream")

type Record []byte

type Log struct{ ... }

type Option func(*Log)

func New(store *objectstore.Store, opts ...Option) *Log

func (l *Log) Append(ctx context.Context, stream string, records []Record) (uint64, error)
func (l *Log) Read(ctx context.Context, stream string, seq uint64) ([]Record, error)
func (l *Log) Tail(ctx context.Context, stream string) (uint64, error)
```

`Log` is a struct, and one implementation holds the whole surface. It follows
`objectstore.Store` in Section 8.1, because an interface with one implementation
selects nothing. A caller that needs a fake declares its own interface over the
three methods.

One `Log` serves many streams. The stream name is a call argument, not
constructor state.

`Append` writes one segment object and returns the sequence number it claimed.
The whole batch lands in that one object. Every record of the batch shares that
number, so the address of a record is the pair `(seq, index)`. A caller that
needs a record boundary for recovery appends that record alone.

`Read` returns every record of one segment. It reports `ErrEndOfStream` when the
segment does not exist, which means the reader reached the head.

`Tail` reports the highest written sequence number. A writer calls it on start
and then counts in memory. It calls `Tail` again only when that counter goes
stale.

#### Delivery guarantee

LogStream guarantees at-least-once delivery. A caller that needs exactly-once
makes its own operations idempotent. LogStream never removes a duplicate.

An acknowledgement can be lost. A `Put` commits the object, and the reply never
reaches the writer. The writer holds an error, and it cannot tell whether its own
segment landed. The next attempt reads a precondition failure on that number,
treats the number as taken, and writes the same batch under the next number.

```text
writer                          object store
  │── Put seq=42, Absent ───────▶│  commits 42
  │        ╳ reply lost ─────────│
  │── Put seq=42, Absent ───────▶│
  │◀──── precondition failed ────│  42 exists, and the writer owns it
  │── Put seq=43, Absent ───────▶│  the same batch, a second time
```

The retry does not have to come from the caller. The GCS client retries a write
that carries a precondition, so the whole exchange fits inside one
`objectstore.Put` call.

Immutability does not prevent this. It prevents a torn segment, an overwritten
segment, and two writers inside one object. Here the log holds two intact
segments that carry the same records.

A sequence number names a segment. It is not a deduplication key, and a reader
cannot detect a repeat from the number alone.

KVFS needs nothing more, because a `KVMutation` folded twice gives the same
manifest entry. A caller with a non-idempotent operation, such as a counter
increment, puts a transaction identifier in its payload and rejects a repeat on
replay.

#### Record format

A record is an opaque byte slice. The RecordIO frame holds the caller bytes with
no envelope around them.

Section 5.1 defines `logstream.v1.LogRecord`, which adds a header map. No caller
needs a header today, because KVFS puts a `KVMutation` in the payload. The
envelope costs a protobuf dependency in the lowest reusable layer, so it waits
for the first caller that needs a header. It arrives as a new format version.

#### Use of the object store

LogStream needs a conditional create, and never a compare-and-swap.

| Call                             | Purpose                             |
| -------------------------------- | ----------------------------------- |
| `Put` with `Condition{Absent}`   | claims the sequence number          |
| `Get`                            | reads one segment                   |
| `List`                           | the cold start alone, never a read  |

`Condition{GenerationMatch}` serves `refs/heads/<branch>`, which Section 8.3
owns. LogStream writes only immutable objects, so it compares no token. It also
discards the generation token that `Put` and `Get` return.

#### Sequence claims

`Append` claims a sequence number with `objectstore.Put` under
`Condition{Absent: true}`. That call is the linearization point. A record is
durable when the call returns without an error, and at no earlier moment.

```text
Append(stream, records):
    encode the batch into one buffer
    seq = lastKnown[stream] + 1
    conflicts = 0
    loop:
        err = Put(key(stream, seq), buffer, Condition{Absent: true})
        err is nil                ──▶  lastKnown[stream] = seq;  return seq
        not ErrPreconditionFailed ──▶  return err

        conflicts = conflicts + 1
        conflicts < 3             ──▶  seq = seq + 1
        otherwise                 ──▶  seq = max(seq + 1, Tail(stream) + 1)
                                       conflicts = 0
        wait a short random time, then continue
```

A writer targets a free number, or a number that a precondition failure proved
taken. A failure of any other kind returns an error and moves nothing.

That rule holds the contiguity invariant. The live sequence numbers of a stream
always form the range `1..T` with no hole. `Tail` depends on the invariant,
because a binary search over a range with a hole reports the wrong head.

The cost of a collision is one wasted upload. The loser of a race uploads the
segment again under the next number. Appendix B puts the resulting ceiling at 500
to 1,000 appends per second on one stream. Section 9 names the gateway that
raises it.

The retry loop ends when the context ends. It has no attempt limit, because each
precondition failure proves that another writer made progress.

#### Recovery from a stale counter

`lastKnown` goes stale when another writer advances the stream. A walk of `+1`
steps then costs one round trip for each segment the writer missed. A writer that
is 4,000 segments behind cannot reach the head inside a normal request deadline.
Every call thus fails, and the writer never catches up.

Three consecutive precondition failures trigger a `Tail` call, which re-anchors
`lastKnown` in one search. The jump is safe, because `Tail` reports a real head.

A short random delay follows each precondition failure. Two writers that collide
also fail at the same moment. A loop with no delay keeps them in step, and they
collide again on the next number. The delay is an implementation constant, and it
promises a caller nothing.

#### Cold-start tail search

`Tail` reads the first page of the stream prefix, then probes.

```text
Tail(stream):
    objs = List("wal/<stream>/", "", listLimit)
    len(objs) == 0         ──▶  return 0
    len(objs) < listLimit  ──▶  return seq(last key of objs)

    lo = seq(last key of objs)
    hi = lo * 2
    while exists(hi):
        lo = hi
        hi = hi * 2
    while hi - lo > 1:
        mid = lo + (hi - lo) / 2
        exists(mid)  ──▶  lo = mid
        otherwise    ──▶  hi = mid
    return lo
```

`listLimit` defaults to 1000, which matches the page size of the supported
backends. One `List` call thus answers every stream of less than 1000 segments,
and that is the common case.

`objectstore.List` filters `startAfter` on the client. A search that pages
through the prefix thus reads every key from the start of the prefix on every
call. Section 1.3 makes garbage collection a non-goal, so the segment count only
grows. A repeated `List` walk is the wrong shape for that reason.

Round trips against the segment count:

| Segments  | A probe alone | One `List`, then a probe |
| --------- | ------------- | ------------------------ |
| 0         | 1             | 1                        |
| 500       | 18            | 1                        |
| 1,000     | 20            | 12                       |
| 1,000,000 | 40            | 31                       |

The binary search takes `lo` from the last key the `List` returned, and never
from `listLimit`. The two agree only while segment 1 still exists. An operator
lifecycle rule that deletes an old prefix breaks that equality, and the search
then starts from a number that no object holds. The last returned key exists by
construction, so it is always a valid lower bound.

`exists(seq)` calls `List` with the full segment key as the prefix and a limit of
1. The call returns one object or none, and it transfers no body. A `Get` probe
would open the body of a large segment.

A metadata read costs less than a list call on every cloud backend. `objectstore`
has no such call today, and `Tail` runs once for each writer start, so the extra
cost is small. A short-lived writer that appends once pays it on every run.
`TODO.md` records the `Head` call as a requirement for the ObjectStore rework.

#### Segment corruption

A conditional `Put` lands whole or not at all. A partial frame in a segment thus
means a damaged object, and never a torn tail.

`Read` reports the RecordIO error and changes nothing. `recordio.ErrTornWrite`
carries no recovery here, because an immutable object has no truncation step. The
caller stops the replay, which is what `recordio.md` requires for mid-stream
corruption.

### 8.3 Layer 2: KVFS (`kvfs`)

```go
package kvfs

import (
	"context"
	"io"
)

type Manifest struct {
	LastWALSeq        uint64
	Entries           map[string]string
	ParentManifestIDs []string
}

type WriteResult struct {
	WALSeq   uint64
	BlobHash string
}

type CommitResult struct {
	ManifestID string
	LastWALSeq uint64
	Mutations  int
}

type Store interface {
	Put(ctx context.Context, branch, path string, data io.Reader) (WriteResult, error)
	Get(ctx context.Context, branch, path string) (io.ReadCloser, error)
	Delete(ctx context.Context, branch, path string) (WriteResult, error)
	Branch(ctx context.Context, newBranch, parentBranch string) (string, error)
	PutBlob(ctx context.Context, data io.Reader) (string, error)
	GetBlob(ctx context.Context, blobHash string) (io.ReadCloser, error)
}

type BlobStore interface {
	Put(ctx context.Context, data io.Reader) (string, error)
	Get(ctx context.Context, blobHash string) (io.ReadCloser, error)
}

type BranchReader interface {
	Head(ctx context.Context, branch string) (manifestID string, generation string, err error)
	Manifest(ctx context.Context, manifestID string) (*Manifest, error)
}

type Committer interface {
	Commit(ctx context.Context, branch string) (CommitResult, error)
}
```

`Store` is the whole client surface. It matches `KVStoreService` in Section 7,
including the two direct content-addressed calls that skip branch resolution.

`BlobStore` and `BranchReader` split the read path. `BlobStore` serves the
1-round-trip hash read. `BranchReader` serves the 3-round-trip path read, and it
owns the manifest cache, because a manifest is immutable.

`Committer` runs in the background, one instance per branch. `Commit` folds the
segments above `Manifest.LastWALSeq`, writes the new manifest, and advances the
ref. It returns without an error and with zero mutations when the branch has no
pending segment.

`Manifest.Entries` is a Go map, and a map has no order. Section 5.3 covers the
stable hash that the manifest id needs.

### 8.4 Call Tree

```text
kvfs.Store
├── Get(branch, path)
│   ├── kvfs.BranchReader.Head      ──► objectstore.Get                   refs/heads/<branch>
│   ├── kvfs.BranchReader.Manifest  ──► objectstore.Get                   manifests/<m0>/<m1>/<id>
│   └── kvfs.BlobStore.Get          ──► objectstore.Get                   objects/<h0>/<h1>/<hash>
│
├── Put(branch, path, data) / Delete(branch, path)
│   ├── kvfs.BlobStore.Put          ──► objectstore.Put(Absent)           objects/<h0>/<h1>/<hash>
│   └── logstream.Log.Append        ──► objectstore.Put(Absent)           wal/<branch>/<seq>.recordio
│
├── Branch(newBranch, parentBranch)
│   ├── kvfs.BranchReader.Head      ──► objectstore.Get                   refs/heads/<parent>
│   └──                                 objectstore.Put(Absent)           refs/heads/<new>
│
└── GetBlob / PutBlob
    └── kvfs.BlobStore              ──► objectstore                       objects/<h0>/<h1>/<hash>

kvfs.Committer.Commit(branch)                        (background, one per branch)
├── kvfs.BranchReader.Head/Manifest ──► objectstore.Get                   refs + manifest
├── logstream.Log.Read(seq+1…)      ──► objectstore.Get                   wal/<branch>/<seq>.recordio
├── fold KVMutation records into Manifest.Entries
├── write the new manifest          ──► objectstore.Put                   manifests/<m0>/<m1>/<id>
└── advance the ref                 ──► objectstore.Put(GenerationMatch)  refs/heads/<branch>
```

The write path never reads a manifest. The read path never touches the log. Only
the committer spans both, which is why it is the only component that needs the
`last_wal_seq` watermark.

---

## 9. Scaling Evolution: Direct Writers to Gateway Service

To scale the system across orders of magnitude without breaking API contracts:

```text
Scale Tier                      Ingestion Architecture                   Concurrency & Latency
-------------------------------------------------------------------------------------------------------------
Phase 1 (Baseline)              Direct Sequential Writers                • 1-20 writes/sec per stream
                                (Client writes directly to S3/GCS)       • Latency: ~30-40ms (1 S3 PUT)
                                                                         • Zero server infrastructure

Phase 2 (Congestion Mitigation) Dedicated WALWriter Gateway Service      • 1,000+ writes/sec per stream
                                (gRPC Gateway micro-batches writes)      • Latency: ~35ms (1ms gRPC + S3 PUT)
                                                                         • 100x fewer S3/GCS API calls
```

---

## 10. Failure & Concurrency Semantics

| Scenario | Behavior / Resolution |
| :--- | :--- |
| **Concurrent Append Collision** | If two writers target sequence $N$, one succeeds with `if-generation-match=0`. The other receives `412 Conflict`, increments to $N+1$, and retries. |
| **Ambiguous Append Acknowledgement** | A `Put` commits sequence $N$ and loses its reply. The retry reads `412 Conflict` on the writer's own segment, so the batch lands again at $N+1$. LogStream is at-least-once, and Section 8.2 leaves deduplication to the caller. |
| **Committer Crash / Re-execution** | Manifest tracks `last_wal_seq`. The committer resumes by requesting `last_wal_seq + 1`, guaranteeing complete idempotency and zero ghost overwrites. |
| **Cross-Branch Isolation** | Streams and branches are partitioned under distinct prefixes (`wal/<branch>/`, `refs/heads/<branch>`), eliminating cross-branch contention. |
| **Read-After-Write Consistency** | V1 provides committed snapshot isolation (reads reflect the latest committed branch manifest). [Appendix C](#appendix-c-read-your-own-writes-placeholder) sketches a read-your-own-writes (RYOW) extension. Nothing is committed. |
| **Orphaned Payload Writes** | Blobs uploaded to `objects/<hash>` before a failed append remain content-addressed and unreferenced, causing zero data corruption. |
| **Branch Creation Collision** | `if-generation-match=0` prevents overwriting an existing branch pointer during `BRANCH` calls. |

---

# Appendix: Architecture Evolutions & Scaling Strategy

---

## Appendix A: LSM & SSTable Metadata Scaling Strategy (V2 Roadmap)

When a branch scales beyond $\sim 100,000$ keys, downloading and parsing a single flat manifest on every commit incurs I/O and GC overhead. To scale to **100M+ keys** without redesigning storage primitives, the manifest engine transitions to an **LSM (Log-Structured Merge-tree) SSTable architecture**.

```text
                                [ WAL Records ] (Layer 1)
                                      │
                                      ▼
                        [ Committer Flushes L0 SST ]
                                      │
           ┌──────────────────────────┴──────────────────────────┐
           ▼                                                     ▼
┌──────────────────────┐                              ┌──────────────────────┐
│  L0 SSTable 001.sst  │ (Keys: b, d, z)              │  L0 SSTable 002.sst  │ (Keys: a, d, k)
└──────────┬───────────┘                              └──────────┬───────────┘
           │                                                     │
           └──────────────────────────┬──────────────────────────┘
                                      │  (Deferred Background Compaction)
                                      ▼
┌────────────────────────────────────────────────────────────────────────────┐
│                             L1 Compacted SSTables                          │
│  ┌───────────────────────────────┐     ┌────────────────────────────────┐  │
│  │   L1 SSTable (Keys: a .. m)   │     │    L1 SSTable (Keys: n .. z)   │  │
│  └───────────────────────────────┘     └────────────────────────────────┘  │
└────────────────────────────────────────────────────────────────────────────┘
```

### A.1 SSTable-Based Manifest Schema

Instead of inlining millions of key strings, the `Manifest` stores sorted **range boundaries and content-addressed pointers** to immutable SSTable blocks:

```protobuf
syntax = "proto3";

package kvfs.v2;

message SSTableMeta {
  string sst_hash = 1;         // Content-addressed pointer (manifests/sst/<sst_hash>.sst)
  string smallest_key = 2;     // Range boundary (e.g., "docs/a.txt")
  string largest_key = 3;      // Range boundary (e.g., "users/z.json")
  uint64 entry_count = 4;      // Number of entries in this SSTable
  uint64 size_bytes = 5;       // Byte size of SSTable
  bytes bloom_filter = 6;      // Inlined Bloom filter (~1 KB) to eliminate false read RPCs
}

message Manifest {
  uint64 last_wal_seq = 1;     // Committed WAL high-watermark

  // Level 0: Newly flushed batches (keys may overlap across L0 tables)
  repeated SSTableMeta l0_tables = 2;

  // Level 1+: Compacted non-overlapping sorted runs
  repeated SSTableMeta l1_tables = 3;
  repeated SSTableMeta l2_tables = 4;

  repeated string parent_manifest_ids = 5;
}
```

### A.2 Performance Bounds & Zero-Copy Invariants

* **$O(1)$ Commit Latency**: Committing a batch only writes a small $\sim 30\text{ KB}$ L0 SSTable file and advances the manifest. The commit cycle stays **$<40\text{ms}$** regardless of whether the branch has 10,000 or 100,000,000 keys.
* **$O(1)$ Branching Preserved**: Branches share existing SSTables by pointer; zero data duplication.
* **Bounded Cold Start**: The root manifest only contains range pointers ($\sim 300\text{ KB}$ for 100M keys). Clients download only the root manifest on branch startup ($1\text{ RTT} \approx 30\text{ms}$) and fetch individual SSTables on-demand.
* **Compaction Decoupling**: Compaction is an asynchronous, background worker task that merges L0 tables into L1 without blocking the commit pipeline.

---

## Appendix B: Failure Modes & Breaking Thresholds Analysis

| Dimension | Threshold Where Base Architecture Breaks | Root Bottleneck Cause | V2 Mitigating Strategy |
| :--- | :--- | :--- | :--- |
| **Total Keys per Branch** | $\gt 100,000$ items | Flat manifest download & parse serialization overhead ($O(N)$ write cost) | LSM SSTable Chunked Range Manifests ([Appendix A](#appendix-a-lsm--sstable-metadata-scaling-strategy-v2-roadmap)) |
| **Concurrent Writes (Direct Mode)** | $\gt 3\text{ writes/sec}$ on a single branch | GCS caps mutations of one object. A measurement from a us-west1 VM against a us-west1 bucket sustained 2.7 mutations/sec on one key, and a faster loop got HTTP 429. | WAL staging tier with asynchronous batch consolidation |
| **Concurrent Writes (WAL Mode)** | $\gt 500\text{--}1,000\text{ writes/sec}$ on a single stream | Direct client conditional create congestion on sequence numbers | `WALWriter Service` gateway micro-batching into RecordIO streams |
| **Cloud Storage API Limits** | $\gt 3,500\text{--}5,500\text{ req/sec}$ on root prefixes | Hotspotting on `objects/`, `manifests/`, or `wal/` prefixes | 2-byte deterministic hash prefixes (`objects/a3/f1/...`) |

---

## Appendix C: Read-Your-Own-Writes (Placeholder)

**This appendix is a placeholder. The project is not committed to this design.
It records the shape of the problem, not a decision.**

V1 gives committed snapshot isolation. A `GET` reflects the manifest that the
committer last published. A client that reads straight after its own `PUT` can
miss that write, because the commit is asynchronous.

One possible direction:

```text
[ GET(branch, path, after_seq) ]
          │
          ├── 1. Session write buffer (local, uncommitted)
          │      └─ hit ──► return the payload
          │
          ├── 2. Segments above Manifest.LastWALSeq (remote, uncommitted)
          │      └─ hit ──► resolve blob_hash ──► fetch the object
          │
          └── 3. Committed manifest
                 └─ resolve path ──► fetch the object
```

`KVPutResponse` already returns `wal_sequence_number`. A reader passes that value
back as an `after_seq` token. When the branch manifest is behind the token, the
reader folds the uncommitted segments over the manifest during lookup.

Open questions:
* The cost of step 2. A reader far behind the committer pays one `GET` per
  pending segment, and the count is unbounded.
* The token must cross process boundaries, or the guarantee holds inside one
  client only.
* `KVGetRequest` carries no `after_seq` field. Adding one changes the contract.
* Step 2 needs the same fold order as the committer, which duplicates commit
  logic on the read path.
