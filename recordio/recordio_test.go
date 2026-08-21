package recordio_test

import (
	"bytes"
	"errors"
	"io"
	"math/rand"
	"os"
	"testing"

	"github.com/iampat/cloudy-neigh/recordio"
)

type mockSyncerCloser struct {
	bytes.Buffer
	synced bool
	closed bool
	err    error
}

func (m *mockSyncerCloser) Sync() error {
	if m.err != nil {
		return m.err
	}
	m.synced = true
	return nil
}

func (m *mockSyncerCloser) Close() error {
	m.closed = true
	return nil
}

type nonSeekerReader struct {
	r io.Reader
}

func (n *nonSeekerReader) Read(p []byte) (int, error) {
	return n.r.Read(p)
}

type errReader struct {
	err error
}

func (e *errReader) Read(p []byte) (int, error) {
	return 0, e.err
}

func TestWriterReader_RoundTrip(t *testing.T) {
	var buf bytes.Buffer
	writer := recordio.NewWriter(&buf)

	records := [][]byte{
		{},
		[]byte("hello world"),
		bytes.Repeat([]byte("a"), 1000),
		bytes.Repeat([]byte("b"), 128*1024),
	}

	expectedOffsets := make([]int64, len(records))
	for i, rec := range records {
		expectedOffsets[i] = writer.Offset()
		n, offset, err := writer.WriteRecord(rec)
		if err != nil {
			t.Fatalf("WriteRecord(%d) failed: %v", i, err)
		}
		if offset != expectedOffsets[i] {
			t.Errorf("WriteRecord(%d) offset = %d, want %d", i, offset, expectedOffsets[i])
		}
		if n != len(rec)+16 {
			t.Errorf("WriteRecord(%d) n = %d, want %d", i, n, len(rec)+16)
		}
	}

	if err := writer.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	reader := recordio.NewReader(bytes.NewReader(buf.Bytes()))
	dst := make([]byte, 256*1024)

	for i, expected := range records {
		n, err := reader.ReadRecord(dst)
		if err != nil {
			t.Fatalf("ReadRecord(%d) failed: %v", i, err)
		}
		if n != len(expected) {
			t.Errorf("ReadRecord(%d) n = %d, want %d", i, n, len(expected))
		}
		if !bytes.Equal(dst[:n], expected) {
			t.Errorf("ReadRecord(%d) payload mismatch", i)
		}
		if reader.Offset() != expectedOffsets[i] {
			t.Errorf("reader.Offset(%d) = %d, want %d", i, reader.Offset(), expectedOffsets[i])
		}
	}

	_, err := reader.ReadRecord(dst)
	if !errors.Is(err, io.EOF) {
		t.Errorf("ReadRecord at end of stream err = %v, want io.EOF", err)
	}
	if reader.LastValidOffset() != writer.Offset() {
		t.Errorf("reader.LastValidOffset = %d, want %d", reader.LastValidOffset(), writer.Offset())
	}
}

func TestWriter_WriteRecordFrom(t *testing.T) {
	t.Run("HappyPath", func(t *testing.T) {
		var buf bytes.Buffer
		writer := recordio.NewWriter(&buf)

		payload := []byte("streamed-payload-content")
		src := bytes.NewReader(payload)

		startOffset := writer.Offset()
		n, offset, err := writer.WriteRecordFrom(src, int64(len(payload)))
		if err != nil {
			t.Fatalf("WriteRecordFrom failed: %v", err)
		}
		if offset != startOffset {
			t.Errorf("offset = %d, want %d", offset, startOffset)
		}
		if n != int64(len(payload)+16) {
			t.Errorf("n = %d, want %d", n, len(payload)+16)
		}
		if err := writer.Flush(); err != nil {
			t.Fatalf("Flush failed: %v", err)
		}

		scanner := recordio.NewScanner(&buf)
		if !scanner.Scan() {
			t.Fatalf("Scan failed: %v", scanner.Err())
		}
		if !bytes.Equal(scanner.Record(), payload) {
			t.Errorf("scanned record = %q, want %q", scanner.Record(), payload)
		}
	})

	t.Run("ShortRead_PoisonContract", func(t *testing.T) {
		var buf bytes.Buffer
		writer := recordio.NewWriter(&buf)

		shortPayload := []byte("short")
		src := bytes.NewReader(shortPayload)

		_, _, err := writer.WriteRecordFrom(src, 100)
		if !errors.Is(err, recordio.ErrUnexpectedEOF) {
			t.Errorf("WriteRecordFrom short read err = %v, want ErrUnexpectedEOF", err)
		}

		_, _, err = writer.WriteRecord([]byte("after poison"))
		if !errors.Is(err, recordio.ErrUnexpectedEOF) {
			t.Errorf("WriteRecord after poison err = %v, want ErrUnexpectedEOF", err)
		}
	})
}

