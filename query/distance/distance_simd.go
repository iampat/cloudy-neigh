//go:build goexperiment.simd && go1.27

package distance

import (
	"math"
	"simd"
	"simd/archsimd"

	// rules_go omits transitive standard-library dependencies of GOEXPERIMENT
	// packages from -importcfg, so the explicit reference forces the entry.
	"simd/internal/bridge"
)

var _ bridge.ZeroSized

func l2Squared(a, b []float32) float32 {
	n := len(a)
	chunks := n &^ 3
	acc := archsimd.Float32x4{}

	for i := 0; i < chunks; i += 4 {
		va := archsimd.LoadFloat32x4(a[i:])
		vb := archsimd.LoadFloat32x4(b[i:])
		diff := va.Sub(vb)
		acc = diff.MulAdd(diff, acc)
	}

	p1 := acc.ConcatAddPairs(acc)
	p2 := p1.ConcatAddPairs(p1)
	sum := p2.GetElem(0)

	for i := chunks; i < n; i++ {
		d := a[i] - b[i]
		sum += d * d
	}
	return sum
}

func dotProduct(a, b []float32) float32 {
	n := len(a)
	chunks := n &^ 3
	acc := archsimd.Float32x4{}

	for i := 0; i < chunks; i += 4 {
		va := archsimd.LoadFloat32x4(a[i:])
		vb := archsimd.LoadFloat32x4(b[i:])
		acc = va.MulAdd(vb, acc)
	}

	p1 := acc.ConcatAddPairs(acc)
	p2 := p1.ConcatAddPairs(p1)
	sum := p2.GetElem(0)

	for i := chunks; i < n; i++ {
		sum += a[i] * b[i]
	}
	return sum
}

func cosine(a, b []float32) (float32, error) {
	n := len(a)
	chunks := n &^ 3
	accDot := archsimd.Float32x4{}
	accA := archsimd.Float32x4{}
	accB := archsimd.Float32x4{}

	for i := 0; i < chunks; i += 4 {
		va := archsimd.LoadFloat32x4(a[i:])
		vb := archsimd.LoadFloat32x4(b[i:])

		accDot = va.MulAdd(vb, accDot)
		accA = va.MulAdd(va, accA)
		accB = vb.MulAdd(vb, accB)
	}

	pDot1 := accDot.ConcatAddPairs(accDot)
	pDot2 := pDot1.ConcatAddPairs(pDot1)
	dot := pDot2.GetElem(0)

	pA1 := accA.ConcatAddPairs(accA)
	pA2 := pA1.ConcatAddPairs(pA1)
	sumA := pA2.GetElem(0)

	pB1 := accB.ConcatAddPairs(accB)
	pB2 := pB1.ConcatAddPairs(pB1)
	sumB := pB2.GetElem(0)

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
	chunks := n &^ 3
	vInv := archsimd.BroadcastFloat32x4(invNorm)

	for i := 0; i < chunks; i += 4 {
		vec := archsimd.LoadFloat32x4(v[i:])
		vec = vec.Mul(vInv)
		vec.Store(v[i:])
	}

	for i := chunks; i < n; i++ {
		v[i] *= invNorm
	}
	return nil
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
