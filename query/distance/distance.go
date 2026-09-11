package distance

import (
	"errors"
	"slices"
)

var (
	ErrDimensionMismatch = errors.New("dimension mismatch")
	ErrZeroVector        = errors.New("zero vector")
)

func Cosine(a, b []float32) (float32, error) {
	if len(a) != len(b) {
		return 0, ErrDimensionMismatch
	}
	return cosine(a, b)
}

func L2Squared(a, b []float32) (float32, error) {
	if len(a) != len(b) {
		return 0, ErrDimensionMismatch
	}
	return l2Squared(a, b), nil
}

func DotProduct(a, b []float32) (float32, error) {
	if len(a) != len(b) {
		return 0, ErrDimensionMismatch
	}
	return dotProduct(a, b), nil
}

func Normalize(v []float32) ([]float32, error) {
	out := slices.Clone(v)
	if err := NormalizeInPlace(out); err != nil {
		return nil, err
	}
	return out, nil
}

func NormalizeInPlace(v []float32) error {
	return normalizeInPlace(v)
}
