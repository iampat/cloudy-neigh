package recordio_test

import (
	"bytes"
	"errors"
	"io"
	"math/rand"
	"os"
	"testing"

	"github.com/iampat/cloudy-neigh/recordio"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

type faultyReader struct {
	data   []byte
	pos    int
	failAt int
	failed bool
	err    error
}

func (f *faultyReader) Read(p []byte) (int, error) {
	if !f.failed && f.pos >= f.failAt {
		f.failed = true
		return 0, f.err
	}
	if f.pos >= len(f.data) {
		return 0, io.EOF
	}
	n := len(p)
	if !f.failed && f.pos+n > f.failAt {
		n = f.failAt - f.pos
	}
	if f.pos+n > len(f.data) {
		n = len(f.data) - f.pos
	}
	copy(p, f.data[f.pos:f.pos+n])
	f.pos += n
	return n, nil
}

func TestWriterReaderRoundTrip(t *testing.T) {
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
		require.NoError(t, err)
		assert.Equal(t, expectedOffsets[i], offset)
		assert.Equal(t, len(rec)+16, n)
	}

	require.NoError(t, writer.Flush())

	reader := recordio.NewReader(bytes.NewReader(buf.Bytes()))
	dst := make([]byte, 256*1024)

	for i, expected := range records {
		n, err := reader.ReadRecord(dst)
		require.NoError(t, err)
		assert.Equal(t, len(expected), n)
		assert.Equal(t, expected, dst[:n])
		assert.Equal(t, expectedOffsets[i], reader.Offset())
	}

	_, err := reader.ReadRecord(dst)
	assert.ErrorIs(t, err, io.EOF)
	assert.Equal(t, writer.Offset(), reader.LastValidOffset())
}

func TestWriterWriteRecordFrom(t *testing.T) {
	t.Run("HappyPath", func(t *testing.T) {
		var buf bytes.Buffer
		writer := recordio.NewWriter(&buf)

		payload := []byte("streamed-payload-content")
		src := bytes.NewReader(payload)

		startOffset := writer.Offset()
		n, offset, err := writer.WriteRecordFrom(src, int64(len(payload)))
		require.NoError(t, err)
		assert.Equal(t, startOffset, offset)
		assert.Equal(t, int64(len(payload)+16), n)
		require.NoError(t, writer.Flush())

		scanner := recordio.NewScanner(&buf)
		require.True(t, scanner.Scan())
		assert.NoError(t, scanner.Err())
		assert.Equal(t, payload, scanner.Record())
	})

	t.Run("ShortRead_PoisonContract", func(t *testing.T) {
		var buf bytes.Buffer
		writer := recordio.NewWriter(&buf)

		shortPayload := []byte("short")
		src := bytes.NewReader(shortPayload)

		_, _, err := writer.WriteRecordFrom(src, 100)
		assert.ErrorIs(t, err, recordio.ErrUnexpectedEOF)

		_, _, err = writer.WriteRecord([]byte("after poison"))
		assert.ErrorIs(t, err, recordio.ErrUnexpectedEOF)
	})
}

func TestWriterSyncAndClose(t *testing.T) {
	mock := &mockSyncerCloser{}
	writer := recordio.NewWriter(mock, recordio.WithWriterSyncOnFlush(true))

	_, _, err := writer.WriteRecord([]byte("durability test"))
	require.NoError(t, err)

	require.NoError(t, writer.Flush())
	assert.True(t, mock.synced)

	require.NoError(t, writer.Close())
	assert.True(t, mock.closed)

	_, _, err = writer.WriteRecord([]byte("after close"))
	assert.ErrorIs(t, err, os.ErrClosed)
}

func TestWriterSyncFailurePoison(t *testing.T) {
	syncErr := errors.New("fsync failed: disk error")
	mock := &mockSyncerCloser{err: syncErr}
	writer := recordio.NewWriter(mock)

	_, _, err := writer.WriteRecord([]byte("data"))
	require.NoError(t, err)

	assert.ErrorIs(t, writer.Sync(), syncErr)

	_, _, err = writer.WriteRecord([]byte("new data"))
	assert.ErrorIs(t, err, syncErr)
}

