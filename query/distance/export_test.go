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

	L2SquaredArch        = l2SquaredArch
	DotProductArch       = dotProductArch
	CosineArch           = cosineArch
	NormalizeInPlaceArch = normalizeInPlaceArch

	L2SquaredPortable        = l2SquaredPortable
	DotProductPortable       = dotProductPortable
	CosinePortable           = cosinePortable
	NormalizeInPlacePortable = normalizeInPlacePortable
)
