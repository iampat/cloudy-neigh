# Storage

**Status:** Draft — 2026-08-20 — v2

The code in `internal/cas` and `internal/index` implements the v0 draft of
this note. That code is outdated. Where the code and this note disagree, the
note wins, and the code catches up.

## Problem

The gRPC API note names the storage engine a non-goal. The server still needs a
place to put a document and a way to find it again by identifier.

The shape of the data bounds the design:

- A document blob is less than 10 MiB.
- A key is a UTF-8 byte string, 200 bytes on average, 1 KiB at most.
- A namespace holds up to 100 million documents. The scale section shows
  where the design bends and where it breaks.
- A local disk is an SSD. The design ignores seek-bound media.

Three constraints shape the answer. A write survives a crash, or it does not
appear at all. A batch applies to every document or to none, because the API
promises that a client can always retry. Several writers, in one process or
in several, commit to one store without a lock service.

A fourth constraint comes from the purpose of this build. The storage has to
show its own work. A reader who doubts a claim inspects the objects of a
development store. Readability serves development and debugging, nothing
more.

## Goals

- One store over `gocloud.dev/blob`: memory for tests, local files for
  development, GCS and S3 for production.
- A batch commits with one conditional create. No lock file, no lease, and no
  fsync of our own.
- A write-ahead log (WAL) with checkpoints. Recovery reads one checkpoint and
  replays a bounded tail.
- A branch is one committed record. Branches share all blobs, so a branch is
  copy-on-write from birth.
- A person inspects a development store with `cat` and `shasum`, and a
  production bucket with the provider's listing tools.

## Non-goals

- Local files in production. The local driver serves development and tests.
- Merge between branches. The model leaves room for it.
- A blob over 10 MiB. The large-blob section sketches the extension only.
- Sharding across stores, replication, and an index of any kind.

## Future work

- Merge between branches.
- Collection of unreachable blobs and old WAL records. Mark from the live
  checkpoints. Pin by WAL position and not by wall-clock age, so a slow batch
  cannot lose its blobs to the sweeper.
- A chunk tree for blobs over 10 MiB.
- A read cache for manifest shards, once a namespace outgrows memory.
- Compaction of manifest shards into sorted runs, if a namespace passes
  100 million documents before sharding across stores does.

## Model

The store holds immutable objects in one bucket. Nothing is ever overwritten.

A blob is an immutable sequence of bytes. Its name is the SHA-256 of its
content. Two writes of the same bytes produce one blob.

A manifest is a blob that maps keys to document digests. A namespace that
outgrows one manifest splits into a two-level tree. The root manifest maps
key ranges to shard digests, and a shard maps keys to document digests.

A WAL record is an object at a sequenced name. It holds the operations of one
committed batch: upserts, deletes, and ref updates.

A ref names a branch and holds a manifest digest. The set of refs lives in
the checkpoint, and a ref update is an operation in a WAL record.

A checkpoint is a blob that holds the refs, the manifest tree digests, and
the WAL sequence number it covers. A pointer object at a sequenced name marks
each checkpoint.

```
  wal/…0041 ─┐ ops                 checkpoints/…0040
  wal/…0042 ─┤                          │
             ▼                          ▼
        state in memory  ◄────  checkpoint blob
                                 refs {"main": m1}
                                        │
                              root manifest m1
                               ┌────────┴───────┐
                               ▼                ▼
                           shard blob       shard blob
                               │
                               ▼
                          document blob
```

The current state of a branch is its checkpoint plus the WAL tail after it.
Every object under a name is immutable, so a reader never sees a torn or a
half-applied state.

## The store and the bucket

The store is one concrete type over `*blob.Bucket`. The v1 driver interface
and the hand-written drivers disappear.

```diff
 internal/cas/
-├── disk.go
-├── memory.go
 └── cas.go     # Store over *blob.Bucket
```

`gocloud.dev/blob` is a new dependency, agreed on 2026-08-14. It replaces our
rename, sync, and directory code with drivers that Google runs over memory,
local files, GCS, and S3. `main` wires a bucket in one line per environment.

The store still owns every portable rule, so it exists once over all four
drivers:

- `Put` hashes the bytes and names the blob. A driver that holds the name
  already skips the write.
- `Get` hashes the result and rejects a mismatch. A mismatch is bit rot and
  not a miss.
