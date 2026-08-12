# gRPC write and query API

**Status:** Draft — 2026-08-11

## Problem

cloudy-neigh stores documents that carry vectors, text, and scalar attributes. A
client writes those documents and then retrieves them three ways: by vector
similarity, by text match, and by exact key. This note defines the wire contract
for the write path and the query path.

Four constraints shape the contract.

**Clients are polyglot.** Go, Python, and TypeScript clients read the same
generated code. A rule the schema cannot express becomes a run-time error in three
languages, and each language reports it differently.

**Attributes are dynamic, but the index needs types.** A client attaches arbitrary
attributes to a document. The index cannot store an attribute until it knows the
type and the width. The contract must carry both the value and enough type
information to index it.

**Vectors are large and have many representations.** A vector is the largest part
of a document. Models emit float32, float16, and int8. The index wants a
representation the model never produced. The contract must not force the storage
engine into the representation that appears on the wire.

**The wire contract outlives the engine.** The storage engine, the index format,
and the query planner all change. The messages a client compiled against do not.
Anything the contract fixes today is expensive to move later.

## Goals

- One call to write and one call to query.
- An invalid request fails at compile time wherever the schema can express the
  rule.
- The wire representation of a vector does not constrain its storage
  representation.
- An identifier has a total order, so a range scan and a cursor both work.
- A field added later does not break a client compiled today.

## Non-goals

The storage engine, the index format, the query planner, sharding, replication,
authentication, and server-side embedding are out of scope. The note names a
constraint from those areas only where it changes the contract.

## Model

A namespace holds documents. A document has one identifier, zero or more named
vector columns, and a flat set of attributes. The first write creates the
namespace.

```
  namespace "products"
  ┌────────────────────────────────────────────────────────┐
  │  document                                              │
  │  ┌──────────┬─────────────────┬──────────────────────┐ │
  │  │    id    │  vector columns │      attributes      │ │
  │  │  bytes   │  title: [f32]   │  name: "foo"         │ │
  │  │          │  image: [f32]   │  tags: ["a", "b"]    │ │
  │  └──────────┴─────────────────┴──────────────────────┘ │
  └────────────────────────────────────────────────────────┘
```

A vector column is an attribute with a vector type and an index. It is not a
separate kind of thing. The model keeps it in a separate box because its schema
carries an index configuration that a scalar attribute does not need.

## Design

### Service

The service has two unary calls. The namespace is a field in the request message.

```proto
service Index {
  rpc Write(WriteRequest) returns (WriteResponse);
  rpc Query(QueryRequest) returns (QueryResponse);
}
```

Write and query stay separate because they have different cost profiles, different
consistency needs, and different scaling limits. A combined call would force both
paths through the same quota and the same timeout.

A failure returns a gRPC status code, and the response message carries no status
field. Backpressure returns `RESOURCE_EXHAUSTED`.

### Write operations

A write carries exactly one operation, held in a `oneof`.

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

The operation set grows. Patch, delete, and delete-by-filter join the same
`oneof`. A request that mixed two operations would need a defined order between
them, and no order is obviously right. The `oneof` removes the question.

The cost is a wrapper message per operation, because a `oneof` cannot hold a
repeated field. That wrapper later holds the per-operation options, such as a
condition or a partial-completion flag.

A batch write applies to every document or to none. All-or-nothing is the simplest
contract to reason about, and it keeps a retry safe after a failure. An upsert is
idempotent, so a client can retry after `UNAVAILABLE`. Conditional writes will end
that property, and this note must record the change when they arrive.

### Identifiers

An identifier is an opaque byte string. Byte order is the only order.

The engine compares identifiers with a byte comparison and never interprets them.
A client encodes a typed identifier into a byte string that preserves the order of
the source type.

```
  encode(uint64 9)   →  00 00 00 00 00 00 00 09   ┐ byte order matches
  encode(uint64 10)  →  00 00 00 00 00 00 00 0A   ┘ numeric order

  "9"                →  39                        ┐ byte order does not
  "10"               →  31 30                     ┘ match numeric order
```

