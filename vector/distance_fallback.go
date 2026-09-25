//go:build !goexperiment.simd

package vector

func l2Squared(a, b []float32) float32 {
	return l2SquaredPure(a, b)
}

func dotProduct(a, b []float32) float32 {
	return dotProductPure(a, b)
}

func cosine(a, b []float32) (float32, error) {
	return cosinePure(a, b)
}

func normalizeInPlace(v []float32) error {
	return normalizeInPlacePure(v)
}

func l2SquaredPortable(a, b []float32) float32 {
	return l2SquaredPure(a, b)
}

func dotProductPortable(a, b []float32) float32 {
	return dotProductPure(a, b)
}

func cosinePortable(a, b []float32) (float32, error) {
	return cosinePure(a, b)
}

func normalizeInPlacePortable(v []float32) error {
	return normalizeInPlacePure(v)
}

const (
	simdBuild = false
	hasAVX512 = false
)

func simdKernels() Kernels {
	return Kernels{}
}

func fp16Kernels() Kernels {
	return Kernels{}
}
