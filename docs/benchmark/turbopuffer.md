# Proposal: benchmark cloudy-neigh with turbopuffer's tool

**Status:** Draft, 2026-08-27, v0

## The idea

We want an apples-to-apples comparison against turbopuffer.

turbopuffer published the tool behind its public latency numbers:
[tpuf-benchmark](https://github.com/turbopuffer/tpuf-benchmark). One Go binary,
MIT licensed, with the datasets, the query shapes, the request rate and the
warm-up all committed to that repository.

The tool talks to one thing: turbopuffer's HTTP and JSON API. Build that API on
cloudy-neigh, and `tpufbench` runs against it unchanged. Same binary, same
definitions, same datasets, same measurement. Two engines.

## Endpoints the benchmark needs

Only one file in `tpuf-benchmark` knows the API, and it makes six calls. Every
request carries an `authorization: Bearer <key>` header.

| Call | What it does |
| --- | --- |
| `POST /v2/namespaces/{ns}` | writes documents, both the bulk load and the measured writes |
| `POST /v2/namespaces/{ns}/query` | every measured query, plus a count probe before the run |
| `DELETE /v2/namespaces/{ns}` | clears a namespace so a run starts clean |
| `GET /v1/namespaces/{ns}/metadata` | row and byte totals, and the index status |
| `GET /v1/namespaces/{ns}/_debug/warm_cache` | pulls a namespace into cache before measuring |
| `GET /v1/namespaces/{ns}/_debug/purge_cache` | drops the cache for a cold run |

Three details in the responses shape what we build.

A query response carries a `performance` block with the server time, a cache
hit ratio and a cache temperature of `hot`, `warm` or `cold`. The reporter
splits every percentile by that temperature, and it crashes on a fourth value.
The metadata response has to report an up-to-date index at some point, because
the run blocks until it does. And a 404 on the query path is silent: the run
counts nothing, reports nothing and still looks healthy.

## Datasets and workloads

Eleven definitions ship with the tool. They draw on four real datasets plus a
random generator, and cover semantic, lexical, hybrid and aggregation
workloads.

| Dataset | Source | Size in a run | Vectors | Retrieval | Metric |
| --- | --- | --- | --- | --- | --- |
| Cohere Wikipedia | Hugging Face, 7 languages | 1M, 10M | 1024-dim | semantic | cosine, exact k-NN and ANN |
| Cohere MSMarco | Hugging Face passages and queries | 10M, 100M | 1024-dim, expanded to 2048 | lexical, semantic, hybrid | BM25, cosine, RRF fusion |
| Deep1B | Yandex `base.1B.fbin` | 1B | 96-dim | semantic | cosine, ANN |
| TPC-H lineitem SF10 | generated | 59,986,052 rows | none | scalar filters | sum aggregation, query 6 |
| random | PCG generator | any | any width | query vectors only | matches the definition |

MSMarco ships a query set with embeddings, so the text and hybrid runs search
with real queries rather than noise. The Wikipedia runs seed real embeddings
and then query with random vectors. The 2048-dimension runs repeat the same
1024-dim MSMarco vectors to 2048, stored as `f16`.

Every definition also sets a warm-up policy of its own. Some wait for a 100%
cache hit ratio before the clock starts. The cold variants purge the cache
first, or disable it on every query.

The tool downloads and caches each dataset on local disk. The nightly runs
happen on a GCE `c4a-standard-32-lssd` in `us-central1-c`, in the same region
as the service, which is where we would run ours too.

## Where we start

Milestone 1 gives us writes, exact k-NN and attribute filters. That already
covers `vector-knn-1m-hot.toml` and `vector-knn-1m-cold.toml`: a million
documents, 1024 dimensions, five minutes per run. Neither needs an ANN index or
a text index, so they are the first numbers we can publish.

| Milestone | What it unlocks |
| --- | --- |
| 1. Ingestion and exact retrieval | the two exact k-NN definitions |
| 2. Tiered caching | the warm and cold split across every definition |
| 3. Lexical search | the three MSMarco text definitions |
| 4. Hybrid search | the two hybrid definitions |
| 5. ANN indexing | the ANN and 100 million document definitions |
| 6. Aggregations | TPC-H query 6 |
