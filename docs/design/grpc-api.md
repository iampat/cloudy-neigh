# gRPC API

**Status:** Draft — 2026-08-12 — v0

## Problem

cloudy-neigh stores documents made of vectors, text, and scalar attributes.
Clients write those documents and then find them again by vector similarity, by
text match, or by key. Real relevance usually needs more than one of those at
once, so the query path has to combine them rather than pick one.

This note defines the wire contract for both paths: the service shape, the
document model, the schema rules, the type of every attribute, and the query
message.

Three things make the contract awkward.

Attributes are dynamic, but the index is not. A client attaches whatever
attributes it likes, and the index cannot store one until it knows the type.

Vectors are the bulk of a document, and they come in several widths. A model may
emit `float32`, `float16`, or `int8`, and the index usually wants none of those.

The contract outlives the engine. Storage, index format, and planner all get
replaced. The messages a client compiled against do not.

This is v0. Breaking the contract before v1 is acceptable, so a decision here is
cheap to revisit. After v1 it is not.

## Goals

- A simple, ergonomic API.
- One query call covers key lookup, vector search, text search, and any
  combination of them.
- The API is optimized for client simplicity, not for storage or index
  performance. The internal systems that serve it stay simple and cheap.
- IDs support range queries.
- User data schemas evolve with backward and forward compatibility, following the
  same practice as protobuf itself.
- Adding a rank function, a filter predicate, or a fusion strategy never reshapes
  a request.

## Non-goals

Planned for a later version, and shaped for by the messages below:

- Patch, delete, and delete-by-filter operations.
- Client-streaming writes for large batches.
- Aggregations, grouping, and counting.
- Computed attributes and result highlighting.
- Sparse vectors and late-interaction retrieval.
- Query-time embedding, where the server runs the model.
- Cross-encoder reranking, and weighted fusion beside rank fusion.
- A string query language that compiles to the query messages.
- A plan-only `Explain` call.
- Snapshot pagination over a retained version.

Out of scope entirely: the storage engine, the index format, the query planner,
the cost model, sharding, replication, and authentication. The note names a
constraint from those areas only where it changes the contract.

## Model

A namespace is a logical boundary, like a table or a collection. It holds
documents. A document has one ID, zero or more named vector columns, and a flat
set of attributes.

```
  namespace "products"
  ┌────────────────────────────────────────────────────────┐
  │  document                                              │
  │  ┌──────────┬─────────────────┬──────────────────────┐ │
  │  │    id    │  vector columns │      attributes      │ │
  │  │  string  │  title: [f32]   │  name: "foo"         │ │
  │  │          │  image: [f32]   │  tags: ["a", "b"]    │ │
  │  └──────────┴─────────────────┴──────────────────────┘ │
  └────────────────────────────────────────────────────────┘
```

A vector column is an attribute with a vector type and an index. The model shows
it separately because its schema carries an index configuration that a scalar
attribute does not need.

### Schema evolution

There is no create-namespace call and no migration call. A namespace appears on
its first write. A schema change applies on the first write that carries it, if it
passes four rules.

1. A new attribute is added automatically. It reads as null on every document
   written before it.
2. The type of an existing attribute never changes. A write that changes one
   fails.
3. A vector column is declared before use. Scalar types are inferred from the
   data; vector types are not.
4. Index configuration is fixed once set. A tokenizer, a distance metric, a
   quantization mode, and a match index cannot change in place, because the index
   on disk is a product of them. Changing one requires a reindex.

Rules 2 and 4 are the ones that bite. Both mean a mistake costs a rewrite of the
namespace rather than an update.

## Service

```proto
service Index {
  rpc Write(WriteRequest) returns (WriteResponse);
  rpc Query(QueryRequest) returns (QueryResponse);
}
```

Write and Query stay separate because they have different cost profiles, different
consistency needs, and different scaling limits. A combined call would force both
through the same quota and the same timeout.

## Write path

### Write operations

A write carries exactly one operation.

```proto
message WriteRequest {
  string namespace = 1;

  message Upsert {
    repeated Document documents = 1;
  }

  oneof operation {
    Upsert upsert = 2;
  }
}
```

