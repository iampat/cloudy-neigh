# Storage Subsystem Architecture Specification

## 1. System Architecture and Layering

Cloudy-neigh implements a cloud-native storage engine for vector search.
The engine runs directly on Cloud Object Storage.
Supported backends include Google Cloud Storage, Amazon S3, and local disks.
The architecture requires no external database or consensus cluster.
Object storage provides durable persistence and atomic mutations.

The storage engine consists of three distinct layers:

```text
┌──────────────────────────────────────────────────────────────────────────────┐
│ Layer 2: Segments, Branch Manifests, and Catalogs                            │
│   • Segments: immutable record batches (segments/<sha256>.recordio)          │
│   • Manifests: ordered segment pointers (refs/heads/<branch>.json)           │
│   • Catalogs: namespace (ns.json) and branch (branches.json) metadata       │
└──────────────────────────────────────┬───────────────────────────────────────┘
                                       ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│ Layer 1: LogStream Sequential Write-Ahead Log (WAL)                          │
│   • Contiguous append log (wal/<020d_seq>.recordio)                          │
│   • Atomic conditional appends using object generation preconditions         │
└──────────────────────────────────────┬───────────────────────────────────────┘
                                       ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│ Layer 0: Cloud ObjectStore Adapter                                           │
│   • Uniform storage interface (objectstore.Store)                            │
│   • Generation match, preconditions, and range reads                         │
└──────────────────────────────────────────────────────────────────────────────┘
```

Layer 2 manages data and metadata structures.
Segment files store immutable batches of document mutations.
Manifests link segments in sequence order.
Catalogs register active namespaces and branches.

Layer 1 provides an append-only Write-Ahead Log (WAL) for each namespace.
The log sequences incoming writes before committing them to segments.

Layer 0 abstracts cloud storage systems through `objectstore.Store`.
It provides raw operations including Get, Put, Stat, Delete, Exists, and List.
It also supports `ReadRange` for reading partial object byte slices.
Generation preconditions enforce optimistic concurrency control without locks.

## 2. Tenant Physical Storage Isolation

Every tenant receives dedicated storage root isolation.
Cross-tenant data access is strictly prohibited.
Storage paths isolate data physically at the bucket or directory boundary.

### Static Configuration

Tenants are configured statically at boot time.
The service takes the `--tenants-file=<path>` flag on ingest and query commands.
The configuration file, typically `tenants.json`, maps tenant identifiers to
storage root URLs.
Invalid configurations cause the server process to exit immediately.

The configuration follows the `TenantsConfigFile` protojson schema:

```json
{
  "tenants": {
    "acme": {
      "storage_url": "gs://acme-bucket/data"
    },
    "krusty-krab": {
      "storage_url": "file:///data/krusty"
    }
  }
}
```

The top map key identifies the tenant.
An inner tenant identifier field is omitted.
Static configuration requires no version field.

### Storage Root Layout

The storage URL isolates the tenant root directory.
For example, `gs://acme-bucket/data/` holds Acme data.
Paths inside the storage root omit tenant name prefixes.
The root contains `ns.json` and the `ns/` namespace tree directly.

### In-Memory Tenant Registry

Servers initialize a registry of storage adapters at startup.
The registry maintains an immutable map.
It maps tenant identifiers to store instances.
Query and ingest paths perform in-memory lookups in O(1) time.
Handlers execute zero storage round trips to resolve tenant storage.

### Request Routing via x-tenant-id

Clients pass the tenant identifier in gRPC metadata.
The header key is `x-tenant-id`.
A server unary interceptor parses and validates this header.
The interceptor injects the resolved tenant store into the request context.
Missing or unconfigured tenants return gRPC status `NotFound`.
Protobuf request messages contain no tenant fields.

## 3. Storage Hierarchy and Key Ownership

Storage layout separates tenant metadata, namespace metadata, and data files.

```text
<tenant-storage-root>/
├── ns.json                           TenantCatalog: namespace registry
└── ns/<namespace>/
    ├── branches.json                 BranchCatalog: branch name registry
    ├── refs/heads/
    │   ├── main.json                 BranchManifest for main branch
    │   └── feature-1.json            BranchManifest for feature-1 branch
    ├── segments/
    │   ├── 4b825dc6...recordio       Immutable content-addressed segment
    │   └── e3b0c442...recordio       Immutable content-addressed segment
    └── wal/
        ├── 00000000000000000001.recordio  Sequential WAL record batch
        └── 00000000000000000002.recordio  Sequential WAL record batch
```

Package `namespace` strictly owns all storage keys.
Callers pass explicit namespace and branch parameters to helper functions.
No code parses storage path strings to discover namespaces or branches.
Control plane catalogs resolve names unambiguously.

The `namespace.Scope` struct encapsulates path construction:
- `Prefix()` returns `ns/<namespace>`.
- `WALPrefix()` returns `ns/<namespace>/wal`.
- `ManifestKey(branch)` returns `ns/<namespace>/refs/heads/<branch>.json`.
- `SegmentsPrefix()` returns `ns/<namespace>/segments`.
- `SegmentKey(segID)` returns `ns/<namespace>/segments/<segID>.recordio`.

