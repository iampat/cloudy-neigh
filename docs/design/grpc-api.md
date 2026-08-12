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

Planned for a later version. Of these, only the write operations reserve field
numbers today:

- Patch, delete, and delete-by-filter operations.
- Client-streaming writes for large batches.
- Aggregations, grouping, and counting.
- Computed attributes and result highlighting.
- Sparse vectors and late-interaction retrieval.
- Query-time embedding, where the server runs the model.
- Cross-encoder reranking, and weighted fusion beside rank fusion.
- A string query language that compiles to the query messages.
- Snapshot pagination over a retained version.

Out of scope entirely: the storage engine, the index format, the query planner,
the cost model, sharding, replication, and authentication. The note names a
constraint from those areas only where it changes the contract.

## Conventions

A planned message appears empty. It reserves the name and the field number, and
the server returns `UNIMPLEMENTED` until the message is specified. An empty
message is deliberate. It says the shape is decided and the detail is not.

A field arrives when its semantics can be defended, not when they can be guessed.
v0 allows a breaking change, but a field that ships wrong already has clients
depending on it.

v0 states no limits. A limit in a contract is a promise, and every limit we could
write today is a guess. An operator sets what a deployment carries. A real limit
arrives with the measurement behind it.

No field name is a keyword in Go, Python, or TypeScript. The Python protobuf
runtime does not rename a colliding field, so `f.not` does not parse.

## Model

A namespace is a logical boundary, like a table or a collection. It holds
documents. A document has one ID and a flat set of named attributes.

```
  namespace "products"
  ┌────────────────────────────────────────────────────────┐
  │  document                                              │
  │  ┌──────────┬───────────────────┬────────────────────┐ │
  │  │    id    │ vector attributes │ scalar attributes  │ │
  │  │  string  │  title: [f32]     │  name: "foo"       │ │
  │  │          │  image: [f32]     │  tags: ["a", "b"]  │ │
  │  └──────────┴───────────────────┴────────────────────┘ │
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
3. A vector column is declared before use, in the `schema` field of a write.
   Scalar types are inferred from the data; vector types are not.
4. Index configuration is fixed once set. A tokenizer, a distance metric, a
   quantization mode, and a match index cannot change in place, because the index
   on disk is a product of them. Changing one requires a reindex.

Rules 2 and 4 are the expensive ones. Both mean a mistake costs a rewrite of the
namespace rather than an update.

## Service

```proto
service Index {
  rpc Write(WriteRequest) returns (WriteResponse);
  rpc Query(QueryRequest) returns (QueryResponse);
  rpc Plan(QueryRequest) returns (PlanResponse);
}
```

Write and Query stay separate because they have different cost profiles, different
consistency needs, and different scaling limits. A combined call would force both
through the same quota and the same timeout.

`Plan` takes the same `QueryRequest` as `Query`, so a client plans exactly what it
would send. The call runs nothing.

A failure returns a gRPC status code. Structured detail travels in the status
details, and no response message carries a status field. This note names a code
only where the choice is not obvious.

### Namespaces

Namespace administration has no messages yet. Four operations are required:

- **List.** Enumerate the namespaces an account holds.
- **Delete.** Drop a namespace and its data.
- **Branch.** Copy a namespace cheaply, so the copy and the source diverge from a
  shared starting point. A snapshot is a branch nobody writes to, so branching
  covers both.
- **Archive.** Mark a namespace read only.

Create is not one of them. A namespace appears on its first write, so a separate
call would only duplicate what a write already does.

CONSIDER(ali): what does namespace administration look like? A delete is either
synchronous or a background reclaim the client polls. A namespace name needs a
character rule, since it addresses stored data. A branch needs a story for what
the two namespaces share on disk and what happens when one of them is deleted.

## Write path

### Write operations

A write carries exactly one operation.

```proto
message WriteRequest {
  string namespace = 1;
  map<string, AttributeSchema> schema = 6;

  message Upsert {
    repeated Document documents = 1;
  }
  message Patch {}
  message Delete {}
  message DeleteByFilter {}

  oneof operation {
    Upsert upsert = 2;
    Patch patch = 3;
    Delete delete = 4;
    DeleteByFilter delete_by_filter = 5;
  }
}

