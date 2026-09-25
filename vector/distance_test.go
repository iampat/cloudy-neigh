package vector_test

import (
	"encoding/binary"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/iampat/cloudy-neigh/vector"
)

func randomVector(dim int, seed uint64) []float32 {
	rng := rand.New(rand.NewPCG(seed, seed+1))
	v := make([]float32, dim)
	for i := range v {
		v[i] = rng.Float32()*2 - 1
	}
	return v
}

func TestL2Squared(t *testing.T) {
	t.Run("known vectors", func(t *testing.T) {
		a := []float32{1, 2, 3}
		b := []float32{4, 6, 8}
		got, err := vector.L2Squared(a, b)
		require.NoError(t, err)
		require.InDelta(t, float32(50), got, 1e-5)
	})

	t.Run("identical vectors", func(t *testing.T) {
		a := []float32{1, 2, 3}
		got, err := vector.L2Squared(a, a)
		require.NoError(t, err)
		require.InDelta(t, float32(0), got, 1e-5)
	})

	t.Run("orthogonal vectors", func(t *testing.T) {
		a := []float32{1, 0, 0}
		b := []float32{0, 1, 0}
		got, err := vector.L2Squared(a, b)
		require.NoError(t, err)
		require.InDelta(t, float32(2), got, 1e-5)
	})

	t.Run("zero vectors", func(t *testing.T) {
		a := []float32{0, 0, 0}
		b := []float32{0, 0, 0}
		got, err := vector.L2Squared(a, b)
		require.NoError(t, err)
		require.InDelta(t, float32(0), got, 1e-5)
	})

	t.Run("dimension mismatch", func(t *testing.T) {
		a := []float32{1, 2}
		b := []float32{1}
		_, err := vector.L2Squared(a, b)
		require.ErrorIs(t, err, vector.ErrDimensionMismatch)
	})

	t.Run("empty vectors", func(t *testing.T) {
		got, err := vector.L2Squared([]float32{}, []float32{})
		require.NoError(t, err)
		require.Equal(t, float32(0), got)
	})
}

func TestDotProduct(t *testing.T) {
	t.Run("known vectors", func(t *testing.T) {
		a := []float32{1, 2, 3}
		b := []float32{4, 6, 8}
		got, err := vector.DotProduct(a, b)
		require.NoError(t, err)
		require.InDelta(t, float32(40), got, 1e-5)
	})

	t.Run("identical vectors", func(t *testing.T) {
		a := []float32{1, 2, 3}
		got, err := vector.DotProduct(a, a)
		require.NoError(t, err)
		require.InDelta(t, float32(14), got, 1e-5)
	})

	t.Run("orthogonal vectors", func(t *testing.T) {
		a := []float32{1, 0, 0}
		b := []float32{0, 1, 0}
		got, err := vector.DotProduct(a, b)
		require.NoError(t, err)
		require.InDelta(t, float32(0), got, 1e-5)
	})

	t.Run("zero vectors", func(t *testing.T) {
		a := []float32{0, 0, 0}
		b := []float32{0, 0, 0}
		got, err := vector.DotProduct(a, b)
		require.NoError(t, err)
		require.InDelta(t, float32(0), got, 1e-5)
	})

	t.Run("dimension mismatch", func(t *testing.T) {
		a := []float32{1, 2}
		b := []float32{1}
		_, err := vector.DotProduct(a, b)
		require.ErrorIs(t, err, vector.ErrDimensionMismatch)
	})

	t.Run("empty vectors", func(t *testing.T) {
		got, err := vector.DotProduct([]float32{}, []float32{})
		require.NoError(t, err)
		require.Equal(t, float32(0), got)
	})
}

