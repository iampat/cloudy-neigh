# gRPC write and query API

**Status:** Draft — 2026-08-11

## Scope

This note defines the first version of the client-facing gRPC API. It covers the
two remote procedure calls, the document model, and the type of each attribute on
the wire.

The turbopuffer HTTP API is the reference for this design. Every decision below
states what turbopuffer does and why we agree or differ.

Out of scope: the storage engine, the index format, the query planner, sharding,
replication, and authentication.

## Model

A namespace holds documents. A document has one identifier, zero or more named
vector columns, and a flat set of attributes. A namespace is created by its first
write.

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

## Decisions

### One service, two calls

The service has one `Write` call and one `Query` call. Both are unary. The
namespace is a field in the request message.

turbopuffer uses one HTTP endpoint for writes and one for queries. The namespace
is a path parameter. We keep the same split because the two paths have different
cost profiles and different consistency needs. gRPC has no path, so the namespace
moves into the message.

```proto
service Index {
  rpc Write(WriteRequest) returns (WriteResponse);
  rpc Query(QueryRequest) returns (QueryResponse);
}
```

### The write mode is a `oneof`

A write carries exactly one operation. Protobuf enforces this.

turbopuffer puts seven optional fields on one JSON body: `upsert_rows`,
`patch_rows`, `deletes`, `delete_by_filter`, and others. The OpenAPI schema marks
none of them required and declares no mutual exclusion. The server rejects a bad
combination at run time.

We differ because protobuf can express the constraint that JSON Schema cannot. An
illegal combination fails at compile time in the generated client.

The cost is a wrapper message per mode. A `oneof` cannot hold a repeated field.

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

Only `Upsert` exists today. `Patch`, `Delete`, and `DeleteByFilter` become
additional members of the same `oneof`.

### Errors use gRPC status codes

A failure returns a gRPC status code. The response message carries no status
field.

turbopuffer returns `status: "OK"` inside a successful body and an `ErrorResponse`
schema on failure. HTTP forces this, because an HTTP status code cannot carry a
structured reason.

We differ because gRPC already has a status channel. A second status field in the
body drifts from the transport code and gives two sources of truth.

Backpressure returns `RESOURCE_EXHAUSTED` in place of the HTTP 429 response.

### One identifier type, ordered by encoding

An identifier is an opaque byte string. The storage engine orders identifiers by
byte comparison. A client encodes a typed identifier into an order-preserving byte
string before the write.

turbopuffer supports three identifier types: string, UUID, and integer. We differ
because one type removes a type switch from every code path. A byte string is also
usable as a map key in Go, which a `oneof` message is not.

Order comes from the encoding, not from the type. A fixed-width big-endian
encoding preserves numeric order under byte comparison. A decimal text encoding
does not.

```
  encode(uint64 9)   →  00 00 00 00 00 00 00 09   ┐ byte order matches
  encode(uint64 10)  →  00 00 00 00 00 00 00 0A   ┘ numeric order

  "9"                →  39                        ┐ byte order does not
  "10"               →  31 30                     ┘ match numeric order
```

The field is `bytes`, not `string`. A proto3 `string` must hold valid UTF-8, and a
big-endian integer encoding produces bytes that are not valid UTF-8.

The encoder must handle four cases:

- A signed integer needs its sign bit flipped. Two's complement sorts negative
  values after positive values.
- A float needs all bits flipped when the sign bit is set, and the sign bit
  flipped otherwise.
- A namespace that mixes identifier types needs a type tag as the first byte.
- A composite key needs escaping, because `"ab"` is a prefix of `"abc"`.

We adopt the turbopuffer limit of 64 bytes per identifier. The limit bounds the
identifier dictionary, which is a memory control rather than a style rule.

### Attributes are flat

An attribute holds a scalar, a timestamp, or a homogeneous list. An attribute
never holds another attribute set.

turbopuffer is also flat. We agree for three reasons. A column store indexes
columns, and a nested object has no column identity until it is flattened. A flat
model keeps the `Value` type free of recursion, which removes a depth limit and a
parser attack surface. A flat model also makes every attribute addressable by name
in a filter.

Lists stay, because a list of strings is the most common filter target in a search
index. Explicit list variants keep `Value` free of recursion. A `repeated Value`
would reintroduce it.

The cost is that a client with nested source data flattens it before the write. A
client that needs the original shape stores it in an unindexed `bytes` attribute.

### `Value` is a custom type

`Value` is a `oneof` written for this project.

We reject `google.protobuf.Struct`. Its only number type is `double`, so an
integer above 2^53 loses precision and a range filter on it returns wrong rows.
`Struct` is also recursive, so it can express the nesting we reject.

We adopt `google.protobuf.NullValue` for an explicit null and
`google.protobuf.Timestamp` for a date and time.

We reject `google.protobuf.Any`, because it carries a type URL of about 50 bytes
per value and the server cannot index a payload it cannot read. We reject the
scalar wrapper messages, because proto3 `optional` and `oneof` both carry presence
at lower cost. We reject `google.protobuf.FieldMask`, because its paths address
static message fields and ours are dynamic map keys.

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

`Value` is the most frequent message in the system. There is one instance per
attribute per document. Every variant uses a field number from 1 to 15, where the
tag encodes in a single byte. Six numbers remain in that range.

### Vector columns are named and declared

Every vector column has a name and a schema entry. There is no default column.

