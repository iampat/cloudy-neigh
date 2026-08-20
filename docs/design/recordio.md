# RecordIO Go Library Design

**Status:** Draft — 2026-08-20 — v0

## Problem

Streaming large volumes of structured binary records (most notably serialized Protocol Buffers) is a fundamental primitive across data pipelines, write-ahead logs (WAL), training datasets, and distributed batch processing.

Standard record serialization formats face three conflicting constraints:
1. **Data Integrity vs Throughput:** Records must be individually validated against bit-rot and network/disk corruption without paying a devastating CPU penalty on checksum calculation.
2. **Streaming vs Memory Footprint:** Applications must stream multi-gigabyte or terabyte files record-by-record with a bounded, constant memory profile (O(1) heap allocation).
3. **Format Interoperability vs Ergonomics:** Storage formats must remain byte-payload agnostic and wire-compatible with standard ecosystems (e.g. TensorFlow TFRecord / RecordIO framing), while application code requires strongly-typed, ergonomic APIs with zero serialization overhead.

This document defines the architecture, wire format, memory model, and public Go API for a high-performance, zero-allocation **RecordIO** library in Go.

---

## Goals

- **Zero-Allocation Hot Path:** Achieve 0 B/op and 0 allocs/op in steady-state record writing and sequential scanning.
- **Standard Framing Compatibility:** Comply strictly with the canonical RecordIO / TFRecord framing specification (16-byte framing overhead with Little-Endian uint64 length and masked CRC32C checksums).
- **First-Class Protobuf Integration:** Provide type-safe generic wrappers (`ProtoWriter[T]`, `ProtoScanner[T]`) that marshal directly into pre-allocated write buffers via `proto.MarshalOptions.MarshalAppend`.
- **Hardware-Accelerated Integrity:** Utilize hardware-accelerated CRC32C (SSE4.2 on x86_64, ARMv8 CRC extensions on aarch64) via `hash/crc32` with Castagnoli polynomial.
- **Idiomatic Go API:** Expose both a high-level `bufio.Scanner`-style iterator and low-level stream primitives (`io.Reader`, `io.Writer`, `io.Seeker`).
- **Layered Extensibility:** Support pluggable stream-level and chunk-level compression (Zstd, Snappy, Gzip) without compromising core zero-copy semantics.

---

## Non-Goals

- **Random Record Access / Secondary Indexing:** RecordIO is strictly a sequential, append-only streaming format. Point lookups by record offset or primary key require an external index structure (e.g. sidecar index `.idx` or SSTable index blocks).
- **Columnar Layouts / SQL Predicate Pushdown:** RecordIO is strictly a sequential, row-oriented binary streaming format. Columnar projection (Parquet, ORC, Arrow) is out of scope.
- **In-Place Updates:** RecordIO files are append-only. Mutation requires rewriting or compaction.
- **Distributed Coordination:** The library provides single-stream / range readers; cluster-level partition assignment is handled by the calling orchestration system.

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
2. **Buffered I/O by Default:** The `Writer` embeds a default 64 KB staging buffer (`DefaultBufferSize = 64 * 1024`) to amortize syscall overhead. Writing 100B–1KB records without buffering would bottleneck on kernel transitions.
3. **Concurrency Contract:** `recordio.Writer` is strictly **single-goroutine (unlocked)** for maximum throughput. Callers with concurrent producers synchronize externally via mutex or fan-in channels.
4. **`WriteRecordFrom(io.Reader, int64)`:** Allows piping large blobs directly into the stream without holding the full record in memory.

### B. Zero-Allocation Read Path (`Scanner`)

1. **Buffer Reusability:** The `Scanner` owns a single internal backing buffer (`[]byte`) that dynamically grows up to `MaxRecordSize` (default: 64 MB), reused across every subsequent record.
2. **Sub-slice Borrowing:** `Scanner.Record()` returns a sub-slice pointing directly into the internal buffer:
   - **Contract:** The returned slice is valid **only until the next call to `Scan()`**.
   - **Ownership Escape:** Callers needing to retain the record across iterations must call `Scanner.RecordCopy()`, which explicitly allocates a dedicated copy.
3. **Poison-Pill & OOM Defense:** Before allocating or growing any buffer, the scanner validates `Length <= MaxRecordSize` **and** verifies `LengthCRC`. Corrupted length headers (e.g. `0xFFFFFFFFFFFFFFFF`) fail immediately with zero heap allocation.
4. **`io.Seeker` Fast-Path for `Skip()`:** If the underlying reader satisfies `io.Seeker` (such as `*os.File`), `Scanner.Skip()` performs a single fast kernel `Seek(N + 4, io.SeekCurrent)` syscall (~200 ns) to bypass payloads without transferring discard bytes across user/kernel space. For streaming network inputs (`io.Reader`), it falls back to streaming discard.

