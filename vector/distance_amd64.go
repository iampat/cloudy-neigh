//go:build goexperiment.simd && amd64

package vector

import (
	"math"
	"simd/archsimd"
)

var hasAVX512 = archsimd.X86.AVX512()

func l2Squared(a, b []float32) float32 {
	if hasAVX512 {
		return l2SquaredAVX512(a, b)
	}
	return l2SquaredPortable(a, b)
}

func dotProduct(a, b []float32) float32 {
	if hasAVX512 {
		return dotProductAVX512(a, b)
	}
	return dotProductPortable(a, b)
}

func cosine(a, b []float32) (float32, error) {
	if hasAVX512 {
		return cosineAVX512(a, b)
	}
	return cosinePortable(a, b)
}

func normalizeInPlace(v []float32) error {
	if hasAVX512 {
		return normalizeInPlaceAVX512(v)
	}
	return normalizeInPlacePortable(v)
}

func simdKernels() Kernels {
	if hasAVX512 {
		return Kernels{Name: "avx512", Dot: dotProductAVX512, L2: l2SquaredAVX512}
	}
	return Kernels{Name: "portable", Dot: dotProductPortable, L2: l2SquaredPortable}
}

func fp16Kernels() Kernels {
	return Kernels{Name: "avx512", Dot16: dotFP16AVX512, L216: l2FP16AVX512, Decode16: decodeFP16AVX512}
}

func sumLanes(v archsimd.Float32x16) archsimd.Float32x4 {
	h := v.GetLo().Add(v.GetHi())
	return h.GetLo().Add(h.GetHi())
}

func l2SquaredAVX512(a, b []float32) float32 {
	b = b[:len(a)]
	var acc0, acc1, acc2, acc3, acc4, acc5, acc6, acc7 archsimd.Float32x16
	for len(a) >= 128 && len(b) >= 128 {
		d0 := archsimd.LoadFloat32x16(a[0:16]).Sub(archsimd.LoadFloat32x16(b[0:16]))
		d1 := archsimd.LoadFloat32x16(a[16:32]).Sub(archsimd.LoadFloat32x16(b[16:32]))
		d2 := archsimd.LoadFloat32x16(a[32:48]).Sub(archsimd.LoadFloat32x16(b[32:48]))
		d3 := archsimd.LoadFloat32x16(a[48:64]).Sub(archsimd.LoadFloat32x16(b[48:64]))
		d4 := archsimd.LoadFloat32x16(a[64:80]).Sub(archsimd.LoadFloat32x16(b[64:80]))
		d5 := archsimd.LoadFloat32x16(a[80:96]).Sub(archsimd.LoadFloat32x16(b[80:96]))
		d6 := archsimd.LoadFloat32x16(a[96:112]).Sub(archsimd.LoadFloat32x16(b[96:112]))
		d7 := archsimd.LoadFloat32x16(a[112:128]).Sub(archsimd.LoadFloat32x16(b[112:128]))
		acc0 = d0.MulAdd(d0, acc0)
		acc1 = d1.MulAdd(d1, acc1)
		acc2 = d2.MulAdd(d2, acc2)
		acc3 = d3.MulAdd(d3, acc3)
		acc4 = d4.MulAdd(d4, acc4)
		acc5 = d5.MulAdd(d5, acc5)
		acc6 = d6.MulAdd(d6, acc6)
		acc7 = d7.MulAdd(d7, acc7)
		a, b = a[128:], b[128:]
	}
	for len(a) >= 64 && len(b) >= 64 {
		d0 := archsimd.LoadFloat32x16(a[0:16]).Sub(archsimd.LoadFloat32x16(b[0:16]))
		d1 := archsimd.LoadFloat32x16(a[16:32]).Sub(archsimd.LoadFloat32x16(b[16:32]))
		d2 := archsimd.LoadFloat32x16(a[32:48]).Sub(archsimd.LoadFloat32x16(b[32:48]))
		d3 := archsimd.LoadFloat32x16(a[48:64]).Sub(archsimd.LoadFloat32x16(b[48:64]))
		acc0 = d0.MulAdd(d0, acc0)
		acc1 = d1.MulAdd(d1, acc1)
		acc2 = d2.MulAdd(d2, acc2)
		acc3 = d3.MulAdd(d3, acc3)
		a, b = a[64:], b[64:]
	}
	for len(a) >= 16 && len(b) >= 16 {
		d := archsimd.LoadFloat32x16(a[:16]).Sub(archsimd.LoadFloat32x16(b[:16]))
		acc0 = d.MulAdd(d, acc0)
		a, b = a[16:], b[16:]
	}
	if len(a) > 0 {
		va, _ := archsimd.LoadFloat32x16Part(a)
		vb, _ := archsimd.LoadFloat32x16Part(b)
		d := va.Sub(vb)
		acc1 = d.MulAdd(d, acc1)
	}
	s := sumLanes(acc0.Add(acc1).Add(acc2.Add(acc3)).Add(acc4.Add(acc5).Add(acc6.Add(acc7))))
	var out [4]float32
	s.ConcatAddPairs(s).ConcatAddPairs(s).Store(out[:])
	archsimd.ClearAVXUpperBits()
	return out[0]
}