One identifier type removes a type switch from every code path in the engine. It
also makes an identifier usable as a map key in Go, which a `oneof` message is
not. Order is a property of the encoding, so the engine gets a total order without
knowing any types.

The field is `bytes`, not `string`. A proto3 `string` must hold valid UTF-8, and a
big-endian integer encoding produces bytes that are not valid UTF-8.

An order-preserving encoder handles four cases:

- A signed integer needs its sign bit flipped, because two's complement sorts a
  negative value after a positive one.
- A float needs all bits flipped when the sign bit is set, and only the sign bit
  flipped otherwise.
- A namespace that mixes source types needs a type tag as the first byte.
- A composite key needs escaping, because `"ab"` is a prefix of `"abc"`.

An identifier is limited to 64 bytes. The limit bounds the identifier dictionary,
which is a memory control rather than a style rule.

### Attributes

An attribute holds a scalar, a timestamp, or a homogeneous list. An attribute
never holds another attribute set.

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

The explicit null matters for the patch operation. A patch must distinguish
"set this attribute to null" from "leave this attribute alone". Presence in the
attribute map answers the second, and `NullValue` answers the first.

`Value` is the most frequent message in the system, with one instance per
attribute per document. Every variant uses a field number from 1 to 15, where the
tag encodes in a single byte. Six numbers remain in that range, so a later variant
must earn one.

The cost is that a client with nested source data flattens it before the write. A
client that needs the original shape stores it in an unindexed `bytes` attribute.

### Vectors

Every vector column has a name, a schema entry, and its own distance metric. There
is no default column and no cap on the count.

```proto
message VectorColumn {
  uint32 dimensions = 1;
  DistanceMetric metric = 2;
  Quantization index_quantization = 3;
  bool store_original = 4;
}
```

A column with a magic name would become a special case in every rule that follows
it: type inference, schema validation, query ranking, and patch behavior. Naming
every column keeps one rule for all of them. The cost is that the first write
needs a schema, so a client cannot create a vector column by writing data alone.

A per-column metric follows from per-column embeddings. Two columns can hold
output from two models with different geometry, and a namespace-wide metric would
force a client to split one logical collection across two namespaces.

A vector crosses the wire as `repeated float`. float32 holds every value of int8,
float16, and bfloat16 without loss, so the wire type is a lossless container for
every narrower format. The cost is bandwidth, not precision. The choice is also
reversible by addition: a packed `bytes` field with a data type in the column
schema can arrive later, while changing the type of field 2 would break the wire.

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
source text and the stored attribute holds the original. The cost is storage
amplification, and it is the amplification a text index already accepts.

Quantization has three possible owners. The server owns it by default. A client
that sends a pre-quantized vector owns it. A model that emits int8 owns it, and
then neither the server nor the client may quantize that output again.
`QUANTIZATION_NATIVE` records the third case.

A server that owns quantization owns the recall budget. It therefore owes the
client a way to measure recall.

### Text analysis

Each text attribute declares its own tokenizer, language, and analysis options. A
title, a body, and a product code need different analysis, and a multilingual
corpus needs a different language per attribute.

A tokenizer name carries a version.

```proto
enum Tokenizer {
  TOKENIZER_UNSPECIFIED = 0;
  TOKENIZER_WORD_V1 = 1;
  TOKENIZER_PRE_TOKENIZED = 2;
}
```

Tokenization is an on-disk contract, because every posting list is a product of
it. A tokenizer cannot change in place. It can only gain a new version, and an
existing namespace keeps the old version until a reindex. An unversioned name
would therefore be a permanent commitment to today's behavior.

`TOKENIZER_PRE_TOKENIZED` lets a client supply tokens directly. A client that owns
its analysis pipeline should not have to reimplement it inside our tokenizer
names.

### One pipeline for write and query

A query must use the same analysis pipeline as the write that built the index.

A tokenizer that stems at write time but not at query time returns no match for a
stemmed term, and the failure is silent. The same rule holds for an embedding
model. A float32 query vector cannot rank against a column that a model quantized
to int8, because the client does not hold the calibration.

