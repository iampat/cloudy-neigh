//go:build !go1.27

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