Patch, delete, and delete-by-filter join the same `oneof` later. Each wrapper
message then holds its own options, such as a condition or a partial-completion
flag.

Every operation is idempotent, and that is the default we design toward. A batch
applies to every document or to none, so a client can always retry. If a future
operation has to break idempotency, this note says so loudly at that point.

### ID

An ID is a string. It supports four lookups: exact key, range, prefix, and glob.

Prefix lookup is a range, so `doc:` becomes `["doc:", "doc;")`. A glob that starts
with a wildcard has no prefix to seek to, so it scans the range the rest of the
filter leaves.

The engine compares IDs byte by byte and never interprets them. String is the only
ID type we support. A client holding a typed key encodes it into a string that
keeps the order of the source type, and owns that encoding.

| Source type | Encoding | Ordered | Readable |
| --- | --- | --- | --- |
| Text | as written | yes | yes |
| Integer | zero-padded decimal | yes | yes |
| Float | order-preserving bit transform, then fixed-width hex | yes | no |
| Bytes, UUID | base32hex, or hex | yes | no |

```
  encode(uint64 9)   →  "0000000000000009"   ┐ string order matches
  encode(uint64 10)  →  "0000000000000010"   ┘ numeric order

  "9"                →  "9"                  ┐ string order does not
  "10"               →  "10"                 ┘ match numeric order
```

A float needs its sign bit flipped when positive and all bits flipped when
negative, then renders as 16 hex characters. A byte string needs an alphabet whose
characters are already in ASCII order. base32hex from RFC 4648 uses `0-9A-V` and
qualifies; hex costs 2x, base32hex 1.6x.

Readability is the reason for choosing a string, and it survives for text and
integer keys only. A hex-encoded float or UUID is opaque, so those cases pay the
expansion and get nothing back. Within the 64-byte limit, hex holds 32 raw bytes
and base32hex holds 40, which is enough for a UUID either way. If binary keys turn
out to dominate, `bytes` is the better home, and v0 is the window to move.

Two limits worth stating. String order is UTF-8 byte order, which equals code
point order but not linguistic collation, so `"Z"` sorts before `"a"`. A composite
key needs escaping, because `"ab"` is a prefix of `"abc"`.

An ID is limited to 64 bytes. The limit bounds the ID dictionary, which is a
memory control rather than a style rule.

### Attributes

An attribute holds a scalar, a timestamp, or a homogeneous list. Nested attributes
are intentionally not supported at this stage.

```proto
message Value {
  oneof kind {
    google.protobuf.NullValue null = 1;
    string text = 2;
    int64 int = 3;
    uint64 uint = 4;
    double real = 5;
    bool boolean = 6;
    google.protobuf.Timestamp timestamp = 7;
    bytes blob = 8;
    StringList text_list = 9;
    IntList int_list = 10;
  }
}
```

A column store indexes columns, and a nested object has no column identity until
something flattens it to a path. A client can flatten as well as the server can. A
flat model also keeps `Value` free of recursion, which removes a depth limit and a
parser attack surface, and it makes every attribute addressable by name in a
filter.

Lists stay, because a list of strings is the most common filter target in a search
index. Explicit list variants keep `Value` free of recursion, which a
`repeated Value` would reintroduce.

The explicit null matters for patch. A patch must distinguish "set this attribute
to null" from "leave this attribute alone". Presence in the attribute map answers
the second, and `NullValue` answers the first.

`Value` is the most frequent message in the system, with one instance per
attribute per document. Its variants stay inside field numbers 1 to 15, where a
tag costs one byte instead of two. Six numbers remain, so a later variant has to
earn one.

A client with nested source data flattens it before the write. A client that needs
the original shape stores it in an unindexed `bytes` attribute.

### Vectors

A namespace holds any number of vector columns. Each one is named, declared in the
schema, and carries its own distance metric.

