//go:build goexperiment.simd && go1.27

package distance

import (
	"math"
	"simd"

	// rules_go omits transitive standard-library dependencies of GOEXPERIMENT
	// packages from -importcfg, so the explicit reference forces the entry.
	"simd/internal/bridge"
)

var _ bridge.ZeroSized

func Implementation() string {
	return "simd"
}

func l2Squared(a, b []float32) float32 {
	return l2SquaredPortable(a, b)
}

func dotProduct(a, b []float32) float32 {
	return dotProductPortable(a, b)
}

func cosine(a, b []float32) (float32, error) {
	return cosinePortable(a, b)
}

func normalizeInPlace(v []float32) error {
	return normalizeInPlacePortable(v)
}

func l2SquaredPortable(a, b []float32) float32 {
	n := len(a)
	vl := simd.Float32s{}.Len()
	chunks := n - (n % vl)
	acc := simd.Float32s{}

	for i := 0; i < chunks; i += vl {
		va := simd.LoadFloat32s(a[i:])
		vb := simd.LoadFloat32s(b[i:])
		diff := va.Sub(vb)
		acc = diff.MulAdd(diff, acc)
	}

	var buf [16]float32
	acc.Store(buf[:vl])
	var sum float32
	for i := 0; i < vl; i++ {
		sum += buf[i]
	}

	for i := chunks; i < n; i++ {
		d := a[i] - b[i]
		sum += d * d
	}
	return sum
}

func dotProductPortable(a, b []float32) float32 {
	n := len(a)
	vl := simd.Float32s{}.Len()
	chunks := n - (n % vl)
	acc := simd.Float32s{}

	for i := 0; i < chunks; i += vl {
		va := simd.LoadFloat32s(a[i:])
		vb := simd.LoadFloat32s(b[i:])
		acc = va.MulAdd(vb, acc)
	}

	var buf [16]float32
	acc.Store(buf[:vl])
	var sum float32
	for i := 0; i < vl; i++ {
		sum += buf[i]
	}

	for i := chunks; i < n; i++ {
		sum += a[i] * b[i]
	}
	return sum
}

func cosinePortable(a, b []float32) (float32, error) {
	n := len(a)
	vl := simd.Float32s{}.Len()
	chunks := n - (n % vl)
	accDot := simd.Float32s{}
	accA := simd.Float32s{}
	accB := simd.Float32s{}

	for i := 0; i < chunks; i += vl {
		va := simd.LoadFloat32s(a[i:])
		vb := simd.LoadFloat32s(b[i:])

		accDot = va.MulAdd(vb, accDot)
		accA = va.MulAdd(va, accA)
		accB = vb.MulAdd(vb, accB)
	}

	var bufDot, bufA, bufB [16]float32
	accDot.Store(bufDot[:vl])
	accA.Store(bufA[:vl])
	accB.Store(bufB[:vl])

	var dot, sumA, sumB float32
	for i := 0; i < vl; i++ {
		dot += bufDot[i]
		sumA += bufA[i]
		sumB += bufB[i]
	}

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

func normalizeInPlacePortable(v []float32) error {
	sum := dotProductPortable(v, v)
	if sum == 0 {
		return ErrZeroVector
	}
	norm := float32(math.Sqrt(float64(sum)))
	invNorm := 1 / norm

	n := len(v)
	vl := simd.Float32s{}.Len()
	chunks := n - (n % vl)
	vInv := simd.BroadcastFloat32s(invNorm)

	for i := 0; i < chunks; i += vl {
		vec := simd.LoadFloat32s(v[i:])
		vec = vec.Mul(vInv)
		vec.Store(v[i : i+vl])
	}

	for i := chunks; i < n; i++ {
		v[i] *= invNorm
	}
	return nil
}
