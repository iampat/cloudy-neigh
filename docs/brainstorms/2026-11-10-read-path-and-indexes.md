# Brainstorm: read path and missing indexes

**Status:** Open, started 2026-11-10. This log records a running brainstorm. It
is not a design note. Each decision moves into `docs/design/` when it settles.

The reader question: what did we decide about the missing features, and why?

## Context

- Object storage is the source of truth.
- A namespace supports native branches. Branches never cross namespaces.
- Write operations: insert, delete, upsert. Conditional delete and conditional
  patch come next. We test the design against them later, but they do not
  drive it.
- The design moves toward a log-structured merge (LSM) tree.
- Read latency is the top priority. Nothing justifies a higher tail read
  latency.

Missing features:

1. Point lookup: `Get(id, branch)`.
2. Inverted index search over tokenized fields.
3. Nearest neighbor search in candidate clusters over vector indexes.
4. Range queries over numeric fields.
5. Set lookup over high-cardinality fields, e.g. `owner-id in [...]`.

## Assumption check

A subagent checked four assumptions against the GCS and S3 docs. The numbers
are from vendor docs, not from our measurements.

| Assumption | Verdict | What matters |
| --- | --- | --- |
| Objects are immutable | Partly | True on GCS Standard and S3 Standard. GCS Rapid (zonal) and S3 Express One Zone allow appends with one writer. GCS allows 1 write/s per object name. |
| Many concurrent reads | Holds after a ramp | GCS starts near 5k reads/s per bucket and doubles at most every 20 min. S3 gives 5.5k GET/s per prefix. GCS caps bandwidth at 200 Gbps per region per project. |
| Block size does not matter | Wrong | First byte costs 100 to 200 ms on S3 Standard. A request costs the same at 4 KB and at 1 MB. Size reads for latency and request cost. |
| One object or many | Fewer, larger objects with range reads | Requests and listings cost money. Both vendors recommend hedged requests for tail latency. |

Consequences:

- A cold read costs at least one object store round trip. Only a cache, a
  prefetch, or a faster tier removes it.
- The main read metric is round trips per query, not bytes.
- GCS Rapid and S3 Express can serve as a hot tier, not as the source of
  truth. They are single zone and have no versioning.

## Workload

- Design target: 200M docs per namespace. The design must still work at 20B.
  Only a few large customers need 20B.
- A doc is small: two or three vectors and about one page of text.
- Vectors have 500 to 5000 dimensions. Some are unit length.
- A doc has two or three indexed vector fields. Any number of vector fields can
  stay unindexed.
- Each indexed field costs us storage and compute. The customer pays for each
  index, and the bill shows it.
- Cold queries follow a distribution. We set no cold target, but we optimize
  for the tail.
- Write rate and write latency are not a concern.
- Agents send many parallel queries in one task. Part of the filter repeats
  across the task, e.g. the same `owner-id` set with a different `type`.

## Principles

- **Caches only.** RAM and NVMe cache the object store. No node holds a whole
  index.
- **Resident metadata, remote data.** Manifests, block indexes, term index
  data, and centroids stay in RAM or on NVMe. A cold query pays one data round
  trip, with parallel reads inside it.
- **Two tests for each structure.** Its resident part stays small at 20B docs.
  Its remote part needs one round trip per query.
- **Immutable shared segments.** A branch is a list of segment references. A
  fork copies the list, not the data.
- **Cache by segment.** A segment never changes, so a cache entry keyed by
  segment is always valid. Every branch that shares the segment shares the
  entry.
- **Wide fan-out needs hedging.** The slowest of N parallel reads sets the
  tail.

## Identifiers

The system sets `id` at insert. It is monotonic, e.g. UUIDv7 or uint64. A user
field such as `doc-id` is a normal high-cardinality field with no special path.

We derive `id` from the namespace write-ahead log (WAL) sequence and the
position in the batch. It is then unique across all branches of a namespace
and needs no extra counter.

## Upsert

An upsert resolves at write time, in the flusher. A patch reads the old doc
and writes a full new version. The Upsert ordering section says why the
flusher does it. A read sees one version and never merges. This favors reads over
writes.

Upserts and deletes of old docs are rare. A lower rate limit applies to them,
and the customer pays more for them. No heavy-upsert path exists.

## Point lookup