func TestCosine(t *testing.T) {
	t.Run("identical vectors", func(t *testing.T) {
		a := []float32{1, 2, 3}
		got, err := vector.Cosine(a, a)
		require.NoError(t, err)
		require.InDelta(t, float32(0), got, 1e-5)
	})

	t.Run("orthogonal vectors", func(t *testing.T) {
		a := []float32{3, 0}
		b := []float32{0, 4}
		got, err := vector.Cosine(a, b)
		require.NoError(t, err)
		require.InDelta(t, float32(1), got, 1e-5)
	})

	t.Run("opposite vectors", func(t *testing.T) {
		a := []float32{1, 2, 3}
		b := []float32{-1, -2, -3}
		got, err := vector.Cosine(a, b)
		require.NoError(t, err)
		require.InDelta(t, float32(2), got, 1e-5)
	})

	t.Run("zero vector first", func(t *testing.T) {
		a := []float32{0, 0, 0}
		b := []float32{1, 2, 3}
		_, err := vector.Cosine(a, b)
		require.ErrorIs(t, err, vector.ErrZeroVector)
	})

	t.Run("zero vector second", func(t *testing.T) {
		a := []float32{1, 2, 3}
		b := []float32{0, 0, 0}
		_, err := vector.Cosine(a, b)
		require.ErrorIs(t, err, vector.ErrZeroVector)
	})

	t.Run("both zero vectors", func(t *testing.T) {
		a := []float32{0, 0, 0}
		b := []float32{0, 0, 0}
		_, err := vector.Cosine(a, b)
		require.ErrorIs(t, err, vector.ErrZeroVector)
	})

	t.Run("dimension mismatch", func(t *testing.T) {
		a := []float32{1, 2}
		b := []float32{1}
		_, err := vector.Cosine(a, b)
		require.ErrorIs(t, err, vector.ErrDimensionMismatch)
	})

	t.Run("empty vectors", func(t *testing.T) {
		_, err := vector.Cosine([]float32{}, []float32{})
		require.ErrorIs(t, err, vector.ErrZeroVector)
	})
}

func TestNormalize(t *testing.T) {
	t.Run("known vector", func(t *testing.T) {
		v := []float32{3, 4}
		norm, err := vector.Normalize(v)
		require.NoError(t, err)
		require.InDelta(t, float32(0.6), norm[0], 1e-5)
		require.InDelta(t, float32(0.8), norm[1], 1e-5)
		require.Equal(t, []float32{3, 4}, v)
	})

	t.Run("empty vector", func(t *testing.T) {
		_, err := vector.Normalize([]float32{})
		require.ErrorIs(t, err, vector.ErrZeroVector)
	})

	t.Run("zero vector", func(t *testing.T) {
		_, err := vector.Normalize([]float32{0, 0, 0})
		require.ErrorIs(t, err, vector.ErrZeroVector)
	})
}

func TestNormalizeInPlace(t *testing.T) {
	t.Run("known vector", func(t *testing.T) {
		v := []float32{3, 4}
		err := vector.NormalizeInPlace(v)
		require.NoError(t, err)
		require.InDelta(t, float32(0.6), v[0], 1e-5)
		require.InDelta(t, float32(0.8), v[1], 1e-5)
	})

	t.Run("empty vector", func(t *testing.T) {
		err := vector.NormalizeInPlace([]float32{})
		require.ErrorIs(t, err, vector.ErrZeroVector)
	})

	t.Run("zero vector", func(t *testing.T) {
		err := vector.NormalizeInPlace([]float32{0, 0})
		require.ErrorIs(t, err, vector.ErrZeroVector)
	})
}

func hasNonFinite(v []float32) bool {
	for _, x := range v {
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return true
		}
	}
	return false
}

func encodeVectors(header byte, a, b []float32) []byte {
	buf := make([]byte, 1+(len(a)+len(b))*4)
	buf[0] = header
	for i, v := range a {
		binary.LittleEndian.PutUint32(buf[1+i*4:], math.Float32bits(v))
	}
	offset := 1 + len(a)*4
	for i, v := range b {
		binary.LittleEndian.PutUint32(buf[offset+i*4:], math.Float32bits(v))
	}
	return buf
}

func deriveVectors(data []byte) ([]float32, []float32) {
	if len(data) == 0 {
		return nil, nil
	}
	header := data[0]
	body := data[1:]
	numFloats := len(body) / 4
	if numFloats == 0 {
		return []float32{}, []float32{}
	}
	floats := make([]float32, numFloats)
	for i := range floats {
		floats[i] = math.Float32frombits(binary.LittleEndian.Uint32(body[i*4:]))
	}
	switch header % 4 {
	case 0:
		mid := len(floats) / 2
		return slices.Clone(floats[:mid]), slices.Clone(floats[mid : 2*mid])
	case 1:
		split := int(header) % (len(floats) + 1)
		return slices.Clone(floats[:split]), slices.Clone(floats[split:])
	case 2:
		return slices.Clone(floats), slices.Clone(floats[:len(floats)/2])
	default:
		return slices.Clone(floats), slices.Clone(floats)
	}
}

func encodeSingleVector(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, x := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(x))
	}
	return buf
}

