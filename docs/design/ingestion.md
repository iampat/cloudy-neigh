# Ingestion and Materialization

**Status:** Partly built. The architecture, the record format, the ingestion
protocol, and the flusher describe the code. Memtables, point-in-time queries,
bulk backfill, and synchronous ingestion are future work.

## Problem

A search engine must ingest real-time document mutations across multiple dataset branches. It must also support bulk backfills and point-in-time recovery. Running external coordination or workflow clusters increases operational cost.

## Goals

- One write-ahead log (WAL) per namespace for all document mutations across its branches.
- In-memory dispatch of log records to per-branch Memtables.
- Point-in-time recovery and snapshot queries through manifest sequence anchors.
- Bulk backfill via direct immutable segment creation with zero external coordination.
- Periodic flushing of Memtables into immutable segments.

## Non-goals

- Partitioning write-ahead logs into per-branch storage prefixes.
- External workflow engines such as Temporal or distributed locking services.
- Distributed multi-worker lease heartbeats for single-node ingestion.

## Architecture

All keys sit under `ns/<namespace>/`.

```
┌──────────────────────────┐ Upsert / Delete / Fork (namespace, branch)
│ grpcapi.IngestServer     ├───────────────┐
└──────────────────────────┘               ▼
                             ┌──────────────────────────┐
                             │ ingest.Ingester          │
                             └─────────────┬────────────┘
                                           │ Append
                                           ▼
                             ┌──────────────────────────┐
                             │ wal/<020d_seq>.recordio  │
                             └─────────────┬────────────┘
                                           │ Read(seq)
                                           ▼
                             ┌──────────────────────────┐
                             │ ingest.Flusher           │
                             │ one stream per namespace │
                             └─────────────┬────────────┘
                                           │ one segment per WAL entry
                                           ▼
                             ┌──────────────────────────┐
                             │ segments/<020d_seq>      │
                             │   .recordio              │
                             └─────────────┬────────────┘
                                           │ CAS
                                           ▼
                             ┌──────────────────────────┐
                             │ refs/heads/<branch>.json │
                             │ branches.json            │
                             └──────────────────────────┘
```

## Record Format

Mutations and branch lifecycle events serialize into RecordIO frames inside the
WAL. The messages live in `proto/storage/v1/storage.proto`.