message AttributeSchema {
  oneof kind {
    VectorColumn vector = 1;
    TextIndex text = 2;
  }
}

message WriteResponse {}
```

`Patch`, `Delete`, and `DeleteByFilter` hold the field numbers and nothing else.
Each one gains its own options when it is specified, such as a condition or a
partial-completion flag.

`schema` is where a declaration lives. It is the only carrier for a
`VectorColumn` or a `TextIndex`, because there is no create-namespace call. The
server validates it against the four rules before it applies the operation, and
rejects the whole request if a rule fails. A declaration that repeats the stored
one unchanged passes, so a client can send the same schema on every write.

`WriteResponse` is empty, and it is the one exception to the placeholder
convention above: the RPC is implemented, and a successful write tells a client
nothing it did not already know. A partial-completion report arrives with the
patch and delete operations that need one.

Every operation is idempotent, and that is the default we design toward. A batch
applies to every document or to none, so a client can always retry. A future
operation that cannot hold idempotency states the exception where it is defined.

### Document

A document is an ID, a map of scalar attributes, and a map of vector attributes.

```proto
message Document {
  string id = 1;
  map<string, Value> attributes = 2;
  map<string, Vector> vectors = 3;
}

message Vector {
  repeated float values = 1;
}
```

A vector is an attribute, and it still sits outside `Value`. A vector is the only
attribute that carries its own index configuration and its own storage split, so
it needs a schema entry and a stored-copy decision that no scalar needs. Keeping
it out of `Value` also keeps `Value` small, which matters because a document
holds one `Value` per attribute.

The two maps share one set of names. A name used in `vectors` cannot appear in
`attributes`, so a filter, a projection, and a rank address an attribute by one
name whatever its type.

### ID

An ID is a string. It supports four lookups: exact key, range, prefix, and glob.

Prefix lookup is a range, so `doc:` becomes `["doc:", "doc;")`. A glob that starts
with a wildcard has no prefix to seek to, so it scans the range the rest of the
filter leaves.

The engine compares IDs byte by byte and never interprets them. String is the only
ID type we support. A client holding a typed key encodes it into a string that
keeps the order of the source type, and is responsible for that encoding.

| Source type | Encoding | Ordered | Readable |
| --- | --- | --- | --- |
| Text | as written | yes | yes |
| Unsigned integer | zero-padded decimal | yes | yes |
| Signed integer | bias by 2^63, then zero-padded decimal | yes | no |
| Float | order-preserving bit transform, then fixed-width hex | yes | no |
| Bytes, UUID | unpadded base32hex, or hex | yes | no |

```
  encode(uint64 9)   →  "0000000000000009"   ┐ string order matches
  encode(uint64 10)  →  "0000000000000010"   ┘ numeric order

  "9"                →  "9"                  ┐ string order does not
  "10"               →  "10"                 ┘ match numeric order
```

A signed integer needs a bias of 2^63 before padding, because a leading `-` sorts
before every digit and `-5` would otherwise land after `3`. The bias maps the
whole range onto `uint64` and restores the order, at the cost of a key no reader
can decode by eye.

A float needs its sign bit flipped when positive and all bits flipped when
negative, then renders as 16 hex characters. A byte string needs an alphabet whose
characters are already in ASCII order. base32hex from RFC 4648 uses `0-9A-V` and
qualifies, without the `=` padding, which sorts between `9` and `A` and breaks
the order for a key shorter than the block size. Hex costs 2x, base32hex 1.6x.

Readability is the reason for choosing a string, and it survives for text and
unsigned integer keys only. A hex-encoded float or UUID is opaque, so those cases
pay the expansion and get nothing back. If binary keys turn out to dominate,
`bytes` is the better home, and that change has to land before v1.

Two consequences worth stating. String order is UTF-8 byte order, which equals
code point order but not linguistic collation, so `"Z"` sorts before `"a"`. A
composite key needs escaping, because `"ab"` is a prefix of `"abc"`.

`"id"` is a reserved attribute name, and a client cannot use it for an attribute
of its own. `Compare` addresses it like any other attribute, so an exact, range,
prefix, or glob lookup on an ID uses the ordinary filter path and needs no
message of its own.

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

message StringList {
  repeated string values = 1;
}

message IntList {
  repeated int64 values = 1;
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
tag costs one byte instead of two. Five numbers remain, so a later variant takes
one only if no existing variant can carry it.

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

enum Quantization {
  QUANTIZATION_UNSPECIFIED = 0;
  QUANTIZATION_NATIVE = 1;
}

enum DistanceMetric {
  DISTANCE_METRIC_UNSPECIFIED = 0;
  DISTANCE_METRIC_COSINE = 1;
  DISTANCE_METRIC_EUCLIDEAN_SQUARED = 2;
  DISTANCE_METRIC_DOT_PRODUCT = 3;
}
```