func TestWriter_SyncAndClose(t *testing.T) {
	mock := &mockSyncerCloser{}
	writer := recordio.NewWriter(mock, recordio.WithWriterSyncOnFlush(true))

	_, _, err := writer.WriteRecord([]byte("durability test"))
	if err != nil {
		t.Fatalf("WriteRecord failed: %v", err)
	}

	if err := writer.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}
	if !mock.synced {
		t.Errorf("mock.synced = false, want true after Flush with syncOnFlush")
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if !mock.closed {
		t.Errorf("mock.closed = false, want true after Close")
	}

	_, _, err = writer.WriteRecord([]byte("after close"))
	if !errors.Is(err, os.ErrClosed) {
		t.Errorf("WriteRecord after close err = %v, want os.ErrClosed", err)
	}
}

func TestWriter_SyncFailurePoison(t *testing.T) {
	syncErr := errors.New("fsync failed: disk error")
	mock := &mockSyncerCloser{err: syncErr}
	writer := recordio.NewWriter(mock)

	_, _, err := writer.WriteRecord([]byte("data"))
	if err != nil {
		t.Fatalf("WriteRecord failed: %v", err)
	}

	if err := writer.Sync(); !errors.Is(err, syncErr) {
		t.Fatalf("Sync() err = %v, want %v", err, syncErr)
	}

	_, _, err = writer.WriteRecord([]byte("new data"))
	if !errors.Is(err, syncErr) {
		t.Errorf("WriteRecord after failed Sync err = %v, want %v", err, syncErr)
	}
}

func TestReader_BufferLimitsAndRetry(t *testing.T) {
	var buf bytes.Buffer
	writer := recordio.NewWriter(&buf)
	payload1 := []byte("first-1234567890")
	payload2 := []byte("second-record")
	_, _, _ = writer.WriteRecord(payload1)
	_, _, _ = writer.WriteRecord(payload2)
	_ = writer.Flush()

	t.Run("BufferTooSmall_Retryable", func(t *testing.T) {
		reader := recordio.NewReader(bytes.NewReader(buf.Bytes()))
		smallDst := make([]byte, 5)

		needed, err := reader.ReadRecord(smallDst)
		if !errors.Is(err, recordio.ErrBufferTooSmall) {
			t.Fatalf("ReadRecord small buffer err = %v, want ErrBufferTooSmall", err)
		}
		if needed != len(payload1) {
			t.Errorf("needed length = %d, want %d", needed, len(payload1))
		}

		// Retrying with correctly sized buffer must succeed and not read corrupt payload
		largeDst := make([]byte, needed)
		n, err := reader.ReadRecord(largeDst)
		if err != nil {
			t.Fatalf("Retry ReadRecord failed: %v", err)
		}
		if !bytes.Equal(largeDst[:n], payload1) {
			t.Errorf("Retry payload = %q, want %q", largeDst[:n], payload1)
		}

		// Second record should read normally
		n, err = reader.ReadRecord(largeDst)
		if err != nil {
			t.Fatalf("Read second record failed: %v", err)
		}
		if !bytes.Equal(largeDst[:n], payload2) {
			t.Errorf("Second record = %q, want %q", largeDst[:n], payload2)
		}
	})

	t.Run("RecordTooLarge", func(t *testing.T) {
		reader := recordio.NewReader(bytes.NewReader(buf.Bytes()), recordio.WithReaderMaxRecordSize(5))
		dst := make([]byte, 20)
		_, err := reader.ReadRecord(dst)
		if !errors.Is(err, recordio.ErrRecordTooLarge) {
			t.Errorf("ReadRecord exceeding MaxRecordSize err = %v, want ErrRecordTooLarge", err)
		}
	})
}

