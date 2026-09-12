//go:build !goexperiment.simd || !go1.27

package distance

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

func l2SquaredArch(a, b []float32) float32 {
	return l2SquaredPure(a, b)
}

func dotProductArch(a, b []float32) float32 {
	return dotProductPure(a, b)
}

func cosineArch(a, b []float32) (float32, error) {
	return cosinePure(a, b)
}

func normalizeInPlaceArch(v []float32) error {
	return normalizeInPlacePure(v)
}
