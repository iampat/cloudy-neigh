# Storage Architecture & Layering Specification

## 1. System Overview
The storage engine runs on top of Cloud Object Storage (Google Cloud Storage / AWS S3) with no external database or consensus cluster.

The system consists of two distinct layers plus a consumer ecosystem:

```text
┌─────────────────────────────────────────────────────────────────────────────┐
│                    Layer 2: Key-Value Filesystem (KVFS)                     │
│  • Content-Addressed Storage (CAS) for blobs and manifests                  │
│  • Branch heads (refs/heads/<branch>) and zero-copy branching ($O(1)$)       │
│  • See kvfs.md                                                              │
└─────────────────────────────────────┬───────────────────────────────────────┘
                                      │
                                      ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                       Layer 1: LogStream (WAL / Queue)                      │
│  • Direct sequential keys: wal/<stream>/<020d_seq>.recordio                 │
│  • Atomic conditional appends (if-generation-match=0)                       │
│  • See wal.md                                                               │
└─────────────────────────────────────┬───────────────────────────────────────┘
                                      │
                                      ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                     Layer 0: Cloud ObjectStore Adapter                      │
│  • Raw object operations with generation preconditions                      │
└─────────────────────────────────────────────────────────────────────────────┘
```

## 2. Storage Layout Hierarchy

```text
[Bucket Root]
├── refs/heads/                  <-- Layer 2: Mutable Branch Heads (conditional write)
│   ├── main                     --> "1a2beff8..."
│   └── feature-1                --> "1a2beff8..."
│
├── manifests/                   <-- Layer 2: Content-Addressed Manifest Snapshots (2-Byte Sharded)
│   └── 1a/2b/1a2beff890...      --> Manifest { last_wal_seq: 10, entries: { "docs/a.txt": "a3f1..." } }
│
├── cas/                         <-- Layer 2: Content-Addressed Raw Payloads (2-Byte Sharded)
│   └── a3/f1/a3f1c8901b...      --> [raw binary data]
│
└── wal/                         <-- Layer 1: Direct Sequenced Logs
    ├── _meta/                   <-- Branch lifecycle discovery events
    │   └── 00000000000000000001.recordio
    ├── main/
    │   ├── 00000000000000000001.recordio
    │   └── 00000000000000000002.recordio
    └── feature-1/
        └── 00000000000000000001.recordio
```

## 3. Related Specifications
- [**wal.md**](wal.md): Layer 1 LogStream specification, conditional append protocol, and tail search.
- [**kvfs.md**](kvfs.md): Layer 2 Key-Value Filesystem, CAS blobs, manifests, and branch operations.
- [**consumer.md**](consumer.md): LogStream consumer tailing, multi-worker coordination leases, deduplication, and branch initialization.
- [**recordio.md**](recordio.md): Binary framing format with CRC32C checksums.
