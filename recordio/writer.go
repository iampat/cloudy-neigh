package recordio

import (
	"bufio"
	"encoding/binary"
	"io"
	"os"
)

type syncer interface {
	Sync() error
}

type WriterOption func(*Writer)

func WithWriterBufferSize(size int) WriterOption {
	return func(w *Writer) {
		if size > 0 {
			w.bufSize = size
		}
	}
}

func WithWriterSyncOnFlush(sync bool) WriterOption {
	return func(w *Writer) {
		w.syncOnFlush = sync
	}
}

type Writer struct {
	w           io.Writer
	bw          *bufio.Writer
	bufSize     int
	offset      int64
	syncOnFlush bool
	closed      bool
	err         error
	header      [headerSize]byte
	footer      [footerSize]byte
}

func NewWriter(w io.Writer, opts ...WriterOption) *Writer {
	writer := &Writer{
		w:       w,
		bufSize: DefaultBufferSize,
	}
	for _, opt := range opts {
		opt(writer)
	}
	writer.bw = bufio.NewWriterSize(w, writer.bufSize)
	return writer
}

func (w *Writer) Reset(out io.Writer) {
	w.w = out
	w.bw.Reset(out)
	w.offset = 0
	w.closed = false
	w.err = nil
}

func (w *Writer) WriteRecord(record []byte) (n int, offset int64, err error) {
	if w.err != nil {
		return 0, w.offset, w.err
	}
	if w.closed {
		return 0, w.offset, os.ErrClosed
	}

	startOffset := w.offset
	length := uint64(len(record))

	binary.LittleEndian.PutUint64(w.header[0:8], length)
	binary.LittleEndian.PutUint32(w.header[8:12], computeMaskedCRC(w.header[0:8]))
	binary.LittleEndian.PutUint32(w.footer[0:4], computeMaskedCRC(record))

	if _, err := w.bw.Write(w.header[:]); err != nil {
		w.err = err
		return 0, startOffset, err
	}
	if len(record) > 0 {
		if _, err := w.bw.Write(record); err != nil {
			w.err = err
			return 0, startOffset, err
		}
	}
	if _, err := w.bw.Write(w.footer[:]); err != nil {
		w.err = err
		return 0, startOffset, err
	}

	totalWritten := frameOverhead + len(record)
	w.offset += int64(totalWritten)
	return totalWritten, startOffset, nil
}

func (w *Writer) Offset() int64 {
	return w.offset
}

func (w *Writer) Flush() error {
	if w.syncOnFlush {
		return w.Sync()
	}
	if w.err != nil {
		return w.err
	}
	if err := w.bw.Flush(); err != nil {
		w.err = err
		return err
	}
	return nil
}

func (w *Writer) Sync() error {
	if w.err != nil {
		return w.err
	}
	if err := w.bw.Flush(); err != nil {
		w.err = err
		return err
	}
	if s, ok := w.w.(syncer); ok {
		if err := s.Sync(); err != nil {
			w.err = err
			return err
		}
	}
	return nil
}

func (w *Writer) Close() error {
	if w.closed {
		return nil
	}
	flushErr := w.Flush()
	w.closed = true
	if closer, ok := w.w.(io.Closer); ok {
		closeErr := closer.Close()
		if flushErr != nil {
			return flushErr
		}
		return closeErr
	}
	return flushErr
}
