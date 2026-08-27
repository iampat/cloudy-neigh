package recordio

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
)

type ReaderOption func(*Reader)

func WithReaderBufferSize(size int) ReaderOption {
	return func(r *Reader) {
		if size > 0 {
			r.bufSize = size
		}
	}
}

func WithReaderMaxRecordSize(max int) ReaderOption {
	return func(r *Reader) {
		if max > 0 {
			r.maxRecordSize = max
		}
	}
}

type Reader struct {
	br              *bufio.Reader
	bufSize         int
	offset          int64
	lastValidOffset int64
	maxRecordSize   int
	err             error
	footer          [footerSize]byte
}

func NewReader(r io.Reader, opts ...ReaderOption) *Reader {
	reader := &Reader{
		bufSize:       DefaultBufferSize,
		maxRecordSize: DefaultMaxRecordSize,
	}
	for _, opt := range opts {
		opt(reader)
	}
	reader.br = bufio.NewReaderSize(r, reader.bufSize)
	return reader
}

func (r *Reader) ReadRecord(dst []byte) (n int, err error) {
	if r.err != nil {
		return 0, r.err
	}

	startOffset := r.lastValidOffset
	hdr, err := r.br.Peek(headerSize)
	if err != nil {
		if len(hdr) == 0 && (errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)) {
			return 0, io.EOF
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			r.err = ErrTornWrite
			return 0, ErrTornWrite
		}
		return 0, err
	}

	length := binary.LittleEndian.Uint64(hdr[0:8])
	headerCRC := binary.LittleEndian.Uint32(hdr[8:12])
	if computeMaskedCRC(hdr[0:8]) != headerCRC {
		r.err = ErrHeaderCorrupted
		return 0, ErrHeaderCorrupted
	}

	if length > uint64(r.maxRecordSize) {
		return 0, ErrRecordTooLarge
	}
	if int(length) > len(dst) {
		return int(length), ErrBufferTooSmall
	}

	if _, err := r.br.Discard(headerSize); err != nil {
		r.err = err
		return 0, err
	}

	if length > 0 {
		if _, err := io.ReadFull(r.br, dst[:length]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				r.err = ErrTornWrite
				return 0, ErrTornWrite
			}
			r.err = err
			return 0, err
		}
	}

	if _, err := io.ReadFull(r.br, r.footer[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			r.err = ErrTornWrite
			return 0, ErrTornWrite
		}
		r.err = err
		return 0, err
	}

	dataCRC := binary.LittleEndian.Uint32(r.footer[0:4])
	if computeMaskedCRC(dst[:length]) != dataCRC {
		r.err = ErrDataCorrupted
		return 0, ErrDataCorrupted
	}

	r.offset = startOffset
	r.lastValidOffset = startOffset + int64(frameOverhead) + int64(length)
	return int(length), nil
}

func (r *Reader) Offset() int64 {
	return r.offset
}

func (r *Reader) LastValidOffset() int64 {
	return r.lastValidOffset
}
