# System Architecture

Cloudy-neigh is a cloud-native search engine. It decouples compute from
storage and uses cloud object storage as the source of truth. Stateless
ingest and query nodes persist write-ahead logs, immutable segments, and
branch manifests to object storage.

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
   │  <seq>.recordio   <seq>.recordio                    heads/  │
   └─────────────────────────────────────────────────────────────┘
```

---

## Ingestion Pipeline (Write Path)

Clients write document mutations and branch lifecycle events over gRPC. The
ingest node appends records synchronously to the write-ahead log, tails the
log in a background flusher, and materializes immutable segments to object
storage.

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
       ├──▶ Writes segments/<020d_seq>.recordio with segment.Writer
       ├──▶ Updates refs/heads/<branch>.json manifest with manifest.Write
       └──▶ Registers branch in branches.json with namespace.AddBranch
```

### Ingestion Execution Flow

```text
grpcapi.IngestServer.Upsert
  resolveNamespace
  resolveBranch
  ingest.Ingester.Upsert (namespace, branch)
    logstream.Log.Append
      recordio.Writer.WriteRecord
      objectstore.Store.Put (wal/<020d_seq>.recordio)
grpcapi.IngestServer.Fork
  resolveNamespace, validate source and target branches
  ingest.Ingester.Fork (namespace, source, target)
    manifest.Read (Scope.ManifestKey(source))
    manifest.Write (Scope.ManifestKey(target), Absent: true)
    namespace.AddBranch (branches.json)
    logstream.Log.Append (BranchLifecycleEvent_FORK)
ingest.Flusher.Run (Background goroutine)
  logstream.Log.Read
  ingest.Flusher.processRecords
  ingest.Flusher.flushBranch
    segment.Writer.Write (segments/<020d_seq>.recordio)
    manifest.Write (refs/heads/<branch>.json, CAS generation)
    namespace.AddBranch (branches.json)
```

---

## Query Pipeline (Read Path)

Query nodes read immutable segment references from branch manifests. They
replay segments into a flat in-memory table. They compute exact k-NN vector
distances and attribute filters.

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
│      query.Loader       │ Reads refs/heads/<branch>.json and segments
└──────┬──────────────────┘
       │ Stream segment records into table
       ▼
┌─────────────────────────┐
│       query.Table       │ Flat contiguous vector array ([]float32)
└──────┬──────────────────┘
       │ Compute distance & filter attributes
       ▼
┌─────────────────────────┐
│         vector          │ Kernel set chosen by the -distance variant
└─────────────────────────┘
```

### Query Execution Flow

```text
query.Engine.Run (Background sync loop)
  namespace.ActiveNamespaces (ns.json, skips deleted_at != 0)
  namespace.ListBranches (branch names from branches.json)
  query.Loader.Sync (Scope.ManifestKey(branch))
    manifest.Read (refs/heads/<branch>.json)
    segment.NewReader (segments/<020d_seq>.recordio)
    query.Table.Clone, then Table.UpsertRecord (flat []float32 or []uint16)
grpcapi.QueryServer.Query
  namespace.ValidateName, resolveBranch
  query.Engine.Query (namespace, branch)
    atomic load of the branch table
    query.Table.Search
      vector.DotProduct / Cosine / L2Squared
      evaluate attribute equality predicates
      bounded min-heap top-k selection
```

---

## Storage Layout & Hierarchy

Cloud object storage maintains strict isolation through path prefixes. Branch
manifests under `refs/heads/` reference immutable segments.

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
        │   ├── 00000000000000000001.recordio
        │   └── 00000000000000000003.recordio
        ├── branches.json
        └── refs/
            └── heads/
                ├── main.json
                └── <branch>.json
```

Each tenant has its own isolated storage root URL configured via `--tenants-file`
(or a dedicated `<tenant>/` directory in shared storage). Inside it, `ns.json`
lists namespaces and `ns/` holds namespace directories.
Each namespace contains:
- `wal/`: append-only write-ahead log files.
- `segments/`: flat immutable segment files, one per flushed WAL entry,
  named by the WAL sequence number.
- `branches.json`: a `BranchCatalog` in protojson. It maps each branch name
  to its metadata, e.g. `{"branches":{"main":{}}}`.
- `refs/heads/<branch>.json`: the branch manifest, a `BranchManifest` from
  `proto/storage/v1/storage.proto` in protojson. It holds the checkpoint
  sequence and the segment references of the branch.

```json
{
  "checkpoint_seq": "996",
  "schema_version": "1",
  "segments": [
    {
      "segment_id": "00000000000000000996",
      "doc_count": "1000",
      "docs_size": "5242880"
    }
  ]
}
```

The server CLI (`cmd/cloudy`) acts strictly as an assembly root with
dependency injection, delegating multi-tenant log stream routing to the
`ingest` library.

### Storage Invariants

1. **WAL records are immutable**: Once written, a WAL sequence file is never
   modified or overwritten.
2. **The WAL sequence names a segment**: A segment holds the mutations of one
   WAL entry. One WAL entry holds the mutations of one branch. Thus the
   sequence number alone names the segment (`<020d_seq>.recordio`). The name
   holds no branch. Segments live directly under `segments/` and forked
   branches share them.
3. **Branch heads advance monotonically**: Manifest commits update
   `<tenant-storage-root>/ns/<namespace>/refs/heads/<branch>.json` with
   `manifest.Write` using conditional creates (`Absent: true`) or
   generation-matched CAS updates.
4. **Branch catalog**: `branches.json` holds branch names, not storage keys.
   CAS updates change it.
5. **Package `namespace` owns the key layout**: A caller asks
   `namespace.Scope.ManifestKey(branch)` for a manifest key. WAL records
   and APIs carry the branch name. No code parses a key to find a namespace
   or a branch.

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
| `vector` | Math | Distance kernels per variant (see `docs/design/distance-variants.md`) |
| `segment` | Storage | Segment encoding and decoding over RecordIO |
| `manifest` | Storage | Branch manifest read and CAS write |
| `objectstore` | Storage | Store drivers (GCS, local disk, memory) |
