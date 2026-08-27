# RecordIO

**Status:** Draft — 2026-08-20 — v1

## Problem

Streaming large volumes of structured binary records, such as serialized
Protocol Buffers (protobuf), is a common task in data pipelines, write-ahead
logs (WAL), training datasets, and distributed batch processing.

Standard record serialization formats face three conflicting constraints:
1. Data integrity against disk corruption without slow checksum calculation.
2. Streaming large files record by record with a constant memory profile.
3. Storage formats that stay payload agnostic while Go applications need
   idiomatic APIs.

This document defines the wire format, memory model, and public Go API for a
RecordIO library in Go.

## Goals

- Zero heap allocations in steady-state record writing and sequential scanning.
- Framing compatibility with the RecordIO and TFRecord specification (16-byte
  framing with Little-Endian uint64 length and masked CRC32C checksums).
- Core package depends only on the Go standard library (`hash/crc32`, `io`,
  `bufio`, `errors`).
- Type-safe generic sub-packages (`recordio/protoio`) for protobuf messages.
- Hardware-accelerated CRC32C with the Castagnoli polynomial through
  `hash/crc32`.
- Separate low-level `Reader` and high-level `Scanner` types.
- Explicit offset reporting, physical sync hooks, and crash-truncation boundaries
  for Write-Ahead Log implementations.
- Extensibility for stream and chunk compression in sub-packages.

## Non-goals

- Random record access or secondary indexing. RecordIO is a sequential,
  append-only streaming format.
- Columnar layouts and SQL predicate pushdown.
- In-place record updates.
- Distributed coordination across partition workers.

## Future work

- Block-level compression that preserves seekability across record chunks.
- Sharded range scanner for parallel MapReduce-style readers.
- Pluggable compression codecs (`recordio/codec/*`) for Zstandard, Snappy, and
  Gzip.
- Generic protobuf integration (`recordio/protoio`).

## Model

A **stream** is an ordered sequence of bytes containing binary records.

A **record** is a single variable-length byte payload.

A **frame** is the wire encapsulation of a record, containing a 12-byte header,
the data payload, and a 4-byte footer.

Offsets name physical positions in a stream:
- **Write stream offset:** The current position of the write head, where the next
  frame will begin.
- **Record start offset:** The physical byte offset of the first byte of a
  record's 12-byte header.
- **Last valid offset:** The byte offset immediately after the 4-byte footer of
  the last cleanly verified record.

A **torn write** is a partial frame at the tail of a stream, caused by a crash
or unexpected EOF before the footer was written.

## Wire format

Each record is encapsulated by 16 bytes of framing overhead.

```text
┌───────────┬───────────┬──────────┬───────────┐
│ Length    │ LengthCRC │ Data     │ DataCRC   │
│ uint64 8B │ uint32 4B │ N bytes  │ uint32 4B │
└───────────┴───────────┴──────────┴───────────┘
└──── header 12B ───────┘          └ footer 4B ┘
```

The header contains:
- `Length` (8 bytes, Little-Endian): Length N of the payload.
- `LengthCRC` (4 bytes, Little-Endian): Masked CRC32C checksum of `Length`.

The body contains:
- `Data` (N bytes): Raw byte payload.

The footer contains:
- `DataCRC` (4 bytes, Little-Endian): Masked CRC32C checksum of `Data`.

### Checksum calculation

RecordIO uses the Castagnoli polynomial (0x82F63B78) masked with bit rotation
and constant addition:

```go
const maskDelta = 0xa282ead8

func mask(crc uint32) uint32 {
	return ((crc >> 15) | (crc << 17)) + maskDelta
}

func unmask(masked uint32) uint32 {
	rot := masked - maskDelta
	return (rot >> 17) | (rot << 15)
}
```

Masking protects against checksum collisions with file system signatures.

## Memory model

```text
               [ File / io.Reader ]
                        │
                        ▼ (Sequential read / Seek)
┌────────────────────────────────────────────────────────┐
│ Scanner Reusable Buffer                                │
│ ┌───────────────┬────────────────────────┬───────────┐ │
│ │  12B Header   │   Payload (N bytes)    │ 4B Footer │ │
│ └───────────────┴───────────┬────────────┴───────────┘ │
└─────────────────────────────┼──────────────────────────┘
                              │
              Borrowed sub-slice: buf[12 : 12+N]
                              │
                              ▼
                [ Caller / Deserializer ]
```

### Write path

1. **Fixed frame staging:** The 12-byte header and 4-byte footer use preallocated
   arrays to prevent heap allocations.
2. **Buffered output:** The `Writer` uses a default 64 KB buffer to amortize
   syscall overhead.
3. **Offset accounting:** The `Writer` tracks the stream offset and returns the
   record start offset for every write.
4. **Single-goroutine:** `recordio.Writer` is not safe for concurrent writes.
   Callers must synchronize external access.

### Read path

1. **Interface segregation:**
   - `Scanner` manages a reusable buffer with sub-slice borrowing (`Record()`).
   - `Reader` reads directly into caller-managed buffers (`ReadRecord(dst)`).
2. **Sub-slice borrowing:** `Scanner.Record()` returns a slice pointing into the
   internal buffer. It is valid only until the next call to `Scan()`.
3. **Memory limit protection:** Before allocating memory, the scanner verifies
   that `Length <= MaxRecordSize` and checks `LengthCRC`. Corrupted length
   headers fail without allocating memory.
4. **Fast skip:** When the source implements `io.Seeker`, `Scanner.Skip()`
   discards buffered bytes and seeks the file position forward.

## API

### Writer

