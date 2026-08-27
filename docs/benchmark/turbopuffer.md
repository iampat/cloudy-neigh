# Proposal: benchmark cloudy-neigh with the turbopuffer harness

**Status:** Draft — 2026-08-27 — v0

## Problem

cloudy-neigh needs numbers a reader trusts. A benchmark we write ourselves
proves little. We pick the data, the queries, and the load, so we pick the
result.

turbopuffer publishes `tpuf-benchmark`, the harness behind its public latency
numbers. It is one Go binary under the MIT license. The datasets, the query
shapes, the load, and the metric all sit in that repository. Those numbers are
the reference our reader already knows.

The harness speaks the turbopuffer HTTP API. cloudy-neigh speaks gRPC.

## Proposal

cloudy-neigh serves a turbopuffer-compatible HTTP API. We then run `tpufbench`
against it unchanged. One binary, one set of TOML definitions, and one set of
datasets drive both systems.

The compatible API runs in the process that holds the engine, beside the gRPC
service. It covers the six calls the harness makes, and nothing else.

Two alternatives lose the comparison.

- **Fork the harness and give it a gRPC client.** This is cheaper to build. Each
  system then faces a different client and a different encoder, so we compare
  two harnesses instead of two engines.
- **Run a translating gateway in front of the engine.** The harness stays
  intact. Our side alone pays one extra hop and one extra encode.

In process, both systems parse the same JSON bytes from the same client. The
engine is the difference that remains.

The cost is a second API surface. It stays scoped to what the definitions call,
and it never enters `docs/design/grpc-api.md`. It also buys a migration path,
because a turbopuffer client can point at cloudy-neigh without a rewrite.

## Non-goals

- Recall and relevance. The harness measures latency, throughput, and ingest
  time.
- A price comparison.
- A complete turbopuffer API. We serve what the definitions call.

## The harness

One run reads one TOML definition and walks five stages.

```
tpufbench run benchmarks/vector-knn-1m-hot.toml
├── sanity      10 documents and one query, before any dataset download
├── setup       render and bulk upsert the documents
├── index wait  poll until the index reports up to date
├── cache       warm it, purge it, or wait for a hit ratio
└── measure     run every workload for the definition duration
```

A definition names the dataset, the document count, the document shape, and the
workloads. A workload carries its own rate, worker count, and query template.
Eleven definitions ship. They cover exact k-NN, ANN, BM25, hybrid search with
reciprocal rank fusion, and one filtered aggregation from TPC-H query 6.
Document counts run from 1 million to 1 billion.

Data comes from Cohere Wikipedia embeddings, Cohere MSMarco passages, Yandex
Deep1B, generated TPC-H lineitem rows, or a random generator. The harness
downloads and caches each dataset on local disk.

The reporter records the client-side latency of every request. It prints
percentiles every 10 seconds, writes one CSV row per request, and writes a
`report.json` at the end.

The nightly workflow runs the harness on a GCE `c4a-standard-32-lssd` in
`us-central1-c`. The client always runs in the region of the service.

## What we must serve

Only one file in the harness knows the API. It makes six calls, with one
`authorization: Bearer <key>` header.

| Call | Stage |
| --- | --- |
| `POST /v2/namespaces/{ns}` | setup upsert, and every measured write |
| `POST /v2/namespaces/{ns}/query` | every measured query, and the size probe |
| `DELETE /v2/namespaces/{ns}` | clear a namespace before a run |
| `GET /v1/namespaces/{ns}/metadata` | index wait, byte and row totals |
| `GET /v1/namespaces/{ns}/_debug/warm_cache` | cache stage |
| `GET /v1/namespaces/{ns}/_debug/purge_cache` | cache stage |

Three of the responses change what we build.

- A query response carries a `performance` block. It holds the server time, the
  cache hit ratio, and a cache temperature of `hot`, `warm`, or `cold`. The
  reporter splits every percentile by that temperature, and any fourth value
  crashes it.
- The metadata response must reach an up-to-date index status. The index wait
  never ends otherwise.
- A 404 on the query path counts nothing and raises no error. A run against a
  wrong namespace name looks healthy and measures nothing.

## First target

Milestone 1 delivers write, exact k-NN, and attribute filters. That covers
`vector-knn-1m-hot.toml` and `vector-knn-1m-cold.toml` end to end: 1 million
documents, 1024 dimensions, exact k-NN, five minutes. Neither needs an ANN
index or a text index.

| Milestone | Definitions it unlocks |
| --- | --- |
| 1. Ingestion and exact retrieval | the two exact k-NN definitions |
| 2. Tiered caching | the warm and cold split of every definition |
| 3. Lexical search | the three MSMarco text definitions |
| 4. Hybrid search | the two hybrid definitions |
| 5. ANN indexing | the ANN and 100 million document definitions |
| 6. Aggregations | TPC-H query 6 |

## Limits of the comparison

The harness equalises the client, the dataset, the query stream, the load
shape, and the metric. It cannot equalise the server.

turbopuffer is a managed service. We know its client machine and its region. We
do not know its node count, its node size, or its cache budget. Every
cloudy-neigh result thus states our node shape, and the reader compares one
known server against one unknown one.

## Open

`CONSIDER(ali):` Where does the compatible API live? A build tag keeps one
binary and one deployment path. A separate `cmd/` binary keeps the production
server clean of a compatibility layer.

`CONSIDER(ali):` turbopuffer does not publish the hit ratio that separates
`hot`, `warm`, and `cold`. Do we measure the thresholds from a run against the
service, or do we report the combined percentile alone?

`CONSIDER(ali):` Do we run the turbopuffer side ourselves, or cite its
published nightly numbers? Our own run costs an API key and service usage, and
it removes every question about the client and the date.
