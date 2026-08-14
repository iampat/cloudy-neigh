# Storage

**Status:** Draft — 2026-08-14 — v1

The code in `internal/cas` and `internal/index` implements the v0 draft of
this note. That code is outdated. Where the code and this note disagree, the
note wins, and the code catches up.

## Problem

The gRPC API note names the storage engine a non-goal. The server still needs a
place to put a document and a way to find it again by identifier.

Two constraints shape the answer. A write survives a crash, or it does not
appear at all. A batch applies to every document or to none, because the API
promises that a client can always retry.

A third constraint comes from the purpose of this build. The storage has to show
its own work. A reader who doubts the durability claim inspects the files.

## Goals

- One store over one driver interface, with a memory driver and a disk driver.
- A driver moves named bytes and nothing more. The store owns every rule.
- A batch applies whole or not at all.
- Recovery reads one file. No log replay.
- A person reads the on-disk state with `cat` and `shasum`.

## Non-goals

- Sharding and replication.
- An index of any kind. This layer answers a lookup by identifier and no more.
- Access to one directory from two processes.

## Future work

- An object-store driver, for example GCS or S3. A remote call can hang, so
  that driver reads a context. Its arrival puts a context parameter on every
  driver method. The change stays cheap while every driver lives in this
  repository.
- Collection of unreachable blobs. Mark from the root, then sweep the blob
  directory. The sweep spares a blob younger than a grace period, because a
  running batch owns blobs that no root names yet.
- A lock file under `flock`, so a second process fails to open the directory.
  `flock` holds on the local file systems this store targets.
- An incremental manifest, so that a write does not rewrite a whole namespace.
- Group commit, so that concurrent writers share one flush.
- A streaming path for a document too large to hold in memory. It arrives as
  an optional driver interface. The store probes for it with a type assertion
  and falls back to the byte-slice path.

## Model

The store holds two kinds of thing.

A blob is an immutable sequence of bytes. Its name is the SHA-256 of its
content. Two writes of the same bytes produce one blob.

The root is the one cell that changes. It holds the digest of the current
manifest.

A manifest is a blob. It maps a namespace and a document identifier to the
digest of that document.

```
  root ──► manifest blob
             {"repo": {"cas/disk.go": "e4cbad1c…"}}
                                          │
                                          ▼
                                    document blob
```

A content-addressed store names bytes. It cannot answer which document a
namespace holds under an identifier, because that answer changes. The root
carries the change, so an update to the whole store is one swap of one pointer.

The cost is that every write rewrites the manifest. The section on manifest cost
gives the measurement.

## The store and the driver

One concrete store sits above one driver interface. A caller sees the store
and never a driver.

```
  index layer
       │
       ▼
  cas.Store           concrete — hash, check, wrap
       │
       ▼
  driver.Driver       interface — move named bytes
       │
   ┌───┴────┬───────────────┐
   ▼        ▼               ▼
 memory    disk      object store (future)
```

The driver interface moves into its own package. A caller that imports
`cas/driver` is visible in review, so the raw interface stays out of
application code.

```diff
 internal/cas/
+├── driver/
+│   └── driver.go   # Driver, Digest, ErrNotFound
 ├── cas.go          # Store
 ├── disk.go
 └── memory.go
```

```go
package driver

type Driver interface {
	WriteBlob(d Digest, data []byte) error
	ReadBlob(d Digest) ([]byte, error)
	SetRoot(d Digest) error
	Root() (Digest, bool, error)
}
```

The store is concrete, so every portable rule exists once:

- `Put` hashes the bytes and names the blob.
- `Get` hashes the result and rejects a mismatch. The name of a blob is the
  hash of its content, so a mismatch is bit rot and not a miss.
- The store rejects a malformed digest before any driver call.
- The store wraps a driver error with the operation and the digest.

The driver receives that list as a guarantee, mirrored. A digest that reaches
a driver is valid. Bytes that reach `WriteBlob` hash to their digest. A driver
thus holds no defensive code, and a new driver is four byte operations.
gocloud.dev runs this exact shape over memory, disk, GCS, and S3, and its
drivers stay small for the same reason.

The cost is one hash per read under every driver. The memory driver pays it
for a bit rot that a map cannot suffer.

The memory driver holds a map and a variable. The disk driver holds files.
Nothing above the store branches on which one it has.

A blob is immutable, so two writes of one name carry the same bytes. A driver
can skip a write of a name it already holds.

The store is a struct and not an interface. One implementation exists, and an
interface with one implementation selects nothing. A test reaches every store
behaviour through the memory driver. The interface returns when a second store
shape appears, for example a cache in front of a slower driver.

`main` wires a driver into the store in one line. A registry that selects a
driver from a URL scheme earns its place at many drivers, and this store has
two.

`SetRoot` requires the caller to serialize its calls. Two concurrent calls race,
and neither the store nor a driver orders them. The index layer holds one mutex,
which supplies that order.

`Put` takes a byte slice and not a reader. The store hashes a whole blob before
it can name it, and every caller already holds the bytes. The cost is that a
document too large for memory has no path through the store.

No method takes a context. Both drivers are local, and neither `os.Rename` nor
`File.Sync` observes a context. A parameter that no implementation reads claims
that cancellation works. The object-store driver in future work does read one,
and its arrival puts a context on every method.

The index layer above does take a context, because it has work to abandon. It
checks the context once per document, and again after it holds the commit lock.
A batch of ten thousand documents thus stops when its caller goes away.

## Errors

A driver returns its own error, and marks a missing name with
`driver.ErrNotFound`. The disk driver translates `fs.ErrNotExist` into the
sentinel. The memory driver returns the sentinel itself.

