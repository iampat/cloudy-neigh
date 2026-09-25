# Storage Architecture & Layering Specification

## 1. System Overview
The storage engine runs on top of Cloud Object Storage (Google Cloud Storage / AWS S3) with no external database or consensus cluster.

The system consists of three layers:

```text
┌────────────────────────────────────────────────────────────────┐
│ Layer 2: Segments and Branch Manifests                         │
│  • segments/<020d_seq>.recordio: DocumentMutation protos       │
│  • refs/heads/<branch>.json: BranchManifest in protojson       │
│  • branches.json: the branch names of the namespace            │
│  • Forks share segments. A fork copies the manifest only.      │
│  • See ingestion.md and namespace-catalog.md                   │
└───────────────────────────────┬────────────────────────────────┘
                                ▼
┌────────────────────────────────────────────────────────────────┐
│ Layer 1: LogStream, one WAL per namespace                      │
│  • Direct sequential keys: wal/<020d_seq>.recordio             │
│  • Atomic conditional appends (if-generation-match=0)          │
│  • See wal.md                                                  │
└───────────────────────────────┬────────────────────────────────┘
                                ▼
┌────────────────────────────────────────────────────────────────┐
│ Layer 0: Cloud ObjectStore Adapter                             │
│  • Raw object operations with generation preconditions         │
└────────────────────────────────────────────────────────────────┘
```

## 2. Storage Layout Hierarchy

Package `namespace` owns every key. Callers pass the namespace and the branch
as separate parameters. No code parses a key to find them.

```text
<tenant-storage-root>/
├── ns.json                        TenantCatalog: namespaces
└── ns/<namespace>/
    ├── branches.json              BranchCatalog: branch names
    ├── refs/heads/
    │   ├── main.json              BranchManifest {checkpoint_seq: 104}
    │   └── feature-1.json         BranchManifest {checkpoint_seq: 82}
    ├── segments/
    │   ├── 00000000000000000082.recordio
    │   └── 00000000000000000104.recordio
    └── wal/
        ├── 00000000000000000001.recordio
        └── 00000000000000000104.recordio
```

A segment is one recordio file of `DocumentMutation` protos. It holds the
mutations of one WAL entry, and the WAL sequence number names it. Segments
have no columnar split, no footer, and no block index.

## 3. Related Specifications
- [**wal.md**](wal.md): Layer 1 LogStream specification, conditional append protocol, and tail search.
- [**ingestion.md**](ingestion.md): Ingestion pipeline and the flusher.
- [**namespace-catalog.md**](namespace-catalog.md): `ns.json`, `branches.json`, and the namespace lifecycle.
- [**recordio.md**](recordio.md): Binary framing format with CRC32C checksums.
- [**storage-simplification.md**](storage-simplification.md): Planned read-path simplifications for sub-2 ms query latency on GCS.