`Get(ids[], branch)` takes a batch. Agents use both patterns: fetch the bodies
of search results, and read a known id.

```
segment object (immutable, shared by every branch that references it)
┌──────────────┬──────────────┬───────────┬──────────┬────────┐
│ doc blocks   │ vector cols  │ postings  │ numeric  │ footer │
│ sorted by id │              │           │          │        │
└──────────────┴──────────────┴───────────┴──────────┴────────┘
resident, per segment:
  [min_id, max_id] + fence pointers (first id per block, offset)
  small delta segments only: exact id set or bloom filter
```

1. The branch gives its segment list, newest first.
2. Keep the segments whose range and fences can hold `id`. Monotonic ids
   leave one base segment and a few delta segments.
3. Fetch all candidate blocks in parallel, in one round trip. The newest
   version wins. A tombstone also wins.

A read takes a whole block of about 256 KB, because the first byte costs more
than the bytes. A bloom filter on every key costs about 25 GB at 20B docs, so
it fails the resident test. Fence pointers cost one entry per block.

Estimates at about 10 KB per doc and 16 B per fence entry:

| Docs | Data | Blocks | Fence pointers |
| --- | --- | --- | --- |
| 200M | 2 TB | 8M | 130 MB, in RAM |
| 20B | 200 TB | 800M | 13 GB, top level in RAM, leaves on NVMe |

At 20B a cold fence leaf costs one more round trip. We accept that cost
because 200M is the design target.

## Branches and history

A branch works like a git branch. Each flush makes one commit. Operations are
atomic, and a batch gets its commit number at flush time.

```
main:  c1 ── c2 ── c3 ── c4   ← head
                   └── a1 ── a2   ← branch-a, forked at c3
bisect: find the commit at time T, open its manifest, query it
```

- A commit is immutable, with a timestamp and a parent pointer. The head is a
  pointer to the newest commit. This avoids the GCS limit of 1 write/s per
  object name, because only the head pointer changes. Today the code rewrites
  `refs/heads/<branch>.json` in place.
- The commit number is the WAL sequence of the flush. The manifest already
  holds it as `CheckpointSeq`. It is monotonic across the namespace, so commits
  on two branches compare by number.
- A commit stores the segments it adds and removes. A full snapshot follows
  every N commits. A full manifest per flush costs about 4 GB per day per
  branch at 1,000 segments.
- Only the head manifest stays resident. Bisect is not on the hot path.
- Branch creation is free and unlimited, with a rate limit. The write rate
  limit applies to the namespace, not to each branch. Together they bound the
  delta segments and the compaction work per namespace.

### Retention

History lasts as long as a time to live (TTL). We are not a database and do not
keep history forever. A customer who needs GDPR compliance sets the TTL.

- GC and compaction run in the background. They remove commits older than the
  TTL and segments that no retained commit reaches.
- Compaction runs on a deadline. A segment that head still references and that
  holds a tombstone near the TTL gets rewritten. Without that rule the TTL never
  removes the doc.
- A cache evicts a segment when GC deletes it.

## Postings

The inverted index and set lookup share one structure. A text token and a
field value both map a key to a list of docs.

```
key ──▶ postings (doc ordinals in segment)
  "text:fox"      ─┐
  "owner:u1"      ─┼─ one term dictionary + postings format
  "tag:scifi"     ─┘
```

- The dictionary key is the raw bytes `field + \0 + value`. A 64-bit hash of
  the key gives about 0.001 collisions at 200M values and about 10 at 20B. A
  collision merges two owners' docs, which leaks tenant data. A hash is fine as
  a prefilter.
- Postings hold doc ordinals in the segment as roaring bitmaps, plus term
  frequency (tf).
- The dict entry holds the document frequency (df). The match round trip
  returns it.

### Round trips per term

A sparse index over the dictionary costs two dependent round trips: one for the
dict block, one for the postings. The fix depends on the length of the
postings.

```
per segment, resident: sparse dict index + sharded bloom + hot-term entries
query term ─▶ bloom says no → skip segment
           └▶ hot term       → RT → postings
           └▶ other term     → RT → dict block with inline postings
```

- A sharded bloom filter per segment removes the misses. Long-tail values
  such as owner ids miss most segments, so the bloom prunes the fan-out.
- Short postings sit inline in the dict block. Lucene called this "pulsing".
  CONSIDER(ali): this is from memory. Check it before a design note cites it.