```proto
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
    // DELETE = 2;
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

A segment is one recordio file of `DocumentMutation` protos.

## Ingestion Protocol

Upsert and Delete:

1. Client sends a batch of mutations for one namespace and one branch.
2. On the first write to a namespace, the ingester adds it to `ns.json`.
3. The ingester wraps each mutation into a `WalRecord`.
4. The ingester appends the batch to `logstream.Log` as one WAL entry.
5. `logstream.Log` commits the entry under `ns/<namespace>/wal/<020d_seq>.recordio`.
6. The node returns success after the WAL write commits.

Fork:

1. The ingester copies the source manifest to `refs/heads/<target>.json` with
   `Absent: true`.
2. It adds the target to `branches.json`.
3. It appends a `FORK` event to the WAL.
4. If the append fails, it deletes the target manifest and removes the target
   from `branches.json`.

## Flusher

`ingest.Flusher` reads the active namespaces from `ns.json` at each poll
interval (default 100 ms). It starts one stream per namespace.

1. A stream reads `branches.json` and the manifest of each branch. It starts at
   the minimum `checkpoint_seq + 1`.
2. For each WAL entry at `seq`, it groups the mutations by branch. It skips a
   mutation when the branch checkpoint is at `seq` or later.
3. On a `FORK` event, it flushes the pending parent mutations of the entry. It
   sets the child checkpoint to `seq`.
4. For each branch, it writes one segment `segments/<020d_seq>.recordio` with
   `Absent: true`.
5. It reads the branch manifest, appends the `SegmentRef`, and sets
   `checkpoint_seq = seq`. It writes the manifest with a generation match and
   retries on `412`.
6. It adds the branch to `branches.json`.
7. On shutdown, it drains the WAL to its end.

Each WAL entry that holds mutations becomes a segment. The flusher has no
Memtable, and no size or time threshold.

## Consumer and Memtable Materialization

Future work.

1. The consumer tails `logstream.Log` sequentially starting from `checkpoint_seq + 1`.
2. For each `WalRecord`:
   - If `branch_event.type == FORK`:
     1. Freeze the parent branch Memtable.
     2. Flush parent Memtable into segment files.
     3. Commit updated `BranchManifest` for parent branch (`checkpoint_seq = fork_seq`).
     4. Write `refs/heads/<child>.json` with parent `BranchManifest` and precondition `Absent: true`.
     5. Open new active Memtables for both parent and child branches.
   - If `branch_event.type == DELETE`:
     1. Purge the in-memory Memtable, index structures, and lookup maps for the target branch.
     2. Delete `refs/heads/<branch>.json`.
   - If `mutation`:
     1. Route the mutation to the target branch active Memtable.
3. The branch Memtable updates its internal structures:
   - Vector buffer for brute-force distance calculation.
   - Inverted index postings for lexical matching.
   - Attribute map for document retrieval and scalar filtering.
   - Tombstone bitset for deletions.
4. The consumer updates `last_applied_seq = seq`.

## Memtable Flush Protocol

Future work. A branch Memtable flushes on two independent triggers:
- **Size threshold (e.g. 64 MB):** Bounds memory usage under high write throughput.
- **Time threshold (e.g. 1 minute):** Bounds persistence latency for idle or low-volume branches. A single document flushes even if no further writes arrive.

When either threshold triggers:
1. Freeze active Memtable and open a new active Memtable.
2. Serialize frozen state into columnar segment files:
   - `segments/<id>.vec`
   - `segments/<id>.post`
   - `segments/<id>.doc`
3. Store columnar files in `objectstore.Store`.
4. Commit updated `BranchManifest` directly to `refs/heads/<branch>.json` with GCS `if-generation-match`.
5. Discard the frozen Memtable.

## Point-in-Time Recovery and Queries

Future work. Each branch manifest records `checkpoint_seq`. A `fork_seq` field
does not exist yet.

To query branch `B` at historical sequence `T`:
1. Load the manifest snapshot on branch `B` with `checkpoint_seq <= T`.
2. Load cached segment blobs referenced by this manifest.
3. Replay WAL records from `checkpoint_seq + 1` up to `T` where `mutation.branch == B`.
4. Apply the replayed mutations to the in-memory candidate set.
5. Execute query across the combined dataset.

## Bulk Backfill Orchestration

Future work. Bulk backfills bypass the sequential write-ahead log.

1. **Segment Generation**:
   Workers read source datasets and write immutable segment files directly to `segments/`.
2. **Manifest Commit Batches**:
   Workers commit segment references in chunks directly to `refs/heads/<branch>.json`.
   Commits use conditional GCS `if-generation-match` writes.
3. **Crash Recovery**:
   If a worker crashes, the replacement worker reads `refs/heads/<branch>.json`.
   It resumes backfill from the last committed segment chunk.
4. **Zero Coordination**:
   The protocol requires no external database or workflow orchestrator.

## Concurrency and Coordination Model

Future work. Document ingestion and materialization split concurrency across two distinct phases:

### In-Memory Mutation Dispatch (Hot Path)

- Each document mutation updates three internal structures:
  1. Attribute map for document retrieval.
  2. Inverted index postings for lexical search.
  3. Vector buffer for similarity search.
- The consumer updates these structures sequentially under a Memtable mutex.
- Sub-microsecond memory writes do not justify per-document goroutine spawn overhead.

### Segment Flush and Storage Sink (Cold Path)

- Mutations do not write to object storage individually.
- When the size or time threshold triggers, three concurrent goroutines upload the columnar segment files in parallel.
- An `errgroup.Group` coordinates the three upload tasks:
  - `segments/<id>.doc`
  - `segments/<id>.post`
  - `segments/<id>.vec`
- After all uploads succeed, a single atomic commit updates `refs/heads/<branch>.json`.

## Appendix: Ingestion Modes (Async vs Sync)

The ingestion pipeline supports asynchronous ingestion. Synchronous ingestion
is future work.

### 1. Asynchronous Ingestion (Default)

- The write returns immediately after appending to `logstream.Log`.
- The flusher tails the log and writes segments in the background.
- Read queries observe mutations after the flush and the next query sync.

### 2. Synchronous Ingestion (Read-After-Write)

Future work.

- A read sees the write as soon as the write returns.
- Writers return once the log write commits. They do not wait for index materialization.
- The query engine executes across two sources:
  1. The indexed dataset (active Memtable and segment files).
  2. The unindexed pending buffer in `logstream.Log`.
- The engine unions and deduplicates candidates before ranking.
- Linear scan over small unindexed buffers keeps write and read latencies balanced.
