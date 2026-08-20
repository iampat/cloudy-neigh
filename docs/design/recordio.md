# RecordIO

**Status:** Approved Draft — 2026-08-20 — v1

## Problem

Streaming large volumes of structured binary records (most notably serialized Protocol Buffers) is a fundamental primitive across data pipelines, write-ahead logs (WAL), training datasets, and distributed batch processing.

Standard record serialization formats face three conflicting constraints:
1. **Data Integrity vs Throughput:** Records must be individually validated against bit-rot and network/disk corruption without paying a devastating CPU penalty on checksum calculation.
2. **Streaming vs Memory Footprint:** Applications must stream multi-gigabyte or terabyte files record-by-record with a bounded, constant memory profile ($O(1)$ heap allocation).
3. **Format Interoperability vs Ergonomics:** Storage formats must remain byte-payload agnostic and wire-compatible with standard ecosystems (e.g. TensorFlow TFRecord / RecordIO framing), while application code requires strongly-typed, ergonomic APIs with zero serialization overhead.

This document defines the architecture, wire format, memory model, and public Go API for a high-performance, zero-allocation **RecordIO** library in Go.

---

## Goals

- **Zero-Allocation Hot Path:** Achieve 0 B/op and 0 allocs/op in steady-state record writing and sequential scanning.
- **Canonical Framing Compatibility:** Comply strictly with the canonical RecordIO / TFRecord framing specification (16-byte framing overhead with Little-Endian uint64 length and masked CRC32C checksums).
- **Zero-Dependency Core:** The root `recordio` package depends strictly on the Go standard library (`hash/crc32`, `io`, `bufio`, `errors`).
- **First-Class Protobuf Integration:** Provide type-safe generic sub-packages (`recordio/protoio`) that marshal directly into pre-allocated write buffers via `proto.MarshalOptions.MarshalAppend` and support zero-allocation deserialization via `ScanInto(dst T)`.
- **Hardware-Accelerated Integrity:** Utilize hardware-accelerated CRC32C (SSE4.2 on x86_64, ARMv8 CRC extensions on aarch64) via `hash/crc32` with Castagnoli polynomial.
- **Idiomatic Go API & Interface Segregation:** Expose both a high-level `bufio.Scanner`-style iterator (`Scanner`) and a low-level buffer-controlled streaming reader (`Reader`), alongside an offset-aware buffered `Writer`.
- **Durability & WAL Recovery Primitives:** Provide explicit offset reporting (`Offset() int64`), physical sync hooks (`Sync() error`), and precise crash-truncation boundaries (`LastValidOffset() int64`) for Write-Ahead Log implementations.
- **Layered Extensibility:** Support pluggable stream-level and chunk-level compression (Zstd, Snappy, Gzip) in isolated sub-packages (`recordio/codec/*`) with zero-value default compatibility.

---

## Non-Goals

- **Random Record Access / Secondary Indexing:** RecordIO is strictly a sequential, append-only streaming format. Point lookups by record offset or primary key require an external index structure (e.g. sidecar index `.idx` or SSTable index blocks).
- **Columnar Layouts / SQL Predicate Pushdown:** RecordIO is strictly a sequential, row-oriented binary streaming format. Columnar projection (Parquet, ORC, Arrow) is out of scope.
- **In-Place Updates:** RecordIO files are append-only. Mutation requires rewriting or compaction.
- **Distributed Coordination:** The library provides single-stream and range readers; cluster-level partition assignment is handled by the calling orchestration system.

---

## 1. Wire Format & Framing Specification

The canonical RecordIO framing encapsulates variable-length binary payloads with separate header and payload checksums to allow early corruption detection.

### Binary Layout

```text
+-----------------------+-----------------------------+-----------------------------------+-----------------------------+
| uint64: Length (8B)   | uint32: Masked CRC32C (4B)  | byte[]: Data Payload (Length B)   | uint32: Masked CRC32C (4B)  |
| Little-Endian         | Little-Endian               | Raw byte stream                   | Little-Endian               |
+-----------------------+-----------------------------+-----------------------------------+-----------------------------+
|<---------------- Header: 12 Bytes ----------------->|<----- Payload: Length Bytes ----->|<----- Footer: 4 Bytes ----->|
```