Name validation requires an initial ASCII letter.
Remaining characters must be letters, numbers, hyphens, or underscores.

## 4. Tenant Catalog Protocol (ns.json)

A single `ns.json` file resides at `<tenant-storage-root>/ns.json`.
It records all namespaces belonging to the tenant.
The catalog format follows the `TenantCatalog` protojson schema.

```json
{
  "tenant": "acme",
  "namespaces": {
    "catalog": {
      "epoch": 0,
      "created_at": 1774000000
    },
    "analytics": {
      "epoch": 0,
      "created_at": 1773000000,
      "deleted_at": 1773500000
    }
  }
}
```

The catalog uses storage generations to guard writes.
A nonzero `deleted_at` field marks a namespace as deleted.
The `epoch` field defaults to zero.

### Namespace Creation

The ingester creates a namespace on the first write.
The creation routine reads `ns.json` and records the object generation.
If the namespace exists in active or deleted state, it returns an error.
That error is `ErrNamespaceAlreadyExists`.
The routine adds the new entry with the current Unix timestamp in `created_at`.
It writes `ns.json` using generation match preconditions.
If the precondition fails, the routine retries the read-modify-write cycle.
Ingest workers catch and ignore `ErrNamespaceAlreadyExists`.

### Namespace Soft Deletion

Deleting a namespace executes through `DeleteNamespace`.
The operation updates `ns.json`.
It sets `deleted_at` to the current Unix timestamp.
The write uses compare-and-swap preconditions.
Recreating a soft-deleted namespace fails.
The call returns `ErrNamespaceAlreadyExists`.
Namespace names cannot be reused after deletion.
Future background jobs will purge storage objects for deleted namespaces.

### Active Namespace Discovery

The `ActiveNamespaces` helper filters for entries where `deleted_at == 0`.
If `ns.json` is missing, it returns the default namespace name `default`.
Servers employ `CatalogCache` to hold metadata in memory.
A background worker refreshes cached entries every sixty seconds.

## 5. Branch Catalog Protocol (branches.json)

Each namespace directory maintains a `branches.json` file.
It tracks active branch names within `ns/<namespace>/branches.json`.
The file stores a `BranchCatalog` protojson message.

```json
{
  "branches": {
    "main": {},
    "feature-1": {}
  }
}
```

The map key is the branch name.
A missing or empty `branches.json` file defaults to branch `main`.
The system creates `branches.json` lazily when branches are added.

### Atomic Update Loop

Branch modifications use the internal `updateBranches` helper.
The loop reads `branches.json` and captures the object generation.
It applies a mutation callback to the branch map.
If the callback makes no changes, the function returns immediately.
Otherwise, it serializes the catalog to protojson.
It writes the object with a `GenerationMatch` precondition.
When the object does not yet exist, it passes `Absent: true`.
Precondition conflicts trigger an automatic retry.
The `AddBranch` method inserts new branches safely.
The `RemoveBranch` method deletes branch entries.

## 6. Branch Manifests (refs/heads/<branch>.json)

Each branch manifest records the durable state of a single branch.
The manifest file lives at `ns/<namespace>/refs/heads/<branch>.json`.
It provides single-hop metadata resolution.
Readers retrieve the complete branch state in a single storage read.
This avoids multiple round trips to separate metadata objects.

```json
{
  "checkpoint_seq": 104,
  "schema_version": 1,
  "segments": [
    {
      "segment_id": "4b825dc642cb6eb9a060e54b3cb614a6005d911",
      "doc_count": 500,
      "docs_size": 245760
    }
  ]
}
```

### Field Definitions

The `checkpoint_seq` field stores the highest applied WAL sequence number.
The `schema_version` field tracks the storage layout version.
The `segments` array contains an ordered list of segment references.
Each `SegmentRef` contains `segment_id`, `doc_count`, and `docs_size`.
The query loader derives object keys directly using the segment identifier.

### Bounded Size Invariant

Manifests are bounded to fourteen kilobytes.
This size fits approximately one hundred fifty segment descriptors.
The payload fits in the standard TCP initial congestion window of ten packets.
Clients fetch manifests in a single round trip on cold connections.
Compaction merges older segments to maintain this size ceiling.

## 7. Content-Addressed Segment Storage

Segments reside at `ns/<namespace>/segments/<sha256>.recordio`.
The segment identifier is the hexadecimal SHA-256 digest of its RecordIO bytes.
Computing the digest over file bytes guarantees content-addressed immutability.
Segments have no columnar split, no footer, and no block index.
Each segment stores framed `DocumentMutation` protobuf records.

### Idempotent Uploads

The flusher writes segments using `objectstore.Condition{Absent: true}`.
If the segment exists, storage returns `ErrPreconditionFailed` (HTTP 412).
The flusher treats HTTP 412 as a successful operation.
Identical mutation batches produce identical byte payloads and digests.
An existing segment already contains the required data.