- The store maps `gcerrors` codes to its own errors. A caller tests
  `errors.Is(err, cas.ErrNotFound)` and never imports a driver package.

Every store method takes a context and passes it to the bucket, because two
of the four drivers cross the network.

Facts this design relies on, checked against the go-cloud source and the
provider documents on 2026-08-14:

- fileblob writes a temporary file and renames it into place on `Close`. A
  reader never sees a partial blob.
- fileblob never calls fsync, on the file or on the directory. A crash can
  lose an acknowledged write. Local files thus serve development only. The
  design needs no fsync code of its own, because GCS and S3 acknowledge only
  durable writes.
- `WriterOptions.IfNotExist` is a server-side atomic create on GCS, S3, and
  memblob. On fileblob it is a stat-then-rename race, safe only for one
  process.
- Compare-and-swap overwrite is not portable. GCS and S3 reach it through
  per-provider `BeforeWrite` hooks, memblob and fileblob not at all. Thus
  the design never overwrites an object.
- GCS throttles writes to one object name to one per second. S3 serves at
  least 3,500 writes per second per prefix. Only same-name overwrite meets
  the GCS limit, and this design has no same-name overwrite.

## The write path

Linearization comes from one primitive: create an object if it is absent.
The WAL sequence is the order of the store.

```
  1. upload every document blob        parallel, no contention
  2. build the WAL record for the batch
  3. stop here if the context is done
  4. create wal/<next> with IfNotExist
  5. won the slot: the batch is committed
     lost the slot: read the winner's record, apply it, go to 4
  6. apply the record to the state in memory
```

A blob upload never contends, because the name is the content. Two writers
that race on step 4 both hold durable blobs, and the loser retries with only
one small object write. A loser waits a random, growing delay before the
retry, so racing processes do not collide in lockstep. A writer batches by
time, count, or byte size before it enters the path, which keeps the slot
rate low.

Step 6 is the linearization point for readers in the process. Step 4 makes
the batch durable before step 6 makes it visible, so a crash never removes a
document that a query returned.

One committer per process takes step 4, and local writers queue behind it.
Concurrent batches in one process thus share the slot race. This is group
commit over the WAL.

A retried batch is harmless. Its blobs deduplicate to the same names, and its
operations are upserts of digests, so a record applied twice yields the same
state.

On S3 a lost conditional create can also surface as a conflict error that
go-cloud reports as `gcerrors.Unknown`, not `FailedPrecondition`. The commit
loop treats both as a lost slot. This is a workaround for the missing mapping
in s3blob.

Every `wal/<seq>` name is new, so the GCS one-write-per-second limit on a
single object name never applies.

## Checkpoints

Every K records, or T seconds, the committer materializes the state. It
writes the dirty manifest shards, the root manifests, and the checkpoint
blob. It then creates a pointer at `checkpoints/<seq>` with `IfNotExist`.
K and T are tuning knobs, not promises, and K bounds recovery.

Recovery lists `checkpoints/`, reads the highest one, and replays the WAL
records after its sequence number. The replay is bounded by K. There is no
torn record to detect, because a record is one immutable object.

Replay fetches in parallel. One listing of `wal/` finds the outstanding
records, parallel reads fetch them, and the replay applies them in sequence
order. Sequential probes would cost K round trips, near 3 s at K = 100. The
parallel path costs a fraction of a second at the same K. As estimates,
K near 200 or T near 15 seconds holds recovery under one second, against a
target of a few seconds.

A checkpoint references only blobs that commits already made durable, so a
checkpoint found at restart always resolves.

## The bucket layout

```
  blobs/<64 hex>        document, manifest, and checkpoint blobs
  wal/<20 digits>       one record per commit, created if absent
  checkpoints/<20 digits>   pointer to a checkpoint blob
```

Keys never appear as object names. Object names are digests and sequence
numbers, so provider limits on object names never constrain keys. fileblob
maps this layout onto a directory tree, and `cat` and `shasum` still answer
a doubt in development.

## Branches

A ref update is an ordinary operation in a WAL record. A branch is born from
one committed record that points a new ref at an existing manifest digest.

The new branch shares every shard and every document blob with its parent.
A write to either branch builds new shards for the touched ranges and leaves
the shared rest in place. Copy-on-write is not a feature. It is what an
immutable tree does.

