//go:build goexperiment.simd && amd64

package vector

//go:noescape
func dotFP16Blocks(q *float32, row *uint16, blocks int) float32

//go:noescape
func l2FP16Blocks(q *float32, row *uint16, blocks int) float32

func dotFP16AVX512(q []float32, row []uint16) float32 {
	row = row[:len(q)]
	n := len(q) / 16 * 16
	var sum float32
	if n > 0 {
		sum = dotFP16Blocks(&q[0], &row[0], n/16)
	}
	for i := n; i < len(q); i++ {
		sum += q[i] * fp16ToFloat32(row[i])
	}
	return sum
}

func l2FP16AVX512(q []float32, row []uint16) float32 {
	row = row[:len(q)]
	n := len(q) / 16 * 16
	var sum float32
	if n > 0 {
		sum = l2FP16Blocks(&q[0], &row[0], n/16)
	}
	for i := n; i < len(q); i++ {
		d := q[i] - fp16ToFloat32(row[i])
		sum += d * d
	}
	return sum
}

//go:noescape
func cvtFP16Blocks(dst *float32, src *uint16, blocks int)

func decodeFP16AVX512(dst []float32, src []uint16) {
	if len(src) == 0 {
		return
	}
	_ = dst[len(src)-1]
	dst = dst[:len(src)]
	blocks := len(src) / 16
	if blocks > 0 {
		cvtFP16Blocks(&dst[0], &src[0], blocks)
	}
	for i := blocks * 16; i < len(src); i++ {
		dst[i] = fp16ToFloat32(src[i])
	}
}
