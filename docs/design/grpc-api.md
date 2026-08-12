# gRPC API, part 1: the write path

**Status:** Draft — 2026-08-11 — v0

## Problem

cloudy-neigh stores documents made of vectors, text, and scalar attributes.
Clients write those documents and then find them again by vector similarity, by
text match, or by key.

This note covers the write path: the service shape, the document model, the schema
rules, and the type of every attribute on the wire. Part 2 covers the query path.
The write path comes first because it fixes the data model, and the query path
then has something to read.

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
- The API is optimized for client simplicity, not for storage or index
  performance. The internal systems that serve it stay simple and cheap.
- IDs support range queries.
- User data schemas evolve with backward and forward compatibility, following the
  same practice as protobuf itself.

## Non-goals

Part 2 covers the query message, filters, ranking, projection, pagination, and
consistency levels. This note fixes only what the write path has to settle first.

The storage engine, the index format, the query planner, sharding, replication,
authentication, and server-side embedding are out of scope entirely. The note
names a constraint from those areas only where it changes the contract.

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
4. Index configuration is fixed once set. A tokenizer, a distance metric, and a
   quantization mode cannot change in place, because the index on disk is a
   product of them. Changing one requires a reindex.

Rules 2 and 4 are the ones that bite. Both mean a mistake costs a rewrite of the
namespace rather than an update.

## API design

```proto
service Index {
  rpc Write(WriteRequest) returns (WriteResponse);
  rpc Query(QueryRequest) returns (QueryResponse);
}
```

Write and Query stay separate because they have different cost profiles, different
consistency needs, and different scaling limits. A combined call would force both
through the same quota and the same timeout.

`Query` appears here for the service shape. Its messages belong to part 2.

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
with a wildcard is not a range. It needs a scan or a separate index, and it is not
in scope for v0.

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
container for every narrower format. The cost is bandwidth, not precision. A
packed `bytes` field can arrive later, while changing the type of field 2 would
break the wire.

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
would be a permanent commitment to today's behavior.

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

## Alternatives

| Option | Why not |
| --- | --- |
| `bytes` ID | Holds any binary key without expansion, and needs no encoding for the byte case. Rejected because it makes every ID opaque in a log, a trace, and an error message. |
| Typed ID, as a `oneof` of string, integer, and UUID | Adds a type switch to every path and cannot be a Go map key. |
| Optional sibling fields for write operations | Cannot express mutual exclusion, so every invalid combination fails at run time. |
| `google.protobuf.Struct` for attributes | Stores every number as a `double`, so an integer above 2^53 loses precision and a range filter returns wrong rows. It is also recursive. |
| `google.protobuf.Any` for attributes | Carries a type URL of about 50 bytes per value, and the server cannot index a payload it cannot read. |
| Scalar wrapper messages, such as `Int64Value` | proto3 `optional` and `oneof` both carry presence at lower cost. |
| `google.protobuf.FieldMask` for projection and patch | Its paths address static message fields. Ours are dynamic map keys, and we allow no nesting for a path to traverse. |
| `google.protobuf.Empty` for a future void response | A field can never be added to it. |
| Columnar documents, as parallel arrays per column | Cheaper for bulk ingest, because it sends each attribute name once instead of once per document. Rejected for readability at this stage, and reconsidered when ingest throughput becomes the constraint. |
| Client-streaming write | Needed for a large batch, and additive later as a third call. |
| Namespace-wide distance metric | Forces a second namespace when a client adds a second embedding model. |
| Vectors as `bytes` with a data type | Required for `float16` and `int8` on the wire. Additive later as a second field. |

## Prior art

turbopuffer solves the same problem over HTTP and JSON, and it publishes its
operating limits. Three of its choices carry information we could not get on our
own.

**Versioned tokenizer names.** turbopuffer ships `word_v0` through `word_v3`. A
version in a public enum marks a migration the system could not perform in place.
That is direct evidence for treating an analyzer as an on-disk contract, and we
copy the convention.

**A wire type wider than the storage type.** turbopuffer stores a column as
`[512]f16` or `[512]i8`, and still moves `float32` on the wire. Its schema
documentation states this twice, and the second is explicit:

> This does not affect the base64 vector encoding in the API, which always uses a
> little-endian float32 binary format, regardless of the schema's element type.
>
> — <https://turbopuffer.com/docs/schema>, "Vectors"

A production system already runs this split, which is stronger evidence than our
own reasoning for it. We take two things from it. The wire keeps one vector type
for good, so no client ever negotiates a width. Storage width stays a schema
concern, so a future narrower format needs no wire change at all.

We also take its 64-byte ID limit and its per-attribute analysis configuration.

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

`CONSIDER(ali):` Do we take a dependency on the `googleapis` protos? A failed batch
write should name the document that caused the failure, and the idiomatic carrier
is `google.rpc.BadRequest.FieldViolation` in the status details. That is a new
third-party dependency and needs agreement first.