- Long postings belong to a few terms. Their dict entries stay resident.
- Every term costs one round trip.

### Tokenizer

The tokenizer is the same at ingest and at query time. The server runs it, but
a client can run it too.

- The field schema and each segment store the tokenizer name and version.
- A query with a different tokenizer version than a segment is an error.
- A tokenizer change needs a reindex.

### Set lookup

An `IN` set takes at most 1,000 values as a start. Latency is not the limit,
cold cost is. 1,000 values on 20 segments costs up to 20k GET requests, about
$0.008 per query. The agent pattern saves the cost, because the set repeats
and its union gets a cache entry per segment. We measure and adjust.

### Matching and ranking

Matching and ranking are two steps. Matching is bag of terms.

```
match:  terms ─▶ postings ─▶ candidate set (usually < 100k docs)
rank:   candidates ─▶ fetch tf + doc length ─▶ BM25 with branch stats ─▶ top-k
```

- Each segment keeps its doc count and total token length resident. Branch
  stats are sums over its segments.
- Doc length is one byte per doc. That is about 200 MB at 200M docs, on NVMe.
- Shadowed and deleted docs inflate df until compaction. We accept the error.
- Term proximity belongs to ranking. Positions go in their own stream, apart
  from the match postings. Version one has no positions.

## Ranges

We cannot assume a selectivity. The storage supports both plans, and the query
planner picks one.

```
per numeric field, per segment:
  column           value by ordinal          → scan plan (wide range)
  sorted run       (value, ordinal) pairs    → index plan (narrow range)
  resident         fences over sorted run + min/max per column block
```

- Index plan: the fences give the start, then one contiguous read in one
  round trip. The ordinals become a bitmap.
- Scan plan: min and max per block skip blocks, then a column read. It works
  well for a field that follows insert order, e.g. `created_at`.
- Cost: about 12 B per doc per indexed field, 2.4 GB per field at 200M docs.

One primitive, a sorted run with resident fences, now serves `id`, the term
dictionary, and numeric ranges.

## Vectors

A graph index such as HNSW or DiskANN takes tens of dependent hops. A hop costs
100 ms or more on object storage, so a cold query takes seconds. Clusters take
one parallel round trip, with two or three levels of centroids.

- Levels cut CPU, not round trips. Every level must stay resident.
- Quantized centroids route, full centroids rank the routed set.
- Recall depends on two settings: the probe count (`nprobe`) and the rerank
  depth. Rerank is optional. We publish the recall and latency curve for
  each setting.

Estimates per vector field at about 1,000 vectors per cluster and 1024
dimensions:

| Docs | Leaf centroids | fp16 | 1-bit |
| --- | --- | --- | --- |
| 200M | 200k | 400 MB | 25 MB |
| 20B | 20M | 40 GB | 2.5 GB |

| Element type | Index size per field at 200M docs |
| --- | --- |
| fp16 | 400 GB |
| int8 | 200 GB |
| 1-bit | 25 GB |

A probe of 1,000 fp16 vectors at 1024 dimensions reads 2 MB. Sixteen probes
read 32 MB. The 200 Gbps quota then caps cold queries near 800 per second per
project. A probe must read quantized vectors. Probe precision and rerank are
config, and the customer pays for the choice.

### Clusters and segments

We keep two options and pick one by experiment. The workload is still open.
The metrics, in order:

1. Read latency, p99 cold and p99 warm, at the same recall@k for both options.
2. Cost: storage, requests, and compute per query.
3. Write throughput.

Both options run at the same recall, or the latency numbers do not compare.

**Option B: shared centroids.** One versioned centroid set per namespace. Each
segment groups its vectors by those centroids.

```
centroids v3 (resident, namespace-wide)
query ─▶ pick c7,c12 ─▶ read slice c7,c12 from each segment (1 RT, parallel)
fresh small segment ─▶ exact scan over 1-bit vectors
```

- Gain: one routing step per query. Branches share the centroids.
- Cost: a branch whose data drifts from main fits the centroids badly. A new
  centroid version makes compaction rewrite segments.

**Option C: one object chain per cluster, SPFresh style.** The index is
partitioned by cluster. Each branch has its own cluster map.