func TestScanner_ScanAndRecordCopy(t *testing.T) {
	var buf bytes.Buffer
	writer := recordio.NewWriter(&buf)

	data := [][]byte{
		[]byte("record-1"),
		[]byte("record-2"),
		[]byte("record-3"),
	}

	for _, d := range data {
		if _, _, err := writer.WriteRecord(d); err != nil {
			t.Fatalf("WriteRecord failed: %v", err)
		}
	}
	_ = writer.Flush()

	scanner := recordio.NewScanner(bytes.NewReader(buf.Bytes()), recordio.WithScannerInitialBufferSize(8))

	var copies [][]byte
	idx := 0
	for scanner.Scan() {
		if idx >= len(data) {
			t.Fatalf("scanned more records than expected")
		}
		if !bytes.Equal(scanner.Record(), data[idx]) {
			t.Errorf("Record %d = %q, want %q", idx, scanner.Record(), data[idx])
		}
		copies = append(copies, scanner.RecordCopy())
		idx++
	}

	if err := scanner.Err(); err != nil {
		t.Fatalf("Scanner error: %v", err)
	}

	if len(copies) != len(data) {
		t.Fatalf("Scanned %d copies, want %d", len(copies), len(data))
	}

	for i, c := range copies {
		if !bytes.Equal(c, data[i]) {
			t.Errorf("RecordCopy %d = %q, want %q", i, c, data[i])
		}
	}
}

func TestScanner_Skip(t *testing.T) {
	testData := [][]byte{
		[]byte("record-alpha"),
		[]byte("record-beta-to-skip"),
		[]byte("record-gamma"),
		[]byte("record-delta-to-skip"),
		[]byte("record-epsilon"),
	}

	runSkipTest := func(t *testing.T, r io.Reader) {
		scanner := recordio.NewScanner(r)

		if !scanner.Scan() || !bytes.Equal(scanner.Record(), testData[0]) {
			t.Fatalf("Scan alpha failed: %v", scanner.Err())
		}
		if !scanner.Skip() {
			t.Fatalf("Skip beta failed: %v", scanner.Err())
		}
		if !scanner.Scan() || !bytes.Equal(scanner.Record(), testData[2]) {
			t.Fatalf("Scan gamma failed: %v", scanner.Err())
		}
		if !scanner.Skip() {
			t.Fatalf("Skip delta failed: %v", scanner.Err())
		}
		if !scanner.Scan() || !bytes.Equal(scanner.Record(), testData[4]) {
			t.Fatalf("Scan epsilon failed: %v", scanner.Err())
		}
		if scanner.Scan() {
			t.Errorf("Scan at EOF returned true")
		}
		if scanner.Err() != nil {
			t.Errorf("Scanner err at clean EOF: %v", scanner.Err())
		}
	}

	var buf bytes.Buffer
	writer := recordio.NewWriter(&buf)
	for _, d := range testData {
		_, _, _ = writer.WriteRecord(d)
	}
	_ = writer.Flush()

	rawBytes := buf.Bytes()

	t.Run("WithSeeker", func(t *testing.T) {
		runSkipTest(t, bytes.NewReader(rawBytes))
	})

	t.Run("WithoutSeeker", func(t *testing.T) {
		runSkipTest(t, &nonSeekerReader{r: bytes.NewReader(rawBytes)})
	})
}

func TestWALRecovery_TornWrite(t *testing.T) {
	var buf bytes.Buffer
	writer := recordio.NewWriter(&buf)

	_, _, _ = writer.WriteRecord([]byte("valid-record-1"))
	validEndOffset := writer.Offset()
	_, _, _ = writer.WriteRecord([]byte("valid-record-2-payload-data"))
	_ = writer.Flush()

	fullBytes := buf.Bytes()

	t.Run("ScanTruncationLoop", func(t *testing.T) {
		for cut := int(validEndOffset) + 1; cut < len(fullBytes); cut++ {
			truncatedData := fullBytes[:cut]
			scanner := recordio.NewScanner(bytes.NewReader(truncatedData))

			if !scanner.Scan() {
				t.Fatalf("cut=%d: Scan record 1 failed: %v", cut, scanner.Err())
			}
			if !bytes.Equal(scanner.Record(), []byte("valid-record-1")) {
				t.Fatalf("cut=%d: record 1 mismatch", cut)
			}

			if scanner.Scan() {
				t.Fatalf("cut=%d: Scan record 2 succeeded on truncated data", cut)
			}
			if !errors.Is(scanner.Err(), recordio.ErrTornWrite) {
				t.Errorf("cut=%d: Err = %v, want ErrTornWrite", cut, scanner.Err())
			}
			if scanner.LastValidOffset() != validEndOffset {
				t.Errorf("cut=%d: LastValidOffset = %d, want %d", cut, scanner.LastValidOffset(), validEndOffset)
			}
		}
	})

	t.Run("SkipTruncationLoop_Seeker", func(t *testing.T) {
		for cut := int(validEndOffset) + 1; cut < len(fullBytes); cut++ {
			truncatedData := fullBytes[:cut]
			scanner := recordio.NewScanner(bytes.NewReader(truncatedData))

			if !scanner.Scan() {
				t.Fatalf("cut=%d: Scan record 1 failed: %v", cut, scanner.Err())
			}

			// Skipping record 2 must detect torn write even when using seeker
			if scanner.Skip() {
				t.Fatalf("cut=%d: Skip record 2 succeeded on truncated data", cut)
			}
			if !errors.Is(scanner.Err(), recordio.ErrTornWrite) {
				t.Errorf("cut=%d: Err = %v, want ErrTornWrite", cut, scanner.Err())
			}
			if scanner.LastValidOffset() != validEndOffset {
				t.Errorf("cut=%d: LastValidOffset = %d, want %d", cut, scanner.LastValidOffset(), validEndOffset)
			}
		}
	})
}