```go
package recordio

type Writer struct {
	// unexported fields
}

func NewWriter(w io.Writer, opts ...WriterOption) *Writer
func (w *Writer) WriteRecord(record []byte) (n int, offset int64, err error)
func (w *Writer) WriteRecordFrom(r io.Reader, length int64) (n int64, offset int64, err error)
func (w *Writer) Offset() int64
func (w *Writer) Flush() error
func (w *Writer) Sync() error
func (w *Writer) Close() error
```

`WriteRecord` returns the total bytes written and the record start offset.

`WriteRecordFrom` streams a record of known length from `r`. If `r` fails before
reading `length` bytes, the writer transitions to a poisoned error state
(`ErrUnexpectedEOF`). The stream can end with a torn frame that recovery
truncates.

`Sync` flushes user-space buffers and invokes `Sync()` on the destination if it
implements `interface{ Sync() error }` (such as `*os.File`). A failed sync
poisons the writer.

### Reader

```go
type Reader struct {
	// unexported fields
}

func NewReader(r io.Reader, opts ...ReaderOption) *Reader
func (r *Reader) ReadRecord(dst []byte) (n int, err error)
func (r *Reader) Offset() int64
func (r *Reader) LastValidOffset() int64
```

`ReadRecord` reads the next payload into `dst`. If `len(dst)` is less than the
payload length, it leaves the stream at the record start and returns
`(int(length), ErrBufferTooSmall)`.

`Offset` returns the start offset of the current record.

`LastValidOffset` returns the offset up to which records were validated.

### Scanner

```go
type Scanner struct {
	// unexported fields
}

func NewScanner(r io.Reader, opts ...ScannerOption) *Scanner
func (s *Scanner) Scan() bool
func (s *Scanner) Record() []byte
func (s *Scanner) RecordCopy() []byte
func (s *Scanner) Offset() int64
func (s *Scanner) LastValidOffset() int64
func (s *Scanner) Skip() bool
func (s *Scanner) Err() error
```

`Record` returns a borrowed slice valid until the next `Scan()` call.

`RecordCopy` returns an independent copy of the record.

`Skip` advances past the next record, validating `LengthCRC` while bypassing the
payload read and data checksum. It reads the footer to detect mid-payload
truncations.

## Configuration

```go
const (
	DefaultBufferSize    = 64 * 1024        // 64 KB
	DefaultMaxRecordSize = 64 * 1024 * 1024 // 64 MB
)

func WithWriterBufferSize(size int) WriterOption
func WithWriterSyncOnFlush(sync bool) WriterOption

func WithReaderBufferSize(size int) ReaderOption
func WithReaderMaxRecordSize(max int) ReaderOption

func WithScannerBufferSize(size int) ScannerOption
func WithScannerInitialBufferSize(size int) ScannerOption
func WithScannerMaxRecordSize(max int) ScannerOption
```

## Error handling and WAL recovery

```go
var (
	ErrTornWrite       = errors.New("recordio: incomplete record at stream tail (torn write)")
	ErrHeaderCorrupted = errors.New("recordio: header length CRC mismatch mid-stream")
	ErrDataCorrupted   = errors.New("recordio: payload data CRC mismatch mid-stream")
	ErrUnexpectedEOF   = errors.New("recordio: unexpected EOF within record")
	ErrRecordTooLarge  = errors.New("recordio: record size exceeds max limit")
	ErrBufferTooSmall  = errors.New("recordio: user destination buffer too small for record")
)
```

### WAL recovery workflow

```text
Scenario A: Crash during write (torn write at tail) -> Recoverable
┌──────────────────┬──────────────────┬──────────────────────┐
│ Record 1 (Valid) │ Record 2 (Valid) │ Record 3 (Truncated) │ EOF
└──────────────────┴──────────────────┴──────────────────────┘
                                      └── Truncate file at LastValidOffset()

Scenario B: Bit-rot mid-stream -> Fatal
┌──────────────────┬──────────────────┬──────────────────────┐
│ Record 1 (Valid) │ Record 2 (CRC)   │ Record 3 (Valid)     │ EOF
└──────────────────┴──────────────────┴──────────────────────┘
                   └── Stop replay to prevent state divergence
```

1. **Tail crash (`ErrTornWrite`):** If an incomplete header or payload meets
   `io.EOF`, the scanner returns `ErrTornWrite`. The recovery caller uses
   `LastValidOffset()` to truncate the file.
2. **Mid-stream corruption (`ErrHeaderCorrupted` / `ErrDataCorrupted`):** If a
   checksum fails mid-stream, iteration stops immediately. The scanner never
   skips past corrupted records during replay.
3. **Clean EOF:** If `io.EOF` occurs on an exact frame boundary, `Scan()`
   returns `false` and `Err()` returns `nil`.

## Milestones

- **Milestone 1: Core Engine (`recordio`)**
  - Framing and checksum calculations (`crc.go`).
  - Low-level and buffered writer with durability hooks (`writer.go`).
  - Low-level reader with destination buffer control (`reader.go`).
  - High-level scanner with sub-slice borrowing and fast skip (`scanner.go`).
  - Unit tests, WAL recovery verification, and zero-allocation benchmarks.
- **Milestone 2: Generic Protobuf Sub-package (`recordio/protoio`)**
  - `ProtoWriter[T proto.Message]` with zero-allocation marshal append.
  - `ProtoScanner[T proto.Message]` with direct unmarshaling into destination.
- **Milestone 3: Compression Adapters (`recordio/codec/*`)**
  - Stream-level compression adapters for Zstandard, Snappy, and Gzip.
- **Milestone 4: Distributed Range Reader and Fuzzing**
  - Byte-range scanner for uncompressed streams.
  - Fuzz tests for malformed input handling.