### Cross-Branch Sharing

Segment keys contain no branch name.
Payloads contain mutation records for individual branches.
Forking a branch copies the manifest without copying underlying segment files.
Multiple branches reference the same segment files safely.
Shared segments remain immutable throughout their lifecycle.

## 8. Branch Forking Protocol

The `Ingester.Fork` method creates a branch copy in four sequential steps.

```text
┌──────────────────────────────────────────────────────────────────────────────┐
│ Step 1: Read source branch manifest (refs/heads/<source>.json)               │
│         Verify target branch does not already exist                          │
├──────────────────────────────────────────────────────────────────────────────┤
│ Step 2: Write target manifest copy (refs/heads/<target>.json)                │
│         Precondition: Absent = true                                          │
├──────────────────────────────────────────────────────────────────────────────┤
│ Step 3: Register target branch in catalog (branches.json)                    │
│         Atomic CAS via AddBranch                                             │
├──────────────────────────────────────────────────────────────────────────────┤
│ Step 4: Append FORK lifecycle event to namespace WAL                         │
│         WalRecord_BranchEvent with target and parent branch names            │
└──────────────────────────────────────────────────────────────────────────────┘
```

The ingester first reads the parent manifest.
It verifies that the target branch manifest does not exist.
It then writes the parent manifest copy to the target key.
The write uses the `Absent: true` precondition to prevent overwriting.

The ingester then registers the target branch name in `branches.json`.
Finally, it appends a `BranchLifecycleEvent_FORK` record to the namespace WAL.

### Rollback Mechanics

Failures during step 3 or step 4 trigger an automatic rollback routine.
The rollback context uses a five-second execution deadline.
Rollback deletes the target manifest object at `refs/heads/<target>.json`.
Rollback removes the target branch from `branches.json`.
The original error returns to the caller.
Rollback prevents dangling manifest objects and ensures clean retry attempts.

## 9. Crash Recovery Invariants

Ingestion and storage recover from crashes and network partitions.

### Four Durable State Objects

Four objects define the durable state of a namespace:
1. Write-Ahead Log: sequential records at `wal/<020d_seq>.recordio`.
2. Segments: immutable content-addressed data at `segments/<sha256>.recordio`.
3. Manifests: branch checkpoint and segment lists at `refs/heads/<branch>.json`.
4. Catalog: branch registry at `branches.json`.

The query engine only builds loaders for branches registered in `branches.json`.
A branch with a manifest but no catalog entry remains invisible to queries.

### Flusher Three-Step Commit Sequence

The flusher commits each Write-Ahead Log entry using three sequential writes:
- Step 1: Put segment (`<sha256>.recordio`) with `Absent: true`.
- Step 2: Compare-and-swap manifest with `CheckpointSeq = seq`.
- Step 3: Register branch in catalog (`branches.json`) via `AddBranch`.

### Crash Recovery Invariants

A crash can interrupt the flusher after any of the three steps.
The flusher recovers correctly across all failure points:

```text
┌─────────────────┬──────────────────────────┬─────────────────────────────────┐
│ Crash Point     │ Storage State on Replay  │ Replay Action                   │
├─────────────────┼──────────────────────────┼─────────────────────────────────┤
│ Stop after      │ Segment exists in store  │ Flusher treats HTTP 412 error   │
│ Step 1 (Put)    │                          │ as success and continues.       │
├─────────────────┼──────────────────────────┼─────────────────────────────────┤
│ Stop after      │ CheckpointSeq >= seq     │ Flusher skips segment append    │
│ Step 2 (CAS)    │ in branch manifest       │ and executes Step 3.            │
├─────────────────┼──────────────────────────┼─────────────────────────────────┤
│ Stop after      │ Branch missing from      │ Replay invokes AddBranch to     │
│ Step 3 (Branch) │ branches.json catalog    │ restore catalog registration.   │
└─────────────────┴──────────────────────────┴─────────────────────────────────┘
```

The content hash makes segment uploads idempotent.
The `CheckpointSeq` comparison makes manifest updates idempotent.
Replay prevents duplicate segment appends.
`AddBranch` writes nothing if the branch entry already exists.

### Replay Resumption

On startup, the flusher reads `branches.json`.
It determines the minimum `CheckpointSeq` across all listed branches.
The flusher resumes Write-Ahead Log replay from that sequence number.
Entries commit sequentially.
No later entry commits before earlier entries finish branch registration.
The replay starting point never skips uncommitted records.

### Query Loader Monotonic Growth Invariant

The query loader processes delta segments by slice index:
`manifest.Segments[applied:]`.
This operation requires monotonic growth in manifest segment counts.
If a manifest segment count is smaller than `applied`,
the loader returns `ErrManifestTruncated`.
A truncated manifest indicates rollback or corruption.
The loader preserves in-memory query tables until process restart.