func TestCorruptionDetection(t *testing.T) {
	var buf bytes.Buffer
	writer := recordio.NewWriter(&buf)

	_, _, _ = writer.WriteRecord([]byte("first-record"))
	firstEndOffset := writer.Offset()
	_, _, _ = writer.WriteRecord([]byte("second-record"))
	_ = writer.Flush()

	baseBytes := buf.Bytes()

	t.Run("HeaderCRCCorruption", func(t *testing.T) {
		corrupted := append([]byte(nil), baseBytes...)
		// Flip bit in header CRC (bytes 8..11 of second record)
		corrupted[firstEndOffset+9] ^= 0x01

		scanner := recordio.NewScanner(bytes.NewReader(corrupted))
		if !scanner.Scan() {
			t.Fatalf("first scan failed")
		}
		if scanner.Scan() {
			t.Errorf("second scan succeeded on corrupted header CRC")
		}
		if !errors.Is(scanner.Err(), recordio.ErrHeaderCorrupted) {
			t.Errorf("err = %v, want ErrHeaderCorrupted", scanner.Err())
		}
		if scanner.LastValidOffset() != firstEndOffset {
			t.Errorf("LastValidOffset = %d, want %d", scanner.LastValidOffset(), firstEndOffset)
		}
	})

	t.Run("PayloadDataCRCCorruption", func(t *testing.T) {
		corrupted := append([]byte(nil), baseBytes...)
		// Flip bit in payload of second record (offset + 12 is first byte of payload)
		corrupted[firstEndOffset+13] ^= 0x01

		scanner := recordio.NewScanner(bytes.NewReader(corrupted))
		if !scanner.Scan() {
			t.Fatalf("first scan failed")
		}
		if scanner.Scan() {
			t.Errorf("second scan succeeded on corrupted payload")
		}
		if !errors.Is(scanner.Err(), recordio.ErrDataCorrupted) {
			t.Errorf("err = %v, want ErrDataCorrupted", scanner.Err())
		}
		if scanner.LastValidOffset() != firstEndOffset {
			t.Errorf("LastValidOffset = %d, want %d", scanner.LastValidOffset(), firstEndOffset)
		}
	})

	t.Run("FooterCRCCorruption", func(t *testing.T) {
		corrupted := append([]byte(nil), baseBytes...)
		// Flip bit in footer CRC (last 4 bytes)
		corrupted[len(corrupted)-2] ^= 0x01

		scanner := recordio.NewScanner(bytes.NewReader(corrupted))
		if !scanner.Scan() {
			t.Fatalf("first scan failed")
		}
		if scanner.Scan() {
			t.Errorf("second scan succeeded on corrupted footer CRC")
		}
		if !errors.Is(scanner.Err(), recordio.ErrDataCorrupted) {
			t.Errorf("err = %v, want ErrDataCorrupted", scanner.Err())
		}
	})
}

func TestRealIOErrorsPreserved(t *testing.T) {
	customErr := errors.New("network timeout")
	reader := recordio.NewReader(&errReader{err: customErr})
	dst := make([]byte, 64)
	_, err := reader.ReadRecord(dst)
	if !errors.Is(err, customErr) {
		t.Errorf("Reader err = %v, want %v", err, customErr)
	}

	scanner := recordio.NewScanner(&errReader{err: customErr})
	if scanner.Scan() {
		t.Errorf("Scanner.Scan succeeded on errReader")
	}
	if !errors.Is(scanner.Err(), customErr) {
		t.Errorf("Scanner err = %v, want %v", scanner.Err(), customErr)
	}
}