```proto
message VectorColumn {
  uint32 dimensions = 1;
  DistanceMetric metric = 2;
  VectorType original_type = 3;
  Quantization index_quantization = 4;
  bool store_original = 5;
}

enum VectorType {
  VECTOR_TYPE_UNSPECIFIED = 0;
  VECTOR_TYPE_FLOAT32 = 1;
  VECTOR_TYPE_FLOAT16 = 2;
  VECTOR_TYPE_INT8 = 3;
}
```

Naming every column keeps one rule for all of them. A column with a special name
would need its own case in type inference, schema validation, query ranking, and
patch behavior. The cost is that a client cannot create a vector column by writing
data alone. An operator quota bounds the column count, since each column carries
its own index and a filterable attribute is indexed once per index.

A per-column metric follows from per-column embeddings. Two columns can hold
output from two models with different geometry, and a namespace-wide metric would
force a client to split one logical collection across two namespaces.

A vector crosses the wire as `repeated float`. `float32` holds every value of
`int8`, `float16`, and `bfloat16` without loss, so the wire type is a lossless
container for every narrower format. The cost is bandwidth, not precision. The
arrangement is proven: turbopuffer stores a column as `[512]f16` and still moves
`float32` on the wire. Storage width therefore stays a schema concern, and a
future narrower format needs no wire change.

`original_type` names the width the client's values actually have. It lets the
stored copy keep that width instead of paying 4x for values that were never
`float32`. It also marks the case where a producer already chose the
representation, which pairs with `QUANTIZATION_NATIVE` below.

A vector column has two representations, and they answer different questions.

```
  text attribute              vector attribute
  ┌──────────────┐            ┌──────────────┐
  │  "the cat"   │            │  [0.1, 0.9]  │
  └──┬────────┬──┘            └──┬────────┬──┘
     │        │                  │        │
     ▼        ▼                  ▼        ▼
  postings  stored           ANN index  stored
  (lossy)   (exact)          (lossy)    (exact)
```

The index copy is lossy and rebuildable, and it only affects recall, which is
approximate by construction. The stored copy is the client's data. A text index
already has this split, because posting lists are a lossy transformation of the
source text and the stored attribute holds the original.

`store_original` defaults to false. An exact second copy costs storage on every
document, and most clients keep their vectors elsewhere or regenerate them from
source. A client that needs its exact values back turns it on and pays for it. By
default a vector is a derived artifact, and a read returns the index
representation.

Quantization has three possible owners. The server owns it by default. A client
that sends a pre-quantized vector owns it. A model that emits `int8` owns it, and
then neither the server nor the client may quantize that output again.
`QUANTIZATION_NATIVE` records the third case.

A server that owns quantization owns the recall budget, so it owes the client a
way to measure recall.

### Text analysis

Each text attribute declares its own analysis and its own match indexes.

```proto
message TextIndex {
  Tokenizer tokenizer = 1;
  Language language = 2;
  bool glob = 3;
  bool regex = 4;
  bool fuzzy = 5;
}

enum Tokenizer {
  TOKENIZER_UNSPECIFIED = 0;
  TOKENIZER_WORD_V1 = 1;
  TOKENIZER_PRE_TOKENIZED = 2;
}
```

A title, a body, and a product code need different analysis, and a multilingual
corpus needs a different language per attribute.

The `glob`, `regex`, and `fuzzy` flags each build a separate index. A query cannot
use a predicate the attribute did not declare, and rule 4 fixes the choice at
first write.

A tokenizer name carries a version. Tokenization is an on-disk contract, because
every posting list is a product of it. A tokenizer cannot change in place. It can
only gain a new version, and an existing namespace keeps the old version until a
reindex. An unversioned name would be a permanent commitment to today's behavior.

`TOKENIZER_PRE_TOKENIZED` lets a client supply tokens directly. A client that owns
its analysis pipeline should not have to reimplement it inside our tokenizer
names.

### One pipeline for write and query

A query must use the same analysis pipeline as the write that built the index.

A tokenizer that stems at write time but not at query time returns no match for a
stemmed term, and the failure is silent. The same rule holds for an embedding
model. A `float32` query vector cannot rank against a column that a model
quantized to `int8`, because the client does not hold the calibration.

This is one invariant with two instances. Both a tokenizer and an embedding model
carry a version for this reason.

