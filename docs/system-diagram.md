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
   │                                                             │
   │  wal/                     segments/          refs/heads/    │
   │  <020d_seq>.recordio      <id>.seg           <ns>_<branch>  │
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
│     ingest.Flusher      │ Routes mutations into in-memory memtables
└──────┬──────────────────┘
       │ Flush on sequence commit or shutdown
       ├──▶ Writes segments/<id>.seg via segment.Writer
       └──▶ Updates refs/heads/<ns>_<branch> via CAS (Absent: true or GenMatch)
```

### Ingestion Execution Flow

```text
grpcapi.IngestServer.Upsert
  namespace.Scope
  ingest.Ingester.Append
    logstream.Log.Append
      recordio.Writer.WriteRecord
      objectstore.Store.Put (wal/<020d_seq>.recordio)
ingest.Flusher.Run (Background goroutine)
  logstream.Log.ReadSeq
  ingest.Flusher.applyMutation (in-memory branch memtable)
  ingest.Flusher.flushBranch
    segment.Writer.Write (segments/<id>.seg)
    objectstore.Store.Put (refs/heads/<ns>_<branch>, if-generation-match)
```

---

## Query Pipeline (Read Path)

Query nodes read immutable segment references from branch pointers, load
columnar segments into flat contiguous memory, and compute exact k-NN vector
distances and scalar attribute filters.

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
│      query.Loader       │ Reads refs/heads/<ns>_<branch> and segments
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
grpcapi.QueryServer.Query
  query.Engine.Query
    query.Loader.SyncBranch
      objectstore.Store.Get (refs/heads/<ns>_<branch>)
      segment.Reader.Open (segments/<id>.seg)
      query.Table.Add (appends vectors into flat []float32)
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
<storage-root>/
├── wal/
│   ├── 00000000000000000001.recordio
│   ├── 00000000000000000002.recordio
│   └── 00000000000000000003.recordio
├── segments/
│   ├── 01J8ABCDEF0123456789.seg
│   ├── 01J8ABCDEF0123456790.seg
│   └── 01J8ABCDEF0123456791.seg
└── refs/
    └── heads/
        ├── main
        ├── dev
        └── <namespace>_<branch>
```

### Storage Invariants

1. **WAL records are immutable**: Once written, a WAL sequence file is never
   modified or overwritten.
2. **Segment blobs are content-isolated**: Segment files contain immutable
   vector and document data. Segments are shared across forked branches
   without data duplication.
3. **Branch heads advance monotonically**: Manifest commits update
   `refs/heads/<branch>` using conditional creates (`Absent: true`) or
   generation-matched updates (`if-generation-match`).

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
| `query/distance` | Math | Vector distance kernels (scalar and portable SIMD) |
| `segment` | Storage | Segment encoding and decoding over RecordIO |
| `kvfs` | Store | CAS blob storage, branch pointers, and manifests |
| `objectstore` | Storage | Store drivers (GCS, local disk, memory) |
