package vector

var (
	L2SquaredPure        = l2SquaredPure
	DotProductPure       = dotProductPure
	CosinePure           = cosinePure
	NormalizeInPlacePure = normalizeInPlacePure

	L2SquaredPortable        = l2SquaredPortable
	DotProductPortable       = dotProductPortable
	CosinePortable           = cosinePortable
	NormalizeInPlacePortable = normalizeInPlacePortable

	L2SquaredAccelerated        = l2Squared
	DotProductAccelerated       = dotProduct
	CosineAccelerated           = cosine
	NormalizeInPlaceAccelerated = normalizeInPlace
)

var FP16ToFloat32 = fp16ToFloat32

func DotFP16Pure(q []float32, row []uint16) float32 {
	var sum float32
	for i := range q {
		sum += q[i] * fp16ToFloat32(row[i])
	}
	return sum
}

func L2FP16Pure(q []float32, row []uint16) float32 {
	var sum float32
	for i := range q {
		d := q[i] - fp16ToFloat32(row[i])
		sum += d * d
	}
	return sum
}
