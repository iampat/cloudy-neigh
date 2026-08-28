package recordio

import (
	"bufio"
	"encoding/binary"
	"errors"
	"hash/crc32"
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
	copyBuf     []byte
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
	writer.copyBuf = make([]byte, writer.bufSize)
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

func (w *Writer) WriteRecordFrom(r io.Reader, length int64) (n int64, offset int64, err error) {
	if w.err != nil {
		return 0, w.offset, w.err
	}
	if w.closed {
		return 0, w.offset, os.ErrClosed
	}
	if length < 0 {
		return 0, w.offset, ErrRecordTooLarge
	}

	startOffset := w.offset

	binary.LittleEndian.PutUint64(w.header[0:8], uint64(length))
	binary.LittleEndian.PutUint32(w.header[8:12], computeMaskedCRC(w.header[0:8]))

	if _, err := w.bw.Write(w.header[:]); err != nil {
		w.err = err
		return 0, startOffset, err
	}

	if len(w.copyBuf) == 0 {
		bufSize := w.bufSize
		if bufSize <= 0 {
			bufSize = DefaultBufferSize
		}
		w.copyBuf = make([]byte, bufSize)
	}

	var crc uint32
	remaining := length
	for remaining > 0 {
		toRead := int64(len(w.copyBuf))
		if toRead > remaining {
			toRead = remaining
		}
		nr, readErr := r.Read(w.copyBuf[:toRead])
		if nr > 0 {
			chunk := w.copyBuf[:nr]
			crc = crc32.Update(crc, castagnoliTable, chunk)
			if _, writeErr := w.bw.Write(chunk); writeErr != nil {
				w.err = writeErr
				return 0, startOffset, writeErr
			}
			remaining -= int64(nr)
		}
		if readErr != nil {
			if remaining > 0 {
				if readErr == io.EOF || errors.Is(readErr, io.ErrUnexpectedEOF) {
					readErr = ErrUnexpectedEOF
				}
				w.err = readErr
				return 0, startOffset, readErr
			}
			break
		}
		if nr == 0 {
			readErr = io.ErrNoProgress
			w.err = readErr
			return 0, startOffset, readErr
		}
	}

	binary.LittleEndian.PutUint32(w.footer[0:4], mask(crc))

	if _, err := w.bw.Write(w.footer[:]); err != nil {
		w.err = err
		return 0, startOffset, err
	}

	totalWritten := int64(frameOverhead) + length
	w.offset += totalWritten
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