turbopuffer infers a vector type for an attribute named `vector`, and requires an
explicit schema entry for any other vector column. We differ because a magic name
is a special case in every rule that follows it: type inference, schema
validation, query ranking, and patch behavior.

The cost is that the first write needs a schema. A client cannot create a vector
column by writing data alone.

### The number of vector columns is not capped

The schema accepts any number of vector columns.

turbopuffer caps a namespace at two. That cap follows from cost, not from the
protocol. Each vector column carries its own approximate nearest neighbor index,
and a filterable attribute is indexed once per such index. Write cost and storage
cost therefore scale with the column count.

We differ because the operator, not the protocol, should carry that cost. A cap
belongs in a quota that an operator sets, not in the message definition.

The cost is that a namespace with many vector columns is expensive, and nothing in
the API warns the client.

### The distance metric is per column

Each vector column declares its own distance metric.

turbopuffer sets one metric for the whole namespace. We differ because two columns
can hold embeddings from two models with different geometry. A namespace-wide
metric forces the client to split one logical collection across two namespaces.

### Vectors on the wire are float32

A vector crosses the wire as `repeated float`.

float32 holds every value of int8, float16, and bfloat16 without loss. The wire
type is therefore a lossless container for every narrower format. The cost is
bandwidth, not precision.

turbopuffer takes the same position. Its base64 vector encoding is float32 even
when the column is stored as `[512]f16`, so the wire type and the storage type are
already independent there.

This choice is reversible by addition. A packed `bytes` field with a data type in
the column schema can be added later. Changing the type of the existing field
would break the wire.

### The index copy and the stored copy are separate

A vector column has two representations. The index copy is lossy and rebuildable.
The stored copy is exact and returned to the client.

A text index already has this split. Posting lists are a lossy transformation of
the source text, and the stored attribute holds the original.

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

turbopuffer does not expose this split. A client cannot ask for the exact vector
it wrote. We differ because the two artifacts answer different questions. The
server chooses the index representation freely, and the client keeps its data.

The cost is storage amplification. It is the same amplification a text index
already accepts.

The quantization decision has three possible owners. The server owns it by
default. A client that sends a pre-quantized vector owns it. An embedding model
that emits int8 owns it, and neither the server nor the client may re-quantize
that output.

```proto
message VectorColumn {
  uint32 dimensions = 1;
  DistanceMetric metric = 2;
  Quantization index_quantization = 3;
  bool store_original = 4;
}

enum Quantization {
  QUANTIZATION_UNSPECIFIED = 0;  // the server chooses
  QUANTIZATION_NATIVE = 1;       // the producer chose; do not re-quantize
}
```

A server that owns quantization owns the recall budget. turbopuffer publishes a
recall range and ships a recall debugging endpoint for this reason. We take on the
same obligation.

### Text analysis is per attribute and versioned

Each text attribute declares its own tokenizer, language, and related options.

turbopuffer does the same, and we agree. A title, a body, and a product code need
different analysis, and a multilingual corpus needs different languages per
attribute.

The tokenizer name carries a version. turbopuffer ships `word_v0` through
`word_v3`. Tokenization is an on-disk contract, because every posting list is a
product of it. A tokenizer cannot change in place. It can only gain a new version,
and an existing namespace keeps the old one until it is reindexed.

We therefore never ship an unversioned tokenizer name, even while only one exists.

```proto
enum Tokenizer {
  TOKENIZER_UNSPECIFIED = 0;
  TOKENIZER_WORD_V1 = 1;
  TOKENIZER_PRE_TOKENIZED = 2;
}
```

### Index-time and query-time transforms come from one pipeline

A query must use the same analysis pipeline as the write that built the index.

A tokenizer that stems at write time and not at query time returns no match for a
stemmed term. The failure is silent. The same rule holds for an embedding model. A
float32 query vector cannot rank against a column that a model quantized to int8,
because the client does not hold the calibration.

This is one invariant with two instances. Both the tokenizer and the embedding
model carry a version for this reason.

### A batch write is atomic

A write applies to every document in the batch or to none.

turbopuffer does not state a rule for this case. We choose all-or-nothing because
it is the simplest contract to reason about, and it keeps a retry safe after a
partial failure.

An upsert is idempotent. The same identifier and the same content produce the same
state, so a client can retry after `UNAVAILABLE`. This property ends when
conditional writes arrive, and that change must be recorded here.

## Open

`CONSIDER(ali):` Is this a system of record for vectors, or a derived index? If a
vector is always rebuildable by running the embedding model again, then lossy
storage is a rebuild. If this database holds the only copy, lossy storage is data
loss. The answer sets the default for `store_original`.

`CONSIDER(ali):` Does the identifier encoder live in the client or in the server?
A client-side encoder keeps the wire type as opaque `bytes`, and requires shared
conformance vectors across languages. A subtle difference between two encoders is
silent data corruption. A server-side encoder needs the type on the wire, which
returns a typed `oneof` identifier.

`CONSIDER(ali):` Do we take a dependency on the `googleapis` protos? A batch write
that fails needs to name the document that caused it. The idiomatic carrier is
`google.rpc.BadRequest.FieldViolation` in the status details. That is a new
third-party dependency and needs agreement first.

`CONSIDER(ali):` Is native embedding in scope? An embedding call inside the write
path drops throughput by orders of magnitude. turbopuffer allows a 512 MB upsert
batch, but only 30 rows per batch when the server computes the embedding. A write
path that later gains this feature must be asynchronous from the start.
