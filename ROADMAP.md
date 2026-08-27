# Roadmap

**Status:** Draft, 2026-08-25

## Problem

Building a search engine on cloud object storage presents a fundamental trade-off. Remote object storage provides low cost, high durability, and unlimited capacity. But remote network calls add latency. Traditional search systems keep entire indexes in RAM. That design is expensive and limits dataset scale.

`cloudy-neigh` is an open-source, cloud-native search engine. It decouples compute from storage. The engine uses cloud object storage as the source of truth. Stateless query and ingestion nodes use local NVMe SSD and RAM caches to achieve low search latency.

We deliver features through an incremental roadmap. We break down search capabilities into small, shippable milestones. Each milestone delivers functional software that users can run and evaluate immediately.

## Goals

- Provide a cloud-native search engine that uses cloud object storage as primary storage.
- Support hybrid search across vector embeddings, lexical text, and structured attributes.
- Deliver low query latency through tiered local NVMe SSD and memory caching.
- Ship incremental capabilities in distinct, testable milestones.
- Keep the operational footprint minimal with zero external coordination servers.
- Publish latency and throughput numbers that a reader can reproduce.

## Non-goals

- In-memory only architectures that discard cloud object storage.
- Proprietary storage backends that prevent self-hosting on standard object stores.
- Heavy external coordination clusters such as ZooKeeper or distributed key-value stores.

## Architecture

```
                      ┌────────────────────────┐
                      │      gRPC Client       │
                      └───────────┬────────────┘
                                  │ API
                                  ▼
      ┌────────────────────────────────────────────────────────┐
      │                      Compute Node                      │
      │                                                        │
      │  ┌───────────────────────┐   ┌──────────────────────┐  │
      │  │  Query Engine / Cache  │   │  WAL Writer / Buffer │  │
      │  │  (RAM / NVMe SSD)     │   │  (Group Commit)      │  │
      │  └───────────┬───────────┘   └──────────┬───────────┘  │
      └──────────────┼──────────────────────────┼──────────────┘
                     │                          │
                     ▼                          ▼
      ┌────────────────────────────────────────────────────────┐
      │               Cloud Object Storage                     │
      │   (Immutable Index Segments, Manifests, and WAL Logs)  │
      └────────────────────────────────────────────────────────┘
```

The system separates query processing, cache management, and durable storage into three layers:

1. **Storage Layer**: Stores append-only write-ahead logs (`recordio` files), immutable index segments, and manifest snapshots in object storage.
2. **Execution Layer**: Executes vector distance computations, inverted index scans, and score fusions.
3. **Cache Layer**: Caches immutable object segments on local NVMe SSD and in RAM to eliminate remote network round-trips.

---

## Benchmarks

The engine needs a benchmark. A milestone that adds a retrieval path also
publishes the numbers for it, and a reader must be able to reproduce them.

---

## Phased Milestones

```
   ┌────────────────────────────────────────────────────────────────────────┐
   │ 1. Core Storage, WAL & Exact Retrieval                                 │
   └───────────────────────────────────┬────────────────────────────────────┘
                                       ▼
   ┌────────────────────────────────────────────────────────────────────────┐
   │ 2. Tiered Local Caching & Latency Optimization                         │
   └───────────────────────────────────┬────────────────────────────────────┘
                                       ▼
   ┌────────────────────────────────────────────────────────────────────────┐
   │ 3. Lexical Search & Inverted Indexing                                  │
   └───────────────────────────────────┬────────────────────────────────────┘
                                       ▼
   ┌────────────────────────────────────────────────────────────────────────┐
   │ 4. Hybrid Search & Score Fusion                                        │
   └───────────────────────────────────┬────────────────────────────────────┘
                                       ▼
   ┌────────────────────────────────────────────────────────────────────────┐
   │ 5. Approximate Nearest Neighbor (ANN) Indexing                         │
   └───────────────────────────────────┬────────────────────────────────────┘
                                       ▼
   ┌────────────────────────────────────────────────────────────────────────┐
   │ 6. Real-Time Aggregations & Metadata Analytics                         │
   └───────────────────────────────────┬────────────────────────────────────┘
                                       ▼
   ┌────────────────────────────────────────────────────────────────────────┐
   │ 7. Advanced Retrieval: Multi-Vector & Late Interaction                 │
   └───────────────────────────────────┬────────────────────────────────────┘
                                       ▼
   ┌────────────────────────────────────────────────────────────────────────┐
   │ 8. Zero-Copy Branching & Snapshot Lifecycle                            │
   └───────────────────────────────────┬────────────────────────────────────┘
                                       ▼
   ┌────────────────────────────────────────────────────────────────────────┐
   │ 9. Horizontal Scaling & Namespace Sharding                             │
   └───────────────────────────────────┬────────────────────────────────────┘
                                       ▼
   ┌────────────────────────────────────────────────────────────────────────┐
   │ 10. Native Embedding Integration & Cloud Operations                    │
   └────────────────────────────────────────────────────────────────────────┘
```

---

### Milestone 1: Durable Ingestion and Exact Retrieval

Establish the minimal functional search engine. A user can run a single node, ingest documents, store them in object storage, and execute exact search queries.

- **Write-ahead log (WAL)**: Append-only log engine over `recordio` and `objectstore`.
- **Memtable and flush**: In-memory write buffer with background flush of immutable segments to object storage.
- **gRPC service**: Implement basic `Write` and `Query` RPC endpoints.
- **Exact k-NN search**: Brute-force vector distance computation for Cosine, Dot Product, and Euclidean ($L_2$) metrics.
- **Attribute filtering**: Exact match and numerical range predicates on scalar document attributes.

**User Value**: A runnable single-node engine that persists and queries vector data over local disk, AWS S3, or Google Cloud Storage.

---

