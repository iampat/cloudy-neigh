# System Architecture

Cloudy-neigh is a cloud-native search engine. It decouples compute from
storage and uses cloud object storage as the source of truth. Stateless
ingest and query nodes persist write-ahead logs, immutable columnar segments,
and branch manifests to object storage.

---

## High-Level Topology

```text
                      ┌────────────────────────┐
                      │      gRPC Client       │
                      └───────────┬────────────┘
                                  │
                 ┌────────────────┴────────────────┐
                 │ Upsert / Fork / Delete          │ Query
                 ▼                                 ▼
   ┌───────────────────────────┐     ┌───────────────────────────┐
   │        Ingest Node        │     │        Query Node         │
   │                           │     │                           │
   │  ┌─────────────────────┐  │     │  ┌─────────────────────┐  │
   │  │  grpcapi.IngestSvc  │  │     │  │  grpcapi.QuerySvc   │  │
   │  └──────────┬──────────┘  │     │  └──────────┬──────────┘  │
   │             ▼             │     │             ▼             │
   │  ┌─────────────────────┐  │     │  ┌─────────────────────┐  │
   │  │   ingest.Ingester   │  │     │  │    query.Engine     │  │
   │  └──────────┬──────────┘  │     │  └──────────┬──────────┘  │
   │             ▼             │     │             ▼             │
   │  ┌─────────────────────┐  │     │  ┌─────────────────────┐  │
   │  │   ingest.Flusher    │  │     │  │  query.Table / Dist │  │
   │  └──────────┬──────────┘  │     │  └──────────┬──────────┘  │
   └─────────────┼─────────────┘     └─────────────┼─────────────┘
                 │                                 │
                 ▼                                 ▼
   ┌─────────────────────────────────────────────────────────────┐
   │                    Cloud Object Storage                     │
   │            <tenant-storage-root>/ns/<namespace>/            │
   │  wal/             segments/          branches.json  refs/   │
   │  <seq>.recordio   <seg_id>.recordio                 head/   │
   └─────────────────────────────────────────────────────────────┘
```

---

## Ingestion Pipeline (Write Path)

Clients write document mutations and branch lifecycle events over gRPC. The
ingest node appends records synchronously to the write-ahead log, tails the
log in a background flusher, and materializes immutable columnar segments to
object storage.

```text
┌─────────────┐
│ gRPC Client │
└──────┬──────┘
       │ Upsert(doc) / Delete(doc) / Fork(branch)
       ▼
┌─────────────────────────┐
│  grpcapi.IngestServer   │ Validates namespace and branch defaults
└──────┬──────────────────┘
       │ Direct batch append
       ▼
┌─────────────────────────┐
│     ingest.Ingester     │ Enforces linearizability and branch state
└──────┬──────────────────┘
       │ Append(record)
       ▼
┌─────────────────────────┐
│      logstream.Log      │ Append-only WAL over RecordIO frames
└──────┬──────────────────┘
       │ wal/<seq>.recordio
       ▼
┌─────────────────────────┐
│    objectstore.Store    │ Google Cloud Storage, local disk, or memory
└─────────────────────────┘
       ▲
       │ Tail WAL (seq >= checkpoint_seq + 1)
┌──────┴──────────────────┐
│     ingest.Flusher      │ Buffers mutations per sequence in memory
└──────┬──────────────────┘
       │ Flush on sequence commit or shutdown
       ├──▶ Writes segments/<seg_id>.recordio with segment.Writer
       ├──▶ Updates refs/head/<branch> manifest with manifest.Write
       └──▶ Registers branch in branches.json with namespace.AddBranch
```

### Ingestion Execution Flow

```text
grpcapi.IngestServer.Upsert
  resolveNamespace
  resolveBranch (namespace.BranchRef)
  ingest.Ingester.Upsert
    logstream.Log.Append
      recordio.Writer.WriteRecord
      objectstore.Store.Put (wal/<020d_seq>.recordio)
grpcapi.IngestServer.Fork
  resolveForkBranches
  ingest.Ingester.Fork
    manifest.Read (source)
    manifest.Write (target, Absent: true)
    namespace.AddBranch (branches.json)
    logstream.Log.Append (BranchLifecycleEvent_FORK)
ingest.Flusher.Run (Background goroutine)
  logstream.Log.Read
  ingest.Flusher.processRecords
  ingest.Flusher.flushBranch
    segment.Writer.Write (segments/<seg_id>.recordio)
    manifest.Write (refs/head/<branch>, CAS generation)
    namespace.AddBranch (branches.json)
```

---

## Query Pipeline (Read Path)

