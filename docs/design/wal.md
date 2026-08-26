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
- Group commit. One batcher per stream would gather the records of many
  goroutines into one segment, which removes the per-process append cap.
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

A monotonic key is the classic hotspot shape, and it does not bind here. The
stream name sits inside the prefix, so two streams partition apart. One stream
stops at 500 to 1,000 appends per second, because writers contend for the next
number. A backend prefix serves 3,500 requests per second. The contention limit
is the lower one, so the prefix limit is never the binding constraint.

A backend that has not yet partitioned a new prefix can still refuse a burst
while it scales. No measurement here confirms the margin.

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
    reject an empty batch, an invalid stream name, and a canceled context
    encode the batch into one buffer

    take the lock of this stream, and hold it to the end
    lastKnown == 0  ──▶  lastKnown = Tail(stream)

    seq    = lastKnown + 1
    runway = 3
    tries  = 0

    loop:
        check the context
        err = Put(key(stream, seq), buffer, Condition{Absent: true})
        err is nil                 ──▶  lastKnown = seq;  return seq
        not ErrPreconditionFailed  ──▶  return err

        tries = tries + 1
        tries < runway             ──▶  seq = seq + 1
        exists(seq + 16) is false  ──▶  seq = seq + 1;  tries = 0
        otherwise                  ──▶  head, probes = gallop(seq + 16)
                                        seq    = head + 1
                                        runway = max(3, 2 * probes)
                                        tries  = 0
        wait a short random time, then continue
```

A cold counter reads `Tail` before the first attempt. Without that read, a writer
targets sequence 1. An operator that purged an old prefix leaves sequence 1
absent. The conditional create then succeeds, and it writes today's records under
the lowest number in the stream. That tears a hole through the whole purged
range, and it breaks every later search.

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

A repeated search is the wrong cure, and it makes the disease worse. A search
costs many round trips, and the stream advances during every one of them. A
writer that searches, and then allows itself only three linear attempts, finds
those three numbers already taken and searches again. The loop never ends. The
runway must stay proportional to the cost of the search that preceded it.

Two conflicts are thus different problems, and one probe tells them apart.

**Head contention.** Several writers race at the tip of the stream. The loser is
zero segments behind, so a search would find what a single `+1` step finds. The
gate probe at `seq + 16` reports absent, and the writer keeps marching.

**Drift.** The writer is far behind. The gate probe at `seq + 16` reports
present, and a linear walk cannot close the distance.

A gallop closes a drift. It starts from a number that is known to exist, so it
needs no list page:

```text
gallop(lo):                          lo is known to exist
    exists(lo + 1) is false  ──▶     return lo
    d = 2
    while exists(lo + d):  d = d * 2
                                     lo + d/2 exists, and lo + d does not
    binary search that range for the last number that exists
```

The gallop doubles an **offset** from `lo`. It never doubles the sequence number
itself. Take a stream at one million segments, with a head five above `lo`. A
doubled sequence number searches the range up to two million. That costs 20
probes to find a number 5 away.

The gallop costs about `2 log2(delta)` probes in the real distance. A writer 3
segments behind pays 4 probes, and a writer a million behind pays about 40.

The runway is twice the probe count, and never less than 3. A gallop that returns
after one probe would otherwise leave a runway of one. The next collision would
then send the writer straight back to the gate. The stream also advances once per
active writer during every probe. A runway equal to the probe count is thus too
short whenever more than one writer is active.

A short random delay follows each precondition failure. Two writers that collide
also fail at the same moment. A loop with no delay keeps them in step, and they
collide again on the next number. The delay is an implementation constant, and it
promises a caller nothing.

`CONSIDER(ali):` the gate distance of 16 is a guess. Measure a contended stream,
and set it from the segments that a writer misses during one gallop.

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
    return gallop(seq(last key of objs))
```

`Tail` reuses the gallop above. The list page supplies a number that is known to
exist, and the gallop searches from there.

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

A stream name holds one or more characters from `[a-zA-Z0-9_-]`. A slash breaks
the key parse of the tail search, which reads the sequence number from the last
path component. An empty name and a relative component are the same class of
error, and `Append` rejects all three at the boundary.

`Append` writes one segment object and returns the sequence number it claimed.
The whole batch lands in that one object. A caller that needs a record boundary
for recovery appends that record alone.

`Append` runs one call at a time for one stream. Two goroutines of one process
would otherwise read the same counter, upload the same number, and lose one of
the two uploads. Ten goroutines cost 55 uploads to commit 10 batches, so the
serialization removes waste rather than adding delay. Two streams still append at
the same time.

A caller that waits for its turn still observes its context. A cold start inside
the lock runs a tail search, and a stalled backend holds the lock for seconds. A
lock that ignores cancellation keeps every queued goroutine in place through that
stall. Each one then wakes to a context that expired long before.

That caps one process near one append per round trip on one stream, which is
about 33 per second. The cap counts calls to `Append`, and each call carries a
whole batch. A caller that appends 100 records per call thus reaches 3,300
records per second. A deployment reaches the ceiling in the limits below with
more processes. Group commit removes the cap, and Future work holds it.

`Read` returns every record of one segment. It reports `ErrEndOfStream` when the
segment does not exist, which means the reader reached the head.

Every record of one `Read` shares one backing array. A caller that keeps one
record thus keeps the whole segment in memory. The alternative is one allocation
for each record, and a replay of 10,000 segments of 100 records makes a million
of them.

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

| Dimension                                 | Threshold         | Cause                                        |
| ----------------------------------------- | ----------------- | -------------------------------------------- |
| Segments per second, one stream, all writers | 500 to 1,000     | conditional-create contention on the number  |
| Segments per second, one stream, one process | about 33         | one append at a time, at one round trip each |
| Requests per second, one prefix           | 3,500 to 5,500    | backend prefix limits                        |
| Segments per stream                       | no measured bound | retention is permanent, and GC is a non-goal |

Every row counts segments, and one segment carries a whole batch. Record
throughput is the segment rate times the batch size.

The first row counts every writer of a stream. One process reaches the second row
and no more, because it serializes its own appends. Only many processes reach the
first row.

A local backend is much slower. `objectstore/local.go` holds a bucket-wide lock
and waits for the wall clock to advance on every write. So `mem://` and `file://`
cap near 500 writes per second for the whole bucket. That figure is read from the
code, and no benchmark confirms it yet.

## Open questions

`CONSIDER(ali):` the gate distance of 16 and the runway both come from reasoning,
not from a measurement. Run a contended stream, and set each one from the
segments a writer misses during one gallop.

`CONSIDER(ali):` a rollout starts many processes at once, and each one runs a
tail search on its first append. Ten processes issue a few hundred list calls in
one burst, and a backend throttles a list earlier than a read. Measure a rollout
before adding a delay or a stored hint.

`CONSIDER(ali):` `LogStreamService` in [storage.md](storage.md) §7 carries one
record per `AppendRequest` and exposes no `Tail` call. The Go API takes a batch.
Settle both when a server implements that service.