The store wraps every driver error with the operation and the digest, and
keeps the chain with `%w`. A caller tests with
`errors.Is(err, cas.ErrNotFound)` and never imports the driver package.
`cas.ErrNotFound` is `driver.ErrNotFound` under a portable name.

One sentinel is enough for two local drivers. A remote driver returns status
codes, and its arrival grows the sentinel into a small code enum. The chain
keeps the raw error either way, so `errors.As` still reaches an
`*os.PathError` when a test needs one.

## The conformance suite

The driver contract lives in one test suite. The suite drives each driver
through the store's public API, so it tests the behaviour a caller sees and
not the driver's private shape.

```go
func TestMemoryDriver(t *testing.T) { drivertest.Run(t, newMemory) }
func TestDiskDriver(t *testing.T)   { drivertest.Run(t, newDisk) }
```

A new driver passes the suite or it is wrong, and the contract never forks
into per-driver copies. The memory driver doubles as the oracle. It has no
rename and no sync, so a behaviour that differs between the two drivers points
at the disk driver's own code.

## The disk layout

```
  <dir>/blobs/<64 hex characters>   one file per blob
  <dir>/tmp/blob-*                  cleared at open
  <dir>/root                        64 hex characters
```

The blob directory is flat. A flat directory keeps the write path free of
directory creation, which removes the question of whether a new directory entry
is durable. The cost is that a large namespace makes the directory slow to scan
on some file systems. A fanout of the first hex characters is the fix, and the
migration to it is one idempotent move of immutable files. The flat start does
not trap the layout.

A crash leaves a temporary file behind. Nothing can reach that file, so the open
path deletes the contents of `tmp`.

## The write path

```
  1. marshal and store every document blob      no lock held
  2. take the lock
  3. stop here if the context is done
  4. copy the manifest, and point each identifier at its digest
  5. store the new manifest blob
  6. set the root
  7. replace the manifest in memory
  8. release the lock
```

Step 3 is the last point at which a cancelled batch costs nothing. It leaves
unreachable document blobs, and the namespace exactly as it was.

Steps 5 and 6 write to the disk under the lock. The lock exists to serialize the
read-modify-write of the manifest, and the bulk of the work at step 1 stays
outside it.

A published manifest never changes. An upsert builds a new manifest and replaces
a pointer, so a lookup holds the lock only for one map read.

The linearization point is step 7. Step 6 makes a batch durable before step 7
makes it visible. A crash thus never removes a document that a query already
returned.

## What survives a crash

Every file arrives through a rename into its final name. A rename within one
file system is atomic, so a reader sees the old file or the new one. A sync of
the containing directory follows every rename. The file sync alone leaves the
directory entry in the page cache, and a crash can then lose a durable file.

| Crash point | State after restart |
| --- | --- |
| Step 1 | The old root. The batch is absent. |
| Between steps 1 and 6 | The old root. The batch is absent. |
| During the root rename | The old root or the new root. |
| After step 6 | The new root. The batch is complete. |

The order of the three durable writes carries the guarantee. A document blob
reaches the disk before the manifest that names it. The manifest reaches the
disk before the root that names it. A root found at restart thus always resolves.

Recovery reads `root` and then one blob. There is no log to replay and no torn
record to detect.

An upsert is idempotent, because the same bytes give the same digest. A repeated
batch produces the same manifest and the same root.

A failed batch leaves its document blobs on the disk. Nothing points at them,
so they are unreachable rather than wrong.

## The cost of one manifest

A write rewrites a whole manifest, so the cost of a write grows with the size of
the namespace.

The benchmarks in `internal/cas` and `internal/index` run on an Apple M-series
laptop. `Put` stores one blob of 4 KiB. `Upsert` writes one document into a
namespace of 1000 documents, and holds the namespace at that size.

```
  Put,    memory  ║                            0.003 ms
  Put,    disk    ║░░░░░░░░░                  12.5   ms
  Upsert, memory  ║                            0.35  ms
  Upsert, disk    ║░░░░░░░░░░░░░░░░░░░░░░░░   31.9   ms
```

The disk figures are the cost of `fsync`. An upsert costs three durable writes:
the document blob, the manifest blob, and the root. The memory figure is the
manifest rewrite alone, and it is the number that grows with the namespace.

Each benchmark writes new bytes on every iteration. A repeated document takes
the already-stored path in `Put`, which measures a hash and a lookup instead of
a write.

Every write also leaves the manifest it replaced behind as garbage. The blob
count thus grows with the write count and not with the document count, and the
flat directory fills at that rate. Collection is due earlier than the document
count suggests.

An append-only log of identifier and digest records is the alternative. Its
write cost does not grow with the namespace, and its compaction deletes one
old file. It costs a record format, a checksum, and a replay path that
truncates a torn tail. This note takes the manifest, because the demo writes
hundreds of documents and not millions.

## Determinism of a document blob

A document holds a protobuf map of attributes, and protobuf does not order map
entries. The same document would otherwise produce a new digest on every write.

The write path marshals with the deterministic option, which sorts map keys.
Protobuf promises that order within one build and not across library versions.

A toolchain upgrade can thus give a stored document a new digest. That costs
deduplication and not correctness. The manifest records the digest that a write
produced. Nothing in this design reads two equal digests as proof that two
documents are the same.

## Concurrency

One mutex serializes every writer. A reader takes the same mutex for one map
read and then leaves it.

Throughput is one batch per root swap. A root swap makes two files durable,
and each costs a file sync and a directory sync. Concurrent writers queue
behind that.

Two processes on one directory diverge without an error. Each holds its own
manifest, and the later root swap discards the work of the other. The lock
file in future work closes that trap.

CONSIDER(ali): does a lookup need to verify the digest of a document blob? The
store hashes every read, which doubles the cost of a read of a large document.
A background scrubber that walks the blob directory is the alternative, and it
finds bit rot late instead of at the read.
