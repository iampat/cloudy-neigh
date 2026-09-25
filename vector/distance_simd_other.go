//go:build goexperiment.simd && !amd64

package vector

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

const hasAVX512 = false

func simdKernels() Kernels {
	return Kernels{Name: "portable", Dot: dotProductPortable, L2: l2SquaredPortable}
}

func fp16Kernels() Kernels {
	return Kernels{}
}
