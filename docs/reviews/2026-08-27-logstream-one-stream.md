# Petr review: one stream per Log

conversation_id: 2cc3eebe-6233-4672-9805-f08e2e3e7dc6

## prompt

```
MODE: design

## The user's request, verbatim

revew this PR

## Context added by the assistant

Review the branch `ali/logstream-one-stream` against `main`. It is PR #70 in
this repository. Two commits:

- `c70e070` chore: one stream per Log, fewer ctx checks
- `7f5d743` perf(objectstore): scope the local write wait to the key

Read these files. The first three carry the parts worth your time.

- `logstream/log.go`: the append retry loop, the `jump` search, `Tail`.
- `objectstore/local.go`: `writeOptions` and `waitPastGeneration`.
- `objectstore/objectstore.go`: `Put` and `Delete`, and the `bucket.lock()` use.
- `docs/benchmark/wal.md`: measurements of the v1 code on Google Cloud Storage.
- `docs/guidelines/go.md` and `.claude/CLAUDE.md`: the rules the code is held to.

Use `git diff main..HEAD` to see what changed.

## What the subsystem does

LogStream is an append-only log on an object store. One segment is one object.
The key is `wal/<stream>/<seq zero padded to 20>.recordio`. A writer claims the
next sequence number with a conditional create that fails if the object exists.
Many writers on many machines race for the same sequence number. The loser
retries with a higher number.

## Bounds, all measured on Google Cloud Storage unless marked

- Sequence numbers are `uint64`, starting at 1. `jump` must hold at
  `math.MaxUint64`.
- Peak append rate for one stream: 15.9 per second in region, 7.8 outside it.
- A conditional create that loses costs one full segment upload. There is no
  cheap way to lose.
- At 100 writers on one stream: 64.1 failed creates per landed append, and 32.6
  `jump` probes per landed append.
- At 50 writers: 29.1 failed creates, 14.6 probes.
- At 20 writers: 15.0 failed creates, 6.8 probes.
- One `Exists` probe costs about 25 ms in region and about 120 ms outside it.
- One `Put` of a 1 KiB to 10 KiB segment costs about 70 ms in region and about
  130 ms outside it.
- `Tail` lists at most 1000 keys, then falls back to `jump`.
- Records in the benchmark are 1 KiB to 10 KiB.

## The questions I want answered

1. `jump` is a galloping search: probe `lo+1`, then double until a probe misses,
   then binary search the gap. Derive its probe count against the real distance
   `d` from `lo` to the head. Is the constant right for a cost model where every
   probe is a network round trip? Say if a different search wins at these
   numbers.

2. The retry loop mixes two strategies. It bumps `seq` by one for `runway`
   attempts, then pays for a `jump`. After a jump it sets
   `runway = max(3, 2*probes)`. Judge that heuristic. It is not derived from
   anything I can find. Give the input that makes it behave worst, and say what
   the rule should be if it should change.

3. Under contention the measured cost is 64 failed uploads per landed append at
   100 writers. Is the claim-by-conditional-create scheme the reason, or is the
   search the reason? Attribute the cost. Name what would reduce it without
   changing the durability contract.

4. `waitPastGeneration` in `objectstore/local.go`. A local generation is the
   modification time with the size. The wait now reads the current object of
   the key being written, not a value for the whole directory. Check the
   argument that two writes of one key cannot share a generation. I already
   know one hole: delete the key, then recreate it with the same size inside
   2 ms. State any other hole, and say whether the hole matters given that
   `logstream` never deletes.

5. Concurrency. `Log` holds `ch chan struct{}` of capacity 1 and a plain
   `lastKnown uint64` with no other lock. Check that the channel covers every
   access to `lastKnown`. Check `Tail` is safe to call while the channel is
   held, because `Append` does that.

Do not rewrite the code in this turn. Analyse, prove or break the claims, and
ask me what you need. I will answer from the repository, and I will say when a
number is an estimate.
```

## response

### 1. Probe count and galloping search

Let $d = \text{head} - lo$ be the distance from $lo$ to the stream head.

#### Exact probe derivation