```
branch commit ─▶ cluster map ─▶ cluster c7: centroid + chain of vector slices
query ─▶ route over resident centroids ─▶ read nprobe clusters (1 RT, parallel)
split(c7) ─▶ c7a, c7b + reassign nearby vectors (SPFresh LIRE)
```

- Gain: fewest round trips per query. A split is local.
- Cost: clusters get a lifecycle next to segments. The cluster map needs a
  free fork.
- A split runs on a config or in the background, at flush or at compaction.
  Nothing edits an object in place.

### Flush into many clusters

A flush of 10k vectors lands in about 10k clusters. One object per cluster per
flush costs about $0.05 per flush and hits the GCS write ramp. The rate limit
goes on compaction instead.

```
flush ─▶ one L0 object, vectors grouped by cluster, slice index resident
           [c3 │ c7 │ c7 │ c12 │ ...]          1 PUT per flush
compaction (rate limited, worst clusters first)
         ─▶ merge c7's L0 slices into c7's cluster object
query(c7) ─▶ c7 object + c7 slice in each live L0 object   (1 RT, parallel)
```

- The L0 count sets the read cost, so compaction keeps it low, e.g. 8 or less.
- L0 objects are small and new, so NVMe holds them.
- C becomes an LSM: L0 looks like B and L1 holds cluster objects. B and C
  share the L0 code, which keeps the experiment fair.

### Cluster map

```
branch commit ─▶ root: [p0][p1][p2]...[p199]     (page refs, by content hash)
                         │
                         page p7: clusters 7168–8191 (centroid, chain refs)
split in c7200 on branch-a ─▶ rewrite p7 + root only
```

- Fixed pages of about 1k clusters, keyed by cluster id.
- Copy on write: a change rewrites one page and the root. A fork copies the
  root.
- Pages are content addressed, so caches share them across branches.
- A split gives new cluster ids in new pages. Old ids become dead, and map
  compaction removes them.

### Stale vectors

An upsert moves a vector to a new cluster. The old copy stays in the old
cluster.

- Each doc stores its cluster id.
- An upsert already reads the old doc at write time. The writer adds a
  tombstone `(cluster_old, id)` to the L0 slice of that cluster.
- The query reads the slice in the same round trip and drops the stale copy.
- Compaction applies the tombstones.
- This depends on the rate limit for upserts and deletes.

## Dead sets

Every index sees shadowed docs in old segments, not only vectors. One
mechanism serves all of them.

```
per branch, per immutable object: dead set (small, resident, rate-limited)
  Get ── postings ── ranges ── vectors   all filter through it
```

## Compaction

Compaction runs only when performance drops, e.g. many small segments or many
deletes. It is expensive, and it changes the resident metadata.

- It is not latency sensitive. It runs on the cheapest resource, e.g. spot
  VMs, and the customer pays for it.
- The output id is a hash of the input segment ids and the compaction version.
  A PUT with an if-absent condition writes it.
- The output is a pure function of its inputs. It applies only the tombstones
  inside the input segments. A branch tombstone in a newer segment keeps its
  effect on the output.
- A branch that shares the inputs computes the same id and adopts the output
  with a manifest update. The cache stays shared.
- A rerun after a crash writes the same object, so the PUT is a no-op.
- GC deletes a segment when no retained commit references it. Old commits
  inside the TTL still reference the inputs, so a running query never loses
  a segment.

```
s5 = H(s1, s2, s3, compaction version)
main compacts   ─▶ PUT s5 if-absent ─▶ written
branch-a later  ─▶ s5 exists ─▶ swap manifest only, no rewrite
spot VM dies    ─▶ rerun ─▶ same s5 ─▶ PUT is a no-op
```

### Swap warm-up

A swap replaces warm segments with a cold one. The first queries on the new
commit pay extra round trips, which raises tail latency.

A query node keeps serving the old commit while it warms the new segment. The
old commit stays valid, and the TTL keeps its segments alive. The move rule is
an implementation detail. We tune by experiment how warm a node must be, and
how long it may stay on the old commit.

## Agent caching

The cache unit is one filter predicate on one segment, e.g. `owner=u1` gives a
doc set. A query intersects cached sets with new ones. Queries that arrive
together share one resolution.

```
burst t0:  Q1(text, owner∈G)   Q2(text+vec, owner∈G)
                 └──── resolve owner∈G once ────┘
                         ▼  cached per segment
burst t1:  Q3(new text, owner∈G)  → filter is a cache hit
```

