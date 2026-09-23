package recordio

import (
	"errors"
	"hash/crc32"
)

const (
	DefaultBufferSize        = 64 * 1024
	DefaultMaxRecordSize int = 64 * 1024 * 1024

	maskDelta     uint32 = 0xa282ead8
	headerSize           = 12
	footerSize           = 4
	frameOverhead        = 16
)

var (
	ErrTornWrite       = errors.New("recordio: incomplete record at stream tail (torn write)")
	ErrHeaderCorrupted = errors.New("recordio: header length CRC mismatch mid-stream")
	ErrDataCorrupted   = errors.New("recordio: payload data CRC mismatch mid-stream")
	ErrRecordTooLarge  = errors.New("recordio: record size exceeds max limit")
)

var castagnoliTable = crc32.MakeTable(crc32.Castagnoli)

func computeMaskedCRC(data []byte) uint32 {
	crc := crc32.Checksum(data, castagnoliTable)
	return ((crc >> 15) | (crc << 17)) + maskDelta
}