func deriveSingleVector(data []byte) []float32 {
	if len(data) == 0 {
		return nil
	}
	numFloats := len(data) / 4
	if numFloats == 0 {
		return []float32{}
	}
	floats := make([]float32, numFloats)
	for i := range floats {
		floats[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	return floats
}

func seedVectorPairCorpus(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0})
	f.Add([]byte{1, 1, 2, 3, 4})
	dimensions := []int{1, 2, 3, 4, 7, 8, 9, 15, 16, 128, 768, 1024}
	for _, dim := range dimensions {
		a, _ := vector.Normalize(randomVector(dim, uint64(dim*10+1)))
		b, _ := vector.Normalize(randomVector(dim, uint64(dim*10+2)))
		f.Add(encodeVectors(0, a, b))
		f.Add(encodeVectors(1, a, b[:dim/2]))
		f.Add(encodeVectors(2, a, b[:dim/2]))
		f.Add(encodeVectors(3, a, b))
	}
	nanVec := []float32{1, float32(math.NaN())}
	infVec := []float32{1, float32(math.Inf(1))}
	normVec := []float32{1, 2}
	f.Add(encodeVectors(0, nanVec, normVec))
	f.Add(encodeVectors(0, infVec, normVec))
	zeroVec := []float32{0, 0, 0}
	f.Add(encodeVectors(0, zeroVec, zeroVec))
}

func seedSingleVectorCorpus(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{1, 2, 3})
	dimensions := []int{1, 2, 3, 4, 7, 8, 9, 15, 16, 128, 768, 1024}
	for _, dim := range dimensions {
		v, _ := vector.Normalize(randomVector(dim, uint64(dim*10+1)))
		f.Add(encodeSingleVector(v))
		f.Add(encodeSingleVector(randomVector(dim, uint64(dim*10+2))))
	}
	f.Add(encodeSingleVector([]float32{1, float32(math.NaN())}))
	f.Add(encodeSingleVector([]float32{1, float32(math.Inf(1))}))
	f.Add(encodeSingleVector([]float32{0, 0, 0}))
}

func FuzzL2Squared(f *testing.F) {
	seedVectorPairCorpus(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		a, b := deriveVectors(data)
		if len(a) != len(b) {
			_, err := vector.L2Squared(a, b)
			require.ErrorIs(t, err, vector.ErrDimensionMismatch)
			return
		}
		if len(a) == 0 {
			require.Equal(t, float32(0), vector.L2SquaredPure(a, b))
			require.Equal(t, float32(0), vector.L2SquaredPortable(a, b))
			require.Equal(t, float32(0), vector.L2SquaredAccelerated(a, b))
			got, err := vector.L2Squared(a, b)
			require.NoError(t, err)
			require.Equal(t, float32(0), got)
			return
		}
		if hasNonFinite(a) || hasNonFinite(b) {
			return
		}

		l2Pure := vector.L2SquaredPure(a, b)
		l2Port := vector.L2SquaredPortable(a, b)

		if math.IsInf(float64(l2Pure), 0) || math.IsNaN(float64(l2Pure)) {
			return
		}

		tol := float64(1e-5)
		if mag := math.Abs(float64(l2Pure)); mag > 1.0 {
			tol = 1e-5 * mag
		}
		require.InDelta(t, l2Pure, l2Port, tol)
		require.InDelta(t, l2Pure, vector.L2SquaredAccelerated(a, b), tol)

		got, err := vector.L2Squared(a, b)
		require.NoError(t, err)
		require.InDelta(t, l2Pure, got, tol)
	})
}

func FuzzDotProduct(f *testing.F) {
	seedVectorPairCorpus(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		a, b := deriveVectors(data)
		if len(a) != len(b) {
			_, err := vector.DotProduct(a, b)
			require.ErrorIs(t, err, vector.ErrDimensionMismatch)
			return
		}
		if len(a) == 0 {
			require.Equal(t, float32(0), vector.DotProductPure(a, b))
			require.Equal(t, float32(0), vector.DotProductPortable(a, b))
			require.Equal(t, float32(0), vector.DotProductAccelerated(a, b))
			got, err := vector.DotProduct(a, b)
			require.NoError(t, err)
			require.Equal(t, float32(0), got)
			return
		}
		if hasNonFinite(a) || hasNonFinite(b) {
			return
		}

		dotPure := vector.DotProductPure(a, b)
		dotPort := vector.DotProductPortable(a, b)

		if math.IsInf(float64(dotPure), 0) || math.IsNaN(float64(dotPure)) {
			return
		}

		tol := float64(1e-5)
		if mag := math.Abs(float64(dotPure)); mag > 1.0 {
			tol = 1e-5 * mag
		}
		require.InDelta(t, dotPure, dotPort, tol)
		require.InDelta(t, dotPure, vector.DotProductAccelerated(a, b), tol)

		got, err := vector.DotProduct(a, b)
		require.NoError(t, err)
		require.InDelta(t, dotPure, got, tol)
	})
}

