# Storage Architecture & Layering Specification

## 1. System Overview
The storage engine runs on top of Cloud Object Storage (Google Cloud Storage / AWS S3) with no external database or consensus cluster.

The system consists of two distinct layers plus a consumer ecosystem:

```text
┌─────────────────────────────────────────────────────────────────────────────┐
│                 Layer 2: Ingestion & Columnar Segments                      │
│  • Immutable columnar segment files (.vec, .post, .doc)                     │
│  • Branch heads (refs/heads/<branch>) with inlined SegmentRef manifests     │
│  • Zero-copy branching and copy-on-write segment sharing                    │
│  • See ingestion.md and storage-simplification.md                           │
└─────────────────────────────────────┬───────────────────────────────────────┘
                                      │
                                      ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                    Layer 1: LogStream (Single Global WAL)                   │
│  • Direct sequential keys: wal/<020d_seq>.recordio                          │
│  • Atomic conditional appends (if-generation-match=0)                       │
│  • See wal.md                                                               │
└─────────────────────────────────────┬───────────────────────────────────────┘
                                      │
                                      ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                     Layer 0: Cloud ObjectStore Adapter                      │
│  • Raw object operations with generation preconditions and ReadRange        │
└─────────────────────────────────────────────────────────────────────────────┘
```

## 2. Storage Layout Hierarchy

```text
[Bucket Root]
├── refs/heads/                  <-- Layer 2: Mutable Branch Heads (Inlined Manifests <=14 KiB)
│   ├── main                     --> BranchManifest { checkpoint_seq: 104, segments: [...] }
│   └── feature-1                --> BranchManifest { checkpoint_seq: 82,  segments: [...] }
│
├── segments/                    <-- Layer 2: Immutable Columnar Segment Files
│   ├── seg_001.vec              --> Dense float vectors
│   ├── seg_001.post             --> Inverted postings and term dictionaries
│   └── seg_001.doc              --> Document attributes and block footer
│
└── wal/                         <-- Layer 1: Single Global Sequenced Log
    ├── 00000000000000000001.recordio
    └── 00000000000000000002.recordio
```

## 3. Related Specifications
- [**wal.md**](wal.md): Layer 1 LogStream specification, conditional append protocol, and tail search.
- [**ingestion.md**](ingestion.md): Ingestion pipeline, consumer dispatch, Memtable flush, point-in-time recovery, and bulk backfill.
- [**recordio.md**](recordio.md): Binary framing format with CRC32C checksums.
- [**storage-simplification.md**](storage-simplification.md): Five architectural simplifications for sub-2 ms query latency on GCS.