The Euclidean metric is squared. The square root is monotonic, so it changes no
ranking and costs one operation per comparison.

Naming every column keeps one rule for all of them. A column with a special name
would need its own case in type inference, schema validation, query ranking, and
patch behavior. The cost is that a client cannot create a vector column by writing
data alone. Each column also carries its own index, so column count drives index
size.

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

The approximate nearest neighbor (ANN) index copy is lossy and rebuildable, and
it only affects recall, which is approximate by construction. The stored copy is
the client's data. A text index already has this split, because posting lists are
a lossy transformation of the source text and the stored attribute holds the
original.

`store_original` defaults to false. An exact second copy costs storage on every
document, and most clients keep their vectors elsewhere or regenerate them from
source. A client that needs its exact values back turns it on and pays for it. By
default a vector is a derived artifact, and a read returns the index
representation.

Quantization happens in one of three places. The server performs it by default. A
client that sends a pre-quantized vector has already performed it. A model that
emits `int8` has already performed it, and then neither the server nor the client
may quantize that output again. `QUANTIZATION_UNSPECIFIED` leaves the choice to
the server, and `QUANTIZATION_NATIVE` records the third case.

The enum names no mode. A mode name is a recall promise, and it waits on a recall
benchmark that can hold one mode against another.

A server that performs the quantization determines the recall, so it owes the
client a way to measure that recall.

### Text analysis

Each text attribute declares its own analysis and its own match indexes.

```proto
message TextIndex {
  Tokenizer tokenizer = 1;
  string language = 2;
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

`language` is a string rather than an enum. An enum is a commitment to a stemmer
set, and the accepted values wait on the stemmers we ship.

The `glob`, `regex`, and `fuzzy` flags each build a separate index. A query cannot
use a predicate the attribute did not declare, and rule 4 fixes the choice at
first write.

A tokenizer name carries a version. Tokenization is an on-disk contract, because
every posting list is a product of it. A tokenizer cannot change in place. It can
only gain a new version, and an existing namespace keeps the old version until a
reindex. An unversioned name would be a permanent commitment to today's behavior.

`TOKENIZER_PRE_TOKENIZED` lets a client supply tokens directly. A client that runs
its own analysis pipeline should not have to reimplement it inside our tokenizer
names.

### Analysis versioning

A query must use the same analysis pipeline as the write that built the index.

A tokenizer that stems at write time but not at query time returns no match for a
stemmed term, and the server reports no error. The same rule holds for an
embedding model. A `float32` query vector ranks correctly against a column the
server quantized, because the server holds both sides of the calibration. It
cannot rank against a column the client or its model quantized, because there the
client holds the calibration and the server does not.

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
  bool profile = 6;
  bool explain = 7;
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

A retrieve ranks by one of three things: vector similarity, text relevance, or an
attribute order.

```proto
message RankBy {
  oneof kind {
    VectorSearch vector = 1;
    TextSearch text = 2;
    AttributeOrder attribute = 3;
  }
}

