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

func TestWriterScannerRoundTrip(t *testing.T) {
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

	scanner := recordio.NewScanner(bytes.NewReader(buf.Bytes()))
	for i, expected := range records {
		require.True(t, scanner.Scan())
		assert.Equal(t, expected, scanner.Record())
		assert.Equal(t, expectedOffsets[i], scanner.Offset())
	}

	assert.False(t, scanner.Scan())
	assert.NoError(t, scanner.Err())
	assert.Equal(t, writer.Offset(), scanner.LastValidOffset())
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

func TestScannerRecordLimits(t *testing.T) {
	var buf bytes.Buffer
	writer := recordio.NewWriter(&buf)
	payload := []byte("payload-12345")
	_, _, _ = writer.WriteRecord(payload)
	_ = writer.Flush()

	t.Run("RecordTooLarge", func(t *testing.T) {
		scanner := recordio.NewScanner(bytes.NewReader(buf.Bytes()), recordio.WithScannerMaxRecordSize(5))
		assert.False(t, scanner.Scan())
		assert.ErrorIs(t, scanner.Err(), recordio.ErrRecordTooLarge)
	})
}

func TestScannerScan(t *testing.T) {
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

	scanner := recordio.NewScanner(bytes.NewReader(buf.Bytes()))

	var copies [][]byte
	idx := 0
	for scanner.Scan() {
		require.Less(t, idx, len(data))
		assert.Equal(t, data[idx], scanner.Record())
		copies = append(copies, bytes.Clone(scanner.Record()))
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
	scanner := recordio.NewScanner(&errReader{err: customErr})
	assert.False(t, scanner.Scan())
	assert.ErrorIs(t, scanner.Err(), customErr)
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

	t.Run("Scanner_Scan", func(t *testing.T) {
		var buf bytes.Buffer
		writer := recordio.NewWriter(&buf)
		for i := 0; i < 200; i++ {
			_, _, _ = writer.WriteRecord(record)
		}
		_ = writer.Flush()

		scanner := recordio.NewScanner(bytes.NewReader(buf.Bytes()))

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

func TestWriterReset(t *testing.T) {
	var buf1, buf2 bytes.Buffer
	w := recordio.NewWriter(&buf1)
	_, _, err := w.WriteRecord([]byte("first"))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	w.Reset(&buf2)
	_, _, err = w.WriteRecord([]byte("second"))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	s := recordio.NewScanner(&buf2)
	require.True(t, s.Scan())
	assert.Equal(t, []byte("second"), s.Record())
}

func TestWriterWriteAfterClose(t *testing.T) {
	var buf bytes.Buffer
	w := recordio.NewWriter(&buf)
	require.NoError(t, w.Close())

	_, _, err := w.WriteRecord([]byte("fail"))
	assert.ErrorIs(t, err, os.ErrClosed)
}

func TestWriterOptions(t *testing.T) {
	var buf bytes.Buffer
	w := recordio.NewWriter(&buf, recordio.WithWriterBufferSize(1024))
	_, _, err := w.WriteRecord([]byte("custom-size"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
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

	scanner := recordio.NewScanner(bytes.NewReader(raw))
	for b.Loop() {
		if !scanner.Scan() {
			scanner = recordio.NewScanner(bytes.NewReader(raw))
			_ = scanner.Scan()
		}
		_ = scanner.Record()
	}
}