### Milestone 2: Tiered Local Caching and Latency Optimization

Reduce warm query latency from cloud round-trip times to sub-millisecond local reads.

- **Two-tier cache hierarchy**: RAM cache for hot metadata and NVMe SSD cache for immutable index segments.
- **Single-flight request coalescing**: Deduplicate concurrent object fetch requests during cache misses.
- **Asynchronous read-ahead**: Background prefetch for sequential segment reads during scan operations.
- **Cache warming API**: Implement the `WarmCache` pre-flight endpoint to preload namespace data on demand.

**User Value**: Fast, predictable query latency for active namespaces without paying repeated object store transfer costs.

---

### Milestone 3: Lexical Search and Inverted Indexing

Add full-text search capabilities across document text attributes.

- **Text tokenization**: Configurable tokenizer supporting Unicode segmentation, case folding, and stop-word removal.
- **Inverted index format**: Postings lists stored as immutable column segments in object storage.
- **BM25 scoring**: Full-text relevance ranking using the BM25 algorithm.
- **Lexical query syntax**: Support for term matches, phrase searches, and boolean clauses (AND, OR, NOT).

**User Value**: Fast keyword search on the same documents without operating a separate text search system.

---

### Milestone 4: Hybrid Search and Score Fusion

Combine vector similarity and lexical text relevance in a single query call.

- **Reciprocal rank fusion (RRF)**: Combine ranked lists from vector and text retrieval paths.
- **Linear score combination**: Merge normalized vector and BM25 scores with configurable weights.
- **Filter pushdown**: Evaluate metadata predicates directly during hybrid scans to prune candidate sets early.
- **Custom attribute ranking**: Rank results by scalar attribute values or computed distance metrics.

**User Value**: Superior search quality that joins semantic similarity and exact keyword precision in one request.

---

### Milestone 5: Approximate Nearest Neighbor (ANN) Indexing

Deliver scalable sub-linear vector search for collections with millions of documents.

- **Vector quantization**: Implement 8-bit Scalar Quantization (SQ8) and Product Quantization (PQ) for vector compression.
- **Disk-backed graph index**: Cloud-native graph format designed for range reads on object storage and local NVMe SSD.
- **Background compaction worker**: Asynchronously merge WAL segments and construct optimized ANN indexes.
- **Dynamic recall controls**: Runtime query parameters to balance recall and latency (`ef_search` and target recall).

**User Value**: High-throughput vector search over large datasets with minimal memory consumption.

---

### Milestone 6: Real-Time Aggregations and Metadata Analytics

Support statistical summaries and facet counts over filtered document sets.

- **Aggregation functions**: Real-time evaluation of `count`, `sum`, `min`, `max`, and `average`.
- **Group-by bucketing**: Group query results across categorical scalar attributes.
- **Distinct count and facets**: Fast unique value extraction for search facets.
- **Nested attribute filtering**: Query predicates for nested JSON structures and array containment.

**User Value**: Rich faceted navigation and analytical search directly from the search index.

---

### Milestone 7: Advanced Retrieval: Multi-Vector and Late Interaction

Support multi-vector document representations and modern retrieval models.

- **Multiple vector columns**: Store and search multiple independent vector fields per document (e.g. title and body vectors).
- **Late-interaction search**: Support token-level multi-vector scoring (such as ColBERT max-similarity).
- **Sparse vector indexing**: Native storage and retrieval for learned sparse representations.
- **Snippet extraction and highlighting**: Return matched text snippets with highlighted query terms.
- **Fuzzy text matching**: Levenshtein distance and n-gram matching for typo tolerance.

**User Value**: Advanced information retrieval architectures and improved end-user search experiences.

---

### Milestone 8: Zero-Copy Branching and Snapshot Lifecycle

Provide Git-like dataset branching and point-in-time snapshot isolation.

- **Branch head references**: Atomic branch pointers in object storage (`refs/heads/<branch>`) referencing immutable manifests.
- **Zero-copy namespace branching**: Instant creation of isolated branch copies for testing and staging.
- **Point-in-time queries**: Pin queries to specific historical snapshot versions.
- **Garbage collection (GC)**: Background service to clean up unreferenced blobs and superseded manifest files.

**User Value**: Safe experimentation, instant staging environments, and zero-downtime rollbacks with no storage duplication.

---

### Milestone 9: Horizontal Scaling and Namespace Sharding

Scale dataset size and query throughput across multiple compute nodes.

- **Namespace sharding**: Fixed and dynamic shard partitioning for high-volume namespaces.
- **Distributed query coordinator**: Scatter-gather execution across shards with merged ranking.
- **Consistent routing**: Topology-aware request routing and shard mapping.
- **Online resharding**: Rebalance and split shards with zero read downtime.

**User Value**: Linear scaling from small embedded workloads to multi-terabyte search deployments.

---

### Milestone 10: Native Embedding Integration and Cloud Operations

Complete the developer workflow and harden production operations.

- **Server-side embedding inference**: Generate vector embeddings on write and query through external model APIs and local runtimes.
- **Client SDKs**: Official client libraries for Go, Python, TypeScript, and Rust.
- **Data migration tools**: Bulk export, import, and backup restore utilities.
- **Observability**: Prometheus metrics, OpenTelemetry distributed tracing, and health check endpoints.

**User Value**: Turnkey developer experience with enterprise-grade operational controls.

---

## Future Work

- Zero-knowledge client-side encryption for sensitive enterprise datasets.
- Cross-region replication and multi-region read replicas.
- Cross-encoder server-side reranking pipelines.
- Natural language query interface compiling to structured query messages.

## Open Questions

`CONSIDER(ali):` Does index compaction run inside a query node, or in a separate serverless worker? A separate worker isolates compute spikes from read latency, but it adds a deployment component.