func FuzzCosine(f *testing.F) {
	seedVectorPairCorpus(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		a, b := deriveVectors(data)
		if len(a) != len(b) {
			_, err := vector.Cosine(a, b)
			require.ErrorIs(t, err, vector.ErrDimensionMismatch)
			return
		}
		if len(a) == 0 {
			_, errPure := vector.CosinePure(a, b)
			_, errPort := vector.CosinePortable(a, b)
			_, errAcc := vector.CosineAccelerated(a, b)
			require.ErrorIs(t, errPure, vector.ErrZeroVector)
			require.ErrorIs(t, errPort, vector.ErrZeroVector)
			require.ErrorIs(t, errAcc, vector.ErrZeroVector)
			_, err := vector.Cosine(a, b)
			require.ErrorIs(t, err, vector.ErrZeroVector)
			return
		}
		if hasNonFinite(a) || hasNonFinite(b) {
			return
		}

		cosPure, errPure := vector.CosinePure(a, b)
		cosPort, errPort := vector.CosinePortable(a, b)
		cosAcc, errAcc := vector.CosineAccelerated(a, b)

		if errPure != nil {
			require.ErrorIs(t, errPure, vector.ErrZeroVector)
			require.ErrorIs(t, errPort, vector.ErrZeroVector)
			require.ErrorIs(t, errAcc, vector.ErrZeroVector)
			_, err := vector.Cosine(a, b)
			require.ErrorIs(t, err, vector.ErrZeroVector)
			return
		}
		require.NoError(t, errPort)
		require.NoError(t, errAcc)

		if math.IsNaN(float64(cosPure)) || math.IsInf(float64(cosPure), 0) {
			return
		}

		require.InDelta(t, cosPure, cosPort, 1e-5)
		require.InDelta(t, cosPure, cosAcc, 1e-5)

		got, err := vector.Cosine(a, b)
		require.NoError(t, err)
		require.InDelta(t, cosPure, got, 1e-5)
	})
}

func FuzzNormalizeInPlace(f *testing.F) {
	seedSingleVectorCorpus(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		v := deriveSingleVector(data)
		if len(v) == 0 {
			errPure := vector.NormalizeInPlacePure(v)
			errPort := vector.NormalizeInPlacePortable(v)
			errAcc := vector.NormalizeInPlaceAccelerated(v)
			require.ErrorIs(t, errPure, vector.ErrZeroVector)
			require.ErrorIs(t, errPort, vector.ErrZeroVector)
			require.ErrorIs(t, errAcc, vector.ErrZeroVector)
			err := vector.NormalizeInPlace(v)
			require.ErrorIs(t, err, vector.ErrZeroVector)
			return
		}
		if hasNonFinite(v) {
			return
		}

		normPure := slices.Clone(v)
		normPort := slices.Clone(v)
		normAcc := slices.Clone(v)

		errPure := vector.NormalizeInPlacePure(normPure)
		errPort := vector.NormalizeInPlacePortable(normPort)
		errAcc := vector.NormalizeInPlaceAccelerated(normAcc)

		if errPure != nil {
			require.ErrorIs(t, errPure, vector.ErrZeroVector)
			require.ErrorIs(t, errPort, vector.ErrZeroVector)
			require.ErrorIs(t, errAcc, vector.ErrZeroVector)
			err := vector.NormalizeInPlace(slices.Clone(v))
			require.ErrorIs(t, err, vector.ErrZeroVector)
			return
		}
		require.NoError(t, errPort)
		require.NoError(t, errAcc)

		for i := range normPure {
			require.InDelta(t, normPure[i], normPort[i], 1e-5)
			require.InDelta(t, normPure[i], normAcc[i], 1e-5)
		}
	})
}