Every record adds exactly **16 bytes** of framing overhead.

### Field Definitions

| Field | Type | Size | Endianness | Description |
| :--- | :--- | :--- | :--- | :--- |
| `Length` | `uint64` | 8 bytes | Little-Endian | Byte length $N$ of the subsequent data payload. |
| `LengthCRC` | `uint32` | 4 bytes | Little-Endian | Masked CRC32C checksum computed over the 8-byte `Length`. |
| `Data` | `[]byte` | $N$ bytes | N/A | Raw payload bytes (e.g. serialized protobuf). |
| `DataCRC` | `uint32` | 4 bytes | Little-Endian | Masked CRC32C checksum computed over the $N$-byte `Data`. |

### Masked CRC32C Algorithm

RecordIO uses the Castagnoli polynomial ($0x82F63B78$) masked via bit rotation and constant addition. Masking prevents checksum collisions with file system signatures and ensures robustness when checksumming data that itself contains CRC values:

$$\text{MaskedCRC}(x) = \left( (x \gg 15) \mid (x \ll 17) \right) + 0\text{xa282ead8} \pmod{2^{32}}$$

$$\text{UnmaskedCRC}(y) = \left( ((y - 0\text{xa282ead8}) \gg 17) \mid ((y - 0\text{xa282ead8}) \ll 15) \right) \pmod{2^{32}}$$

---

## 2. Memory Model & Zero-Copy Architecture

In high-throughput systems (10GbE–100GbE ingestion, local NVMe reads at 2–7 GB/s), heap allocations dominate execution profiles, triggering severe Garbage Collection (GC) pauses and allocator contention. The core engine is designed for zero-copy operation and defensive bounds.

```text
                    [ Reader / OS File ]
                              │
                              ▼ (Sequential read / Seek)
┌──────────────────────────────────────────────────────────┐
│ Scanner Internal Ring / Reusable Buffer                  │
│ ┌───────────────┬────────────────────────┬─────────────┐ │
│ │  12B Header   │   Payload (N bytes)    │  4B Footer  │ │
│ └───────────────┴───────────┬────────────┴─────────────┘ │
└─────────────────────────────┼────────────────────────────┘
                              │
              Borrowed Sub-slice: buf[12 : 12+N]
                              │
                              ▼
               ┌─────────────────────────────┐
               │ Caller / Proto Unmarshaler  │  (Zero heap allocs)
               └─────────────────────────────┘
```

### A. Zero-Allocation Write Path

1. **Stack-Allocated Framing Staging:** The 12-byte header and 4-byte footer are serialized into fixed arrays on the stack (`var header [12]byte`, `var footer [4]byte`).
2. **Buffered I/O by Default:** The `Writer` embeds a default 64 KB staging buffer (`DefaultBufferSize = 64 * 1024`) to amortize syscall overhead. Writing 100B–1KB records without buffering would bottleneck on kernel transitions ($\sim 100\text{–}300\,\text{ns}$ per syscall).
3. **Offset Accounting:** The `Writer` tracks logical write stream offsets internally, returning the physical record start offset on every write operation for WAL and index builders.
4. **Concurrency Contract:** `recordio.Writer` is strictly **single-goroutine (unlocked)** for maximum throughput. Callers with concurrent producers synchronize externally via mutexes, channels, or ring buffers.
5. **`WriteRecordFrom(io.Reader, int64)` Failure Contract:** Streams large blobs directly into the writer buffer. If the source reader errors or returns an unexpected EOF before `length` bytes are read, the writer invalidates uncommitted frame data and transitions into a poisoned error state (`ErrUnexpectedEOF`) to prevent emitting corrupt half-frames.

### B. Zero-Allocation Read Path (`Scanner` vs. `Reader`)

