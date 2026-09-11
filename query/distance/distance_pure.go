package distance

import "math"

func l2SquaredPure(a, b []float32) float32 {
	var sum float32
	for i := range a {
		diff := a[i] - b[i]
		sum += diff * diff
	}
	return sum
}

func dotProductPure(a, b []float32) float32 {
	var sum float32
	for i := range a {
		sum += a[i] * b[i]
	}
	return sum
}

func cosinePure(a, b []float32) (float32, error) {
	var dot, sumA, sumB float32
	for i := range a {
		dot += a[i] * b[i]
		sumA += a[i] * a[i]
		sumB += b[i] * b[i]
	}
	if sumA == 0 || sumB == 0 {
		return 0, ErrZeroVector
	}
	denom := float32(math.Sqrt(float64(sumA)) * math.Sqrt(float64(sumB)))
	if denom == 0 {
		return 0, ErrZeroVector
	}
	sim := dot / denom
	if sim > 1 {
		sim = 1
	} else if sim < -1 {
		sim = -1
	}
	dist := 1 - sim
	if dist < 0 {
		dist = 0
	}
	return dist, nil
}

func normalizeInPlacePure(v []float32) error {
	var sum float32
	for _, x := range v {
		sum += x * x
	}
	if sum == 0 {
		return ErrZeroVector
	}
	norm := float32(math.Sqrt(float64(sum)))
	if norm == 0 {
		return ErrZeroVector
	}
	invNorm := 1 / norm
	for i := range v {
		v[i] *= invNorm
	}
	return nil
}