func dotProductAVX512(a, b []float32) float32 {
	b = b[:len(a)]
	var acc0, acc1, acc2, acc3, acc4, acc5, acc6, acc7 archsimd.Float32x16
	for len(a) >= 128 && len(b) >= 128 {
		acc0 = archsimd.LoadFloat32x16(a[0:16]).MulAdd(archsimd.LoadFloat32x16(b[0:16]), acc0)
		acc1 = archsimd.LoadFloat32x16(a[16:32]).MulAdd(archsimd.LoadFloat32x16(b[16:32]), acc1)
		acc2 = archsimd.LoadFloat32x16(a[32:48]).MulAdd(archsimd.LoadFloat32x16(b[32:48]), acc2)
		acc3 = archsimd.LoadFloat32x16(a[48:64]).MulAdd(archsimd.LoadFloat32x16(b[48:64]), acc3)
		acc4 = archsimd.LoadFloat32x16(a[64:80]).MulAdd(archsimd.LoadFloat32x16(b[64:80]), acc4)
		acc5 = archsimd.LoadFloat32x16(a[80:96]).MulAdd(archsimd.LoadFloat32x16(b[80:96]), acc5)
		acc6 = archsimd.LoadFloat32x16(a[96:112]).MulAdd(archsimd.LoadFloat32x16(b[96:112]), acc6)
		acc7 = archsimd.LoadFloat32x16(a[112:128]).MulAdd(archsimd.LoadFloat32x16(b[112:128]), acc7)
		a, b = a[128:], b[128:]
	}
	for len(a) >= 64 && len(b) >= 64 {
		acc0 = archsimd.LoadFloat32x16(a[0:16]).MulAdd(archsimd.LoadFloat32x16(b[0:16]), acc0)
		acc1 = archsimd.LoadFloat32x16(a[16:32]).MulAdd(archsimd.LoadFloat32x16(b[16:32]), acc1)
		acc2 = archsimd.LoadFloat32x16(a[32:48]).MulAdd(archsimd.LoadFloat32x16(b[32:48]), acc2)
		acc3 = archsimd.LoadFloat32x16(a[48:64]).MulAdd(archsimd.LoadFloat32x16(b[48:64]), acc3)
		a, b = a[64:], b[64:]
	}
	for len(a) >= 16 && len(b) >= 16 {
		acc0 = archsimd.LoadFloat32x16(a[:16]).MulAdd(archsimd.LoadFloat32x16(b[:16]), acc0)
		a, b = a[16:], b[16:]
	}
	if len(a) > 0 {
		va, _ := archsimd.LoadFloat32x16Part(a)
		vb, _ := archsimd.LoadFloat32x16Part(b)
		acc1 = va.MulAdd(vb, acc1)
	}
	s := sumLanes(acc0.Add(acc1).Add(acc2.Add(acc3)).Add(acc4.Add(acc5).Add(acc6.Add(acc7))))
	var out [4]float32
	s.ConcatAddPairs(s).ConcatAddPairs(s).Store(out[:])
	archsimd.ClearAVXUpperBits()
	return out[0]
}