1. **Interface Segregation:**
   - **`Scanner` (High-Level):** Manages an internal reusable buffer, offering a `bufio.Scanner`-style loop with sub-slice borrowing (`Record() []byte`).
   - **`Reader` (Low-Level):** Reads directly into a caller-supplied destination buffer (`ReadRecord(dst []byte)`), allowing zero-copy integration with caller-managed buffer pools (`sync.Pool`) or arenas.
2. **Sub-slice Borrowing:** `Scanner.Record()` returns a sub-slice pointing directly into the internal buffer:
   - **Contract:** The returned slice is valid **only until the next call to `Scan()`**.
   - **Ownership Escape:** Callers needing to retain the record across iterations must call `Scanner.RecordCopy()`, which explicitly allocates a dedicated copy.
3. **Poison-Pill & OOM Defense:** Before allocating or growing any buffer, the scanner validates `Length <= MaxRecordSize` **and** verifies `LengthCRC`. Corrupted length headers (e.g. `0xFFFFFFFFFFFFFFFF`) fail immediately with zero heap allocation.
4. **`io.Seeker` Fast-Path for `Skip()`:** If the underlying reader satisfies `io.Seeker` (such as `*os.File`), `Scanner.Skip()` consumes any remaining buffered unread bytes first, adjusts seek offset, and performs a fast kernel `Seek(N + 4, io.SeekCurrent)` syscall (~200 ns) to bypass payloads without transferring discard bytes across user/kernel space.
   - *Integrity Note:* `Skip()` validates `LengthCRC` but skips payload transfer, bypassing `DataCRC` computation.

---

## 3. Go API Surface & Package Organization

### A. Package Organization

To enforce clean dependency boundaries ("A little copying is better than a little dependency"), external integrations and codecs are isolated into sub-packages:

```text
recordio/
├── crc.go             # SIMD Castagnoli CRC32C and masking (Go stdlib only)
├── options.go         # Functional options (buffer sizes, validation modes)
├── writer.go          # Buffered & offset-aware RecordIO writer
├── reader.go          # Low-level Reader (caller-managed buffer control)
├── scanner.go         # High-level idiomatic iterator (Scanner)
├── recordio_test.go   # Correctness, fuzzing, and alloc benchmarks
│
├── protoio/           # Generic Protobuf integration (optional dependency)
│   ├── proto.go       # ProtoWriter[T] and ProtoScanner[T] with ScanInto
│   └── proto_test.go  # Protobuf zero-alloc benchmarks
│
└── codec/             # Optional compression codecs
    ├── codec.go       # Codec interfaces and registry
    ├── zstd/          # Zstandard compression adapter
    ├── snappy/        # Snappy compression adapter
    └── gzip/          # Gzip compression adapter
```

### B. Core Writer API (`recordio`)

```go
package recordio

import (
    "io"
)

type Writer struct {
    // unexported fields
}

// NewWriter creates a new RecordIO writer with optional configurations.
func NewWriter(w io.Writer, opts ...WriterOption) *Writer

// WriteRecord writes a single binary record with length prefix and CRC checksums.
// Returns total wire bytes written (n = len(record) + 16), the physical start offset
// of the record in the stream, and any error encountered.
// Thread-safety: Not safe for concurrent writes; callers synchronize if needed.
func (w *Writer) WriteRecord(record []byte) (n int, offset int64, err error)

// WriteRecordFrom streams a record directly from an io.Reader of known length.
// If r fails or returns early, Writer discards staged framing and returns ErrUnexpectedEOF.
func (w *Writer) WriteRecordFrom(r io.Reader, length int64) (n int64, offset int64, err error)

// Offset returns the current logical byte offset in the write stream.
func (w *Writer) Offset() int64

// Flush flushes pending buffered data from user-space memory to the underlying io.Writer.
func (w *Writer) Flush() error

// Sync flushes user-space buffers and invokes Sync() error on the underlying destination
// if it implements interface{ Sync() error } (e.g. *os.File).
// Returns nil without error if the underlying destination does not implement Sync().
// Required for WAL commit durability guarantees.
func (w *Writer) Sync() error

// Close flushes data and closes the underlying writer if it implements io.Closer.
func (w *Writer) Close() error
```