func TestZeroAllocations(t *testing.T) {
	record := bytes.Repeat([]byte("x"), 256)

	t.Run("Writer_WriteRecord", func(t *testing.T) {
		writer := recordio.NewWriter(io.Discard, recordio.WithWriterBufferSize(64*1024))

		allocs := testing.AllocsPerRun(100, func() {
			_, _, _ = writer.WriteRecord(record)
		})
		if allocs != 0 {
			t.Errorf("Writer.WriteRecord allocs/op = %v, want 0", allocs)
		}
	})

	t.Run("Reader_ReadRecord", func(t *testing.T) {
		var buf bytes.Buffer
		writer := recordio.NewWriter(&buf)
		for i := 0; i < 200; i++ {
			_, _, _ = writer.WriteRecord(record)
		}
		_ = writer.Flush()

		reader := recordio.NewReader(bytes.NewReader(buf.Bytes()), recordio.WithReaderBufferSize(64*1024))
		dst := make([]byte, 512)

		allocs := testing.AllocsPerRun(100, func() {
			_, _ = reader.ReadRecord(dst)
		})
		if allocs != 0 {
			t.Errorf("Reader.ReadRecord allocs/op = %v, want 0", allocs)
		}
	})

	t.Run("Scanner_Scan", func(t *testing.T) {
		var buf bytes.Buffer
		writer := recordio.NewWriter(&buf)
		for i := 0; i < 200; i++ {
			_, _, _ = writer.WriteRecord(record)
		}
		_ = writer.Flush()

		scanner := recordio.NewScanner(bytes.NewReader(buf.Bytes()), recordio.WithScannerInitialBufferSize(512))

		allocs := testing.AllocsPerRun(100, func() {
			if scanner.Scan() {
				_ = scanner.Record()
			}
		})
		if allocs != 0 {
			t.Errorf("Scanner.Scan allocs/op = %v, want 0", allocs)
		}
	})
}

func FuzzScanner(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("short"))
	f.Add(bytes.Repeat([]byte{0xff}, 32))

	// Seed with valid record
	var buf bytes.Buffer
	writer := recordio.NewWriter(&buf)
	_, _, _ = writer.WriteRecord([]byte("fuzz-seed-record"))
	_ = writer.Flush()
	f.Add(buf.Bytes())

	f.Fuzz(func(t *testing.T, data []byte) {
		scanner := recordio.NewScanner(bytes.NewReader(data))
		var lastOffset int64
		for scanner.Scan() {
			rec := scanner.Record()
			if rec == nil && scanner.Err() == nil {
				// empty slice is valid, nil is acceptable if length was 0
			}
			offset := scanner.Offset()
			if offset < lastOffset {
				t.Fatalf("offset decreased: %d < %d", offset, lastOffset)
			}
			lastOffset = offset
		}

		if scanner.LastValidOffset() < 0 || scanner.LastValidOffset() > int64(len(data)) {
			t.Fatalf("LastValidOffset %d out of bounds for data len %d", scanner.LastValidOffset(), len(data))
		}
	})
}

func BenchmarkWriter_WriteRecord(b *testing.B) {
	record := make([]byte, 1024)
	rand.Read(record)

	b.ReportAllocs()
	b.SetBytes(int64(len(record) + 16))
	b.ResetTimer()

	writer := recordio.NewWriter(io.Discard, recordio.WithWriterBufferSize(64*1024))
	for i := 0; i < b.N; i++ {
		if _, _, err := writer.WriteRecord(record); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReader_ReadRecord(b *testing.B) {
	record := make([]byte, 1024)
	rand.Read(record)

	var buf bytes.Buffer
	writer := recordio.NewWriter(&buf)
	for i := 0; i < 5000; i++ {
		_, _, _ = writer.WriteRecord(record)
	}
	_ = writer.Flush()

	raw := buf.Bytes()
	dst := make([]byte, 2048)

	b.ReportAllocs()
	b.SetBytes(int64(len(record) + 16))
	b.ResetTimer()

	for i := 0; i < b.N; {
		reader := recordio.NewReader(bytes.NewReader(raw))
		for {
			_, err := reader.ReadRecord(dst)
			if err != nil {
				break
			}
			i++
			if i >= b.N {
				break
			}
		}
	}
}

func BenchmarkScanner_Scan(b *testing.B) {
	record := make([]byte, 1024)
	rand.Read(record)

	var buf bytes.Buffer
	writer := recordio.NewWriter(&buf)
	for i := 0; i < 5000; i++ {
		_, _, _ = writer.WriteRecord(record)
	}
	_ = writer.Flush()

	raw := buf.Bytes()

	b.ReportAllocs()
	b.SetBytes(int64(len(record) + 16))
	b.ResetTimer()

	for i := 0; i < b.N; {
		scanner := recordio.NewScanner(bytes.NewReader(raw), recordio.WithScannerInitialBufferSize(2048))
		for scanner.Scan() {
			_ = scanner.Record()
			i++
			if i >= b.N {
				break
			}
		}
	}
}
