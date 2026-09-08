# Changelog

### Added

- Bazel Python and Protobuf integration:
  - Hermetic Python 3.13 toolchain and pip package parsing via `rules_python`.
  - Python Protobuf and gRPC stubs via `rules_proto_grpc_python`.
  - Python target generation in `BUILD.bazel` files via `rules_python_gazelle_plugin`.
  - Dependency manifest mapping and validation via `gazelle_python_manifest`.
  - Hermetic requirements compilation and locking via `rules_uv`.
  - Ruff formatting for Python files via `//:format`.
  - `//scripts:demoload` runnable `py_binary` target.
- `kvfs`: Layer 2 Key-Value Store foundation.
  - `proto/kvfs/v1/kvfs.proto`: Protobuf schemas for `Manifest`, `ManifestEntry`,
    and `Mutation`.
  - `kvfs/cas.go`: Content-Addressed Storage (CAS) blob engine with SHA-256
    hashing, automatic deduplication, and 2-byte prefix sharding (`cas/<h0>/<h1>/<hash>`).
  - `kvfs/manifest.go`: Immutable Protobuf manifest storage under `manifests/<h0>/<h1>/<hash>`.
  - `kvfs/branch.go`: Branch pointer resolution, generation CAS updates, and atomic branch creation under `refs/heads/<branch>`.
  - `kvfs/store.go`: Branch-scoped key-value store with atomic Batch commits, point reads, writes, deletes, background WAL compaction, Fork, in-memory manifest lease caching, and singleflight coalescing.
- `recordio`: an append-only record framing engine. A frame holds a 12-byte
  header with the payload length and a length CRC, then the payload, then a
  4-byte payload CRC. Both CRCs use Castagnoli and a rotation mask.
- `recordio.Writer`: buffered record writes with `Flush`, `Sync`, and `Close`.
  `WriteRecordFrom` streams a record of known length from an `io.Reader`. A
  failed sync or a short source poisons the writer.
- `recordio.Reader`: reads one record into a caller buffer. It peeks the
  header, so `ErrBufferTooSmall` and `ErrRecordTooLarge` leave the stream on
  the frame start. The caller can retry with a larger buffer.
- `recordio.Scanner`: iterates records and lends the payload through `Record`.
  `Skip` passes a payload with `Seek` when the source is an `io.Seeker`, and
  never computes the payload CRC.
- Write-ahead log recovery separates a torn tail (`ErrTornWrite`) from
  mid-stream corruption (`ErrHeaderCorrupted` and `ErrDataCorrupted`).
  `LastValidOffset` gives the truncation point.
- `docs/design/recordio.md`: the design note for the format and the API.

### Changed

- Replaced `scripts/requirements.txt` with root `requirements.txt`.
- Standardized Protobuf dependencies across `grpcapi`, `kvfs`, and `segment` on generated `*_go_proto` targets.
- `logstream`: Unified stream and prefix into a single prefix path parameter in `logstream.New`. Removed `WithPrefix` option.

### Fixed

- `recordio.Reader`: a non-EOF read error inside a payload or footer now
  poisons the reader. Previously, the reader stayed usable and misread payloads
  as headers.