### C. Low-Level Reader API (`recordio`)

```go
package recordio

import "io"

type Reader struct {
    // unexported fields
}

// NewReader creates a low-level record reader over an io.Reader.
func NewReader(r io.Reader, opts ...ReaderOption) *Reader

// ReadRecord reads the next record payload directly into dst.
// If len(dst) < record length, returns ErrBufferTooSmall.
// Returns io.EOF on clean stream end.
func (r *Reader) ReadRecord(dst []byte) (n int, err error)

// Offset returns the byte offset where the current record begins.
func (r *Reader) Offset() int64

// LastValidOffset returns the byte offset up to which all records were cleanly validated.
func (r *Reader) LastValidOffset() int64
```

### D. High-Level Scanner API (`recordio`)

```go
package recordio

import "io"

type Scanner struct {
    // unexported fields
}

// NewScanner creates a new record scanner over an io.Reader.
func NewScanner(r io.Reader, opts ...ScannerOption) *Scanner

// Scan advances the scanner to the next record.
// Returns false on EOF or unrecoverable error.
func (s *Scanner) Scan() bool

// Record returns the most recently scanned record payload.
// The underlying memory is owned by the Scanner and is overwritten on the next Scan() call.
func (s *Scanner) Record() []byte

// RecordCopy returns a standalone, heap-allocated copy of the current record.
func (s *Scanner) RecordCopy() []byte

// Offset returns the byte offset in the underlying stream where the current record's header begins.
func (s *Scanner) Offset() int64

// LastValidOffset returns the byte offset up to which all previous records were cleanly validated
// (i.e. the byte offset immediately after the footer of the last valid record).
// Essential for WAL crash recovery to safely truncate uncommitted torn writes at the tail.
func (s *Scanner) LastValidOffset() int64

// Skip skips the next record payload without copying it into the record buffer.
// LengthCRC is validated; DataCRC is bypassed.
func (s *Scanner) Skip() bool

// Err returns the first non-EOF error encountered by the scanner.
func (s *Scanner) Err() error
```

### E. Generic Protobuf Layer (`recordio/protoio`)

```go
package protoio

import (
    "github.com/iampat/cloudy-neigh/recordio"
    "google.golang.org/protobuf/proto"
)

// ProtoWriter writes typed protobuf messages directly with zero intermediate slice allocations.
type ProtoWriter[T proto.Message] struct {
    // unexported fields
}

// NewProtoWriter creates a typed protobuf writer wrapping a recordio.Writer.
func NewProtoWriter[T proto.Message](w *recordio.Writer, opts ...ProtoWriterOption) *ProtoWriter[T]

// Write marshals msg via proto.MarshalOptions.MarshalAppend directly into the writer buffer.
// Returns the stream start offset of the written message.
func (pw *ProtoWriter[T]) Write(msg T) (offset int64, err error)

// ProtoScanner scans typed protobuf messages directly from a RecordIO stream.
type ProtoScanner[T proto.Message] struct {
    // unexported fields
}

// NewProtoScanner creates a typed protobuf scanner wrapping a recordio.Scanner.
func NewProtoScanner[T proto.Message](s *recordio.Scanner, opts ...ProtoScannerOption) *ProtoScanner[T]

// Scan advances to the next record. Returns false on EOF or error.
func (ps *ProtoScanner[T]) Scan() bool

// ScanInto unmarshals the current record directly into the caller-provided destination (0 allocs).
func (ps *ProtoScanner[T]) ScanInto(dst T) error

// Err returns the first non-EOF error encountered by the scanner.
func (ps *ProtoScanner[T]) Err() error
```

---

## 4. Configuration & Zero-Value Semantics

To respect Go Proverbs ("Make the zero value useful"), all option structs and enums default to safe production parameters when zero-initialized:

```go
package recordio

const (
    DefaultBufferSize    = 64 * 1024        // 64 KB write/read staging buffer
    DefaultMaxRecordSize = 64 * 1024 * 1024 // 64 MB allocation bomb guard
)

type Codec string

const (
    CompressionNone   Codec = ""       // Zero value defaults to uncompressed
    CompressionZstd   Codec = "zstd"
    CompressionSnappy Codec = "snappy"
    CompressionGzip   Codec = "gzip"
)

type Options struct {
    BufferSize    int   // 0 uses DefaultBufferSize (64 KB)
    MaxRecordSize int64 // 0 uses DefaultMaxRecordSize (64 MB)
    Compression   Codec // "" uses CompressionNone
}
```

---

## 5. Error Handling & WAL Recovery Semantics

Data integrity failures are surfaced with precise sentinel errors to differentiate between crash-induced torn writes and mid-stream data corruption:

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

### WAL Crash Recovery & Replay Workflow

In a Write-Ahead Log (WAL), records represent an ordered sequence of state machine mutations. The recovery engine enforces two distinct policies depending on where corruption occurs:

```text
Scenario A: Crash during write (Torn Write at Tail) -> RECOVERABLE
[ Record 1 (OK) ] [ Record 2 (OK) ] [ Record 3 (Half-written / Crashed) ] EOF
                                    └── Truncate file at LastValidOffset()

Scenario B: Bit-rot in the middle of the log -> FATAL
[ Record 1 (OK) ] [ Record 2 (Corrupt Length/CRC) ] [ Record 3 (OK) ] ... EOF
                  └── HARD STOP! Fail-fast to preserve prefix consistency.
```

1. **Tail Crash / Torn Write (`ErrTornWrite` / `ErrUnexpectedEOF`):**
   - If an incomplete 12-byte header or truncated payload is encountered immediately followed by `io.EOF`, the scanner returns `ErrTornWrite`.
   - The WAL replay engine retrieves `scanner.LastValidOffset()` and truncates the file back to the end of the last complete transaction (`os.Truncate(file, scanner.LastValidOffset())`).
2. **Mid-Stream Bit-Rot (`ErrHeaderCorrupted` / `ErrDataCorrupted`):**
   - If `LengthCRC` or `DataCRC` fails in the middle of the log, iteration immediately terminates.
   - **Prefix Invariant:** The scanner **never** skips forward over corrupted mid-log records during WAL replay, preventing state machine divergence.
3. **Clean EOF:** If `io.EOF` occurs on an exact 16-byte boundary between records, `Scan()` returns `false` with `Err() == nil`.

---

## 6. Extensibility: Compression Codecs & Range Invariants

Compression is supported across two distinct layers with strictly defined trade-offs:

1. **Stream-Level Compression (`recordio/codec/*`):**
   - Wraps the entire `io.Reader` / `io.Writer` in a continuous compressed stream (e.g. Zstd / Gzip framing).
   - **Constraint:** Stream-compressed files are **monolithic and non-splittable**. Random seeking, `io.Seeker` fast-paths, and multi-worker byte-range splitting (`RangeScanner`) are disabled.
2. **Block-Level Compression (Future Extension):**
   - Compresses fixed chunks (e.g. 64 KB blocks containing $N$ records, similar to Pebble/LevelDB SSTable blocks).
   - **Capability:** Preserves seekability and parallel MapReduce-style range sharding.

---

## 7. Implementation Milestones & Tracking

### Milestone 1: Core Zero-Dependency Engine (`recordio`)
- [ ] **Framing & Checksumming (`crc.go`)**
  - [ ] Implement hardware-accelerated CRC32C using Castagnoli polynomial (`hash/crc32`).
  - [ ] Implement `Mask(crc uint32) uint32` and `Unmask(masked uint32) uint32`.
  - [ ] Unit tests verifying standard test vectors and round-trip masking.
