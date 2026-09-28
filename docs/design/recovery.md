# Crash recovery

This note lists the crash and write failures that ingestion recovers from, and
the ones it does not. Each case names the state it leaves in storage and what
repairs it.

## Model

Four objects hold the durable state of a branch:

- WAL: the log of mutations and branch events, one stream per namespace.
- Segment: an immutable blob of mutations, named by the SHA-256 of its bytes.
- Manifest: the ordered segment list of a branch and its `CheckpointSeq`.
- Catalog: `branches.json`, the list of branches in a namespace.

The query engine makes a loader only for a branch in the catalog. A branch with
a manifest but no catalog entry is invisible to queries.

On restart, the flusher reads the catalog, takes the lowest `CheckpointSeq` of
the listed branches, and replays the WAL after it.

## Flush

The flusher commits one WAL entry in three writes:

```text
put segment      <sha256>.recordio, absent-only
CAS manifest     append segment, CheckpointSeq = seq
AddBranch        catalog entry, no write when present
```

A crash or an error can stop the flusher after any of the three. The flusher
recovers from all three [#165]:

| Stop after     | Replay finds                     | Replay does                      |
| -------------- | -------------------------------- | -------------------------------- |
| segment put    | segment present                  | counts the 412 as success        |
| manifest CAS   | `CheckpointSeq >= seq`           | skips the append, runs AddBranch |
| AddBranch      | catalog entry present            | skips, AddBranch writes nothing  |

The content hash makes the segment put idempotent. The same mutations produce
the same bytes, so a present key holds the same segment.

The `CheckpointSeq` skip makes the manifest append idempotent. Before, a replay
appended the segment a second time.

An `AddBranch` error stops the stream worker. Before, the flusher dropped it.
The restart replays the entry, and the skip path writes the catalog entry.

A branch that is missing from the catalog is not missed at restart. The flusher
commits WAL entries in order, so no later entry commits before the missing
branch's entry finishes its `AddBranch`. The replay start is thus never past
that entry.

## Query loader

The loader applies `manifest.Segments[applied:]` by position. This needs a
manifest that only grows.

A Fork rollback deletes a manifest, and a retry from a smaller parent writes a
shorter one. The loader returns `ErrManifestTruncated` for a manifest shorter
than `applied` [#165]. Before, it panicked in `Engine.Run`.

The branch then keeps its old data until the process restarts. See the gaps
below.

## Fork

Fork does four steps:

```text
read parent manifest
write target manifest   copy of parent, absent-only
AddBranch(target)
append FORK event       to the WAL
```

When `AddBranch` or the WAL append fails, Fork deletes the target manifest,
removes the catalog entry, and returns the error [#166]. A retry then starts
from a clean state.

Before, Fork dropped the `AddBranch` error. The client saw success, queries on
the branch returned empty results, and a retry got `ErrBranchAlreadyExists`.

## Gaps

These cases have no recovery yet.

### Crash between a Fork failure and its rollback

The rollback runs in the same process as the failure. A crash between the two
leaves this state:

```text
target manifest   present, copy of parent
branches.json     no target
WAL               no FORK event
client            no answer
```

The results:

- A query on the target returns empty results.
- A Fork retry returns `ErrBranchAlreadyExists`. The client cannot tell a real
  fork from an orphan.
- A write to the target repairs it. `Ingester.Upsert` does not check that the
  branch exists. The flusher commit adds the catalog entry, and the branch
  then holds the parent data at fork time plus the new writes.
- Without a write, the orphan stays forever.

The same state appears when the rollback itself fails. The rollback drops the
errors of `Delete` and `RemoveBranch`.

Two repairs fit:

- An idempotent Fork retry. Fork treats a present target manifest as its own
  when the manifest equals the parent's and the WAL has no FORK event for the
  target. It then finishes `AddBranch` and the event.
- Garbage collection. It deletes a manifest that has no catalog entry, has no
  FORK event, and is older than a timeout. This rides on the GC worker in
  `TODO.md`.

CONSIDER(ali): GC is cheaper, but it keeps the retry ambiguous until the
timeout passes. The idempotent retry fixes the client view but adds a WAL scan
to Fork.

### Stale loader after a shorter manifest

After `ErrManifestTruncated`, the loader logs the error on every sync. The
branch serves its old data until a restart. The fix is a loader that rebuilds
its table from the full manifest when the manifest shrinks.

### Local store durability

`localStore.Put` does not fsync the file before the link. After a power loss,
a segment key can hold a truncated file. The flush treats a present key as
stored, so the replay accepts the truncated segment without an error. GCS
writes are atomic, so this affects only the local store. `TODO.md` holds the
fsync fix.
