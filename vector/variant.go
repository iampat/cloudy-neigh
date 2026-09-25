package vector

import (
	"fmt"
)

type Variant int

const (
	Pure Variant = iota
	SIMD
	FP16
)

var variantNames = [...]string{
	Pure: "pure",
	SIMD: "simd",
	FP16: "fp16",
}

func Variants() []Variant {
	out := make([]Variant, len(variantNames))
	for i := range out {
		out[i] = Variant(i)
	}
	return out
}

func (v Variant) String() string {
	if v < 0 || int(v) >= len(variantNames) {
		return fmt.Sprintf("variant(%d)", int(v))
	}
	return variantNames[v]
}

func ParseVariant(s string) (Variant, error) {
	for i, name := range variantNames {
		if name == s {
			return Variant(i), nil
		}
	}
	return 0, fmt.Errorf("vector: unknown variant %q", s)
}

type Kernels struct {
	Name     string
	Dot      func(a, b []float32) float32
	L2       func(a, b []float32) float32
	Dot16    func(q []float32, row []uint16) float32
	L216     func(q []float32, row []uint16) float32
	Decode16 func(dst []float32, src []uint16)
}

func (v Variant) Kernels() (Kernels, error) {
	switch v {
	case Pure:
		return Kernels{Name: "pure", Dot: dotProductPure, L2: l2SquaredPure}, nil
	case SIMD:
		return simdKernels(), nil
	case FP16:
		if !hasAVX512 {
			return Kernels{}, fmt.Errorf("vector: variant %s requires AVX-512", v)
		}
		return fp16Kernels(), nil
	default:
		return Kernels{}, fmt.Errorf("vector: unknown variant %s", v)
	}
}
