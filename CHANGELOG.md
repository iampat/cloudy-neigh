# Changelog

### Added

- Multi-tenant log stream routing and root `tenants.json` catalog management. [#144]
- `manifest`: lightweight branch manifest storage with generation-matched CAS updates. [#142]
- `namespace`: branch catalog tracking in `branches.json` with CAS updates. [#142]
- `grpcapi.IngestService.Fork`: RPC to fork a branch within a namespace with WAL sequencing. [#138]
- `ingest.Ingester`: synchronous batch ingestion appending client document batches directly to the WAL. [#140]
- Automatic namespace creation on first document flush in `ingest.Flusher`. [#140]
- `grpcapi`: IngestService with Upsert and Delete WAL appends. A namespace
  package adds Validate helpers and a Scope hierarchy. [#97, #100, #139]
- `cmd/cloudy`: CLI with `ingest` and `query` subcommands. `ingest` hosts
  the gRPC IngestService over the logstream WAL with bounded graceful stop.
  `query` hosts a QueryService stub. [#102, #105, #106]
- `ingest.Flusher`: tails the WAL, routes mutations into per-branch
  memtables, flushes monotonic segment blobs, and commits manifests with a
  CAS retry. [#103]
- `query`: in-memory columnar table and manifest loader. [#107, #111, #128]
- `query/distance`: float32 distance kernels (L2 squared, dot product,
  cosine, normalize) in three variants: pure scalar, portable simd, and
  archsimd Neon. Portable simd is the production path on Go 1.27 with
  GOEXPERIMENT=simd. Benchmarks live in docs/benchmarks/distance.md. [#108]
- Bazel Python and Protobuf integration: [#101, #104]
  - Hermetic Python 3.13 toolchain and pip package parsing via `rules_python`. [#101]
  - Python Protobuf and gRPC stubs via `rules_proto_grpc_python`. [#101, #104]
  - Python target generation in `BUILD.bazel` files via `rules_python_gazelle_plugin`. [#101]
  - Dependency manifest mapping and validation via `gazelle_python_manifest`. [#101]
  - Hermetic requirements compilation and locking via `rules_uv`. [#101]
  - Ruff formatting for Python files via `//:format`. [#101]
  - `//scripts:demoload` runnable `py_binary` target. [#101, #102]
- `kvfs`: Layer 2 Key-Value Store foundation. [#77, #78, #79, #80, #83, #84, #88]
  - `proto/kvfs/v1/kvfs.proto`: Protobuf schemas for `Manifest`, `ManifestEntry`,
    and `Mutation`. [#77]
  - `kvfs/cas.go`: Content-Addressed Storage (CAS) blob engine with SHA-256
    hashing, automatic deduplication, and 2-byte prefix sharding (`cas/<h0>/<h1>/<hash>`). [#77]
  - `kvfs/manifest.go`: Immutable Protobuf manifest storage under `manifests/<h0>/<h1>/<hash>`. [#78]
  - `kvfs/branch.go`: Branch pointer resolution, generation CAS updates, and atomic branch creation under `refs/heads/<branch>`. [#78]
  - `kvfs/store.go`: Branch-scoped key-value store with atomic Batch commits, point reads, writes, deletes, background WAL compaction, Fork, in-memory manifest lease caching, and singleflight coalescing. [#79, #80, #83, #84, #88]
- `recordio`: an append-only record framing engine. A frame holds a 12-byte
  header with the payload length and a length CRC, then the payload, then a
  4-byte payload CRC. Both CRCs use Castagnoli and a rotation mask. [#34, #48]
- `recordio.Writer`: buffered record writes with `Flush`, `Sync`, and `Close`.
  `WriteRecordFrom` streams a record of known length from an `io.Reader`. A
  failed sync or a short source poisons the writer. [#34]
- `recordio.Reader`: reads one record into a caller buffer. It peeks the
  header, so `ErrBufferTooSmall` and `ErrRecordTooLarge` leave the stream on
  the frame start. The caller can retry with a larger buffer. [#34]
- `recordio.Scanner`: iterates records and lends the payload through `Record`.
  `Skip` passes a payload with `Seek` when the source is an `io.Seeker`, and
  never computes the payload CRC. [#34]
- Write-ahead log recovery separates a torn tail (`ErrTornWrite`) from
  mid-stream corruption (`ErrHeaderCorrupted` and `ErrDataCorrupted`).
  `LastValidOffset` gives the truncation point. [#34]
- `docs/design/recordio.md`: the design note for the format and the API. [#32, #33]

### Changed

- `namespace`: unified `ValidateTenant`, `ValidateNamespace`, and `ValidateBranch` into single `ValidateName`. [#149]
- `recordio`: inlined single-caller `mask` helper into `computeMaskedCRC`. [#149]
- Inlined table mutations directly on `Table` and added nil guard to `Table.Clone`. [#147]
- Simplified `BranchLifecycleEvent.Type` enum and removed unused fields from `SegmentRef`. [#146]
- `ingest`: routed log streams under tenant prefixes and removed root catalog fallbacks. [#144]
- Replaced `kvfs` package with `manifest` package for branch manifest reads and CAS writes. [#142]
- Flattened segment storage under `<tenant>/ns/<namespace>/segments/` without branch subdirectories. [#142]
- Unified storage layout and key paths under explicit tenant and namespace prefixes. [#142]
- `query.Engine`: simplified branch sync to discover branches through `branches.json` without internal branch state wrappers. [#142]
- `cmd/cloudy`: renamed `-listen` to `-addr` and `-url` to `-storage-root`. [#141]
- `cmd/cloudy`: internalized `walStream` and set `maxMsgSize` as a code constant. [#141]
- `ingest.Flusher`: simplified flusher to materialize segments directly per WAL sequence and drain partial sequences on shutdown. [#140]
- `query.Table`: replaced chunked copy-on-write slices with flat contiguous vector storage. [#133, #134]
- `query.Loader`: stream segments directly into table builders without intermediate slice buffers. [#133, #134]
- `logstream.Log`: removed lock held across network I/O in Append. [#134]
- `ingest.Flusher`: inlined drain helper and advanced tail CAS monotonically. [#134]
- Migrated the wire format, ingest, and query to the protobuf Record message. [#97, #99, #100]
- Replaced `scripts/requirements.txt` with root `requirements.txt`. [#101]
- Standardized Protobuf dependencies across `grpcapi`, `kvfs`, and `segment` on generated `*_go_proto` targets. [#98, #101, #104]
- `logstream`: Unified stream and prefix into a single prefix path parameter in `logstream.New`. Removed `WithPrefix` option. [#81]

### Removed

- `objectstore`: deleted `Condition.validate` and validation call sites. [#150]
- `namespace`: deleted redundant `NewScope` constructor. [#150]
- `recordio`: deleted `Writer.Sync` capability sniffing. [#150]
- Deleted dead sentinels `ErrNilLog`, `recordio.ErrUnexpectedEOF`, and `recordio.ErrBufferTooSmall`. [#149]
- `query.Table.Builder`: deleted redundant forwarding builder wrapper and constructors. [#147]
- `storage.proto`: deleted unused `BlockEntry` and `SegmentFooter` schemas and fields. [#146]
- `kvfs`: deleted key-value filesystem package in favor of `manifest` package. [#142]
- `query`: deleted `getOrCreateBranch` and `branchState` wrappers. [#142]
- `ingest.BatchIngester`: removed server-side batch buffering, actor goroutine, and timer loops. [#140]
- `cmd/cloudy`: removed `-batch-docs`, `-batch-interval`, `-stream`, and `-max-msg-size` flags. [#140, #141]
- `query.QueryExecutor`: deleted redundant query executor abstraction. [#133, #134]
- `query/distance`: deleted dead SIMD intrinsics and fallback stubs. [#134]

### Fixed

- `ingest.Flusher`: fixed cancellation handling to retry the active sequence on shutdown rather than skipping records. [#140]
- `ingest.Ingester`: roll back created branch if appending the fork event fails. [#140]
- `cloudy ingest`: gRPC GracefulStop bounded by a 5-second timeout with a
  hard-stop fallback. The server stops when the flusher stops. [#105]
- `recordio.Reader`: a non-EOF read error inside a payload or footer now
  poisons the reader. Previously, the reader stayed usable and misread payloads
  as headers. [#34, #52]
