# gRPC API, part 2: the query path

**Status:** Draft — 2026-08-12 — v0

Part 1 covers the write path: [grpc-api.md](grpc-api.md).

## Problem

A client finds documents three ways: by vector similarity, by text match, and by
key. Real relevance usually needs more than one of those at once, so the query
path has to combine them rather than pick one.

This note covers the query message, filters, ranking, fusion, projection, and
pagination.

Part 1 settled three things that constrain everything below.

IDs are totally ordered, so a range scan, a prefix scan, and a real cursor are all
available. That only helps a query ordered by an attribute, not a ranked one.

`store_original` defaults to false, so a query often cannot return the exact
vector a client wrote. Projection defaults have to account for that.

Analysis is symmetric, so query text runs through the analyzer the attribute
declared at write time. A query never carries its own tokenizer.

This is v0. Breaking the contract before v1 is acceptable.

## Goals

- One query message covers key lookup, vector search, text search, and any
  combination of them.
- The query is typed. A client builds messages, not strings.
- Adding a rank function or a filter operator later does not reshape the request.
- The API is optimized for client simplicity, not for planner convenience.

## Non-goals

Aggregations, grouping, computed attributes, highlighting, sparse vectors, late
interaction, and query-time embedding are out of scope for v0. Each is additive.

The query planner, the index structures, and the cost model are out of scope
entirely.

## Model

A query is a list of sub-queries plus a fusion step. A sub-query names one way to
rank, and fusion merges the ranked lists into one.

```
  QueryRequest
  ┌──────────────────────────────────────────────────────┐
  │  sub-query "semantic"    sub-query "keyword"         │
  │  ┌──────────────────┐    ┌──────────────────┐        │
  │  │ filter           │    │ filter           │        │
  │  │ rank_by: vector  │    │ rank_by: text    │        │
  │  │ top_k: 100       │    │ top_k: 100       │        │
  │  └────────┬─────────┘    └────────┬─────────┘        │
  │           └──────────┬────────────┘                  │
  │                      ▼                               │
  │                 fusion: RRF                          │
  │                      ▼                               │
  │                 top_k: 10                            │
  └──────────────────────────────────────────────────────┘
```

A single-leg query is a list of one. There is no separate simple form. One shape
serves every query, so no rule has to say "unless there is only one query".

## API design

```proto
message QueryRequest {
  string namespace = 1;
  repeated SubQuery queries = 2;
  Fusion fusion = 3;
  uint32 top_k = 4;
  Projection projection = 5;
  Consistency consistency = 6;
  bytes cursor = 7;
}
```

`top_k` on the request is how many rows come back. `top_k` on a sub-query is how
many candidates that leg contributes to fusion. Both are explicit, because a
client tuning recall needs to raise the candidate count without changing the
result count.

`fusion` is required when `queries` holds more than one entry, and must be unset
otherwise. That is the one rule here the schema cannot carry.

### Sub-query

```proto
message SubQuery {
  string name = 1;
  Filter filter = 2;
  RankBy rank_by = 3;
  uint32 top_k = 4;
}
```

`name` labels the leg. Fusion destroys the evidence of why a row landed where it
did, so the response reports each leg's rank per row under this name. Tuning a
hybrid query without that is guesswork.

A sub-query with a filter and no `rank_by` is a plain lookup. It returns matches
in ID order, which part 1 made total.

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

Three rank functions in v0. A fourth joins the same `oneof` later.

`VectorSearch` carries no distance metric, because part 1 put the metric on the
column. `TextSearch` carries no tokenizer, because part 1 put the analyzer on the
attribute. Both follow from the same rule: an index-time choice stays where the
index was configured, and a query never restates it.

### Filters

A filter is a typed tree. A client builds messages, and the server needs no
parser.

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
    string prefix = 10;
    ValueList contains_any = 11;
  }
}
```

The predicate is a `oneof` of typed fields rather than an operator enum beside a
value list. An enum would let a client send `EQ` with three values, or `IN` with
none, and the server would reject both at run time. This shape cannot express
either. The cost is one field per operator instead of one shared field.

`Value` is the type from part 1, so a filter compares against the same values a
write stores.

Glob, regex, and fuzzy matching are deferred. Each needs its own index
configuration, which belongs to a schema change rather than a query change.

### Fusion

```proto
message Fusion {
  oneof kind {
    ReciprocalRankFusion rrf = 1;
  }
}