- [ ] **Low-Level & Buffered Writer (`writer.go`)**
  - [ ] Implement `Writer` with stack-allocated header/footer staging (`[12]byte`, `[4]byte`).
  - [ ] Implement default 64 KB buffered staging (`DefaultBufferSize = 64 * 1024`).
  - [ ] Implement `WriteRecord(record []byte) (n int, offset int64, err error)`.
  - [ ] Implement `WriteRecordFrom(r io.Reader, length int64) (n int64, offset int64, err error)` with rollback on read failure.
  - [ ] Implement `Offset() int64`, `Flush() error`, and `Close() error`.
  - [ ] Implement `Sync() error` (`fsync` durability contract on commit).
  - [ ] Writer options (`WithBufferSize`, `WithSyncOnFlush`).
- [ ] **Streaming Scanner & Low-Level Reader (`scanner.go`, `reader.go`)**
  - [ ] Implement `Reader` for explicit caller-managed buffer control (`ReadRecord(dst []byte) (n int, err error)`).
  - [ ] Implement `Scanner` with reusable buffer and sub-slice borrowing (`Record() []byte`).
  - [ ] Implement `Scan() bool`, `RecordCopy() []byte`, `Offset() int64`, and `Err() error`.
  - [ ] Implement `LastValidOffset() int64` for crash-recovery tail truncation.
  - [ ] Implement `Skip() bool` with user-buffer drain and `io.Seeker` fast-path (~200ns `lseek`).
  - [ ] Scanner options (`WithInitialBufferSize`, `WithMaxRecordSize` defaulting to 64 MB).
- [ ] **Verification & Allocation Benchmarks (`recordio_test.go`)**
  - [ ] Verify 0 B/op and 0 allocs/op in steady-state loop via `testing.AllocsPerRun`.
  - [ ] Defensive bounds validation: ensure corrupted length headers (e.g. `0xFFFFFFFFFFFFFFFF`) fail with zero memory allocation.
  - [ ] WAL crash recovery test: simulate partial writes at stream tail and assert `ErrTornWrite` + accurate `LastValidOffset()`.
  - [ ] Comprehensive edge cases: 0-byte payloads, multi-megabyte payloads, header bit-flip corruption, payload bit-flip corruption, truncated files at each byte offset.

### Milestone 2: Generic Protobuf Sub-Package (`recordio/protoio`)
- [ ] **Generic ProtoWriter (`protoio/proto.go`)**
  - [ ] Implement `ProtoWriter[T proto.Message]` utilizing `proto.MarshalOptions.MarshalAppend` to avoid heap allocations.
  - [ ] Return record start offset on `Write(msg T) (offset int64, err error)`.
- [ ] **Generic ProtoScanner (`protoio/proto.go`)**
  - [ ] Implement `ProtoScanner[T proto.Message]` with `ScanInto(dst T) error` unmarshaling directly from borrowed record slice (0 allocs).
- [ ] **Protobuf Integration Benchmarks (`protoio/proto_test.go`)**
  - [ ] Benchmark proto serialization and deserialization throughput and allocs against raw byte baselines.

### Milestone 3: Compression Adapters (`recordio/codec/*`)
- [ ] **Compression Adapters**
  - [ ] Define `Codec` type with `CompressionNone = ""` zero-value default.
  - [ ] Implement `recordio/codec/zstd` adapter (`klauspost/compress/zstd`).
  - [ ] Implement `recordio/codec/snappy` adapter (`golang/snappy`).
  - [ ] Implement `recordio/codec/gzip` adapter (`compress/gzip`).
- [ ] **Compression Tests & Benchmarks**
  - [ ] Round-trip validation across all codecs with varying payload entropy and sizes.
  - [ ] Benchmark throughput and compression ratios.

### Milestone 4: Distributed Range Reader & Hardening
- [ ] **Distributed Shard Range Reader (`recordio/splittable.go`)**
  - [ ] Implement `RangeScanner` over byte range `[startOffset, endOffset)` for uncompressed streams.
- [ ] **Fuzzing & Hardening**
  - [ ] Native Go fuzz tests (`testing.F`) for scanner resilience against corrupted and malformed inputs.


