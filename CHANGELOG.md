# Changelog

### Added

- `kvfs`: Layer 2 Key-Value Store foundation.
  - `proto/kvfs/v1/kvfs.proto`: Protobuf schemas for `Manifest`, `ManifestEntry`,
    and `Mutation`.
  - `kvfs/cas.go`: Content-Addressed Storage (CAS) blob engine with SHA-256
    hashing, automatic deduplication, and 2-byte prefix sharding (`objects/<h0>/<h1>/<hash>`).
- `logstream`: Layer 1 append-only write-ahead log over object storage.
  - Atomic conditional append protocol using generation preconditions.
  - Multi-record batch append (`Append(ctx, records ...Record)`).
  - Exponential jump probing for O(log N) cold-start tail recovery.
  - Adaptive RTT tracking and probe win streak optimization.
- `objectstore`: Layer 0 storage driver interface.
  - Drivers for Google Cloud Storage (`gs://`), local filesystem (`file://`), and in-memory (`mem://`).
  - `Stat` method for lightweight object metadata inspection.
  - `Get` returning `(io.ReadCloser, Object, error)` in a single call.
- `cmd/walbench`: Multi-writer benchmarking tool measuring LogStream append
  throughput, write amplification, and collision recovery.
- `docs/design`: Architecture specifications for `storage.md`, `wal.md`,
  `kvfs.md`, `consumer.md`, and `grpc-api.md`.
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

### Fixed

- `recordio.Reader`: a non-EOF read error inside a payload or footer now
  poisons the reader. Previously, the reader stayed usable and misread payloads
  as headers.