This is one invariant with two instances. Both a tokenizer and an embedding model
carry a version for this reason.

## Alternatives

| Option | Why not |
| --- | --- |
| Typed identifier, as a `oneof` of string, integer, and UUID | Adds a type switch to every path and cannot be a Go map key. Still open, see below. |
| Optional sibling fields for write operations | Cannot express mutual exclusion, so every invalid combination fails at run time. |
| `google.protobuf.Struct` for attributes | Stores every number as a `double`, so an integer above 2^53 loses precision and a range filter returns wrong rows. It is also recursive. |
| `google.protobuf.Any` for attributes | Carries a type URL of about 50 bytes per value, and the server cannot index a payload it cannot read. |
| Scalar wrapper messages, such as `Int64Value` | proto3 `optional` and `oneof` both carry presence at lower cost. |
| `google.protobuf.FieldMask` for projection and patch | Its paths address static message fields. Ours are dynamic map keys, and we allow no nesting for a path to traverse. |
| `google.protobuf.Empty` for a future void response | A field can never be added to it. |
| Columnar documents, as parallel arrays per column | Cheaper for bulk ingest, because it sends each attribute name once instead of once per document. Rejected for readability at this stage, and reconsidered when ingest throughput becomes the constraint. |
| Client-streaming write | Needed for a large batch, and additive later as a third call. |
| Namespace-wide distance metric | Forces a second namespace when a client adds a second embedding model. |
| Vectors as `bytes` with a data type | Required for float16 and int8 on the wire. Additive later as a second field. |

## Prior art

turbopuffer solves the same problem over HTTP and JSON, and it has published
operating limits. Three of its choices carry information we could not get on our
own.

**Versioned tokenizer names.** turbopuffer ships `word_v0` through `word_v3`. A
version in a public enum marks a migration that the system could not perform in
place. That is direct evidence for treating an analyzer as an on-disk contract,
and we copy the convention.

**A wire type wider than the storage type.** turbopuffer accepts and returns
float32 even when a column is stored as `[512]f16`. A production system already
separates the two, which supports the same split here.

**A limit that reveals a structural cost.** turbopuffer allows a 512 MB upsert
batch, but only 30 rows per batch when the server computes the embedding. That
gap measures the cost of putting a model in the write path, and it argues for an
asynchronous write path before server-side embedding arrives.

We also take its 64-byte identifier limit and its per-attribute analysis
configuration.

Three places where we go elsewhere:

- turbopuffer infers a vector column from the attribute named `vector` and caps a
  namespace at two vector columns. That cap follows from cost, since each column
  carries its own index and a filterable attribute is indexed once per index. We
  put the cost in an operator quota instead of in the message definition.
- turbopuffer sets one distance metric for a whole namespace.
- turbopuffer does not expose the split between the index copy and the stored
  copy, so a client cannot ask for the exact vector it wrote.

One asymmetry in its design is worth noting, because we inherit the question.
turbopuffer lets a client bypass its tokenizer with a pre-tokenized array, but it
offers no way to supply a pre-quantized vector. The argument for the first applies
to the second.

## Open

`CONSIDER(ali):` Is this a system of record for vectors, or a derived index? If a
vector is always rebuildable by running the embedding model again, lossy storage
is a rebuild. If this database holds the only copy, lossy storage is data loss.
The answer sets the default for `store_original`.

`CONSIDER(ali):` Does the identifier encoder live in the client or in the server?
A client-side encoder keeps the wire type as opaque `bytes` and needs shared
conformance vectors across languages, because a subtle difference between two
encoders is silent data corruption. A server-side encoder needs the source type on
the wire, which brings back the typed identifier.

`CONSIDER(ali):` Do we take a dependency on the `googleapis` protos? A failed
batch write should name the document that caused the failure, and the idiomatic
carrier is `google.rpc.BadRequest.FieldViolation` in the status details. That is a
new third-party dependency and needs agreement first.

`CONSIDER(ali):` When does the write path become asynchronous? Server-side
embedding drops write throughput by orders of magnitude. A synchronous write path
that later gains that feature has to change shape.