message ReciprocalRankFusion {
  uint32 k = 1;
}
```

Reciprocal rank fusion is the only strategy in v0, and it is the right first one
because it consumes ranks rather than scores. A vector distance and a BM25 score
have different units and opposite directions, so any strategy that adds them
needs normalization the client has to tune. Ranks need none.

Weighted fusion joins the same `oneof` when a client needs to bias one leg.

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

The alternative default is to return the ID alone. It is cheaper and it teaches
the cost immediately, but a first query then returns what looks like an empty
document, and the client has to discover the projection field to make the API do
anything. That trade goes the other way for us, because part 1 optimizes for
client simplicity.

### Response

```proto
message QueryResponse {
  repeated Match matches = 1;
  bytes next_cursor = 2;
}

message Match {
  Document document = 1;
  float score = 2;
  repeated SubQueryRank sub_query_ranks = 3;
}

message SubQueryRank {
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

`sub_query_ranks` is populated only for a fused query.

### Pagination

A cursor works only for a query ordered by an attribute or by ID. The response
returns `next_cursor`, and the client sends it back unchanged.

A ranked query returns `top_k` rows and no cursor. Scores shift as documents are
written, and approximate nearest neighbor search does not produce a stable total
order, so a cursor over a ranked result set would silently skip and repeat rows. A
client that needs deeper ranked results raises `top_k`.

This limit is worth stating in the contract rather than discovering in production.

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

Read-your-writes works by default. A client has to opt out of it deliberately.

## Failure modes

**A selective filter degrades vector recall.** Approximate nearest neighbor
search walks a graph or a set of clusters built without knowledge of the filter.
When the filter excludes most documents, the walk finds few surviving candidates.
A filtered vector sub-query may therefore return fewer than `top_k` rows, and the
rows it returns may not be the true nearest neighbors.

v0 states this rather than hiding it. The contract promises at most `top_k` rows,
never exactly `top_k`.

**A cursor outlives the rows it described.** Writes continue while a client pages.
A cursor holds a position in an ordering, not a snapshot, so a document written
into an already-visited range does not appear. Pagination is not a consistent
scan.

**A fused leg can be empty.** A sub-query that matches nothing contributes nothing
and does not fail the query. A query where every leg is empty returns no matches
and no error.

## Alternatives

| Option | Why not |
| --- | --- |
| A string query language, parsed on the server | A second contract with its own grammar, versioning, and error positions. Deferred rather than rejected: a DSL can compile to these messages on either side, which is additive. |
| Untyped nested arrays, as JSON APIs use | Loses every compile-time guarantee and needs a validator that duplicates the schema. |
| A single query, with multi-query added later | The single form becomes a permanent special case in every rule. One leg is a list of one. |
| An operator enum beside a repeated value | Lets a client send `EQ` with three values. Arity becomes a run-time error. |
| `score` as a reserved `$dist` attribute | A JSON workaround for maps that have no typed slot. This response does. |
| Offset pagination | A deep offset scans and discards everything before it. |
| Weighted score fusion in v0 | Needs score normalization across units the client has to tune. Rank fusion needs none. |
| A distance metric on the query | Part 1 put the metric on the column. Restating it invites disagreement. |
| A cursor for ranked queries | No stable total order exists, so it would skip and repeat rows silently. |

## Prior art

turbopuffer runs the same combination over HTTP and JSON, and two of its choices
inform this note.

**Rank fusion over score fusion.** Its multi-query support reranks with `["RRF"]`,
reciprocal rank fusion, rather than a weighted sum. A production system reaching
for ranks first is evidence that normalizing a distance against a BM25 score is
not worth the tuning burden.

**A cap on sub-queries.** turbopuffer allows at most 16 queries per request, and
counts each against the namespace concurrency limit. A fused query costs one full
retrieval per leg, so the cap is a real resource control rather than an arbitrary
number. We adopt the same limit.

Two places where we go elsewhere:

- turbopuffer returns the ID alone unless a query lists attributes explicitly. It
  is the cheaper default and it makes cost visible, but a first query returns what
  reads as an empty document.
- turbopuffer returns the rank score in a `$dist` attribute. That is the right
  answer for an untyped map and the wrong one for a typed message.

## Open

`CONSIDER(ali):` Does a filtered vector sub-query get a recall guarantee, or only
a best effort? A guarantee forces the planner to fall back to an exact scan below
some selectivity, which turns a bounded query into an unbounded one. Best effort
keeps the cost bounded and makes the result depend on data distribution. The
contract has to say which, because a client cannot measure the difference from
outside.

`CONSIDER(ali):` Is `top_k` on a sub-query a candidate count or a result count
when the query has one leg? With one leg and no fusion, the two collapse, and a
client that sets them differently means something we have not defined.
