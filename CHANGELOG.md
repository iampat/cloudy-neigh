# Changelog

### Added

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

- `recordio.Reader`: a non-EOF read error inside a payload or a footer now
  poisons the reader. Before, the reader consumed the header and stayed
  usable, so the next `ReadRecord` read payload bytes as a header and
  reported `ErrHeaderCorrupted` for a transient input or output fault.
