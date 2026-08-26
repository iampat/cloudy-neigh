# LogStream

**Status:** Draft — 2026-08-26 — v1

## Problem

A search index and a branching filesystem both need a durable write path on
object storage. The write must survive a crash, and two writers must never
overwrite each other. A coordination service answers both, and it adds a cluster
to operate.

Object storage offers one primitive that answers both without a coordinator. A
conditional create writes a key only when that key is absent. One writer wins,
and every other writer learns that it lost.

This document defines the object layout, the append protocol, the recovery
rules, and the Go API of LogStream. LogStream is Layer 1 of
[the storage design](storage.md). KVFS builds on it. It also runs alone as a
message queue, an event log, or the write-ahead log of another engine.

## Goals

- Durable append through one conditional create, with no coordination service.
- Strictly contiguous 64-bit sequence numbers.
- Every stored object immutable, and every key written once.
- Deterministic keys that generic tooling lists and reads in order.
- One instance that serves many streams.
- Use outside KVFS, because Layer 1 depends on no layer above it.

## Non-goals

- Garbage collection. A segment is permanent, which
  [storage.md](storage.md) §1.3 states for the whole system.
- A record envelope. Version 1 frames the caller payload with nothing around it.
- Exactly-once delivery. See [Delivery guarantee](#delivery-guarantee).
- Random access to one record inside a segment.
  [RecordIO](recordio.md) is a sequential format.
- A gateway that micro-batches many writers into one stream.
  [storage.md](storage.md) §9 holds that evolution.

## Future work

- The `LogRecord` envelope of [storage.md](storage.md) §5.1, when a caller needs
  a header.
- A writer nonce inside a segment. A writer could then recognize its own
  segment after an ambiguous failure, which
  [Delivery guarantee](#delivery-guarantee) explains.
- Read-ahead across segments, so a replay overlaps the network with the fold.
- A `Head` call in `objectstore`, so the tail search stops paying list pricing.
- A `min_active_seq` watermark, if an operator ever truncates an old prefix.

## Model

A **stream** is a named, append-only sequence of segments. A stream needs no
creation step. It exists when its first segment exists.

A **segment** is one immutable object. It holds N records in
[RecordIO](recordio.md) framing. One `Append` call writes exactly one segment.

A **sequence number** is a 64-bit integer that names a segment inside its
stream. Numbers start at 1 and stay contiguous.

A **record** is an opaque byte slice. LogStream never reads inside it.

The **head** of a stream is the highest sequence number that holds a segment.
`Tail` reports it.

```text
wal/
├── main/
│   ├── 00000000000000000001.recordio      3 records
│   ├── 00000000000000000002.recordio      1 record
│   └── 00000000000000000003.recordio      12 records   ◀── head, seq 3
└── orders-topic/
    └── 00000000000000000001.recordio
```

A record has no number of its own. Every record of a batch shares the sequence
number of its segment, so the address of a record is the pair `(seq, index)`.

## Object layout

A segment key is `wal/<stream>/<seq>.recordio`, and `<seq>` is the sequence
number in 20 decimal digits with leading zeros.

Twenty digits hold `2^64 - 1`, so no number ever needs a longer field.
Fixed-width digits make lexicographic order match numeric order. A list call
thus returns segments in sequence order on every backend, and so does any
generic tool.

The key carries everything. The stream name is the prefix, and the sequence
number is the file name. No container message inside the segment repeats them.

Nothing shards the `wal/` prefix by hash, unlike `objects/` and `manifests/`.
Sequential keys are the point of the layout, and a hash prefix would destroy the
order that the tail search and every operator depend on.
`CONSIDER(ali):` a monotonic key is the classic hotspot shape on S3 and GCS.
Appendix B of [storage.md](storage.md) names `wal/` as a candidate and offers no
mitigation for it.

## Use of the object store

LogStream needs a conditional create, and never a compare-and-swap.

| `objectstore` call                | Purpose                            |
| --------------------------------- | ---------------------------------- |
| `Put` with `Condition{Absent}`    | claims the sequence number         |
| `Get`                             | reads one segment                  |
| `List`                            | the cold start alone, never a read |

`Condition{GenerationMatch}` serves `refs/heads/<branch>`, which Layer 2 owns.
LogStream writes only immutable objects, so it compares no token. It also
discards the generation token that `Put` and `Get` return.

A mutable head object is the alternative. It names the tail in one read, and it
removes the cold-start search. Three reasons refuse it.

**The two calls obey different limits.** A conditional create writes a new key,
so it uses the write budget of the whole prefix. A mutation rewrites one key, and
every backend caps that key on its own. Appendix B of
[storage.md](storage.md) measures 2.7 mutations per second on one key. The
append ceiling is 500 to 1,000 per second, which is three orders of magnitude
apart.

**The key namespace is the only source of truth.** A head object adds a second
one. A partial failure then leaves the two in disagreement, and recovery needs a
repair tool. The search needs none, because it reads the truth directly.

**A sequential key sorts the same way in every tool.** An operator lists the
prefix and reads the log in order. The last name is the head. No tool has to
parse a pointer object or an index format.

The cost is the cold-start search below. It takes about 31 round trips at one
million segments, and a writer pays it once at start.

## Sequence claims

`Append` claims a sequence number with `objectstore.Put` under
`Condition{Absent: true}`. That call is the linearization point. A record is
durable when the call returns without an error, and at no earlier moment.

```text
Append(stream, records):
    encode the batch into one buffer
    seq = lastKnown[stream] + 1
    conflicts = 0
    loop:
        err = Put(key(stream, seq), buffer, Condition{Absent: true})
        err is nil                ──▶  lastKnown[stream] = seq;  return seq
        not ErrPreconditionFailed ──▶  return err

        conflicts = conflicts + 1
        conflicts < 3             ──▶  seq = seq + 1
        otherwise                 ──▶  seq = max(seq + 1, Tail(stream) + 1)
                                       conflicts = 0
        wait a short random time, then continue
```

A writer targets a free number, or a number that a precondition failure proved
taken. A failure of any other kind returns an error and moves nothing.

That rule holds the **contiguity invariant**. The live sequence numbers of a
stream always form the range `1..T` with no hole. The tail search depends on it,
because a binary search over a range with a hole reports the wrong head.

The cost of a collision is one wasted upload. The loser of a race uploads the
segment again under the next number. Appendix B of [storage.md](storage.md) puts
the resulting ceiling at 500 to 1,000 appends per second on one stream.
[storage.md](storage.md) §9 names the gateway that raises it.

The retry loop ends when the context ends. It has no attempt limit, because each
precondition failure proves that another writer made progress.

## Recovery from a stale counter

`lastKnown` goes stale when another writer advances the stream. A walk of `+1`
steps then costs one round trip for each segment the writer missed. A writer that
is 4,000 segments behind cannot reach the head inside a normal request deadline.
Every call thus fails, and the writer never catches up.

Three consecutive precondition failures trigger a `Tail` call, which re-anchors
`lastKnown` in one search. The jump is safe, because `Tail` reports a real head.

A short random delay follows each precondition failure. Two writers that collide
also fail at the same moment. A loop with no delay keeps them in step, and they
collide again on the next number. The delay is an implementation constant, and it
promises a caller nothing.

## Delivery guarantee

LogStream guarantees at-least-once delivery. A caller that needs exactly-once
makes its own operations idempotent. LogStream never removes a duplicate.

An acknowledgement can be lost. A `Put` commits the object, and the reply never
reaches the writer. The writer holds an error, and it cannot tell whether its own
segment landed. The next attempt reads a precondition failure on that number,
treats the number as taken, and writes the same batch under the next number.

```text
writer                          object store
  │── Put seq=42, Absent ───────▶│  commits 42
  │        ╳ reply lost ─────────│
  │── Put seq=42, Absent ───────▶│
  │◀──── precondition failed ────│  42 exists, and the writer owns it
  │── Put seq=43, Absent ───────▶│  the same batch, a second time
```

The retry does not have to come from the caller. The GCS client retries a write
that carries a precondition, so the whole exchange fits inside one
`objectstore.Put` call.

Immutability does not prevent this. It prevents a torn segment, an overwritten
segment, and two writers inside one object. Here the log holds two intact
segments that carry the same records.

A sequence number names a segment. It is not a deduplication key, and a reader
cannot detect a repeat from the number alone.

KVFS needs nothing more, because a `KVMutation` folded twice gives the same
manifest entry. A caller with a non-idempotent operation, such as a counter
increment, puts a transaction identifier in its payload and rejects a repeat on
replay.

## Cold-start tail search

`Tail` reads the first page of the stream prefix, then probes.

```text
Tail(stream):
    objs = List("wal/<stream>/", "", listLimit)
    len(objs) == 0         ──▶  return 0
    len(objs) < listLimit  ──▶  return seq(last key of objs)

    lo = seq(last key of objs)
    hi = lo * 2
    while exists(hi):
        lo = hi
        hi = hi * 2
    while hi - lo > 1:
        mid = lo + (hi - lo) / 2
        exists(mid)  ──▶  lo = mid
        otherwise    ──▶  hi = mid
    return lo
```

`listLimit` defaults to 1000, which matches the page size of the supported
backends. One `List` call thus answers every stream of less than 1000 segments,
and that is the common case.

A repeated `List` walk is the wrong shape. `objectstore.List` filters
`startAfter` on the client, so a paged search reads every key from the start of
the prefix on every call. Retention is permanent, so the segment count only
grows.

Round trips against the segment count:

| Segments  | A probe alone | One `List`, then a probe |
| --------- | ------------- | ------------------------ |
| 0         | 1             | 1                        |
| 500       | 18            | 1                        |
| 1,000     | 20            | 12                       |
| 1,000,000 | 40            | 31                       |

The binary search takes `lo` from the last key the `List` returned, and never
from `listLimit`. The two agree only while segment 1 still exists. An operator
lifecycle rule that deletes an old prefix breaks that equality, and the search
then starts from a number that no object holds. The last returned key exists by
construction, so it is always a valid lower bound.

`exists(seq)` calls `List` with the full segment key as the prefix and a limit of
1. The call returns one object or none, and it transfers no body. A `Get` probe
would open the body of a large segment.

A metadata read costs less than a list call on every cloud backend. `objectstore`
has no such call today, and `Tail` runs once for each writer start, so the extra
cost is small. A short-lived writer that appends once pays it on every run.
`TODO.md` records the `Head` call as a requirement for the ObjectStore rework.

## Segment corruption

A conditional `Put` lands whole or not at all. A partial frame in a segment thus
means a damaged object, and never a torn tail.

`Read` reports the RecordIO error and changes nothing. `recordio.ErrTornWrite`
carries no recovery here, because an immutable object has no truncation step. The
caller stops the replay, which is what [RecordIO](recordio.md) requires for
mid-stream corruption.

## API

```go
package logstream

import (
	"context"
	"errors"

	"github.com/iampat/cloudy-neigh/objectstore"
)

var ErrEndOfStream = errors.New("logstream: end of stream")

type Record []byte

type Log struct{ ... }

type Option func(*Log)

func WithPrefix(prefix string) Option
func WithMaxRecordSize(max int64) Option
func WithTailListLimit(limit int) Option

func New(store *objectstore.Store, opts ...Option) *Log

func (l *Log) Append(ctx context.Context, stream string, records []Record) (uint64, error)
func (l *Log) Read(ctx context.Context, stream string, seq uint64) ([]Record, error)
func (l *Log) Tail(ctx context.Context, stream string) (uint64, error)
```

`Log` is a struct, and one implementation holds the whole surface. It follows
`objectstore.Store`, because an interface with one implementation selects
nothing. A caller that needs a fake declares its own interface over the three
methods.

One `Log` serves many streams. The stream name is a call argument, not
constructor state.

`Append` writes one segment object and returns the sequence number it claimed.
The whole batch lands in that one object. A caller that needs a record boundary
for recovery appends that record alone.

`Read` returns every record of one segment. It reports `ErrEndOfStream` when the
segment does not exist, which means the reader reached the head. A returned
record is an independent copy, because the RecordIO scanner lends a buffer that
it reuses.

`Tail` reports the highest written sequence number. A writer calls it on start
and then counts in memory. It calls `Tail` again only when that counter goes
stale.

`ErrEndOfStream` is the one sentinel. A caller branches on it to stop a replay.
An empty batch, an unknown option value, and a sequence number of 0 are caller
bugs, so each returns a plain error. A sentinel there would invite a retry loop
that can never succeed.

### Record format

A record is an opaque byte slice. The RecordIO frame holds the caller bytes with
no envelope around them.

[storage.md](storage.md) §5.1 defines `logstream.v1.LogRecord`, which adds a
header map. No caller needs a header today, because KVFS puts a `KVMutation` in
the payload. The envelope costs a protobuf dependency in the lowest reusable
layer, so it waits for the first caller that needs a header. It arrives as a new
format version.

`Append` rejects a record larger than `WithMaxRecordSize`.
`recordio.WriteRecord` enforces no limit, and every reader does, so an unchecked
write produces a segment that nothing can read back.

## Limits

Every number here comes from Appendix B of [storage.md](storage.md).

| Dimension                        | Threshold           | Cause                                       |
| -------------------------------- | ------------------- | ------------------------------------------- |
| Appends per second, one stream   | 500 to 1,000        | conditional-create contention on the number |
| Requests per second, one prefix  | 3,500 to 5,500      | backend prefix limits                       |
| Segments per stream              | no measured bound   | retention is permanent, and GC is a non-goal |

A local backend is much slower. `objectstore/local.go` holds a bucket-wide lock
and waits for the wall clock to advance on every write. So `mem://` and `file://`
cap near 500 writes per second for the whole bucket. That figure is read from the
code, and no benchmark confirms it yet.

## Open questions

`CONSIDER(ali):` the `wal/` prefix keeps monotonic keys, which is the hotspot
shape that Appendix B names and does not mitigate. Measure a real prefix before
choosing between a sub-prefix by epoch and no change.

`CONSIDER(ali):` `LogStreamService` in [storage.md](storage.md) §7 carries one
record per `AppendRequest` and exposes no `Tail` call. The Go API takes a batch.
Settle both when a server implements that service.
