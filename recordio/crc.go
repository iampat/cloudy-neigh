package recordio

import (
	"errors"
	"hash/crc32"
)

const (
	DefaultBufferSize          = 64 * 1024
	DefaultMaxRecordSize int64 = 64 * 1024 * 1024

	maskDelta     uint32 = 0xa282ead8
	headerSize           = 12
	footerSize           = 4
	frameOverhead        = 16
)

var (
	ErrTornWrite       = errors.New("recordio: incomplete record at stream tail (torn write)")
	ErrHeaderCorrupted = errors.New("recordio: header length CRC mismatch mid-stream")
	ErrDataCorrupted   = errors.New("recordio: payload data CRC mismatch mid-stream")
	ErrUnexpectedEOF   = errors.New("recordio: unexpected EOF within record")
	ErrRecordTooLarge  = errors.New("recordio: record size exceeds max limit")
	ErrBufferTooSmall  = errors.New("recordio: user destination buffer too small for record")
)

var castagnoliTable = crc32.MakeTable(crc32.Castagnoli)

func mask(crc uint32) uint32 {
	return ((crc >> 15) | (crc << 17)) + maskDelta
}

func computeMaskedCRC(data []byte) uint32 {
	return mask(crc32.Checksum(data, castagnoliTable))
}
