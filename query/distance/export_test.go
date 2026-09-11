package distance

var (
	L2SquaredPure        = l2SquaredPure
	DotProductPure       = dotProductPure
	CosinePure           = cosinePure
	NormalizeInPlacePure = normalizeInPlacePure

	L2SquaredAccelerated        = l2Squared
	DotProductAccelerated       = dotProduct
	CosineAccelerated           = cosine
	NormalizeInPlaceAccelerated = normalizeInPlace
)