---

## 3. Go API Surface

### A. Package Organization

```text
recordio/
├── crc.go             # SIMD Castagnoli CRC32C and masking
├── options.go         # Functional options (buffer sizes, validation modes)
├── writer.go          # Low-level and buffered RecordIO writer
├── reader.go          # Low-level Reader (explicit buffer control)
├── scanner.go         # High-level idiomatic iterator (Scanner)
├── proto.go           # Generic ProtoWriter[T] and ProtoScanner[T]
├── compression.go     # Stream-level compression codecs (None, Zstd, Snappy, Gzip)
└── recordio_test.go   # Correctness, fuzzing, and alloc benchmarks
```

### B. Core Writer API

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
// Thread-safety: Not safe for concurrent writes; callers synchronize if needed.
func (w *Writer) WriteRecord(record []byte) (int, error)

// WriteRecordFrom streams a record directly from an io.Reader of known length.
func (w *Writer) WriteRecordFrom(r io.Reader, length int64) (int64, error)

// Flush flushes any pending buffered data to the underlying writer.
func (w *Writer) Flush() error

// Close flushes data and closes any wrapped resources.
func (w *Writer) Close() error
```

### C. Core Scanner API

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

// Record returns the most recently scanned record.
// The underlying memory is owned by the Scanner and is overwritten on the next Scan() call.
func (s *Scanner) Record() []byte

// RecordCopy returns a standalone, heap-allocated copy of the current record.
func (s *Scanner) RecordCopy() []byte

// Offset returns the byte offset in the underlying stream where the current record begins.
func (s *Scanner) Offset() int64

// Skip skips the next record without reading its payload into the record buffer.
func (s *Scanner) Skip() bool

// Err returns the first non-EOF error encountered by the scanner.
func (s *Scanner) Err() error
```

### D. Generic Protobuf Layer

```go
package recordio

import (
    "google.golang.org/protobuf/proto"
)

// ProtoWriter writes typed protobuf messages with zero intermediate slice allocations.
type ProtoWriter[T proto.Message] struct {
    // unexported fields
}

// NewProtoWriter creates a typed protobuf writer wrapping a recordio.Writer.
func NewProtoWriter[T proto.Message](w *Writer, opts ...ProtoWriterOption) *ProtoWriter[T]

// Write marshals and writes a typed protobuf message directly into the underlying record stream.
func (pw *ProtoWriter[T]) Write(msg T) error

// ProtoScanner scans typed protobuf messages directly from a RecordIO stream.
type ProtoScanner[T proto.Message] struct {
    // unexported fields
}

// NewProtoScanner creates a typed protobuf scanner wrapping a recordio.Scanner.
func NewProtoScanner[T proto.Message](s *Scanner, factory func() T, opts ...ProtoScannerOption) *ProtoScanner[T]

// Scan advances the scanner to the next record and unmarshals it into a typed protobuf message.
func (ps *ProtoScanner[T]) Scan() bool

// Message returns the most recently scanned typed protobuf message.
func (ps *ProtoScanner[T]) Message() T

// Err returns the first non-EOF error encountered by the scanner.
func (ps *ProtoScanner[T]) Err() error
```

---

## 4. Error Handling & Corruption Semantics

Data integrity failures must be surfaced with precise error types to enable recovery or clean failure logging:

```go
var (
    ErrHeaderCorrupted = errors.New("recordio: header length CRC mismatch")
    ErrDataCorrupted   = errors.New("recordio: payload data CRC mismatch")
    ErrUnexpectedEOF   = errors.New("recordio: unexpected EOF within record")
    ErrRecordTooLarge  = errors.New("recordio: record size exceeds max limit")
)
```

### Corruption Recovery Behavior

1. **Header CRC Mismatch:** If `LengthCRC` does not match the calculated CRC of `Length`, the stream is desynchronized. Readers fail immediately unless configured in resilient scan mode.
2. **Payload CRC Mismatch:** If `DataCRC` fails, the scanner flags `ErrDataCorrupted`. In permissive mode, the scanner can advance by $(N + 4)$ bytes and attempt to read the subsequent record.
3. **Clean EOF vs Truncated Record:** If `io.EOF` occurs on an exact 16-byte boundary between records, `Scan()` returns `false` with `Err() == nil`. If EOF occurs inside a record header or payload, `Err()` reports `ErrUnexpectedEOF`.

---

## 5. Extensibility: Compression Codecs

Compression is designed as an orthogonal layer wrapping the stream or record blocks:

```go
type Codec string

const (
    CompressionNone   Codec = "none"
    CompressionZstd   Codec = "zstd"
    CompressionSnappy Codec = "snappy"
    CompressionGzip   Codec = "gzip"
)
```

Because RecordIO operates over standard `io.Writer` and `io.Reader` interfaces, stream-level compression (e.g. Zstd framing over the entire RecordIO stream) is supported out of the box with zero modifications to framing logic.

---

## 6. Implementation Milestones & Tracking

### Milestone 1: Core Zero-Copy Engine
- [ ] **Framing & Checksumming (`crc.go`)**
  - [ ] Implement hardware-accelerated CRC32C using Castagnoli polynomial (`hash/crc32`).
  - [ ] Implement `Mask(crc uint32) uint32` and `Unmask(masked uint32) uint32`.
  - [ ] Unit tests verifying standard test vectors and round-trip masking.
- [ ] **Low-Level & Buffered Writer (`writer.go`)**
  - [ ] Implement `Writer` with stack-allocated header/footer staging (`[12]byte`, `[4]byte`).
  - [ ] Implement default 64 KB buffered staging (`DefaultBufferSize = 64 * 1024`).
  - [ ] Implement `WriteRecord(record []byte) (int, error)`.
  - [ ] Implement `WriteRecordFrom(r io.Reader, length int64) (int64, error)`.
  - [ ] Implement `Flush() error` and `Close() error`.
  - [ ] Writer options (`WithBufferSize`, `WithSyncOnFlush`).
- [ ] **Streaming Scanner & Reader (`scanner.go`, `reader.go`)**
  - [ ] Implement `Scanner` with reusable buffer and sub-slice borrowing (`Record() []byte`).
  - [ ] Implement `Scan() bool`, `RecordCopy() []byte`, `Offset() int64`, and `Err() error`.
  - [ ] Implement `Skip() bool` with `io.Seeker` fast-path (~200ns `lseek`) and streaming fallback.
  - [ ] Implement `Reader` for explicit caller-managed buffer control (`ReadRecord(buf []byte)`).
  - [ ] Scanner options (`WithInitialBufferSize`, `WithMaxRecordSize` defaulting to 64 MB).
- [ ] **Verification & Allocation Benchmarks (`recordio_test.go`)**
  - [ ] Verify 0 B/op and 0 allocs/op in steady-state loop via `testing.AllocsPerRun`.
  - [ ] Defensive bounds validation: ensure corrupted length headers (e.g. `0xFFFFFFFFFFFFFFFF`) fail with zero memory allocation.
  - [ ] Comprehensive edge cases: 0-byte payloads, multi-megabyte payloads, header bit-flip corruption, payload bit-flip corruption, truncated files at each byte offset.

### Milestone 2: Generic Protobuf Layer
- [ ] **Generic ProtoWriter (`proto.go`)**
  - [ ] Implement `ProtoWriter[T proto.Message]` utilizing `proto.MarshalOptions.MarshalAppend` to avoid heap allocations.
  - [ ] Support custom `proto.MarshalOptions` (deterministic serialization, emit unpopulated fields).
- [ ] **Generic ProtoScanner (`proto.go`)**
  - [ ] Implement `ProtoScanner[T proto.Message]` unmarshaling directly from borrowed `Scanner.Record()` slice.
  - [ ] Support reusable message instance pooling and custom `proto.UnmarshalOptions`.
- [ ] **Protobuf Integration Benchmarks**
  - [ ] Benchmark proto serialization and deserialization throughput and allocs against raw byte baselines.

### Milestone 3: Stream Compression
- [ ] **Compression Adapters (`compression.go`)**
  - [ ] Define `CompressionCodec` enum and options (`None`, `Zstd`, `Snappy`, `Gzip`).
  - [ ] Implement `Zstd` reader/writer wrappers (`klauspost/compress/zstd`).
  - [ ] Implement `Snappy` reader/writer wrappers (`golang/snappy`).
  - [ ] Implement `Gzip` reader/writer wrappers (`compress/gzip`).
- [ ] **Compression Tests & Benchmarks**
  - [ ] Round-trip validation across all codecs with varying payload entropy and sizes.
  - [ ] Benchmark throughput and compression ratios.

### Milestone 4: Range Reader & Hardening
- [ ] **Distributed Shard Range Reader (`splittable.go`)**
  - [ ] Implement `RangeScanner` over byte range `[startOffset, endOffset)` with record sync boundary detection.
- [ ] **Fuzzing & Hardening**
  - [ ] Native Go fuzz tests (`testing.F`) for scanner resilience against corrupted and malformed inputs.

