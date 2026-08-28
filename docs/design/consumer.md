# LogStream Consumer

## 1. Overview
The consumer module reads sequential mutation streams from LogStream (Layer 1) and materializes state (such as KV stores or search indexes).

```text
┌─────────────────┐       ┌─────────────────┐       ┌─────────────────┐
│   ObjectStore   │ ----> │ Stream Consumer │ ----> │ State Engine    │
│ (wal/ segments) │ Read  │ (LogStream/Tail)│ Apply │ (In-Memory / KV)│
└─────────────────┘       └─────────────────┘       └─────────────────┘
                                   │
                                   ▼
                          ┌─────────────────┐
                          │ Checkpoint Path │
                          │ (GCS CAS Offset)│
                          └─────────────────┘
```

## 2. Reading and Tailing
- Segment paths obey `wal/<stream>/<020d_seq>.recordio`.
- The consumer reads segments sequentially starting from a committed offset or sequence 1.
- When reaching the stream head, the consumer polls for `seq+1` with exponential backoff (50ms to 1s) and jitter on 404 errors.

## 3. Multi-Instance Coordination & Deduplication
To prevent duplicate processing on expensive workloads (such as LLM inference) across multiple workers:
- **Stream Leases**: Workers acquire a lease object at `consumers/<group>/<stream>/leader.json` with conditional create (`if-generation-match=0`) and a TTL heartbeat.
- **Checkpoints**: Progress commits write to `consumers/<group>/<stream>/checkpoint.json` with conditional write matching the previous generation.
- **Sink Idempotency**: Output mutations carry deterministic keys `hash(stream, seq, record_index)` to make writes idempotent on replay.

## 4. Branch Handling & Discovery
- **Branch Streams**: Each branch writes to its own isolated stream `wal/<branch>/`.
- **Branch Discovery**: Branch lifecycle events append to a global meta stream `wal/_meta/`:
  ```json
  {
    "event": "BRANCH_CREATED",
    "branch": "feature-1",
    "parent_branch": "main",
    "parent_manifest": "1a2beff890..."
  }
  ```
- **State Initialization**:
  1. The consumer for a new branch reads `parent_manifest` once to initialize its base snapshot ($O(1)$).
  2. The consumer tails `wal/<branch>/` starting from sequence 1 for branch-specific deltas.