message VectorSearch {
  string attribute = 1;
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

enum Direction {
  DIRECTION_UNSPECIFIED = 0;
  DIRECTION_ASCENDING = 1;
  DIRECTION_DESCENDING = 2;
}
```

Every rank names an `attribute`, because a vector column is an attribute with a
vector type. One term and one field name cover all three cases.

A retrieve with a filter and no `rank_by` is a plain lookup, returned in ID order.

`VectorSearch` carries no distance metric, because the vector column declares it.
`TextSearch` carries no tokenizer, because the attribute declares it. An
index-time choice stays where the index was configured, and a query never restates
it.

An exact retrieve returns exactly `top_k` rows when at least `top_k` rows match.
An approximate retrieve returns at most `top_k`. ANN search selects its candidates
without knowledge of the filter, so a selective filter can leave fewer surviving
candidates than `top_k`, and the rows that come back may not be the true nearest
neighbors.

### Filters

A filter is a tree of boolean groups whose leaves compare one attribute against
one typed predicate.

```proto
message Filter {
  message Group {
    repeated Filter filters = 1;
  }

  oneof kind {
    Compare compare = 1;
    Group all_of = 2;
    Group any_of = 3;
    Group none_of = 4;
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
    ValueList is_in = 8;
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
}

message ValueList {
  repeated Value values = 1;
}
```

`none_of` holds a group, so one node negates a set rather than a single filter.
The three group cases then share one shape, and a client negating one filter
passes a group of one.

The predicate is a `oneof` of typed fields rather than an operator enum beside a
value list. An enum would let a client send `eq` with three values, or `is_in`
with none, and the server would reject both at run time. This shape cannot express
either. The cost is one field per predicate instead of one shared field.

`Value` is the write-path type, so a filter compares against the same values a
write stores. The fourteen predicates fill field numbers 2 to 15 exactly, so a
fifteenth costs a two-byte tag.

`Fuzzy` carries the term alone. An edit bound waits on the edit distance we
adopt, because a budget means nothing until the rule that counts edits is fixed.

`glob`, `regex`, and `fuzzy` require the attribute to declare the matching index.
A query that uses one against an attribute without it fails rather than falling
back to a scan. The fallback would read the whole namespace at a cost the request
does not show.

`prefix` needs no declared index and works on any string attribute, not only on
the ID. A prefix is a range over a sorted attribute, so the ordinary attribute
index serves it.

### Fusion

A fusion node merges the ranked lists of its inputs into one ranked list.

```proto
message ReciprocalRankFusion {
  optional uint32 k = 1;
}
```

Reciprocal rank fusion (RRF) is the only strategy in v0, and it is the right first
one because it consumes ranks rather than scores. A vector distance and a BM25
(Best Matching 25) score have different units and opposite directions, so any
strategy that adds them needs normalization the client has to tune. Ranks need
none.

RRF scores a document as the sum of `1 / (k + rank)` over every input that
returned it. A rank is 1-based. The join key is the document ID. An input that did
not return the document contributes nothing for it, rather than a rank one past
the end of that input's list. `Match.score` under a fusion root holds the fused
value.

`k` stays in v0, because the formula fixes its meaning without an engine behind
it. It is `optional`, and unset means 60, the value published with the original
method. A plain `uint32` cannot carry that default, because an unset field and a
zero are the same bytes on the wire and `k = 0` is a legal parameter.

Weighted fusion joins the `oneof` in `Fusion` when a client needs to bias one
input.

Fusion combines ranked lists. Reranking re-scores one candidate list with a more
expensive model, usually a cross-encoder. They are different operations, so the
word `rerank` stays reserved for the second.

### Projection

A projection names the attributes a match carries back, either as an include list
or as an exclude list.

```proto
message Projection {
  oneof kind {
    google.protobuf.Empty all = 1;
    AttributeNames include = 2;
    AttributeNames exclude = 3;
  }
}

message AttributeNames {
  repeated string names = 1;
}
```

`all` holds `Empty` rather than `bool`. Setting a `bool` to false still selects
the case, so `all = false` and `all = true` would request the same thing.

An unset projection returns every attribute except vector columns. Vectors
dominate response size, and `store_original` defaults to false, so a vector often
cannot be returned exactly anyway. A client that wants one asks for it.

We rejected an ID-only default, which is cheaper but returns what reads as an
empty document until the client finds the projection field.

### Response

A match carries the document, one score, and the evidence of how it ranked.

```proto
message QueryResponse {
  repeated Match matches = 1;
  bytes next_cursor = 2;
  PlanNode profile = 3;
}

message Match {
  Document document = 1;
  float score = 2;
  repeated NodeRank node_ranks = 3;
  ScoreExplanation explanation = 4;
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

`node_ranks` is populated for a query with more than one node, free and always on.
It is the cheap subset of what `explain` returns per match.

`explanation` is one field rather than a repeated one, because `details` already
nests the contribution of every node under a single root. The server fills it only
for `explain: true`.

### Plan, profile, and explain

Three questions about a query have three separate answers.

| Answer | Question | When | Scope |
| --- | --- | --- | --- |
| Plan | What will the optimizer do? | Before execution | Whole query |
| Profile | What did execution cost? | After execution | Per node |
| Explain | Why is this row at this score? | After execution | Per match |

```proto
message PlanResponse {
  PlanNode root = 1;
}

message PlanNode {
  string name = 1;
  string operation = 2;
  repeated string indexes = 3;
  uint64 estimated_candidates = 4;
  Actuals actuals = 5;
  repeated PlanNode inputs = 6;
}

message Actuals {
  uint64 candidates_examined = 1;
  uint64 rows_emitted = 2;
  bool exact = 3;
  uint64 duration_micros = 4;
}
```

`name` is the node name from the request, so a plan node lines up with a query
node and with `node_ranks`. `exact` reports whether a retrieve ran exactly or
approximately.

`operation` and `indexes` carry server-defined strings. This note names no
operation and no index, because both belong to an executor that does not exist
yet.

Profile reuses the plan's node tree and fills in `actuals`. `Plan` returns the
estimates alone, and `profile: true` returns the estimates beside the actuals in
the same node. That pairing is what makes a bad estimate visible, and a separate
profile message would leave a reader to align two trees by hand.

Profile answers more of what degrades a hybrid query than a plan does, because
those failures are runtime properties: recall collapsing under a selective filter,
or one input dominating fusion.

A plan is a prediction, not a promise. Cache state and index state move, so the
executor can choose differently a second later.

```proto
message ScoreExplanation {
  double value = 1;
  string description = 2;
  repeated ScoreExplanation details = 3;
}
```

`ScoreExplanation` is deliberately loose, and it is the one place in this contract
where typing everything is wrong. A strictly typed breakdown turns every scoring
formula change into a proto change and a client break, for output no program
should branch on. It is diagnostic text for a human.

Per-match explanation is expensive, because it holds the scoring intermediates of
every returned row until the response is built. It stays opt-in for that reason.

### Pagination

Pagination is cursor-based, and the cursor lives in the request.

```proto
message Page {
  bytes cursor = 1;
}
```

`Page` carries the cursor alone, because the root node's `top_k` already sets the
number of rows returned and a second size field would contradict it.

A client resends the whole request with the cursor. The cursor holds a position
and a fingerprint of the query, not the query itself. A cursor that carried the
query would reach kilobytes for a query with a 768-dimension vector, and a
server-side cursor would need storage, an expiry, and an answer for what happens
after failover. A fingerprint that does not match the request fails with
`INVALID_ARGUMENT`, so a filter changed mid-scan is rejected rather than served.

A cursor works for a retrieve ordered by ID or by an attribute, because those
orders are total. A ranked retrieve returns up to `top_k` rows and no cursor.
Scores shift as documents are written and approximate search has no stable total
order, so a cursor over a ranked result would skip and repeat rows.

Pagination requires strong consistency. Two pages served under eventual
consistency can land on replicas with different staleness, and the pages then
disagree about the same document.

The guarantee is this: a document that does not change during the scan is returned
exactly once. A document written into an already-visited range is missed, and a
document updated so that it moves ahead of the cursor can appear twice. Snapshot
pagination fixes both and needs a retained version, which is a later feature.

### Consistency

Strong consistency is the default, and a query opts into eventual per request.

```proto
enum Consistency {
  CONSISTENCY_UNSPECIFIED = 0;
  CONSISTENCY_STRONG = 1;
  CONSISTENCY_EVENTUAL = 2;
}
```

Strong sees every write that completed before the query started. Eventual trades
that for throughput and may miss recent writes.

Read-your-writes works by default. A client opts out of it deliberately.

### Examples

Every example below queries one namespace of source files. A document holds
`path` and `language` as text, `content` as a text attribute with a tokenizer,
and `embedding` as a vector column. `path` declares `glob: true`, so a glob
predicate on it is legal.

Fetch one file by ID.

```proto
namespace: "repo"
query {
  name: "by_id"
  top_k: 1
  retrieve {
    filter {
      compare {
        attribute: "id"
        eq { text: "src/index/writer.go" }
      }
    }
  }
}
```

Every Go file under one directory.

```proto
namespace: "repo"
query {
  name: "go_files"
  top_k: 100
  retrieve {
    filter {
      compare {
        attribute: "path"
        glob: "src/index/*.go"
      }
    }
  }
}
```

The nearest neighbors of a query embedding, restricted to Go files.

```proto
namespace: "repo"
query {
  name: "similar"
  top_k: 10
  retrieve {
    filter {
      compare {
        attribute: "language"
        eq { text: "go" }
      }
    }
    rank_by {
      vector {
        attribute: "embedding"
        vector: [0.12, -0.44, 0.81]
      }
    }
  }
}
```

BM25 over file content.

```proto
namespace: "repo"
query {
  name: "keyword"
  top_k: 10
  retrieve {
    rank_by {
      text {
        attribute: "content"
        query: "checksum mismatch"
      }
    }
  }
}
```

One term across one subtree, retrieved twice and fused. Both legs filter to the
same prefix, and `rrf` unset leaves `k` at 60.

```proto
namespace: "repo"
query {
  name: "hybrid"
  top_k: 10
  fusion {
    inputs {
      name: "semantic"
      top_k: 100
      retrieve {
        filter {
          compare {
            attribute: "path"
            prefix: "src/index/"
          }
        }
        rank_by {
          vector {
            attribute: "embedding"
            vector: [0.12, -0.44, 0.81]
          }
        }
      }
    }
    inputs {
      name: "keyword"
      top_k: 100
      retrieve {
        filter {
          compare {
            attribute: "path"
            prefix: "src/index/"
          }
        }
        rank_by {
          text {
            attribute: "content"
            query: "checksum"
          }
        }
      }
    }
    rrf {}
  }
}
projection {
  include {
    names: "path"
  }
}
```

The response carries the fused score beside the rank each leg gave the row.

```proto
matches {
  document {
    id: "src/index/writer.go"
    attributes {
      key: "path"
      value { text: "src/index/writer.go" }
    }
  }
  score: 0.0323
  node_ranks { name: "semantic" rank: 1 score: 0.14 }
  node_ranks { name: "keyword" rank: 3 score: 8.21 }
}
matches {
  document {
    id: "src/index/reader.go"
    attributes {
      key: "path"
      value { text: "src/index/reader.go" }
    }
  }
  score: 0.0306
  node_ranks { name: "semantic" rank: 2 score: 0.19 }
  node_ranks { name: "keyword" rank: 9 score: 5.02 }
}
```

## Open

CONSIDER(ali): how does a client measure recall when the server performs the
quantization? The server owes a number, and no message carries one.

CONSIDER(ali): what are the quantization mode names? `Quantization` holds
`QUANTIZATION_NATIVE` alone until a benchmark can hold one mode against another.

CONSIDER(ali): what bounds a fuzzy match? `Fuzzy` carries the term alone, and an
edit budget needs a fixed rule for counting edits.

CONSIDER(ali): what shape does weighted fusion take? Each input needs a weight,
which sits either on the strategy message or on the input node.

CONSIDER(ali): what does a read return for a vector column with
`store_original: false`? Returning the index representation under the same name
hands a client values it did not write. Withholding the attribute makes a
declared column invisible.

CONSIDER(ali): what makes read-your-writes work? The consistency section asserts
it, and no token, version, or session appears anywhere on the wire.
