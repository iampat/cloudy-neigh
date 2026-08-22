# Implementation plan: objectstore on gocloud.dev

## Status

Draft. 2026-08-21. Evidence comes from the probe programs in this directory.
Reviewed by agy. Transcript:
[docs/reviews/2026-08-21-objectstore-implementation-plan.md](../../../docs/reviews/2026-08-21-objectstore-implementation-plan.md).

## Problem

Implement the `objectstore.Store` interface from
[storage.md, Section 8.1](../../../docs/design/storage.md) with two local
backends: `memblob` and `fileblob` from `gocloud.dev/blob` v0.46.0.

The hard part is the conditional writes. The portable gocloud API has exactly
one precondition, `WriterOptions.IfNotExist`. No portable option expresses
`PutIfGenerationMatch`, and no driver-independent generation token exists.
The local drivers expose no useful escape hatch either. `memblob` converts
nothing through `As`. `fileblob` converts only `**os.File` in `BeforeWrite`,
which is the temporary file before the rename.

Three probe programs measured the drivers (`probe-memblob/`, `probe-fileblob/`,
`probe-wrapper/`). The findings that shape the design:

- `memblob` implements `IfNotExist` under its bucket mutex. It is race-free.
  32 concurrent writers to a fresh key produced exactly 1 winner.
- `fileblob` implements `IfNotExist` as stat-then-rename. It is racy. The same
  stress produced one winner in only 103 of 200 rounds, with up to 15 winners
  in one round.
- A failed `fileblob` `IfNotExist` write still overwrites the existing key's
  `.attrs` sidecar. Metadata stored there is not safe against losers.
- `fileblob` writes the sidecar before it renames the data file, and it never
  calls fsync. A crash can pair new metadata with old bytes, or lose an
  acknowledged write.
- Both drivers derive `ETag` from the wall clock or the file mtime, not from a
  counter. Neither `ETag` can back a generation.
- The prototype wrapper (`probe-wrapper/condstore/`) passed every conditional
  test on both drivers. `PutIfAbsent` had exactly one winner across 32
  goroutines and across 4 processes. The compare-and-swap counters were exact:
  400 of 400 in-process, and 100 of 100 cross-process at ~0.6 ms per locked
  write.

## Goals

- Three backends behind one interface: memory (`memblob`), local disk
  (`fileblob`), and Google Cloud Storage (GCS, `gcsblob`).
- Conditional-write semantics that match GCS. A generation changes on every
  successful write, identical bytes included. A generation is never reused
  after a delete.
- One contract test suite that all three backends pass.
- An explicit failure on an S3 path. Nothing silently degrades to weaker
  semantics.

## Non-goals

- Durability of the disk backend against power loss. `fileblob` never calls
  fsync, and the wrapper cannot add it from outside. The disk backend serves
  tests and local development. Production traffic goes to GCS.
- Multi-machine coordination for the local backends. The disk lock is a
  same-machine lock.
- An Amazon S3 backend. `Open` rejects an `s3://` URL with an explicit
  error. Section "Cloud backends" records the assumptions a future S3
  backend inherits.
- Hardening beyond normal operation. The local backends never see production.
  When a detail costs simplicity, the simple form wins.

## Future work

- A native disk store with its own layout and fsync, if a durable local
  backend becomes a requirement. The contract suite would make the swap safe.
- An S3 backend, under the assumptions in "Cloud backends".

## Model

- **Wrapper**: the one `Store` implementation over `*blob.Bucket`. All
  correctness lives here, not in the drivers.
- **Generation**: an opaque `string` token, compared only for equality. A
  caller receives it from a read or a write and passes it back unmodified.
  The local wrapper mints it from a counter and stores it in the blob
  metadata under one reserved key. GCS renders its `int64` generation as
  decimal. S3 uses the `ETag`. This amends the `int64` in storage.md,
  Section 8.1, which must change with the implementation.
- **Store lock**: one exclusive lock per store. Every mutation and every
  `GetWithGeneration` holds it. The memory backend uses a `sync.Mutex`. The
  disk backend adds `flock(2)` for cross-process exclusion.
