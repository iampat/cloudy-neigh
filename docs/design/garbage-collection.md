# Garbage collection and point-in-time branches

Status: Draft. 2026-09-28.

The retention and recovery model is proposed for review.
The shared metadata protocol below is a correctness baseline, not an approved production publication architecture.
Its object update rate and crashed-reader cleanup policy remain implementation blockers.

The engine can delete an object only when no supported state or active operation needs it.
Supported states include current branches, retained historical checkpoints, and explicit read-only archives.
This design connects garbage collection to columnar storage, manifest compaction, and document retrieval by identifier.

Point-in-time (PiT) recovery has a configurable history window.
Periodic checkpoints provide the base state for recovery between checkpoints.
Historical branch creation can require substantial work because it is an infrequent operation.
Write-ahead log (WAL) compaction and deletion are outside this design.
The recovery protocol requires the necessary WAL records to remain available.

## State and ownership

A snapshot is an immutable manifest that describes one complete logical branch state.
Its segment list can contain both a compacted base and subsequent mutation segments.
Each segment owns its document columns, search indexes, and document-identifier index.

```text
Namespace state
├── Writable branch ────────▶ snapshot
├── Automatic checkpoint ──▶ snapshot
├── Named archive ─────────▶ snapshot
└── Active pin ────────────▶ snapshot
                                │
                                ▼
                             segments
                                ├── document columns
                                ├── vector and search indexes
                                └── document-identifier index
```

A checkpoint records a snapshot and the logical sequence through which its state is complete.
An automatic checkpoint has a retention policy.
A named archive retains the same kind of state until explicit deletion or a configured expiry.
Neither permits document writes.
Creating an archive or branch can share existing segment files.

Physical collection initially operates on complete segments.
One live segment retains all its column and index files.
Compaction removes obsolete rows before whole-segment collection reclaims their former files.
This costs rewrite work but avoids independent ownership rules for each column.

A manifest contains explicit object references and integrity metadata.
It never implies that every object with a matching name exists.
All uploads finish before their manifest becomes visible.
Package `namespace` constructs keys from explicit namespace, epoch, and operation identifiers.
No component derives a namespace or branch from a path.

## Logical time and branch identity

One WAL sequence identifies one committed batch within a namespace.
A requested sequence includes the entire batch.
The state at sequence `S` includes the source branch's mutations through `S` and its inherited base.
Records for other branches do not change that state.

Three sequences have distinct meanings:

| Field | Meaning |
| --- | --- |
| `source_seq` | Parent state selected for a fork |
| `birth_seq` | WAL sequence of the child's creation event |
| `through_seq` | Contiguous WAL boundary fully considered for this snapshot |

For example, a child created at sequence 300 can inherit its parent at sequence 120.
Its initial state contains parent data through 120.
Child replay begins after 300, not after 120.
The child has no historical states before its own birth.

A branch has an immutable identifier separate from its display name.
New mutation and lifecycle records carry that identifier.
Historical replay must not combine different branches that happen to share a name.
Legacy branch names require an explicit migration mapping.

### Source selection

The public request selects either a parent branch name or a parent sequence within the namespace.
Both forms resolve to a fixed `(branch_id, source_seq)` before recovery starts.

For a branch name, capture a committed namespace WAL boundary and resolve that branch's state at the boundary.
This includes acknowledged writes that have not reached a segment yet.
Concurrent writes beyond the captured boundary do not enter the child.

For a sequence, resolve the owner of that WAL batch.
A document batch identifies its document branch.
A fork event identifies the newly created child.
An empty or ambiguous batch cannot identify a source and returns an explicit error.
This form does not mean an arbitrary branch's state at an unrelated branch's sequence.

The current flusher rejects document batches with mutations from multiple branches.
The sequence-only contract preserves that restriction for eligible source batches.
A future request can add an explicit branch-plus-sequence selector if clients need that separate operation.

The resolved response exposes the source branch identifier and sequence.
A retry uses the stored resolution and never selects a newer parent state.

## Checkpoints and retention

Each namespace configures a history window `W` and checkpoint interval `T`.
These are separate settings.
`W` controls the historical guarantee, while `T` influences replay work.
Neither setting is a query timeout or an object deletion delay.

The checkpoint worker captures a committed WAL boundary `C`.
It materializes each eligible branch through `C`, then publishes its checkpoint reference.
This work initially belongs to the existing flusher, which already consumes the ordered WAL.
The worker is a responsibility, not a new process.
Separate historical recovery tasks can replay an older interval without rewinding the live flusher.
The snapshot's `through_seq` advances only after every relevant record through `C` has been considered.
An idle branch can reuse its existing segments with a newer checkpoint descriptor.