Merge stays in future work. The model keeps it possible: two refs name two
manifest trees, and a merge is a third tree built from both.

## Keys

A key is a UTF-8 byte string of at most 1 KiB. The cap is policy at the API,
not structure in the store: keys live inside manifest entries, never in
object names.

Two extensions stay open, with their costs:

- A 10 KiB cap multiplies the manifest entry size by up to 40. The scale
  table shifts by one order of magnitude.
- Arbitrary byte sequences cost almost nothing. The canonical manifest
  format stores a key as bytes already, and the JSON rendering escapes a
  key that is not valid UTF-8. Only the API cap moves.

## Scale

Estimates, not measurements. One manifest entry is a key (200 B average), a
digest (32 B), and framing, near 250 B. A shard targets 1 MiB, near 4,000
entries. The in-memory map costs roughly 350 B per document.

| Documents | Manifest data | Resident map | Verdict                        |
| --------- | ------------- | ------------ | ------------------------------ |
| 1 M       | ~250 MiB      | ~350 MiB     | Comfortable, memory or disk.   |
| 10 M      | ~2.5 GiB      | ~3.5 GiB     | Fine on a server. Laptop edge. |
| 100 M     | ~25 GiB       | ~35 GiB      | Map no longer fits everywhere. Shards load lazily behind a cache. Design target's edge. |
| 1 B       | ~250 GiB      | out          | Every lookup can touch the bucket. Needs sorted runs and compaction. Out of scope. |
| 10 B      | ~2.5 TiB      | out          | Partition across stores. Out of scope. |

Checkpoint cost scales with dirty shards, not with the namespace. A batch
that touches D distinct shards rewrites near D MiB at the next checkpoint.
The v0 design rewrote the whole manifest on every write. The table above is
the reason it did not survive the first scale question.

At the 100 M edge the resident set shrinks to three tiers. The root
manifest stays resident, under 3 MiB. A Bloom filter per shard costs near
125 MiB at 10 bits per document. The filters absorb almost every lookup of
an absent key. Shards load on demand into a cache under a fixed memory budget.
As estimates: a cache hit answers from memory, and a miss pays one round
trip. A 90 percent hit rate holds the mean read under 5 ms.

A uniform-random key workload dirties almost one shard per updated document.
Two levers tame that checkpoint cost. One is a smaller shard target, 128
to 256 KiB. The other is delta layers, which hold several records of
changes before the shards materialize. Hierarchical keys need neither lever.

CONSIDER(ali): the shard split policy is open. A split at the median key
balances the tree. A fixed prefix of the key hash routes without a root
lookup. The checkpoint pass can do either.

memblob holds blobs, WAL, and checkpoints in process memory. It serves tests
and small corpora, bounded by the process.

## Throughput

Estimates, not measurements. The WAL sequence is one serialization point,
so the commit rate is one over the slot-create round trip:

```
  memblob    ~1 µs      ║  ~10^6 commits/s, CPU-bound
  fileblob   ~1 ms      ║  ~10^3 commits/s, no fsync in the path
  GCS / S3   30–100 ms  ║  ~10–30 commits/s
```

Documents per second is the commit rate times the batch size. Thirty commits
per second with 1,000-document batches is 30,000 documents per second. Blob
uploads bind first past that: S3 serves ~3,500 creates per second per prefix.
Large documents move the bound to bandwidth.

A workload past one WAL sequence needs several sequences, which is sharding
across stores, a non-goal.

## What survives a crash

GCS and S3 acknowledge an object create only after it is durable. The order
of the write path carries the rest.

| Crash point | State after restart |
| --- | --- |
| During step 1 | Unreachable blobs. The batch is absent. |
| Before the slot create returns | The batch is absent, or committed whole. |
| After step 4 | The batch is committed. Recovery replays it. |
| During a checkpoint | The previous checkpoint stands. Replay covers the gap. |

A WAL record references only blobs uploaded before it. A checkpoint
references only committed state. An object found at restart thus always
resolves, and nothing needs a repair pass.

On fileblob none of this holds after a machine crash, because nothing syncs.
That is the accepted cost of a development driver.

## Canonical encoding

A digest is always the hash of the canonical binary encoding. The store can
also render a manifest as JSON, as configuration, for a debugging eye. A
rendering is never hashed, so the mode does not change a digest.

