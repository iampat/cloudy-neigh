# Changelog

### Added

- `kvfs`: Layer 2 Key-Value Store foundation.
  - `proto/kvfs/v1/kvfs.proto`: Protobuf schemas for `Manifest`, `ManifestEntry`,
    and `Mutation`.
  - `kvfs/cas.go`: Content-Addressed Storage (CAS) blob engine with SHA-256
    hashing, automatic deduplication, and 2-byte prefix sharding (`cas/<h0>/<h1>/<hash>`).
  - `kvfs/manifest.go`: Immutable Protobuf manifest storage under `manifests/<h0>/<h1>/<hash>`.
  - `kvfs/branch.go`: Branch pointer resolution, generation CAS updates, and atomic branch creation under `refs/heads/<branch>`.
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