- **Contract suite**: the shared test functions that every backend must pass.

## Design

### Two implementations, three backends

The local backends share one locked wrapper. The GCS backend is its own
implementation with no lock, because the server evaluates the preconditions.

```text
objectstore/
├── objectstore.go   # Store interface, Object, ErrNotFound, ErrPreconditionFailed
├── open.go          # Open(ctx, url): mem://, file://, gs:// — everything else fails
├── condstore.go     # the locked wrapper over *blob.Bucket (memory, disk)
├── locker.go        # mutexLocker, flockLocker
├── mem.go           # OpenMem() -> memblob bucket + mutexLocker
├── disk.go          # OpenDisk(dir) -> fileblob bucket + flockLocker
└── gcs.go           # OpenGCS(bucket) -> precondition-based Store, no lock
```

`Open` parses the scheme and dispatches to the three constructors. Any other
scheme, `s3://` included, returns an explicit unsupported-scheme error. The
gocloud URL muxer is not used: it would accept `s3://` and hand back a bucket
with weaker semantics than the interface promises.

The alternative was two native implementations with zero dependencies: a map
behind a mutex, and a hand-rolled file layout. That buys full control of the
disk format and fsync. The cost is the same conditional-write logic written
twice, and a second on-disk format to maintain. The wrapper keeps one code
path, and the gocloud dependency arrives anyway with the GCS backend. The
probes showed the wrapper approach is sufficient for the local backends' job.

The cost of the wrapper: correctness depends on lock discipline. A writer that
reaches the bucket without the wrapper breaks both the generation metadata and
`PutIfAbsent`. The package keeps the bucket private, so only a process that
opens the same directory out-of-band can bypass the lock.

`condstore.go` does not set `IfNotExist` on any write. The lock already
serializes the exists-check and the write, and one code path serves both
backends. The review preferred a per-backend flag that keeps `memblob`'s
native check, so the driver error translation runs in unit tests. We declined
the flag. The backends are test-only, and the GCS backend brings its own
tests.

`Delete` holds the store lock like every other mutation. On `fileblob` a
delete is two unlinks, data file and sidecar, and the lock keeps readers from
the window between them.

### Generation scheme

Under the store lock, a write computes:

```text
next = max(current + 1, time.Now().UnixNano())    // current = 0 if absent
```

and stores `next`, rendered as decimal, as metadata on the written blob.
`current + 1` makes the generation change on every write to a live key,
identical bytes included. The
wall-clock term keeps the generation increasing across delete and re-create,
so an old token cannot match a re-created key (the ABA case the prototype
exposed). GCS generations rely on the same clock assumption. A clock that
steps backward degrades only the delete case, never a live key.

The scheme needs no persistent counter file. The disk backend keeps exactly
two files per key, the data file and the `fileblob` sidecar, plus one lock
file per store.

The disk backend assumes a monotonic wall clock across the processes that
share one bucket directory. With skewed clocks, a delete and re-create can
mint a generation below an old token. The plan accepts the case. The
prototype's `PutIfGenerationMatch` rejects an absent key even for generation
0 and hardcodes generation 1 in `PutIfAbsent`. The real implementation must
not copy either.

`Put`, `PutIfAbsent`, and `PutIfGenerationMatch` all return the new
generation. `PutIfGenerationMatch` rejects an empty token as invalid input.
`PutIfAbsent` is the one way to write a key that must be absent. A
conditional write that fails returns an empty generation with the error,
because the GCS 412 response does not carry the live generation either.

### The memory backend

`OpenMem` wraps `memblob.OpenBucket` with a `mutexLocker`. Points the probes
settled:

- Metadata round-trips through `memblob` attributes, so the generation lives
  where the disk and GCS backends keep it.
- A reader opened before an overwrite keeps the old bytes, because a write
  installs a fresh entry. `GetWithGeneration` can return the reader and
  release the lock without a copy.
- `memblob`'s race-free `IfNotExist` goes unused. The lock covers it, and
  using it only here would fork the code path from the disk backend.

### The disk backend