func TestFloat16RoundTrip(t *testing.T) {
	decode := func(enc []uint16) []float32 {
		out := make([]float32, len(enc))
		for i, h := range enc {
			out[i] = vector.FP16ToFloat32(h)
		}
		return out
	}
	t.Run("exact", func(t *testing.T) {
		exact := []float32{0, 1, -1, 0.5, 65504, -65504}
		enc := make([]uint16, len(exact))
		require.NoError(t, vector.Float16.Encode(enc, exact))
		require.Equal(t, exact, decode(enc))
	})
	t.Run("random", func(t *testing.T) {
		src := append(randomVector(1000, 7), 1e-5, -1e-5, 0.1, 0.3)
		enc := make([]uint16, len(src))
		require.NoError(t, vector.Float16.Encode(enc, src))
		got := decode(enc)
		for i, x := range src {
			tol := math.Abs(float64(x))/(1<<11) + 1.0/(1<<25)
			require.InDelta(t, x, got[i], tol, "value %v", x)
		}
	})
	t.Run("overflow", func(t *testing.T) {
		err := vector.Float16.Encode(make([]uint16, 1), []float32{70000})
		require.ErrorIs(t, err, vector.ErrFloat16Overflow)
	})
}

func TestKernels16(t *testing.T) {
	if err := vector.FP16.Check(); err != nil {
		t.Skip(err)
	}
	k := vector.FP16.Kernels()
	for _, dim := range []int{1, 2, 12, 15, 16, 17, 31, 32, 64, 100, 128, 129, 200, 256, 1024, 1031} {
		t.Run(strconv.Itoa(dim), func(t *testing.T) {
			q := randomVector(dim, uint64(dim)*3+1)
			row := make([]uint16, dim)
			require.NoError(t, vector.Float16.Encode(row, randomVector(dim, uint64(dim)*3+2)))

			dot := vector.DotFP16Pure(q, row)
			require.InDelta(t, dot, k.Dot16(q, row), 1e-4*(1+math.Abs(float64(dot))))
			l2 := vector.L2FP16Pure(q, row)
			require.InDelta(t, l2, k.L216(q, row), 1e-4*(1+math.Abs(float64(l2))))
		})
	}
}

func TestDecode16(t *testing.T) {
	if err := vector.FP16.Check(); err != nil {
		t.Skip(err)
	}
	decode := vector.FP16.Kernels().Decode16
	for _, dim := range []int{1, 15, 16, 17, 100, 1024, 1031} {
		t.Run(strconv.Itoa(dim), func(t *testing.T) {
			enc := make([]uint16, dim)
			require.NoError(t, vector.Float16.Encode(enc, randomVector(dim, uint64(dim)*5+1)))
			got := make([]float32, dim)
			decode(got, enc)
			for i, h := range enc {
				require.Equal(t, vector.FP16ToFloat32(h), got[i], "index %d", i)
			}
		})
	}
}

func TestVariantParse(t *testing.T) {
	for _, v := range vector.Variants() {
		got, err := vector.ParseVariant(v.String())
		require.NoError(t, err)
		require.Equal(t, v, got)
	}
	for _, name := range []string{"avx1024", "avx512", "avx512-fp16", "pure-fp16", "pure-bf16"} {
		_, err := vector.ParseVariant(name)
		require.Error(t, err, name)
	}
	require.NoError(t, vector.Pure.Check())
}

var benchDims = []int{32, 64, 128, 256, 384, 512, 768, 1024, 1536, 2048, 3072, 4096}

