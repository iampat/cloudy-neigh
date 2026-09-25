package vector

import (
	"errors"
	"math"
)

var ErrFloat16Overflow = errors.New("vector: value overflows float16")

func (f Format) Encode(dst []uint16, src []float32) error {
	if f != Float16 {
		return errors.New("vector: format has no 16-bit encoding")
	}
	for i, x := range src {
		h, ok := fp16FromFloat32(x)
		if !ok {
			return ErrFloat16Overflow
		}
		dst[i] = h
	}
	return nil
}

func fp16FromFloat32(x float32) (uint16, bool) {
	b := math.Float32bits(x)
	sign := uint16(b>>16) & 0x8000
	exp := int((b>>23)&0xFF) - 127 + 15
	mant := b & 0x7FFFFF
	switch {
	case exp >= 31:
		return 0, false
	case exp < -10:
		return sign, true
	case exp <= 0:
		mant |= 0x800000
		shift := uint(14 - exp)
		half := mant >> shift
		rem := mant & (1<<shift - 1)
		mid := uint32(1) << (shift - 1)
		if rem > mid || (rem == mid && half&1 == 1) {
			half++
		}
		return sign | uint16(half), true
	default:
		half := uint32(exp)<<10 | mant>>13
		rem := mant & 0x1FFF
		if rem > 0x1000 || (rem == 0x1000 && half&1 == 1) {
			half++
		}
		if half >= 0x7C00 {
			return 0, false
		}
		return sign | uint16(half), true
	}
}

func fp16ToFloat32(h uint16) float32 {
	sign := uint32(h&0x8000) << 16
	exp := uint32(h>>10) & 0x1F
	mant := uint32(h & 0x3FF)
	switch exp {
	case 0:
		f := float32(mant) * (1.0 / 16777216.0)
		if sign != 0 {
			return -f
		}
		return f
	case 31:
		return math.Float32frombits(sign | 0x7F800000 | mant<<13)
	default:
		return math.Float32frombits(sign | (exp+112)<<23 | mant<<13)
	}
}