The checkpoint descriptor records its branch identifier, `through_seq`, and publication time.
Use storage-assigned publication time, not a client's timestamp.
The storage adapter must expose that time before automatic expiry is enabled.
The WAL boundary is captured before the descriptor is uploaded, so its publication time follows that boundary.

This supplies a conservative time-to-sequence mapping without adding timestamps to every WAL mutation.
It can preserve more history than `W`, especially after checkpoint delays.
It must never preserve less than the promised window.

At time `now`, the retention boundary is `now - W`.
For each retained branch lineage, preserve:

1. The latest checkpoint published at or before that boundary.
2. Every later automatic checkpoint.
3. Every snapshot retained by an archive, branch, or active operation.

If the branch is younger than the window, preserve its initial checkpoint instead of a predecessor.
Each branch receives an initial checkpoint before historical recovery is advertised.
After migration, the guarantee starts at that checkpoint rather than retroactively covering unavailable history.

```text
checkpoint A       window boundary       checkpoint B
    10:00                10:05                10:10
      │                    │
      └── retained ────────▶ recover 10:06 from A plus WAL
```

The example uses wall time for illustration.
Recovery requests use exact WAL sequences.
The service advertises the earliest supported sequence for each branch.
It rejects older sequence requests even if some unprotected files still happen to exist.
An explicitly retained archive remains usable independently of that automatic boundary.

A retention change and its checkpoint removals commit through the namespace state record.
An admitted recovery keeps its pinned inputs even if the window subsequently shrinks.
Increasing `W` cannot restore history already collected.
The advertised floor expands only when the necessary checkpoint and WAL coverage exist.

Missed checkpoints retain the older recovery base.
They increase recovery cost and retained bytes rather than weaken correctness.
An archive retains its exact state, not an implicit guarantee for every later historical sequence.

## Namespace metadata

Independent branch manifests and a separate branch catalog cannot provide an atomic collection boundary.
Introduce one generation-guarded namespace state record for ownership changes.
It is the authority for publication and collection.

Its logical fields are:

```text
NamespaceState
├── format_version
├── allocation_epoch
├── branches: name → branch identity, status, head snapshot
├── checkpoints: branch identity → retained descriptors
├── archives: name → checkpoint descriptor
├── pins: owner identity → protected snapshot references
├── operations: operation identity → inputs, output prefix, phase
└── collection: optional run identity, cutoff, captured roots
```

Updates use compare-and-swap (CAS) against the storage generation.
Branch publication, checkpoint expiry, pin changes, and collection admission use that same CAS boundary.
Readers cache immutable snapshots, so individual queries do not modify this record.