## Query path

### The query tree

A query is a tree of nodes. A node either retrieves documents or fuses the output
of other nodes.

```proto
message QueryRequest {
  string namespace = 1;
  QueryNode query = 2;
  Projection projection = 3;
  Consistency consistency = 4;
  Page page = 5;
  bool explain = 6;
}

message QueryNode {
  string name = 1;
  uint32 top_k = 2;

  oneof kind {
    Retrieve retrieve = 3;
    Fusion fusion = 4;
  }
}

message Retrieve {
  Filter filter = 1;
  RankBy rank_by = 2;
}

message Fusion {
  repeated QueryNode inputs = 1;

  oneof strategy {
    ReciprocalRankFusion rrf = 2;
  }
}
```

One shape covers every query. A key lookup is a single retrieve node. A hybrid
query is a fusion over two retrieves. A nested query is a fusion whose input is
another fusion.

```
  fusion "hybrid"                      top_k 10
  ├── retrieve "semantic"  vector      top_k 100
  └── fusion "keyword"                 top_k 100
      ├── retrieve "title"  text       top_k 200
      └── retrieve "body"   text       top_k 200
```

`top_k` on a node is how many rows that node emits. The root node's `top_k` is
therefore the number of rows returned, and an inner node's `top_k` is a candidate
count feeding its parent. A client raises recall by widening the inner nodes
without touching the result count.

`name` labels a node. Fusion destroys the evidence of why a row landed where it
did, so the response reports each node's rank per row under this name.

The query is typed. A client builds messages, not strings, so the server needs no
grammar, no parser, and no error positions, and a malformed query fails at compile
time. A string language stays possible later and compiles to these same messages
on either side.

### Ranking

```proto
message RankBy {
  oneof kind {
    VectorSearch vector = 1;
    TextSearch text = 2;
    AttributeOrder attribute = 3;
  }
}

message VectorSearch {
  string column = 1;
  repeated float vector = 2;
}

message TextSearch {
  string attribute = 1;
  string query = 2;
}

message AttributeOrder {
  string attribute = 1;
  Direction direction = 2;
}
```

A retrieve with a filter and no `rank_by` is a plain lookup, returned in ID order.

`VectorSearch` carries no distance metric, because the column declares it.
`TextSearch` carries no tokenizer, because the attribute declares it. An
index-time choice stays where the index was configured, and a query never restates
it.

A retrieve returns at most `top_k` rows, never exactly `top_k`. Approximate
nearest neighbor search walks a graph built without knowledge of the filter, so a
selective filter leaves fewer surviving candidates and the rows that come back may
not be the true nearest neighbors. The contract promises a bound, not a count.

### Filters

```proto
message Filter {
  message Group {
    repeated Filter filters = 1;
  }

  oneof kind {
    Compare compare = 1;
    Group all = 2;
    Group any = 3;
    Filter not = 4;
  }
}

message Compare {
  string attribute = 1;

  oneof predicate {
    Value eq = 2;
    Value not_eq = 3;
    Value lt = 4;
    Value lte = 5;
    Value gt = 6;
    Value gte = 7;
    ValueList in = 8;
    ValueList not_in = 9;
    ValueList contains_any = 10;
    ValueList contains_all = 11;
    string prefix = 12;
    string glob = 13;
    string regex = 14;
    Fuzzy fuzzy = 15;
  }
}

message Fuzzy {
  string term = 1;
  uint32 max_edits = 2;
}
```

The predicate is a `oneof` of typed fields rather than an operator enum beside a
value list. An enum would let a client send `eq` with three values, or `in` with
none, and the server would reject both at run time. This shape cannot express
either. The cost is one field per predicate instead of one shared field.

`Value` is the write-path type, so a filter compares against the same values a
write stores. The fourteen predicates fill field numbers 2 to 15 exactly, so a
fifteenth costs a two-byte tag.

`glob`, `regex`, and `fuzzy` require the attribute to declare the matching index.
A query that uses one against an attribute without it fails rather than falling
back to a scan, because a silent scan over a large namespace is worse than an
error.