func TestReaderBufferLimitsAndRetry(t *testing.T) {
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
		assert.ErrorIs(t, err, recordio.ErrBufferTooSmall)
		assert.Equal(t, len(payload1), needed)

		largeDst := make([]byte, needed)
		n, err := reader.ReadRecord(largeDst)
		require.NoError(t, err)
		assert.Equal(t, payload1, largeDst[:n])

		n, err = reader.ReadRecord(largeDst)
		require.NoError(t, err)
		assert.Equal(t, payload2, largeDst[:n])
	})

	t.Run("RecordTooLarge", func(t *testing.T) {
		reader := recordio.NewReader(bytes.NewReader(buf.Bytes()), recordio.WithReaderMaxRecordSize(5))
		dst := make([]byte, 20)
		_, err := reader.ReadRecord(dst)
		assert.ErrorIs(t, err, recordio.ErrRecordTooLarge)
	})

	t.Run("ScannerRecordTooLarge", func(t *testing.T) {
		scanner := recordio.NewScanner(bytes.NewReader(buf.Bytes()), recordio.WithScannerMaxRecordSize(5))
		assert.False(t, scanner.Scan())
		assert.ErrorIs(t, scanner.Err(), recordio.ErrRecordTooLarge)
	})

	t.Run("NonPositiveMaxRecordSizeIgnored", func(t *testing.T) {
		reader := recordio.NewReader(bytes.NewReader(buf.Bytes()), recordio.WithReaderMaxRecordSize(-10))
		dst := make([]byte, 20)
		n, err := reader.ReadRecord(dst)
		require.NoError(t, err)
		assert.Equal(t, len(payload1), n)
	})
}

func TestScannerScanAndRecordCopy(t *testing.T) {
	var buf bytes.Buffer
	writer := recordio.NewWriter(&buf)

	data := [][]byte{
		[]byte("record-1"),
		[]byte("record-2"),
		[]byte("record-3"),
	}

	for _, d := range data {
		_, _, err := writer.WriteRecord(d)
		require.NoError(t, err)
	}
	require.NoError(t, writer.Flush())

	scanner := recordio.NewScanner(bytes.NewReader(buf.Bytes()), recordio.WithScannerInitialBufferSize(8))

	var copies [][]byte
	idx := 0
	for scanner.Scan() {
		require.Less(t, idx, len(data))
		assert.Equal(t, data[idx], scanner.Record())
		copies = append(copies, scanner.RecordCopy())
		idx++
	}

	assert.NoError(t, scanner.Err())
	assert.Equal(t, data, copies)
}

func TestScannerSkip(t *testing.T) {
	testData := [][]byte{
		[]byte("record-alpha"),
		[]byte("record-beta-to-skip"),
		[]byte("record-gamma"),
		[]byte("record-delta-to-skip"),
		[]byte("record-epsilon"),
	}

	runSkipTest := func(t *testing.T, r io.Reader) {
		scanner := recordio.NewScanner(r)

		require.True(t, scanner.Scan())
		assert.Equal(t, testData[0], scanner.Record())
		require.True(t, scanner.Skip())
		assert.Empty(t, scanner.Record())
		require.True(t, scanner.Scan())
		assert.Equal(t, testData[2], scanner.Record())
		require.True(t, scanner.Skip())
		assert.Empty(t, scanner.Record())
		require.True(t, scanner.Scan())
		assert.Equal(t, testData[4], scanner.Record())
		assert.False(t, scanner.Scan())
		assert.Empty(t, scanner.Record())
		assert.NoError(t, scanner.Err())
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

func TestWALRecoveryTornWrite(t *testing.T) {
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

			require.True(t, scanner.Scan(), "cut=%d", cut)
			assert.Equal(t, []byte("valid-record-1"), scanner.Record(), "cut=%d", cut)

			assert.False(t, scanner.Scan(), "cut=%d", cut)
			assert.ErrorIs(t, scanner.Err(), recordio.ErrTornWrite, "cut=%d", cut)
			assert.Equal(t, validEndOffset, scanner.LastValidOffset(), "cut=%d", cut)
		}
	})

	t.Run("SkipTruncationLoop_Seeker", func(t *testing.T) {
		for cut := int(validEndOffset) + 1; cut < len(fullBytes); cut++ {
			truncatedData := fullBytes[:cut]
			scanner := recordio.NewScanner(bytes.NewReader(truncatedData))

			require.True(t, scanner.Scan(), "cut=%d", cut)
			assert.False(t, scanner.Skip(), "cut=%d", cut)
			assert.ErrorIs(t, scanner.Err(), recordio.ErrTornWrite, "cut=%d", cut)
			assert.Equal(t, validEndOffset, scanner.LastValidOffset(), "cut=%d", cut)
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
		corrupted[firstEndOffset+9] ^= 0x01

		scanner := recordio.NewScanner(bytes.NewReader(corrupted))
		assert.True(t, scanner.Scan())
		assert.False(t, scanner.Scan())
		assert.ErrorIs(t, scanner.Err(), recordio.ErrHeaderCorrupted)
		assert.Equal(t, firstEndOffset, scanner.LastValidOffset())
	})

	t.Run("PayloadDataCRCCorruption", func(t *testing.T) {
		corrupted := append([]byte(nil), baseBytes...)
		corrupted[firstEndOffset+13] ^= 0x01

		scanner := recordio.NewScanner(bytes.NewReader(corrupted))
		assert.True(t, scanner.Scan())
		assert.False(t, scanner.Scan())
		assert.ErrorIs(t, scanner.Err(), recordio.ErrDataCorrupted)
		assert.Equal(t, firstEndOffset, scanner.LastValidOffset())
	})

	t.Run("FooterCRCCorruption", func(t *testing.T) {
		corrupted := append([]byte(nil), baseBytes...)
		corrupted[len(corrupted)-2] ^= 0x01

		scanner := recordio.NewScanner(bytes.NewReader(corrupted))
		assert.True(t, scanner.Scan())
		assert.False(t, scanner.Scan())
		assert.ErrorIs(t, scanner.Err(), recordio.ErrDataCorrupted)
	})
}

