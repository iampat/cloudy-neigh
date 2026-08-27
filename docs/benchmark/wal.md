# LogStream append contention

**Status:** Accepted — 2026-08-27 — v1

## Problem

We measure the append performance of one LogStream stream on GCS.

Three numbers matter:

- the rate one stream sustains
- the latency a writer sees at the tail
- the object write cost of a lost race

## Method

`cmd/walbench` runs one writer count at a time. All writers of a run append to
one stream. The run stops at the deadline, and the tool then reads every
segment back to check the log.

| | |
| --- | --- |
| Bucket | `kentrolabs-ai-cloudy-neigh-bench-calm-otter-5127`, US-CENTRAL1 |
| Laptop | MacBook, outside the region |
| VM | `walbench-central`, e2-standard-4, us-central1-a, in the region |
| Payload | 1 KiB to 10 KiB, random |
| Window | 600 s for each writer count |
| Writers | 1, 5, 10, 20, 50, 100 |

The bucket held no objects at the start. Both machines ran one binary, built
from the same commit. The two matrices ran at the same time on separate
streams.

The window is 600 s because a short window truncates the tail. `walbench`
excludes an append that starts inside the window and ends outside it. That rule
drops `n` appends, and it drops the slowest ones. A 60 s window lost 21.3% of
the samples at 100 writers. The 600 s window loses 2.3%.

## Correctness

The log lost no record. Every one of the twelve streams passed the contiguity
check, and no append failed in 120 minutes of contention.

```
PASS f10lap-bench-n100  tail=4326  records=4326  holes=0 corrupt=0 lost=0 dup=0
PASS f10vm-bench-n100   tail=4902  records=4902  holes=0 corrupt=0 lost=0 dup=0
```

The live sequence numbers form `1..T` with no hole in every run. The error rate
was 0.000% on both machines.

## Rate

One stream peaks at 18.5 appends per second in the region, and at 8.4 outside
it. Both peaks sit at 20 writers.

```
  appends per second, VM
  n=1   ║░░░░░░░░░░░░░░░░░░░░░░░░░░░               13.6
  n=5   ║░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░           15.7
  n=10  ║░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░        17.0
  n=20  ║░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░     18.5
  n=50  ║░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░        16.8
  n=100 ║░░░░░░░░░░░░░░░░                           8.0

  appends per second, laptop
  n=1   ║░░░░░░░░░░░░░░░░                           7.9
  n=5   ║░░░░░░░░░░░░░░░░                           8.0
  n=10  ║░░░░░░░░░░░░░░░░                           8.2
  n=20  ║░░░░░░░░░░░░░░░░░                          8.4
  n=50  ║░░░░░░░░░░░░░░░░                           8.2
  n=100 ║░░░░░░░░░░░░░░                             7.0
```

The two curves have different shapes. The VM rate rises 36% from one writer to
twenty. The laptop rate rises 6% over the same range, because the round trip
sets it. A writer that waits 122 ms for one create cannot go faster, and more
writers do not shorten that wait.

Past 20 writers both rates fall. At 100 writers the stream serves 8.0 appends
per second, which is less than one writer serves at 13.6.

The advantage of the same region disappears under load. It is 2.20x at the
peak and 1.14x at 100 writers. Contention costs more than distance once the
stream saturates.

## Latency

The median holds flat across the matrix. The tail grows by three orders of
magnitude.

| writers | laptop p50 | laptop p99 | laptop max | VM p50 | VM p99 | VM max |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | 122 ms | 208 ms | 1.0 s | 70 ms | 141 ms | 0.9 s |
| 5 | 122 ms | 13.5 s | 48.7 s | 65 ms | 3.8 s | 7.9 s |
| 10 | 121 ms | 23.4 s | 61.1 s | 63 ms | 6.2 s | 27.0 s |
| 20 | 122 ms | 41.0 s | 96.9 s | 61 ms | 9.7 s | 23.9 s |
| 50 | 126 ms | 94.6 s | 223.9 s | 474 ms | 21.9 s | 46.0 s |
| 100 | 147 ms | 164.6 s | 489.7 s | 211 ms | 104.0 s | 229.2 s |

On the laptop the median moves from 122 ms to 147 ms. The p99 moves from 208 ms
to 164.6 s over the same range, a factor of 790.

The slowest append took 489.7 s. The retry loop has no attempt limit, so a
writer that loses every race waits until its context ends. A caller that needs
a bound must set a deadline.

## Fairness

No writer starved. Not one writer in any run received one append or less.

The Jain index measures how evenly the appends split across the writers. Writer
`i` lands `x` appends, and `n` writers share the stream.

```
        ( Σ xᵢ )²
  J = ─────────────
       n · Σ xᵢ²
```

The index runs from `1/n` to `1`. A value of `1` means every writer landed the
same count. A value of `1/n` means one writer took the whole stream and the
rest got nothing. The index does not depend on the total, so runs of different
lengths compare directly.

Read the index against its floor, not against zero. At 100 writers the floor is
0.010, so 0.874 sits near the top of the range.

| writers | floor `1/n` | laptop Jain | laptop top share | VM Jain | VM top share |
| --- | --- | --- | --- | --- | --- |
| 5 | 0.200 | 0.969 | 25.7% | 0.996 | 21.7% |
| 10 | 0.100 | 0.980 | 12.4% | 0.996 | 11.1% |
| 20 | 0.050 | 0.938 | 8.3% | 0.993 | 5.7% |
| 50 | 0.020 | 0.898 | 4.0% | 0.988 | 2.5% |
| 100 | 0.010 | 0.821 | 2.6% | 0.874 | 2.3% |

The top share is the second view. It gives the share of the stream that the
busiest writer took. The busiest writer at 100 writers took 2.6% against an
ideal share of 1.0%. The VM stays close to a perfect split at every count.

The laptop is fair but noisier. A long round trip widens the window in which
one writer wins several rounds in a row.

## Write cost

A writer retries the conditional create until one lands. The retry count grows
with the writer count.

| writers | retries before one append lands |
| --- | --- |
| 1 | 0.0 |
| 5 | 3.1 |
| 10 | 6.8 |
| 20 | 14.2 |
| 50 | 36.3 |
| 100 | 70.5 |

The numbers come from the laptop. The VM matches them inside 4%, so the retry
count does not depend on distance.

Every retry uploads a full segment. The loser of a race learns that it lost
only after the upload, so each retry costs one object write. At 100 writers the
stream paid 309,000 object writes to store 4,326 records. This cost is the
reason the rate curve turns down.

## Limits

| Dimension | Threshold | Cause |
| --- | --- | --- |
| Appends per second, one stream, in region | 18.5 | one conditional create for each round trip |
| Appends per second, one stream, cross region | 8.4 | the round trip sets the rate |
| Peak writer count | 20 | write cost grows faster than the gain |
| p99 append latency, 100 writers | 104 s to 165 s | a loser retries with no attempt limit |
| Retries before one append lands, 100 writers | 70.5 | one wasted upload for each lost race |

Every number in this table comes from the 600 s runs.