`OpenDisk(dir)` wraps `fileblob.OpenBucket(dir, ...)` with a `flockLocker`.

`fileblob` keeps two files per key. `probe-attrs/` shows the pair:

```text
docs/a.txt          hello world
docs/a.txt.attrs    {"user.content_type": "text/plain",
                     "user.metadata": {"generation": "42"},
                     "md5": "XrY7u+Ae7tCTyyK7j1rNww==", ...}
```

The sidecar is the anchor of the conditional writes. The generation lives in
`user.metadata`, so one `Attributes` call under the lock reads the live
generation from one small JSON file. The sidecar has no `ETag` and no mtime.
`fileblob` computes `ETag` from `os.Stat` at read time, which is why the
metadata must carry the generation.

- The lock file lives beside the bucket directory (`<dir>.lock`), never inside
  it. A file inside the bucket would surface as a key in `List`.
- `flock` is advisory and per open file description. Two goroutines that share
  the descriptor both "hold" it. For that reason `flockLocker` pairs the
  flock with an in-process `sync.Mutex`. The prototype validated this pairing across 4
  processes.
- `Locker.Lock` does not observe a context, so every method checks `ctx.Err()`
  before it takes the lock. The measured hold time is ~0.6 ms per locked
  read-modify-write on APFS, so a blocked caller waits milliseconds, not
  seconds. The gocloud calls inside the critical section do observe the
  context.
- `Close` releases the descriptor but never unlinks the lock file. An unlink
  lets a new opener lock a fresh inode while an old holder keeps the removed
  one.

Two `fileblob` defects stay documented rather than fixed, because the wrapper
cannot reach them. The sidecar lands before the data rename, and nothing is
fsynced. Both only matter on a crash, which the Non-goals section scopes out
for this backend.

### The GCS backend

`gcs.go` implements `Store` directly on a `gcsblob` bucket. No lock exists.
The server evaluates every precondition. The mechanics are verified in the
v0.46.0 driver source and against a live GCS bucket (`probe-gcs/`, 9 of 9
checks passed):

- Conditional write: `BeforeWrite` converts `**storage.ObjectHandle`. The
  callback replaces the handle with
  `handle.If(storage.Conditions{GenerationMatch: g})` before the driver
  materializes the `*storage.Writer`. The token parses back to the `int64`
  the condition needs. `DoesNotExist: true` covers `PutIfAbsent`, and the
  portable `IfNotExist` already does exactly that.
- `GetWithGeneration`: `Reader.As` converts `*storage.Reader`, whose
  `Attrs.Generation` arrives with the same GET. One round-trip, atomic.
- Every driver maps a precondition conflict to
  `gcerrors.FailedPrecondition`, so the error mapping below already fits.
- The live run confirmed the contract. An 8-way `PutIfAbsent` race had one
  winner. An identical-bytes rewrite changed the generation. A stale
  generation got a 412. A 4x5 compare-and-swap counter landed on exactly 20,
  with 43 retries.

The opaque token also lets S3 in, with these assumptions:

- The token is the `ETag`. `PutIfGenerationMatch` sends `If-Match`, and
  `PutIfAbsent` sends `If-None-Match: *`. Both reach S3 through `BeforeWrite`
  on `**transfermanager.UploadObjectInput` in v0.46.0.
- A single-part `ETag` is the content MD5. A rewrite of identical bytes, or a
  key that returns to earlier content, revives old tokens. The design
  tolerates it: a manifest hash that returns to an earlier value IS that
  earlier state, so a stale match writes nothing wrong.
- Keys under conditional writes stay below the multipart threshold. Branch
  refs hold one manifest id. Multipart would change the `ETag` shape, not
  break equality.
- The v0.46.0 driver maps the S3 409 `ConditionalRequestConflict` to
  `gcerrors.Unknown`. An S3 backend must treat that 409 as a precondition
  failure and retry.

An S3 backend stays future work. These assumptions only keep the interface
from excluding it. Until it exists, an S3 path fails at `Open`.

### The per-object write rate