func TestRealIOErrorsPreserved(t *testing.T) {
	customErr := errors.New("network timeout")
	reader := recordio.NewReader(&errReader{err: customErr})
	dst := make([]byte, 64)
	_, err := reader.ReadRecord(dst)
	assert.ErrorIs(t, err, customErr)

	scanner := recordio.NewScanner(&errReader{err: customErr})
	assert.False(t, scanner.Scan())
	assert.ErrorIs(t, scanner.Err(), customErr)
}

func TestReaderTransientErrorPoisons(t *testing.T) {
	var buf bytes.Buffer
	writer := recordio.NewWriter(&buf)
	payload := bytes.Repeat([]byte("x"), 64)
	for i := 0; i < 2; i++ {
		_, _, err := writer.WriteRecord(payload)
		require.NoError(t, err)
	}
	require.NoError(t, writer.Flush())

	const headerBytes = 12

	tests := []struct {
		name   string
		failAt int
	}{
		{"InPayload", headerBytes + 8},
		{"AtFooter", headerBytes + len(payload)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transient := errors.New("connection reset by peer")
			src := &faultyReader{data: buf.Bytes(), failAt: tt.failAt, err: transient}
			reader := recordio.NewReader(src, recordio.WithReaderBufferSize(16))
			dst := make([]byte, 128)

			_, err := reader.ReadRecord(dst)
			assert.ErrorIs(t, err, transient)
			assert.True(t, src.failed)

			// ReadRecord consumed the header, so a retry would read payload
			// bytes as a header and report corruption for a transient fault.
			_, err = reader.ReadRecord(dst)
			assert.ErrorIs(t, err, transient)
		})
	}
}

func TestZeroAllocations(t *testing.T) {
	record := bytes.Repeat([]byte("x"), 256)

	t.Run("Writer_WriteRecord", func(t *testing.T) {
		writer := recordio.NewWriter(io.Discard, recordio.WithWriterBufferSize(64*1024))

		allocs := testing.AllocsPerRun(100, func() {
			_, _, _ = writer.WriteRecord(record)
		})
		assert.Zero(t, allocs)
	})

	t.Run("Writer_WriteRecordFrom", func(t *testing.T) {
		writer := recordio.NewWriter(io.Discard, recordio.WithWriterBufferSize(64*1024))
		src := bytes.NewReader(record)

		allocs := testing.AllocsPerRun(100, func() {
			src.Reset(record)
			_, _, _ = writer.WriteRecordFrom(src, int64(len(record)))
		})
		assert.Zero(t, allocs)
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
		assert.Zero(t, allocs)
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
		assert.Zero(t, allocs)
	})
}

func FuzzScanner(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("short"))
	f.Add(bytes.Repeat([]byte{0xff}, 32))

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
			_ = rec
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

func BenchmarkWriterWriteRecord(b *testing.B) {
	record := make([]byte, 1024)
	rand.Read(record)
	writer := recordio.NewWriter(io.Discard, recordio.WithWriterBufferSize(64*1024))
	b.ReportAllocs()
	b.SetBytes(int64(len(record) + 16))

	for b.Loop() {
		if _, _, err := writer.WriteRecord(record); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWriterWriteRecordFrom(b *testing.B) {
	record := make([]byte, 1024)
	rand.Read(record)
	writer := recordio.NewWriter(io.Discard, recordio.WithWriterBufferSize(64*1024))
	src := bytes.NewReader(record)
	b.ReportAllocs()
	b.SetBytes(int64(len(record) + 16))

	for b.Loop() {
		src.Reset(record)
		if _, _, err := writer.WriteRecordFrom(src, int64(len(record))); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReaderReadRecord(b *testing.B) {
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

	reader := recordio.NewReader(bytes.NewReader(raw))
	for b.Loop() {
		_, err := reader.ReadRecord(dst)
		if err != nil {
			reader = recordio.NewReader(bytes.NewReader(raw))
			_, _ = reader.ReadRecord(dst)
		}
	}
}

func BenchmarkScannerScan(b *testing.B) {
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

	scanner := recordio.NewScanner(bytes.NewReader(raw), recordio.WithScannerInitialBufferSize(2048))
	for b.Loop() {
		if !scanner.Scan() {
			scanner = recordio.NewScanner(bytes.NewReader(raw), recordio.WithScannerInitialBufferSize(2048))
			_ = scanner.Scan()
		}
		_ = scanner.Record()
	}
}