The storage-owned messages — manifest shards, WAL records, and checkpoints —
obey four rules, and their bytes are then canonical by construction:

- No protobuf map field in a hashed message. A map order belongs to the
  library, not to the format.
- Entries are a repeated field, sorted by the key bytes before the marshal.
  The sort order is part of the format.
- Field tags stay ascending, and a hashed message drops unknown fields.
- One shared marshal configuration: deterministic, no partial messages.

The document blob is different, because the API owns its shape. A document
holds a protobuf map of attributes, and protobuf does not order map entries.
The same document would otherwise produce a new digest on every write.

The write path marshals it with the deterministic option, which sorts map
keys. Protobuf promises that order within one build and not across library
versions.

A toolchain upgrade can thus give a stored document a new digest. That costs
deduplication and not correctness. The manifest records the digest that a write
produced. Nothing in this design reads two equal digests as proof that two
documents are the same.

## Concurrency

Several processes on one bucket is the normal production case on GCS and S3,
not an exception. The conditional create on `wal/<seq>` is the only arbiter,
in one process and across processes. There is no lock file, no lease, and no
store mutex. A process that loses a slot reads the winner's record and
retries. Processes on one bucket thus interleave instead of diverging, which
retires the v0 flock plan.

In one process, readers load the current state from an atomic pointer that
the committer swaps at step 6. A reader never blocks on a writer's network
round trip.

A development store admits one process, because fileblob has no atomic
create. In that process, one lock serializes the commit path, over memblob
and fileblob alike. Tests that need real contention run on memblob, which
arbitrates correctly.

Every read from the bucket verifies the digest. Hardware SHA-256 runs near
2 GiB/s, so a 10 MiB document costs ~5 ms of CPU under a network read that
costs tens of milliseconds. A cache added later holds verified bytes, and a
cache hit skips the hash. A background scrubber would find the same bit rot
late instead of at the read.

## Consistency

Writes are linearizable. The WAL admits one record per sequence number, so
every commit takes one position in one total order. A batch is one record,
so a batch is atomic at its position.

A read serves a snapshot: the state at the process's applied sequence
number. A commit applies to the local state before the call returns. The
snapshot model gives three guarantees and one gap:

- Read-your-writes, in the process that wrote.
- Monotonic reads. The applied position never moves backward.
- Atomic batches. A snapshot sits between records, never inside one.
- The gap: a snapshot can trail a commit from another process until the
  next tail advance.

A process advances with a tail probe: read `wal/<applied+1>` by exact name,
apply it, and repeat until the name is absent. One probe is one round trip.
A process that trails by many records catches up as recovery does: one
listing, parallel reads, and an ordered apply.
When to probe is the freshness knob, and it prices the gap:

| Probe        | Guarantee                          | Cost                    |
| ------------ | ---------------------------------- | ----------------------- |
| On a cadence | Staleness bounded by the cadence   | One probe per interval  |
| Every read   | Linearizable reads                 | ~30–100 ms on every read |

The default is a cadence. A search index tolerates bounded staleness, and a
caller that needs cross-process read-your-writes pays the probe on its own
reads.

Provider facts, checked against the consistency documents on 2026-08-14. S3
gives strong read-after-write consistency, and a new object appears in a
listing at once. GCS gives strong global consistency for reads and for
listing. The GCS exception is a publicly cached object, and this store sets
no public cache control. The portable go-cloud `List` contract promises only
eventual consistency, weaker than either provider. Correctness thus never rests on a listing. The tail probe reads exact
names. The startup listing that finds the latest checkpoint can only
lengthen a replay, never corrupt one.

## Large blobs

Everything to 10 MiB moves as one request on every driver. The door to
1 GiB and 10 GiB stays open but is not designed here:

- Upload mechanics survive as is. The go-cloud writers switch to multipart
  on S3 and resumable upload on GCS on their own.
- `Put([]byte)` does not survive. The digest names the object, and the
  digest is unknown until the last byte. A large blob thus needs a streamed
  hash with a staged upload, or a chunk tree. In a chunk tree the document
  blob becomes a list of chunk digests, each chunk under 10 MiB. The chunk
  tree also caps memory, uploads in parallel, and deduplicates between
  versions of one document.

We recommend the chunk tree when the need arrives.