func BenchmarkDistance(b *testing.B) {
	funcs := []struct {
		name        string
		pure        func(a, b []float32) (float32, error)
		portable    func(a, b []float32) (float32, error)
		accelerated func(a, b []float32) (float32, error)
	}{
		{
			name:     "L2Squared",
			pure:     func(a, b []float32) (float32, error) { return vector.L2SquaredPure(a, b), nil },
			portable: func(a, b []float32) (float32, error) { return vector.L2SquaredPortable(a, b), nil },
			accelerated: func(a, b []float32) (float32, error) {
				return vector.L2SquaredAccelerated(a, b), nil
			},
		},
		{
			name:     "DotProduct",
			pure:     func(a, b []float32) (float32, error) { return vector.DotProductPure(a, b), nil },
			portable: func(a, b []float32) (float32, error) { return vector.DotProductPortable(a, b), nil },
			accelerated: func(a, b []float32) (float32, error) {
				return vector.DotProductAccelerated(a, b), nil
			},
		},
		{
			name:        "Cosine",
			pure:        vector.CosinePure,
			portable:    vector.CosinePortable,
			accelerated: vector.CosineAccelerated,
		},
	}
	for _, f := range funcs {
		for _, dim := range benchDims {
			b.Run(f.name+"/pure/"+strconv.Itoa(dim), func(b *testing.B) {
				v1 := randomVector(dim, 1)
				v2 := randomVector(dim, 2)
				for b.Loop() {
					if _, err := f.pure(v1, v2); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run(f.name+"/simd-portable/"+strconv.Itoa(dim), func(b *testing.B) {
				v1 := randomVector(dim, 1)
				v2 := randomVector(dim, 2)
				for b.Loop() {
					if _, err := f.portable(v1, v2); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run(f.name+"/simd-arch/"+strconv.Itoa(dim), func(b *testing.B) {
				v1 := randomVector(dim, 1)
				v2 := randomVector(dim, 2)
				for b.Loop() {
					if _, err := f.accelerated(v1, v2); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
	for _, dim := range benchDims {
		b.Run("NormalizeInPlace/pure/"+strconv.Itoa(dim), func(b *testing.B) {
			v := randomVector(dim, 1)
			buf := slices.Clone(v)
			for b.Loop() {
				copy(buf, v)
				if err := vector.NormalizeInPlacePure(buf); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run("NormalizeInPlace/simd-portable/"+strconv.Itoa(dim), func(b *testing.B) {
			v := randomVector(dim, 1)
			buf := slices.Clone(v)
			for b.Loop() {
				copy(buf, v)
				if err := vector.NormalizeInPlacePortable(buf); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run("NormalizeInPlace/simd-arch/"+strconv.Itoa(dim), func(b *testing.B) {
			v := randomVector(dim, 1)
			buf := slices.Clone(v)
			for b.Loop() {
				copy(buf, v)
				if err := vector.NormalizeInPlaceAccelerated(buf); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
	for _, v := range vector.Variants() {
		if v.Format() == vector.Float32 {
			continue
		}
		k := v.Kernels()
		kernels := []struct {
			name string
			fn   func(q []float32, row []uint16) float32
		}{
			{name: "DotProduct", fn: k.Dot16},
			{name: "L2Squared", fn: k.L216},
		}
		for _, kern := range kernels {
			for _, dim := range benchDims {
				b.Run(kern.name+"/"+v.String()+"/"+strconv.Itoa(dim), func(b *testing.B) {
					if err := v.Check(); err != nil {
						b.Skip(err)
					}
					q := randomVector(dim, 1)
					row := make([]uint16, dim)
					if err := v.Format().Encode(row, randomVector(dim, 2)); err != nil {
						b.Fatal(err)
					}
					for b.Loop() {
						kern.fn(q, row)
					}
				})
			}
		}
	}
}

func BenchmarkDecode16(b *testing.B) {
	if err := vector.FP16.Check(); err != nil {
		b.Skip(err)
	}
	decode := vector.FP16.Kernels().Decode16
	enc := make([]uint16, 1024)
	require.NoError(b, vector.Float16.Encode(enc, randomVector(1024, 1)))
	dst := make([]float32, 1024)
	for b.Loop() {
		decode(dst, enc)
	}
}

func BenchmarkScanKernel(b *testing.B) {
	const dim = 1024
	sizes := []struct {
		name string
		rows int
	}{
		{name: "256", rows: 256},
		{name: "1M", rows: 1_000_000},
	}
	for _, v := range vector.Variants() {
		for _, size := range sizes {
			b.Run(v.String()+"/"+size.name, func(b *testing.B) {
				if err := v.Check(); err != nil {
					b.Skip(err)
				}
				if testing.Short() && size.rows > 256 {
					b.Skip("needs 4 GB of rows")
				}
				k := v.Kernels()
				q := randomVector(dim, 1)
				pool := make([][]float32, 64)
				for i := range pool {
					pool[i] = randomVector(dim, uint64(i+2))
				}
				if v.Format() == vector.Float32 {
					data := make([]float32, size.rows*dim)
					for r := range size.rows {
						copy(data[r*dim:], pool[r%len(pool)])
					}
					for b.Loop() {
						for off := 0; off < len(data); off += dim {
							k.Dot(q, data[off:off+dim])
						}
					}
				} else {
					data := make([]uint16, size.rows*dim)
					for r := range size.rows {
						if err := v.Format().Encode(data[r*dim:(r+1)*dim], pool[r%len(pool)]); err != nil {
							b.Fatal(err)
						}
					}
					for b.Loop() {
						for off := 0; off < len(data); off += dim {
							k.Dot16(q, data[off:off+dim])
						}
					}
				}
				b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*size.rows), "ns/row")
			})
		}
	}
}