### Fusion

```proto
message ReciprocalRankFusion {
  uint32 k = 1;
}
```

Reciprocal rank fusion is the only strategy in v0, and it is the right first one
because it consumes ranks rather than scores. A vector distance and a BM25 score
have different units and opposite directions, so any strategy that adds them needs
normalization the client has to tune. Ranks need none.

Weighted fusion joins the `oneof` in `Fusion` when a client needs to bias one
input.

Fusion combines ranked lists. Reranking re-scores one candidate list with a more
expensive model, usually a cross-encoder. They are different operations, so the
word `rerank` stays reserved for the second.

### Projection

```proto
message Projection {
  oneof kind {
    bool all = 1;
    AttributeNames include = 2;
    AttributeNames exclude = 3;
  }
}
```

An unset projection returns every attribute except vector columns. Vectors
dominate response size, and `store_original` defaults to false, so a vector often
cannot be returned exactly anyway. A client that wants one asks for it.

Returning the ID alone is cheaper and teaches the cost immediately, but a first
query then returns what reads as an empty document, and a client has to discover
the projection field before the API does anything useful.

### Response

```proto
message QueryResponse {
  repeated Match matches = 1;
  bytes next_cursor = 2;
  Explanation explanation = 3;
}

message Match {
  Document document = 1;
  float score = 2;
  repeated NodeRank node_ranks = 3;
}

message NodeRank {
  string name = 1;
  uint32 rank = 2;
  float score = 3;
}
```

`score` is a field. A score does not belong in the attribute map under a reserved
name such as `$dist`, because a typed response has somewhere better to put it and
a `$` prefix only exists to dodge collisions in an untyped map.

`score` is comparable within one response and meaningless across two. A vector
distance depends on the query vector, and a BM25 score depends on corpus
statistics that change with every write.

`node_ranks` is populated for a query with more than one node.

### Explain

```proto
message Explanation {
  repeated NodeExplanation nodes = 1;
}

message NodeExplanation {
  string name = 1;
  uint64 candidates_examined = 2;
  uint64 candidates_returned = 3;
  bool exact = 4;
  uint64 duration_micros = 5;
}
```

`explain` on the request returns the explanation beside the results, describing
the execution that actually happened. This matters more than a plan, because the
failures that hurt a hybrid query are runtime properties: recall collapsing under
a selective filter, or one input dominating fusion. A plan cannot show either.
`exact` reports whether a retrieve ran exactly or approximately.

A separate plan-only `Explain` call, which describes a query without running it,
is worth adding once the planner has choices worth inspecting. Today it would
report what the request already says.

### Pagination

```proto
message Page {
  uint32 size = 1;
  bytes cursor = 2;
}
```

A client resends the whole request with the cursor. The cursor holds a position
and a fingerprint of the query, not the query itself. A cursor that carried the
query would reach kilobytes for a query with a 768-dimension vector, and a
server-side cursor would need storage, an expiry, and an answer for what happens
after failover. A fingerprint that does not match the request fails with
`INVALID_ARGUMENT`, so a changed filter mid-scan is loud rather than silent.

A cursor works for a retrieve ordered by ID or by an attribute, because those
orders are total. A ranked retrieve returns `top_k` rows and no cursor. Scores
shift as documents are written and approximate search has no stable total order,
so a cursor over a ranked result would skip and repeat rows.

Pagination requires strong consistency. Two pages served under eventual
consistency can land on replicas with different staleness, and the pages then
disagree about the same document.

The guarantee is this: a document that does not change during the scan is returned
exactly once. A document written into an already-visited range is missed, and a
document updated so that it moves ahead of the cursor can appear twice. Snapshot
pagination fixes both and needs a retained version, which is a later feature.

### Consistency

```proto
enum Consistency {
  CONSISTENCY_UNSPECIFIED = 0;
  CONSISTENCY_STRONG = 1;
  CONSISTENCY_EVENTUAL = 2;
}
```

Strong is the default, and it sees every write that completed before the query
started. Eventual trades that for throughput and may miss recent writes.

Read-your-writes works by default. A client opts out of it deliberately.
