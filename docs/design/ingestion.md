# Ingestion and Materialization

## Problem

A search engine must ingest real-time document mutations across multiple dataset branches. It must also support bulk backfills and point-in-time recovery. Running external coordination or workflow clusters increases operational cost.

## Goals

- Single unified write-ahead log for all document mutations across branches.
- In-memory dispatch of log records to per-branch Memtables.
- Point-in-time recovery and snapshot queries through manifest sequence anchors.
- Bulk backfill via direct immutable segment creation with zero external coordination.
- Periodic flushing of Memtables into immutable CAS segment blobs.

## Non-goals

- Partitioning write-ahead logs into per-branch storage prefixes.
- External workflow engines such as Temporal or distributed locking services.
- Distributed multi-worker lease heartbeats for single-node ingestion.

## Architecture

```
Ingestion & Materialization Pipeline
┌────────────────────────┐ Write(branch, doc)
│ Ingestion Client       ├─────────────────────────────────────────┐
└────────────────────────┘                                         ▼
┌────────────────────────┐ Write(branch, doc) ┌────────────────────────────┐
│ Bulk Ingest Worker     ├───────────────────►│ Global WAL (logstream.Log) │
└────────────────────────┘                    │ wal/<020d_seq>.recordio    │
                                              └─────────────┬──────────────┘
                                                            │ Read(seq)
┌───────────────────────────────────────────────────────────▼──────────────┐
│ Consumer & Branch Router                                                 │
│ (Tails global WAL, inspects record.Branch, routes to Memtable)           │
└──────────────┬────────────────────────────────────────────┬──────────────┘
               │                                            │
               ▼                                            ▼
┌────────────────────────────┐               ┌─────────────────────────────┐
│ Branch "main" Memtable     │               │ Branch "dev" Memtable       │
│ (Vectors, Postings, Docs)  │               │ (Vectors, Postings, Docs)   │
└──────────────┬─────────────┘               └──────────────┬──────────────┘
               │ Flush at size/time threshold               │ Flush at size/time threshold
               │ (e.g. 64 MB / 1 min)                       │ (e.g. 64 MB / 1 min)
               ▼                                            ▼
┌────────────────────────────┐               ┌─────────────────────────────┐
│ refs/heads/main            │               │ refs/heads/dev              │
│ (Manifest: checkpoint_seq) │               │ (Manifest: checkpoint_seq)  │
└──────────────┬─────────────┘               └──────────────┬──────────────┘
               │                                            │
               └─────────────────────┬──────────────────────┘
                                     ▼
                      ┌────────────────────────────┐
                      │ kvfs.Store (CAS Blobs)     │
                      │ cas/<sha256_hash>          │
                      └────────────────────────────┘
```

## Record Format

Mutations and branch lifecycle events serialize into RecordIO frames inside the global WAL.