GCS caps mutations of one object. A benchmark from a us-west1 VM against a
us-west1 bucket sustained 2.7 mutations per second on one key. A faster loop
returned HTTP 429 `rateLimitExceeded`, which the driver maps to
`gcerrors.ResourceExhausted`.

The limit binds `refs/heads/<branch>`, the one mutable object per branch. It
does not bind the blob, manifest, or log writes, because each of those
writes a new key. The measurement replaces the 10 to 20 writes per second
estimate in storage.md, Appendix B.

`Store` does not retry a 429. The caller sees `ResourceExhausted` through the
raw error, and the commit loop above it owns the backoff.

CONSIDER(ali): the contract has no sentinel for a rate limit. A caller that
needs to distinguish throttling from a precondition failure needs one.

### Errors

The wrapper translates once, at its boundary, with a switch on
`gcerrors.Code`:

| gcerrors code        | Sentinel                |
| -------------------- | ----------------------- |
| `NotFound`           | `ErrNotFound`           |
| `FailedPrecondition` | `ErrPreconditionFailed` |

Sentinels wrap with `%w` and include the key. `PutIfGenerationMatch` on an
absent key returns `ErrPreconditionFailed`, which matches the GCS 412 for the
same request. `Delete` on an absent key returns `ErrNotFound`. A local
generation metadata value that fails to parse is corruption and surfaces as its own
error, never as a precondition failure.

### List

`List` iterates the portable `blob.List` with the prefix, skips keys at or
before `startAfter`, and stops at `limit`. The portable API has no
`startAfter`, and the page-token shortcut ("token is the last key") is a
driver detail that the next driver can break. The cost is a scan of the
prefix. The design accepts it because the steady-state loops issue zero list
calls (storage.md, Section 8.1).

A `List` result guarantees `Key` and `Size` only. `Generation` is filled when
the driver's listing supplies it, which GCS does and the local drivers do not,
so the local backends return 0. The alternative was one `Attributes` call per
listed key under the store lock. At 10-30 microseconds per stat, a
1,000-item listing would block every writer for tens of milliseconds. No
current caller reads `Generation` from a listing: `Tail` parses the sequence
number from the key.

### Tests

One contract suite, external package `objectstore_test`, run against all
three backends. The memory and disk runs are hermetic. The GCS run needs a
real bucket. An environment variable names the bucket, and the run skips
when the variable is empty.

Happy paths:

- Put, Get round-trip. `GetWithGeneration` returns matched bytes and token.
- The generation changes on every write, identical bytes included.
- `PutIfAbsent` on a fresh key succeeds and returns its generation.
- `List` honors prefix, `startAfter`, `limit`, and lexicographic order.

Failure and concurrency paths:

- 32 goroutines race `PutIfAbsent` on one key: exactly 1 success, 31
  `ErrPreconditionFailed`.
- 8 goroutines run 50 compare-and-swap increments each with retry: the final
  value is exactly 400.
- A stale generation and an unconditional `Put` in between: the next
  `PutIfGenerationMatch` fails.
- Delete then re-create: no old token matches the new key.
- Absent keys: `Get`, `GetWithGeneration`, `Delete`, and
  `PutIfGenerationMatch` return the documented sentinels.
- `PutIfGenerationMatch` rejects an empty token.
- A failed conditional write returns an empty generation with the error.
- A canceled context returns before any mutation.
- Disk only, via re-exec of the test binary: 4 processes race `PutIfAbsent`,
  and exactly one wins. The prototype already proved the cross-process
  compare-and-swap loop, and the suite does not repeat it.
- Disk only: the lock file never appears in `List`, and metadata survives a
  close and reopen of the store.
- `Open` with an `s3://` URL, or any unknown scheme, fails with the
  unsupported-scheme error.

The suite runs under `--config=race`.

### Dependency

`gocloud.dev` v0.46.0 enters the main module. Its tree pulls
`go.opentelemetry.io/otel` and friends. The repository rule requires
agreement on a new dependency before the first production import.

## Open questions

None. The generation type question is resolved: the token is an opaque
string, compared only for equality, and every `Put*` returns the new token.
storage.md, Section 8.1 changes to match when the implementation lands.
