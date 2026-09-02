package recordio

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
)

type ScannerOption func(*Scanner)

func WithScannerBufferSize(size int) ScannerOption {
	return func(s *Scanner) {
		if size > 0 {
			s.bufSize = size
		}
	}
}

func WithScannerMaxRecordSize(max int) ScannerOption {
	return func(s *Scanner) {
		if max > 0 {
			s.maxRecordSize = max
		}
	}
}

type Scanner struct {
	r               io.Reader
	br              *bufio.Reader
	seeker          io.Seeker
	bufSize         int
	buf             []byte
	recordLen       int
	offset          int64
	lastValidOffset int64
	maxRecordSize   int
	err             error
	header          [headerSize]byte
	footer          [footerSize]byte
}

func NewScanner(r io.Reader, opts ...ScannerOption) *Scanner {
	s := &Scanner{
		r:             r,
		bufSize:       DefaultBufferSize,
		maxRecordSize: DefaultMaxRecordSize,
	}
	if seeker, ok := r.(io.Seeker); ok {
		s.seeker = seeker
	}
	for _, opt := range opts {
		opt(s)
	}
	s.br = bufio.NewReaderSize(r, s.bufSize)
	return s
}

func (s *Scanner) readHeader() (uint64, bool) {
	s.recordLen = 0
	readHdr, err := io.ReadFull(s.br, s.header[:])
	if err != nil {
		if readHdr == 0 && (errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)) {
			return 0, false
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			s.err = ErrTornWrite
			return 0, false
		}
		s.err = err
		return 0, false
	}

	length := binary.LittleEndian.Uint64(s.header[0:8])
	headerCRC := binary.LittleEndian.Uint32(s.header[8:12])
	if computeMaskedCRC(s.header[0:8]) != headerCRC {
		s.err = ErrHeaderCorrupted
		return 0, false
	}

	if length > uint64(s.maxRecordSize) {
		s.err = ErrRecordTooLarge
		return 0, false
	}

	return length, true
}

func (s *Scanner) Scan() bool {
	if s.err != nil {
		return false
	}

	startOffset := s.lastValidOffset
	length, ok := s.readHeader()
	if !ok {
		return false
	}

	reqLen := int(length)
	if cap(s.buf) < reqLen || s.buf == nil {
		s.buf = make([]byte, reqLen)
	} else {
		s.buf = s.buf[:reqLen]
	}
	s.recordLen = reqLen
	s.offset = startOffset

	if reqLen > 0 {
		if _, err := io.ReadFull(s.br, s.buf[:reqLen]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				s.err = ErrTornWrite
			} else {
				s.err = err
			}
			return false
		}
	}

	if _, err := io.ReadFull(s.br, s.footer[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			s.err = ErrTornWrite
		} else {
			s.err = err
		}
		return false
	}

	dataCRC := binary.LittleEndian.Uint32(s.footer[0:4])
	if computeMaskedCRC(s.buf[:reqLen]) != dataCRC {
		s.err = ErrDataCorrupted
		return false
	}

	s.lastValidOffset = startOffset + int64(frameOverhead) + int64(reqLen)
	return true
}

func (s *Scanner) Record() []byte {
	return s.buf[:s.recordLen]
}

func (s *Scanner) Offset() int64 {
	return s.offset
}

func (s *Scanner) LastValidOffset() int64 {
	return s.lastValidOffset
}

func (s *Scanner) Skip() bool {
	if s.err != nil {
		return false
	}

	startOffset := s.lastValidOffset
	length, ok := s.readHeader()
	if !ok {
		return false
	}

	payload := int64(length)
	buffered := int64(s.br.Buffered())
	if d := min(payload, buffered); d > 0 {
		if _, err := s.br.Discard(int(d)); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				s.err = ErrTornWrite
			} else {
				s.err = err
			}
			return false
		}
	}

	if remain := payload - buffered; remain > 0 {
		if s.seeker != nil {
			if _, err := s.seeker.Seek(remain, io.SeekCurrent); err != nil {
				s.err = err
				return false
			}
			s.br.Reset(s.r)
		} else if _, err := io.CopyN(io.Discard, s.br, remain); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				s.err = ErrTornWrite
			} else {
				s.err = err
			}
			return false
		}
	}

	if _, err := io.ReadFull(s.br, s.footer[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			s.err = ErrTornWrite
		} else {
			s.err = err
		}
		return false
	}

	s.offset = startOffset
	s.lastValidOffset = startOffset + int64(frameOverhead) + int64(length)
	return true
}

func (s *Scanner) Err() error {
	return s.err
}