The baseline keeps these collections inline.
This adds contention and metadata rewrite cost to publication.
Google Cloud Storage documents one write per second to the same object name.
See [object quotas](https://docs.cloud.google.com/storage/quotas?hl=en).
Operation registration, publication, and pin changes all consume that shared budget.
This limit cannot be dismissed as a future optimization concern.
Before implementation, choose a publication cadence compatible with it or replace the shared record with an equivalent coordination protocol.
Independent branch heads require a new proof for reference transfers and collection boundaries.
Segment manifests remain separate immutable objects rather than document maps inside this record.

The existing branch catalog becomes a compatibility projection during migration.
It cannot independently authorize a read, publication, or deletion after the transition.
There must never be two writable ownership authorities.

## Publication and active pins

An operation registers its identity, input snapshots, and output allocation before reading inputs or uploading outputs.
Registration verifies that every input is still reachable from an existing protected reference.
An object key supplied from a stale cache is not sufficient authority.

Every producer receives a unique operation identifier and the current allocation epoch.
Its output objects live under that explicit allocation:

```text
ns/<namespace>/
├── state.json
├── objects/<epoch>/<operation>/
│   ├── snapshot
│   └── segments/<segment>/...
└── wal/...
```

This changes physical keys from globally reusable content hashes to unique allocation paths.
Content hashes remain useful for integrity.
Forks still share files by explicit reference.
Cross-operation content deduplication is not part of the initial implementation.

An upload can retry within its active operation using absent-only writes.
A completed or cancelled allocation can never publish again.
A new operation cannot adopt an unreferenced old object merely because its content hash matches.

Publication verifies that the operation still exists and its expected branch head remains current.
One namespace CAS installs the new reference and releases the operation's input pins.
A competing head change requires a new publication decision.
The producer cannot overwrite the concurrent state with its stale manifest.

If a response is lost, the operation identifier distinguishes committed publication from an unfinished attempt.
Delayed uploads from a cancelled producer can create garbage, but cannot create new live references.

### Reader pins

A query node registers a durable pin before loading a snapshot or issuing column range reads.
It retains that pin while its cached view or any active query can access the snapshot.
A view replacement acquires the new pin before releasing the old one.
Local reference counts determine when the final query releases the old view.

Search and document retrieval acquire the same local view.
No query performs a namespace CAS merely to read an already pinned view.

The first implementation uses durable pins without automatic time expiry.
A crashed process can retain storage until its pin is reclaimed.
An owner identity includes a process incarnation, so a restart does not inherit stale authority.
An orphan reader pin can be removed only after that process incarnation is confirmed terminated.
A heartbeat timeout alone does not prove termination.

This trades automatic cleanup of crashed readers for a simple safety contract and a cheap read path.
Lease expiry requires a separate deadline and stale-reader protocol before it can replace durable pins.
Administrative pin removal from a live or partitioned reader is unsupported.

Producer cancellation differs from reader-pin reclamation.
The namespace CAS can fence a producer's publication token even if its process remains paused.
Such a producer must discard its output after token loss and cannot return a successful publication.

## Historical branch creation

Historical forks are durable operations because reconstruction can outlive a client connection.
The operation has a caller retry identifier and a fixed source selection.
The target remains unavailable for reads and writes until publication completes.

```text
resolve source and sequence
          │
          ▼
CAS reserve target + pin checkpoint + register operation
          │
          ▼
append FORK(source branch, source sequence, target ID, operation ID)
          │
          ▼
restore checkpoint + replay through source sequence
          │
          ▼
upload complete initial child snapshot
          │
          ▼
CAS publish child + initial checkpoint + release operation pins
```

Admission verifies the advertised history floor and pins the recovery checkpoint in the same namespace update.
Replay applies the source branch's records in `(checkpoint_seq, source_seq]`.
Inherited data is already present in the checkpoint.
It does not replay all later mutations from the parent lineage.

The fork event records the resolved source identity and sequence.
Its own WAL sequence becomes the child's `birth_seq`.
The child's initial snapshot is complete through that birth sequence, with contents inherited from `source_seq`.
The child receives its own initial checkpoint and thereafter has independent retention.

A committed fork event is a recovery obligation.
Client cancellation does not roll it back by deleting a manifest.
Recovery resumes the admitted operation after a crash.
An uncertain WAL append is resolved by its operation identifier before another birth sequence is selected.
If retries create duplicate events, the first event defines birth and later duplicates have no effect.

The namespace flusher records the pending fork durably and can continue processing unrelated branches.
It rejects child mutations until the target is ready.
Restart uses a durable WAL consumption position plus pending operation state.
It must not infer complete namespace progress solely from the minimum checkpoint of ready branches.
Advancing the consumption position requires every earlier event to be applied or represented by a durable recovery obligation.

Missing or corrupt WAL data aborts reconstruction without publishing partial state.
Input protection remains until the operation is repaired or explicitly cancelled under a defined lifecycle policy.
No fallback substitutes the current source state for the requested historical state.

## Compaction and document retrieval

Manifest compaction and data compaction have different effects.
Rewriting metadata can reduce its representation without changing document state.
Reducing the number of segment references requires segment compaction in the current flat-list model.
Neither operation itself deletes replaced objects.

A compactor pins a branch snapshot and writes replacement segments in its registered allocation.
The output contains document columns and the document-identifier index for the same rows.
The new manifest publishes both together.
A document lookup resolves identifiers inside the pinned snapshot, never through an independently updated durable pointer map.

The initial compactor materializes a complete branch snapshot at a fixed sequence.
It resolves mutations in order and writes the visible documents into replacement segments.
This requires more work than partial compaction but gives a direct tombstone rule.
After all older visible versions are covered, deleted documents need no tombstone in that complete replacement state.
Partial compaction must preserve a tombstone whenever an older version remains visible outside its inputs.

Concurrent writes can prevent publication against the expected head.
The first implementation retries from a newly pinned head instead of merging an unverified concurrent manifest.
This can starve compaction under sustained publication traffic.
Measure conflicts before adding a protocol that retains a verified suffix of new segments.

Historical checkpoints retain the old physical representation independently.
A new compacted state cannot replace an older checkpoint with a different logical sequence.

```text
Before:
  main ───────▶ A, B
  checkpoint ─▶ A, B
  fork ───────▶ A, B

After main compaction:
  main ───────▶ C
  checkpoint ─▶ A, B
  fork ───────▶ A, B
```

The query loader must compare snapshot identities rather than segment-list lengths.
Its current positional cursor cannot interpret replacement or same-length rewrites.
The first replacement path constructs a complete new table, then swaps the local view atomically.
Columnar readers likewise switch the manifest, column readers, and document lookup state together.

## Collection protocol

Collection uses a captured root set and a closed allocation epoch.
There is one recorded collection run per namespace.
Its namespace CAS is the collection boundary.

1. Read the namespace state and its generation.
2. Capture every branch, retained checkpoint, archive, pin, and operation input.
3. Capture every active producer's output prefix.
4. Increment `allocation_epoch` and record the run with the captured roots in one CAS.
5. Traverse the immutable graph from those roots and mark every referenced object.
6. List objects in epochs through the closed epoch.
7. Exclude marked objects and every captured active producer prefix.
8. Persist the complete deletion plan before the first delete.
9. Delete the planned object generations, then mark the run complete.

New producers allocate in the new epoch and cannot enter this run's candidate set.
Producers admitted before the boundary retain their entire captured output prefix for this run.
Their uploads can therefore finish after the collector captured its roots.
An abandoned output becomes eligible during a later run after its operation is fenced and removed.

New references to old objects must derive from a currently protected snapshot or registered operation.
They therefore descend from a captured root or captured producer allocation.
A stale reader cannot resurrect an object that the collector classified as unreachable.
Pin registration and reference removal compete through the same namespace generation.

```text
register pin wins CAS       → collector captures the pin
remove last root wins CAS  → pin registration must fail or resolve anew
```

This rule is the safety argument for concurrent publication.
Strong object listing alone cannot provide it.
Google Cloud Storage provides strongly consistent reads and listings, but does not make a multi-object root scan atomic.
See [Cloud Storage consistency](https://docs.cloud.google.com/storage/docs/consistency).

Object age is not a substitute for reader protection.
A month-old segment can leave a branch now while a reader still uses the previous snapshot.
A one-hour object-age cutoff would delete that segment immediately.
A time-based alternative needs retirement time, enforced reader and publisher deadlines, and a protocol that prevents late reference publication.
Those lifecycle contracts do not exist in the current engine.
A second root scan alone does not fence a delayed publication after that scan.
Independent scans can also miss a reference transferred between branches during the scan.
Any replacement protocol must address both cases before replacing the captured-root baseline.

The mark phase must finish successfully before any sweep begins.
A missing manifest, corrupt segment descriptor, unknown metadata version, or failed traversal stops the run.
An incomplete listing can delay collection but cannot justify deletion of an unmarked root.

Persist the run descriptor and deletion plan outside the collectible object tree.
Protect them through the namespace run record until all deletes finish.
Each plan entry contains the object key and observed generation.
Add conditional deletion to `objectstore.Store` before enabling sweep.
An unconditional delete is insufficient for delayed retries.
Google Cloud Storage generation preconditions protect deletion from such races.
See [request preconditions](https://docs.cloud.google.com/storage/docs/request-preconditions).

An absent candidate counts as deleted.
A generation mismatch leaves the object for a later run.
A permission or transient storage error preserves the unfinished plan and retries later.
Deletion can resume after a crash without a new root scan.
Partially deleted unreachable segments remain unreachable.

Workers can resume the same immutable plan, but cannot invent new candidates after marking.
A later run can start only after the namespace records completion of the current run.
Duplicate delayed deletes remain conditional on the original generation.
WAL keys, namespace state, and active collection records are never ordinary sweep candidates.

## Capacity and operations

The baseline namespace record serializes metadata publication, not individual document reads or WAL appends.
Its documented object update limit applies even before contention measurements.
Higher write load can also increase CAS conflicts and snapshot publication lag.
Keep checkpoints and collection off the query execution path.
Apply backpressure to background work when uploads or root updates accumulate.

The collector traverses each unique live manifest and segment descriptor once per run.
The initial implementation retains the mark set in memory and streams object listings into the deletion plan.
If a namespace exceeds available mark memory, the run stops before deletion.
An external mark set is a later implementation choice based on measured namespace size.

Track checkpoint lag, oldest supported sequence, replay work, pinned bytes, orphan allocations, and collection backlog.
Report abandoned reader pins separately from retained user archives.
The retention window is a recovery guarantee, not a hard cap on stored bytes.
Branches, archives, stalled operations, and stale pins can retain objects beyond that window.

Namespace deletion cannot bypass these roots.
The initial collector does not purge a namespace tree merely because its catalog entry is soft-deleted.
Complete namespace purge needs a lifecycle decision for its archives, retained history, and readers.

## Implementation sequence

| Stage | Changes | Completion evidence |
| --- | --- | --- |
| Publication decision | Resolve the shared object's write budget or specify an alternative reference protocol | Explicit capacity contract and equivalent race proof |
| Ownership | Add namespace state, immutable snapshots, branch identity, operation registration, and durable pins | Concurrent publication and pin tests |
| Reader transition | Replace positional loading with atomic snapshot replacement | Shorter and same-length replacement tests |
| Recovery | Add source resolution, fork intent recovery, initial checkpoints, and explicit WAL consumption progress | Exact-sequence forks across restart |
| Retention | Add periodic checkpoints, storage publication times, history floors, and archives | Boundary and policy-change tests |
| Segment replacement | Integrate columnar files and document lookup into one snapshot publication | Search and lookup agree before and after compaction |
| Collection | Add epoch capture, graph traversal, durable plans, and conditional deletion | Race and crash tests, then deletion disabled inventory |
| Enable deletion | Compare candidate inventories against protected roots under concurrent workloads | No protected generation appears in a deletion plan |

Likely package boundaries are `manifest` for immutable snapshots and `namespace` for authoritative root updates.
A `checkpoint` package owns retention and recovery selection.
A `gc` package owns collection runs and deletion plans.
`ingest` owns fork operations and WAL recovery integration.
`query` owns local views and their durable pins.
These are implementation responsibilities, not an additional service requirement.

Migration must stop legacy metadata publishers before installing the new namespace authority.
Import current heads as initial snapshots and record the supported history floor.
Existing segment paths remain valid explicit references during migration.
Legacy query processes must terminate before their unregistered views can lose protection.
Only new allocations use the epoch layout.
Legacy objects require a separately enumerated candidate prefix after the transition completes.
Do not enable collection during mixed ownership protocols.

## Verification cases

Tests use the repository's Bazel targets with `--config=race`.
Fault injection must control interleavings rather than rely on sleeps.

- A sequence between checkpoints produces the exact source state, including deletes and repeated writes to one document.
- Every record in a WAL batch appears atomically at its sequence boundary.
- A child born at 300 from parent sequence 120 excludes parent mutations after 120.
- A sequence-only request resolves its batch owner and rejects an ambiguous source.
- A fork request preserves its resolved source across response loss and retry.
- Recovery resumes after every fork admission, WAL append, upload, and publication boundary.
- A pending fork survives flusher restart while unrelated branches continue.
- The checkpoint before the retention cutoff survives expiry of older checkpoints.
- A failed checkpoint retains the previous recovery base.
- A larger retention setting does not advertise already deleted history.
- An admitted recovery remains protected when retention shrinks.
- A named archive preserves a snapshot after automatic retention expires.
- Branch and checkpoint references independently retain shared column files.
- A snapshot replacement changes search and identifier lookup atomically.
- Same-length and shorter manifests replace the old query state correctly.
- A stale compactor cannot overwrite a concurrent branch update.
- Full compaction preserves deletions, and partial inputs cannot discard necessary tombstones.
- A pin acquisition races with checkpoint expiry and collection admission in both orders.
- A producer uploads after epoch closure and its captured prefix remains protected.
- A cancelled producer resumes but cannot publish into the current namespace state.
- A completed allocation cannot be reused through content-hash deduplication.
- A reference transfer between branches remains protected across every collection interleaving.
- A delayed publication request cannot resurrect an object after its input protection ends.
- A collector crashes during marking, plan persistence, deletion, and completion.
- A partial column upload remains invisible and becomes collectible after operation cancellation.
- A missing root object or unknown format stops collection before deletion.
- A delayed delete cannot remove a different object generation.
- A crashed reader retains its files until its terminated incarnation's pin is reclaimed.
- Namespace deletion does not bypass reader or archive protection.

## Open decisions

CONSIDER(ali): Choose between a publication cadence within the shared object's documented write limit and a different coordination protocol. This blocks implementation.

CONSIDER(ali): Confirm that sequence-only selection means the branch that owns that WAL batch, including the child for a fork event.

CONSIDER(ali): Choose initial checkpoint and retention defaults from acceptable historical recovery cost. No numerical defaults are implied here.

CONSIDER(ali): Confirm explicit reclamation of crashed reader pins for the first implementation, or require automatic lease recovery before release.

CONSIDER(ali): Define archive and historical access after branch or namespace deletion before implementing physical namespace purge.

CONSIDER(ali): Define cancellation of an admitted historical fork after its WAL event commits. The initial recovery path completes the durable obligation.
