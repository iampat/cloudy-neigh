# Crash recovery

This note lists the crash and write failures that ingestion recovers from.
Each case names the state it leaves in storage and what repairs it.
Open bugs and recovery gaps are tracked in `TODO.md`.

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