When the candidate set is small, an exact scan beats an index. The query
planner makes that choice.

## Consistency

We design eventual consistency first. A query node reads flushed segments only,
never the WAL. Freshness is the flush interval plus the manifest propagation
time. The freshness math comes after the design.

Stronger consistency is a stretch goal with levels. The lowest level is read
after write for one client. The highest is global within a namespace. We
choose when a customer asks. The reader picks the level per query and pays in
dollars or in latency.

Each decision must state whether it makes a level impossible or very expensive.

| Decision | Read after write | Global in namespace |
| --- | --- | --- |
| Query nodes read flushed segments only | Open: the ack returns the WAL sequence, and the read waits for that commit or reads the WAL tail | Open: find the WAL tail, then read it. Costs round trips. |
| One WAL per namespace | Open | Open. A namespace has one total order. Sharding the WAL would make this level expensive. |
| Upsert resolves at flush | Open | Open |
| Cache keyed by segment | Open. The WAL tail has no index and gets a scan. The flush interval bounds its size. | Same |
| Dead sets | Open. A delete in the WAL tail adds to the dead set for that read. | Same |
| Node stays on the old commit while it warms | Open. A consistent read reads the WAL tail or moves the node. | Same |

No decision so far makes a level impossible. Keep one WAL and one flusher per
namespace. Conditional operations need the same order.

### Upsert ordering

An upsert or patch resolves in the flusher, not in the ingester. The flusher
applies the WAL in order and sees every earlier write to the doc. The
ingester reads flushed state only, so two patches in one flush window would
lose an update. The `(cluster_old, id)` tombstone for vectors also comes from
the flusher.

## Open questions

- CONSIDER(ali): what workload runs the B or C experiment?
- CONSIDER(ali): when rerank is on, where does it get full vectors, and does
  it cost a second round trip? Probe precision and rerank are config.
- CONSIDER(ali): new centroids in B and splits in C run on a config, in the
  background or at flush. Which signal starts them?

## Parked

- Branch merge. Explore it after the current decisions settle.
- The query planner: selectivity, scan or index, filter order.
- A prolly tree for the cluster map. It makes a branch diff cheap, which a
  merge needs.
- The B or C experiment.
- Freshness math for eventual consistency.
- Conditional delete and conditional patch. They are weak transactions. We
  revisit them when we have a better feel for the product. The flusher
  applies the WAL in order, so it can check a condition against the latest
  state. A sharded flusher per namespace would make them hard.

## Log

| Round | Topic | Outcome |
| --- | --- | --- |
| 1 | Read path, scale, branches | Object store holds the data. 200M target, 20B ceiling. Branches serve agents, CI, and snapshots. |
| 2 | Branch cost | Branch creation and namespace writes get rate limits. Metadata stays resident. |
| 3 | Filters, ids, docs | Part of a filter repeats. Monotonic `id`. Eventual consistency first. |
| 4 | Upsert, `Get`, id source | Write-time replace. Batch `Get`. `id` set at insert. |
| 5 | Merge, history, deletes | No merge for now. Git-like history. GC handles deletes. |
| 6 | History retention | TTL bounds history. One commit per flush. |
| 7 | Tokenizer, set size, round trips | Fixed tokenizer. `IN` up to 1,000. Bloom for misses. |
| 8 | Ranking | Match, then rank. Candidates under 100k. Track stats. |
| 9 | Ranges, phrases | No selectivity assumption. Proximity goes to ranking. |
| 10 | Vectors | Clusters, not graphs. Recall is config. Index cost goes to the customer. |
| 11 | B or C | User prefers C. B stays as a candidate. |
| 12 | C feasibility | Keep B and C, decide by experiment. Explore flush, map, and stale vectors. |
| 13 | Heavy upserts, experiment metric | Rate limit and price upserts. Metrics: read latency, then cost and write throughput. |
| 14 | Compaction | Runs on lost performance, on cheap VMs. Output id from input ids. GC counts references. |
| 15 | Swap warm-up | Serve the old commit while warming. Thresholds come from experiments. |
| 16 | Consistency | Stretch goal with levels, chosen per query and paid for. Each decision states its impact. Upserts resolve in the flusher. |
| 17 | Conditional operations | Weak transactions. Parked until we have a feel for the product. |