Query nodes read immutable segment references from branch pointers. They load
columnar segments into flat memory. They compute exact k-NN vector distances
and attribute filters.

```text
┌─────────────┐
│ gRPC Client │
└──────┬──────┘
       │ Query(vector, top_k, filter)
       ▼
┌─────────────────────────┐
│   grpcapi.QueryServer   │ Resolves namespace and target branch
└──────┬──────────────────┘
       │ Query(ctx, req)
       ▼
┌─────────────────────────┐
│      query.Engine       │ Orchestrates table sync and candidate ranking
└──────┬──────────────────┘
       │ Sync / Load manifests
       ▼
┌─────────────────────────┐
│      query.Loader       │ Reads refs/head/<branch> and segments
└──────┬──────────────────┘
       │ Stream segment records into table
       ▼
┌─────────────────────────┐
│       query.Table       │ Flat contiguous vector array ([]float32)
└──────┬──────────────────┘
       │ Compute distance & filter attributes
       ▼
┌─────────────────────────┐
│     query/distance      │ Cosine, DotProduct, L2Squared (SIMD / Scalar)
└─────────────────────────┘
```

### Query Execution Flow

```text
query.Engine.Run (Background sync loop)
  namespace.ListBranches (reads branches.json)
  query.Loader.Sync
    manifest.Read (refs/head/<branch>)
    segment.NewReader (segments/<id>.recordio)
    table.Builder.UpsertRecord (appends into flat []float32)
grpcapi.QueryServer.Query
  resolveBranch (namespace.BranchRef)
  query.Engine.Query
    loader.Table (atomic read of immutable table)
    query.Table.Search
      query/distance.DotProduct / Cosine / L2Squared
      evaluate attribute equality predicates
      bounded min-heap top-k selection
```

---

## Storage Layout & Hierarchy

Cloud object storage maintains strict isolation through path prefixes. Branch
pointers reference immutable manifests. Manifests reference immutable
segments.

```text
<tenant-storage-root>/
├── ns.json
└── ns/
    └── <namespace>/
        ├── wal/
        │   ├── 00000000000000000001.recordio
        │   ├── 00000000000000000002.recordio
        │   └── 00000000000000000003.recordio
        ├── segments/
        │   ├── 00000000000000000001-<branch>.recordio
        │   └── 00000000000000000002-<branch>.recordio
        ├── branches.json
        └── refs/
            └── head/
                ├── main
                └── <branch>
```

Each tenant has its own isolated storage root URL configured via `--tenants-file`
(or a dedicated `<tenant>/` directory in shared storage). Inside it, `ns.json`
lists namespaces and `ns/` holds namespace directories.
Each namespace contains:
- `wal/`: append-only write-ahead log files.
- `segments/`: flat immutable columnar segment files without branch
  subdirectories.
- `branches.json`: catalog of active branches in the namespace.
- `refs/head/`: protobuf manifest files tracking checkpoint sequence and
  segment IDs per branch.

The server CLI (`cmd/cloudy`) acts strictly as an assembly root with
dependency injection, delegating multi-tenant log stream routing to the
`ingest` library.

### Storage Invariants

1. **WAL records are immutable**: Once written, a WAL sequence file is never
   modified or overwritten.
2. **Segment blobs are flat and content-isolated**: Segment files contain
   immutable vector and document data. Segments live directly under
   `segments/` without branch subdirectories and are shared across forked
   branches.
3. **Branch heads advance monotonically**: Manifest commits update
   `<tenant>/ns/<namespace>/refs/head/<branch>` with `manifest.Write` using
   conditional creates (`Absent: true`) or generation-matched CAS updates.
4. **Branch catalog**: Active branches are cataloged in `branches.json` with
   CAS updates.

---

## Component Index

| Package | Layer | Role |
| --- | --- | --- |
| `cmd/cloudy` | CLI | Subcommands for `ingest` and `query` servers |
| `grpcapi` | Service | gRPC IngestService and QueryService endpoints |
| `namespace` | Catalog | Namespace validation, default fallbacks, and keys |
| `ingest` | Pipeline | Direct batch ingestion and background flusher |
| `logstream` | WAL | Monotonic append-only log over RecordIO |
| `recordio` | Framing | Framed binary reader, writer, and scanner |
| `query` | Engine | Columnar in-memory table and manifest loader |
| `query/distance` | Math | Distance kernels (scalar and portable SIMD) |
| `segment` | Storage | Segment encoding and decoding over RecordIO |
| `manifest` | Storage | Branch manifest read and CAS write |
| `objectstore` | Storage | Store drivers (GCS, local disk, memory) |