func cosineAVX512(a, b []float32) (float32, error) {
	b = b[:len(a)]
	var dot0, dot1, sa0, sa1, sb0, sb1 archsimd.Float32x16
	for len(a) >= 64 && len(b) >= 64 {
		va0 := archsimd.LoadFloat32x16(a[0:16])
		vb0 := archsimd.LoadFloat32x16(b[0:16])
		va1 := archsimd.LoadFloat32x16(a[16:32])
		vb1 := archsimd.LoadFloat32x16(b[16:32])
		dot0 = va0.MulAdd(vb0, dot0)
		sa0 = va0.MulAdd(va0, sa0)
		sb0 = vb0.MulAdd(vb0, sb0)
		dot1 = va1.MulAdd(vb1, dot1)
		sa1 = va1.MulAdd(va1, sa1)
		sb1 = vb1.MulAdd(vb1, sb1)
		va2 := archsimd.LoadFloat32x16(a[32:48])
		vb2 := archsimd.LoadFloat32x16(b[32:48])
		va3 := archsimd.LoadFloat32x16(a[48:64])
		vb3 := archsimd.LoadFloat32x16(b[48:64])
		dot0 = va2.MulAdd(vb2, dot0)
		sa0 = va2.MulAdd(va2, sa0)
		sb0 = vb2.MulAdd(vb2, sb0)
		dot1 = va3.MulAdd(vb3, dot1)
		sa1 = va3.MulAdd(va3, sa1)
		sb1 = vb3.MulAdd(vb3, sb1)
		a, b = a[64:], b[64:]
	}
	for len(a) >= 16 && len(b) >= 16 {
		va := archsimd.LoadFloat32x16(a[:16])
		vb := archsimd.LoadFloat32x16(b[:16])
		dot0 = va.MulAdd(vb, dot0)
		sa0 = va.MulAdd(va, sa0)
		sb0 = vb.MulAdd(vb, sb0)
		a, b = a[16:], b[16:]
	}
	if len(a) > 0 {
		va, _ := archsimd.LoadFloat32x16Part(a)
		vb, _ := archsimd.LoadFloat32x16Part(b)
		dot1 = va.MulAdd(vb, dot1)
		sa1 = va.MulAdd(va, sa1)
		sb1 = vb.MulAdd(vb, sb1)
	}
	d := sumLanes(dot0.Add(dot1))
	sa := sumLanes(sa0.Add(sa1))
	sb := sumLanes(sb0.Add(sb1))
	var out [4]float32
	d.ConcatAddPairs(sa).ConcatAddPairs(sb.ConcatAddPairs(sb)).Store(out[:])
	archsimd.ClearAVXUpperBits()

	dot, sumA, sumB := out[0], out[1], out[2]
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

func normalizeInPlaceAVX512(v []float32) error {
	sum := dotProductAVX512(v, v)
	if sum == 0 {
		return ErrZeroVector
	}
	norm := float32(math.Sqrt(float64(sum)))
	inv := archsimd.BroadcastFloat32x16(1 / norm)
	for len(v) >= 16 {
		archsimd.LoadFloat32x16(v[:16]).Mul(inv).Store(v[:16])
		v = v[16:]
	}
	if len(v) > 0 {
		x, _ := archsimd.LoadFloat32x16Part(v)
		x.Mul(inv).StorePart(v)
	}
	archsimd.ClearAVXUpperBits()
	return nil
}