```proto
syntax = "proto3";
package cloudyneigh.ingest;

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
    TYPE_UNSPECIFIED = 0;
    FORK = 1;
    DELETE = 2;
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

## Ingestion Protocol

1. Client sends a document mutation or branch lifecycle request.
2. The ingestion node wraps the operation into a `WalRecord`.
3. The node appends the record to `logstream.Log`.
4. `logstream.Log` commits the segment file under `wal/<020d_seq>.recordio`.
5. The node returns sequence number `seq` to the client.

## Consumer and Memtable Materialization

1. The consumer tails `logstream.Log` sequentially starting from `checkpoint_seq + 1`.
2. For each `WalRecord`:
   - If `branch_event.type == FORK`:
     1. Freeze the parent branch Memtable.
     2. Flush parent Memtable into CAS segment blobs.
     3. Commit a new manifest snapshot `M` for the parent branch (`checkpoint_seq = fork_seq`).
     4. Write `refs/heads/<child>` pointing to `M` with precondition `Absent: true`.
     5. Open new active Memtables for both parent and child branches.
   - If `branch_event.type == DELETE`:
     1. Purge the in-memory Memtable, index structures, and lookup maps for the target branch.
     2. Delete `refs/heads/<branch>`.
   - If `mutation`:
     1. Route the mutation to the target branch active Memtable.
3. The branch Memtable updates its internal structures:
   - Vector buffer for brute-force distance calculation.
   - Inverted index postings for lexical matching.
   - Attribute map for document retrieval and scalar filtering.
   - Tombstone bitset for deletions.
4. The consumer updates `last_applied_seq = seq`.

## Memtable Flush Protocol

A branch Memtable flushes on two independent triggers:
- **Size threshold (e.g. 64 MB):** Bounds memory usage under high write throughput.
- **Time threshold (e.g. 1 minute):** Bounds persistence latency for idle or low-volume branches. A single document flushes even if no further writes arrive.

When either threshold triggers:
1. Freeze active Memtable and open a new active Memtable.
2. Serialize frozen state into columnar CAS segment blobs:
   - `segments/vectors/<id>`
   - `segments/postings/<id>`
   - `segments/docs/<id>`
3. Store blobs in `objectstore.Store` via `kvfs.Store.PutBlob`.
4. Commit a new manifest snapshot using `kvfs.Store.Batch`.
5. Update `refs/heads/<branch>` with `checkpoint_seq = last_applied_seq`.
6. Discard the frozen Memtable.

## Point-in-Time Recovery and Queries

Each branch manifest records `checkpoint_seq` and `fork_seq`.

To query branch `B` at historical sequence `T`:
1. Load the manifest snapshot on branch `B` with `checkpoint_seq <= T`.
2. Load cached segment blobs referenced by this manifest.
3. Replay WAL records from `checkpoint_seq + 1` up to `T` where `mutation.branch == B`.
4. Apply the replayed mutations to the in-memory candidate set.
5. Execute query across the combined dataset.

## Bulk Backfill Orchestration

Bulk backfills bypass the sequential write-ahead log.

1. **Segment Generation**:
   Workers read source datasets and write immutable CAS segment blobs directly to `cas/<hash>`.
2. **Manifest Commit Batches**:
   Workers commit segment references in chunks using `kvfs.Store.Batch`.
   Commits use conditional CAS writes on `refs/heads/<branch>`.
3. **Crash Recovery**:
   If a worker crashes, the replacement worker reads `refs/heads/<branch>`.
   It resumes backfill from the last committed segment chunk.
4. **Zero Coordination**:
   The protocol requires no external database or workflow orchestrator.

## Concurrency and Coordination Model

Document ingestion and materialization split concurrency across two distinct phases:

### In-Memory Mutation Dispatch (Hot Path)

- Each document mutation updates three internal structures:
  1. Attribute map for document retrieval.
  2. Inverted index postings for lexical search.
  3. Vector buffer for similarity search.
- The consumer updates these structures sequentially under a Memtable mutex.
- Sub-microsecond memory writes do not justify per-document goroutine spawn overhead.

### Segment Flush and Storage Sink (Cold Path)

- Mutations do not write to `kvfs.Store` individually.
- When the size or time threshold triggers, three concurrent goroutines upload the CAS segment blobs in parallel.
- An `errgroup.Group` coordinates the three upload tasks:
  - `segments/docs/<id>`
  - `segments/postings/<id>`
  - `segments/vectors/<id>`
- After all uploads succeed, a single atomic CAS commit updates `refs/heads/<branch>`.

## Appendix: Ingestion Modes (Async vs Sync)

The ingestion pipeline supports two delivery modes:

### 1. Asynchronous Ingestion (Default)

- The write returns immediately after appending to `logstream.Log`.
- The consumer tails the log and updates the branch Memtable in the background.
- Read queries observe mutations only after Memtable dispatch.

### 2. Synchronous Ingestion (Read-After-Write)

- The write ensures immediate read visibility.
- Writers return once the log write commits. They do not wait for index materialization.
- The query engine executes across two sources:
  1. The indexed dataset (active Memtable and CAS segment blobs).
  2. The unindexed pending buffer in `logstream.Log`.
- The engine unions and deduplicates candidates before ranking.
- Linear scan over small unindexed buffers keeps write and read latencies balanced.