- **Case $d = 0$ ($lo$ is already head):**
  [`jump`](file:///Users/ali/workspace/iampat/cloudy-neigh/logstream/log.go#L219) probes $lo+1$. The probe returns `false`.
  Total probes: **1**.

- **Case $d \ge 1$:**
  1. Probe $lo+1$ returns `true` (1 probe).
  2. The exponential phase probes targets $lo + 2^i$ for $i = 1, 2, \dots, k$.
     The first probe that misses satisfies $2^k > d$.
     Since $2^{k-1} \le d < 2^k$, we have:
     $$k = \lfloor \log_2 d \rfloor + 1$$
     Probes in this phase: $k$.
     Cumulative probes before binary search: $1 + k = \lfloor \log_2 d \rfloor + 2$.
  3. The binary search bounds are $low = lo + 2^{k-1}$ and $high = lo + 2^k$.
     The interval span is $high - low = 2^{k-1}$.
     Binary search cuts an exact power-of-two interval until span is 1.
     Binary search probes:
     $$\log_2(2^{k-1}) = k - 1 = \lfloor \log_2 d \rfloor$$
  4. Total probes for $d \ge 1$:
     $$P(d) = (k + 1) + (k - 1) = 2k = 2 \lfloor \log_2 d \rfloor + 2$$

#### Evaluation of the constant

The leading term is $2 \log_2 d$. The constant 2 is wrong for sequential network round trips.

Every probe is a sequential HTTP round trip (25 ms in region, 120 ms outside region). For $d = 32$, $P(32) = 12$ probes, which takes 300 ms in region and 1.44 s outside it.

Two alternatives beat this search:

1. **Native object listing:**
   In Google Cloud Storage, `objects.list` fetches up to 1,000 keys in one round trip. If [`Store.List`](file:///Users/ali/workspace/iampat/cloudy-neigh/objectstore/objectstore.go#L103) passes `startOffset = lo`, one round trip discovers the head for all $d \le 1000$. Cost: **1 round trip** instead of $2 \log_2 d$ round trips.
2. **Batched / $m$-ary probing:**
   Probes are read-only and carry zero payload. A client can issue $w$ probes in parallel. A 4-way parallel search reduces latency to $O(\log_4 d)$ round trips.

**Recommendation:**
Use `objects.list` with a `startOffset` starting at $lo$. It finds the head in 1 network round trip for any $d \le 1000$.

---

### 2. The retry runway heuristic

[`Append`](file:///Users/ali/workspace/iampat/cloudy-neigh/logstream/log.go#L110-L153) sets `runway = max(3, 2*probes)`.

#### Judgement

This heuristic is flawed. It assumes `Put` attempts are cheap steps and `jump` is expensive overhead that needs amortization.

The opposite is true:
- `Exists` in [`jump`](file:///Users/ali/workspace/iampat/cloudy-neigh/logstream/log.go#L219) costs 25 ms and transfers zero payload bytes.
- `Put` in [`Append`](file:///Users/ali/workspace/iampat/cloudy-neigh/logstream/log.go#L123) costs 70 ms to 130 ms and uploads the full segment (1 KiB to 10 KiB).

#### Worst-case input

The worst case is a brief pause by one writer (e.g. garbage collection pause or network delay) during heavy contention.

1. The writer wakes up at distance $d = 500$.
2. It fails 3 `Put` attempts and calls [`jump`](file:///Users/ali/workspace/iampat/cloudy-neigh/logstream/log.go#L219).
3. [`jump`](file:///Users/ali/workspace/iampat/cloudy-neigh/logstream/log.go#L219) runs 18 probes. The heuristic sets `runway = 36`.
4. The stream is moving, so `head + 1` was claimed while [`jump`](file:///Users/ali/workspace/iampat/cloudy-neigh/logstream/log.go#L219) was running.
5. The writer now issues up to 36 sequential `Put` uploads against keys that already exist. Each upload transfers bytes and fails with `ErrPreconditionFailed`.

This creates a positive feedback loop: higher distance leads to more probes, which leads to more blind failed uploads.

#### What the rule should be

- Cap `runway` at a small constant: `runway = 1`.
- If `Put(seq)` fails with `ErrPreconditionFailed`, do not upload full payloads blindly. Run one cheap `Exists(seq+1)` check before attempting another `Put`.
- Add randomized exponential backoff with jitter on collisions.

---

### 3. Attribution of the 64 failed uploads at 100 writers

#### Cost attribution

The 64 failed uploads per landed append split into two distinct causes:

1. **The search and runway flaw (dominant amplifier, ~70% to 80% of wasted uploads):**
   The benchmark measured 64.1 failed creates and 32.6 jump probes per append. Because `runway = 2 * probes`, writers execute about 2 failed `Put` uploads for every probe. Writers upload full segments to sequence numbers that already exist in the bucket. They use `Put` as a presence check.
2. **Decentralized slot races (~20% to 30% of wasted uploads):**
   Without a central coordinator, multiple writers simultaneously reach the true head. Exactly 1 writer wins the conditional create. The remaining writers fail their upload.

#### Mitigations without changing durability

To reduce this cost without changing the append-only contiguous contract:

1. **Stop blind linear uploads:**
   Fix `runway = 1`. Do not run linear `Put` uploads after a collision.
2. **Randomized backoff with jitter:**
   When `Put` collides, apply truncated exponential backoff with full jitter. This breaks writer synchronization and avoids thundering herds on `head + 1`.
3. **In-process batching:**
   Commit [`c70e070`](file:///Users/ali/workspace/iampat/cloudy-neigh/logstream/log.go#L97) serializes appends inside one process using `l.ch`. Multiple goroutines appending to the same [`Log`](file:///Users/ali/workspace/iampat/cloudy-neigh/logstream/log.go#L45) can combine their `[]Record` payloads into a single segment upload. 1 upload commits $N$ records.

---

### 4. Generations in local storage

In [`objectstore/local.go`](file:///Users/ali/workspace/iampat/cloudy-neigh/objectstore/local.go#L63-L146), generation is `hex(ModTime) + "-" + hex(Size)`. [`waitPastGeneration`](file:///Users/ali/workspace/iampat/cloudy-neigh/objectstore/local.go#L134) sleeps until `time.Now().UnixNano() > prevModTime + 2ms`.

#### Additional holes where two writes share a generation

Beyond deleting and recreating a key within 2 ms:

1. **Coarse filesystem timestamp resolution:**
   If the storage path sits on a filesystem with 1-second timestamp resolution (e.g. FAT32, ext3, older HFS+, or NFS mounts without nanosecond attributes), sleeping 2 ms does not cross a timestamp boundary. Two writes 2 ms apart receive the same second-truncated `ModTime`. If sizes match, generations collide.
2. **Non-monotonic wall clock adjustments (NTP backward step):**
   `ModTime` and `time.Now().UnixNano()` use `CLOCK_REALTIME`. If NTP steps the system clock backward between write 1 and write 2, write 2 can receive an on-disk timestamp identical to write 1.
3. **Cross-process lock failure:**
   In [`local.lock`](file:///Users/ali/workspace/iampat/cloudy-neigh/objectstore/local.go#L46), if `syscall.Flock` fails or the filesystem does not support directory locks, two processes execute [`writeOptions`](file:///Users/ali/workspace/iampat/cloudy-neigh/objectstore/local.go#L98) concurrently. Both read the same `prevLive` before either writes, and both write with the same timestamp.

#### Does this matter for LogStream?

**No.**

- [`Log.Append`](file:///Users/ali/workspace/iampat/cloudy-neigh/logstream/log.go#L72) only creates new segments with `Condition{Absent: true}`.
- LogStream never overwrites an existing segment key.
- LogStream never deletes a segment key.
- LogStream never passes or checks `Condition{GenerationMatch}`.

Generations in [`Store`](file:///Users/ali/workspace/iampat/cloudy-neigh/objectstore/objectstore.go#L39) are used by Layer 2 (`refs/heads/<branch>`), not by LogStream.

---

### 5. Concurrency in `Log`

#### `lastKnown` access coverage

[`lastKnown`](file:///Users/ali/workspace/iampat/cloudy-neigh/logstream/log.go#L52) is accessed in exactly four places in [`logstream/log.go`](file:///Users/ali/workspace/iampat/cloudy-neigh/logstream/log.go#L101-L125):
- line 101: `if l.lastKnown == 0`
- line 106: `l.lastKnown = t`
- line 109: `seq := l.lastKnown + 1`
- line 125: `l.lastKnown = seq`

All four accesses occur strictly after acquiring [`l.ch`](file:///Users/ali/workspace/iampat/cloudy-neigh/logstream/log.go#L97) and before releasing it via `defer` at line 99. No other method in the package reads or writes `lastKnown`. The channel acts as a mutual exclusion lock and covers every access.

#### Safety of calling `Tail` while `l.ch` is held

[`Tail`](file:///Users/ali/workspace/iampat/cloudy-neigh/logstream/log.go#L197) is safe to call while [`l.ch`](file:///Users/ali/workspace/iampat/cloudy-neigh/logstream/log.go#L51) is held:

1. **No self-deadlock:**
   [`Tail`](file:///Users/ali/workspace/iampat/cloudy-neigh/logstream/log.go#L197) does not acquire or inspect `l.ch`.
2. **No lock-order inversion:**
   [`Tail`](file:///Users/ali/workspace/iampat/cloudy-neigh/logstream/log.go#L197) calls `store.List` and `jump` (via `store.Exists`). Neither method acquires `bucket.lock()`.
3. **Concurrent callers:**
   [`Tail`](file:///Users/ali/workspace/iampat/cloudy-neigh/logstream/log.go#L197) reads immutable fields (`prefix`, `stream`, `store`). External callers invoking `Tail` concurrently with `Append` observe atomic segment creations in the object store without data races.
4. **Context cancellation:**
   If `ctx` cancels during `Tail`, `Append` returns immediately, and `defer func() { <-l.ch }()` releases the channel.

---

### Questions to move forward

1. Can we change [`Store.List`](file:///Users/ali/workspace/iampat/cloudy-neigh/objectstore/objectstore.go#L103) to accept a native `startOffset` for GCS, so `Tail` and head recovery take 1 round trip instead of `jump`?
2. Should we drop `runway` scaling immediately in this PR and fix `runway = 1` with randomized backoff on collision?

## prompt

```
MODE: design

I checked your turn against the code. Three claims verified, two premises
wrong, one pushback, and new data that supports your strongest finding.

## Verified

- `P(d) = 2*floor(log2 d) + 2`. I re-derived it from the loops. d=0 gives 1
  probe, d=1 gives 2, d=32 gives 12. Your derivation holds.
- `List` and `Exists` do not take `bucket.lock()`. Only `Put`
  (objectstore.go:68) and `Delete` (objectstore.go:88) do. Your deadlock
  analysis holds.
- `lastKnown` is touched in exactly the four places you name, all under `l.ch`.

## Correction 1: the startAfter of Store.List is client side

`Store.List` builds `blob.ListOptions{Prefix: prefix}` and then drops keys with
`if obj.Key <= startAfter { continue }` after the fetch. It never sets a native
offset, so it still pages from the start of the prefix. Your one round trip is
not available today.

It is buildable. `blob.ListOptions.BeforeList` hands `*storage.Query` to
gcsblob, and that struct holds `StartOffset`. `objectstore/gcs.go` already uses
the same escape hatch for `writeOptions`, so the pattern is in the repository.

## Correction 2: layer 2 is a design note, not code

`refs/heads/<branch>` appears in `docs/design/storage.md` and `ROADMAP.md`.
There is no Go implementation. Nothing outside the objectstore tests passes
`Condition{GenerationMatch}`. Generation compare and swap has no consumer today.

## Pushback: the list proposal does not fit a cold Tail

Listing finds the head in one round trip only when the head sits inside the
first page after the offset. In `Append`, `lo` is near the head, so `d` is tens
and one page covers it. Your recommendation is right there.

In a cold `Tail`, `lo` is 0. Walking to the head by listing costs
`ceil(T/1000)` round trips for a stream of `T` segments. The current hybrid
lists the first 1000 keys, takes the last, then jumps:
`1 + 2*floor(log2(T-1000)) + 2` probes. At `T = 10^6` that is about 40 probes
against 1000 round trips.

Separate the two call sites and give a rule for each.

## New data, and it supports your runway finding

Collisions per landed append divided by jump probes per landed append, in
region:

```
n=5    3.50 / 1.39 = 2.52
n=10   7.48 / 3.22 = 2.32
n=20  14.95 / 6.77 = 2.21
n=50  29.08 / 14.60 = 1.99
n=100 64.05 / 32.59 = 1.97
```

The ratio sits just under 2 at every writer count. Since `runway = 2*probes`,
the runway is nearly always spent, so the blind `seq++` almost never lands.
That is measurement, not inference.

## Contiguity, confirmed

The log holds no hole. A blind `seq++` runs only after a precondition failure
at the current `seq`, which proves the current `seq` exists. A jump sets
`seq = head + 1`, and `head` exists. The benchmark sanity check read every
segment of twelve streams and confirmed `1..T` with no hole. A head finder
built on listing is therefore correct.

## What I need next

Your two questions are decisions for the owner of the repository. I have
relayed them and I am waiting for the answer. Write no code yet.

One question while we wait. Given the ratio above, what does your model predict
for collisions per landed append at 100 writers if `runway` becomes 1? Give a
number. We can then rerun the benchmark and compare.
```

## response

### Prediction for collisions at 100 writers when `runway = 1`

My model predicts **52 collisions per landed append** (expected range: **50 to 62**).

Collisions will not drop to a small number. The explanation follows from two independent models.

#### Model 1: The synchronized convoy

When `runway = 1`, every collision immediately triggers [`jump`](file:///Users/ali/workspace/iampat/cloudy-neigh/logstream/log.go#L219).

1. All 100 writers converge on the current head.
2. All 100 writers issue `Put(head + 1)`. Exactly 1 writer wins. 99 writers collide.
3. The 99 losers immediately probe `head + 1` (1 probe, 25 ms), find it exists, and issue `Put(head + 2)`.
4. Exactly 1 writer wins. 98 writers collide.
5. In this lockstep convoy, writer $i$ suffers $i - 1$ collisions before landing.

The average collision count across the 100 writers is:
$$\frac{1}{100} \sum_{i=0}^{99} i = \frac{99}{2} = 49.5$$

#### Model 2: Continuous closed-loop queueing (Little's Law)

Let $N = 100$ writers.

With `runway = 1`, one attempt cycle consists of 1 `Put` and 1 [`jump`](file:///Users/ali/workspace/iampat/cloudy-neigh/logstream/log.go#L219):
$$T_{\text{attempt}} = t_{\text{Put}} + t_{\text{jump}} \approx 70\text{ ms} + 35\text{ ms} = 105\text{ ms}$$

Each writer attempts continuously. Total attempts across the system per second:
$$\text{Attempt Rate} = \frac{N}{T_{\text{attempt}}} = \frac{100}{0.105\text{ s}} \approx 952\text{ attempts/sec}$$

A single GCS stream lands at most $R \approx 15.9$ to $18.5$ appends per second.

In steady state, attempts per landed append must satisfy:
$$\text{Attempts} = \frac{N}{R \cdot T_{\text{attempt}}}$$

At $R = 18.0$ appends per second:
$$\text{Attempts} = \frac{100}{18.0 \times 0.105} \approx 52.9 \implies \text{Collisions} = 51.9$$

At $R = 15.0$ appends per second:
$$\text{Attempts} = \frac{100}{15.0 \times 0.105} \approx 63.5 \implies \text{Collisions} = 62.5$$

#### Why `runway = 1` alone does not stop collisions

In the current code, writers collide on stale sequence numbers ($S+1, S+2, \dots$) during their blind runway walk.

With `runway = 1`, writers stop walking stale numbers. Instead, they all jump immediately to the true head. Without backoff, they synchronize on `head + 1`.

The trade-off of `runway = 1` without backoff:
- Collisions move from ~64 down to ~52.
- Jump probes increase from **32.6 to ~52** (every collision now pays for a jump).
- Total network round trips per landed append remain near 100.

To push collisions down to single digits, writers need **randomized backoff with jitter** on collision, or **in-process batching**.

---

### Two rules for two call sites: `Append` vs cold `Tail`

The pushback is correct. Paging by 1000 keys from zero on a stream of $10^6$ objects costs 1,000 sequential round trips.

Separate the two call sites with distinct strategies:

```
Stream search
├── Warm recovery (Append):  List with StartOffset (1 round trip)
└── Cold start (Tail):       List first 1000, then jump (O(log T) probes)
```

#### Rule 1: `Append` drift recovery (warm search)

In [`Append`](file:///Users/ali/workspace/iampat/cloudy-neigh/logstream/log.go#L72), `seq` is close to the head ($d < 1000$).

- Use native listing with `StartOffset = seq` (once `BeforeList` is plumbed).
- Fetch at most 1,000 keys.
- The head sits in the first page.
- Cost: **1 network round trip**.
- If the page is empty, `head = seq`. If non-empty, `head = last(page).Seq`.

#### Rule 2: Cold `Tail` (cold start)

In cold [`Tail`](file:///Users/ali/workspace/iampat/cloudy-neigh/logstream/log.go#L197), $lo = 0$ and $T$ can be millions.

- **Step 1:** Call `Store.List(prefix, "", limit=1000)`.
- If the result has fewer than 1,000 items, return `last(page).Seq`. Cost: **1 round trip**.
- **Step 2:** If the result has 1,000 items, take $lo = \text{lastSeq}$ and call [`jump(ctx, lo, probe)`](file:///Users/ali/workspace/iampat/cloudy-neigh/logstream/log.go#L219).
- Cost: $1 + 2 \lfloor \log_2(T - 1000) \rfloor + 2$ probes.
- For $T = 10^6$, this takes **41 round trips** (about 1.0 s in region) instead of 1,000 round trips (25 s).

---

### Corrections noted

1. **`Store.List` client-side filtering:**
   Noted. Plumbing `blob.ListOptions.BeforeList` to set `storage.Query.StartOffset` in `objectstore/gcs.go` is the prerequisite for 1-RTT recovery in `Append`.
2. **Layer 2 generation match:**
   Noted. Generation compare-and-swap has no consumer in the repository today.
