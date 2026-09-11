//go:build go1.27

package distance

import "math"

func l2Squared(a, b []float32) float32 {
	n := len(a)
	chunks := n &^ 7
	var s0, s1, s2, s3, s4, s5, s6, s7 float32

	for i := 0; i < chunks; i += 8 {
		d0 := a[i] - b[i]
		d1 := a[i+1] - b[i+1]
		d2 := a[i+2] - b[i+2]
		d3 := a[i+3] - b[i+3]
		d4 := a[i+4] - b[i+4]
		d5 := a[i+5] - b[i+5]
		d6 := a[i+6] - b[i+6]
		d7 := a[i+7] - b[i+7]

		s0 += d0 * d0
		s1 += d1 * d1
		s2 += d2 * d2
		s3 += d3 * d3
		s4 += d4 * d4
		s5 += d5 * d5
		s6 += d6 * d6
		s7 += d7 * d7
	}

	sum := (s0 + s1) + (s2 + s3) + (s4 + s5) + (s6 + s7)
	for i := chunks; i < n; i++ {
		d := a[i] - b[i]
		sum += d * d
	}
	return sum
}

func dotProduct(a, b []float32) float32 {
	n := len(a)
	chunks := n &^ 7
	var s0, s1, s2, s3, s4, s5, s6, s7 float32

	for i := 0; i < chunks; i += 8 {
		s0 += a[i] * b[i]
		s1 += a[i+1] * b[i+1]
		s2 += a[i+2] * b[i+2]
		s3 += a[i+3] * b[i+3]
		s4 += a[i+4] * b[i+4]
		s5 += a[i+5] * b[i+5]
		s6 += a[i+6] * b[i+6]
		s7 += a[i+7] * b[i+7]
	}

	sum := (s0 + s1) + (s2 + s3) + (s4 + s5) + (s6 + s7)
	for i := chunks; i < n; i++ {
		sum += a[i] * b[i]
	}
	return sum
}

func cosine(a, b []float32) (float32, error) {
	n := len(a)
	chunks := n &^ 3
	var d0, d1, d2, d3 float32
	var a0, a1, a2, a3 float32
	var b0, b1, b2, b3 float32

	for i := 0; i < chunks; i += 4 {
		ai0, ai1, ai2, ai3 := a[i], a[i+1], a[i+2], a[i+3]
		bi0, bi1, bi2, bi3 := b[i], b[i+1], b[i+2], b[i+3]

		d0 += ai0 * bi0
		d1 += ai1 * bi1
		d2 += ai2 * bi2
		d3 += ai3 * bi3

		a0 += ai0 * ai0
		a1 += ai1 * ai1
		a2 += ai2 * ai2
		a3 += ai3 * ai3

		b0 += bi0 * bi0
		b1 += bi1 * bi1
		b2 += bi2 * bi2
		b3 += bi3 * bi3
	}

	dot := (d0 + d1) + (d2 + d3)
	sumA := (a0 + a1) + (a2 + a3)
	sumB := (b0 + b1) + (b2 + b3)

	for i := chunks; i < n; i++ {
		ai, bi := a[i], b[i]
		dot += ai * bi
		sumA += ai * ai
		sumB += bi * bi
	}

	if sumA == 0 || sumB == 0 {
		return 0, ErrZeroVector
	}

	denom := float32(math.Sqrt(float64(sumA)) * math.Sqrt(float64(sumB)))
	sim := dot / denom
	if sim > 1 {
		sim = 1
	} else if sim < -1 {
		sim = -1
	}
	return 1 - sim, nil
}

func normalizeInPlace(v []float32) error {
	sum := dotProduct(v, v)
	if sum == 0 {
		return ErrZeroVector
	}
	norm := float32(math.Sqrt(float64(sum)))
	invNorm := 1 / norm

	n := len(v)
	chunks := n &^ 7
	for i := 0; i < chunks; i += 8 {
		v[i] *= invNorm
		v[i+1] *= invNorm
		v[i+2] *= invNorm
		v[i+3] *= invNorm
		v[i+4] *= invNorm
		v[i+5] *= invNorm
		v[i+6] *= invNorm
		v[i+7] *= invNorm
	}
	for i := chunks; i < n; i++ {
		v[i] *= invNorm
	}
	return nil
}
