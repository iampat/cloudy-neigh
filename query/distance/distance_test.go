package distance_test

import (
	"encoding/binary"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/iampat/cloudy-neigh/query/distance"
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
		got, err := distance.L2Squared(a, b)
		require.NoError(t, err)
		require.InDelta(t, float32(50), got, 1e-5)
	})

	t.Run("identical vectors", func(t *testing.T) {
		a := []float32{1, 2, 3}
		got, err := distance.L2Squared(a, a)
		require.NoError(t, err)
		require.InDelta(t, float32(0), got, 1e-5)
	})

	t.Run("orthogonal vectors", func(t *testing.T) {
		a := []float32{1, 0, 0}
		b := []float32{0, 1, 0}
		got, err := distance.L2Squared(a, b)
		require.NoError(t, err)
		require.InDelta(t, float32(2), got, 1e-5)
	})

	t.Run("zero vectors", func(t *testing.T) {
		a := []float32{0, 0, 0}
		b := []float32{0, 0, 0}
		got, err := distance.L2Squared(a, b)
		require.NoError(t, err)
		require.InDelta(t, float32(0), got, 1e-5)
	})

	t.Run("dimension mismatch", func(t *testing.T) {
		a := []float32{1, 2}
		b := []float32{1}
		_, err := distance.L2Squared(a, b)
		require.ErrorIs(t, err, distance.ErrDimensionMismatch)
	})

	t.Run("empty vectors", func(t *testing.T) {
		got, err := distance.L2Squared([]float32{}, []float32{})
		require.NoError(t, err)
		require.Equal(t, float32(0), got)
	})
}

func TestDotProduct(t *testing.T) {
	t.Run("known vectors", func(t *testing.T) {
		a := []float32{1, 2, 3}
		b := []float32{4, 6, 8}
		got, err := distance.DotProduct(a, b)
		require.NoError(t, err)
		require.InDelta(t, float32(40), got, 1e-5)
	})

	t.Run("identical vectors", func(t *testing.T) {
		a := []float32{1, 2, 3}
		got, err := distance.DotProduct(a, a)
		require.NoError(t, err)
		require.InDelta(t, float32(14), got, 1e-5)
	})

	t.Run("orthogonal vectors", func(t *testing.T) {
		a := []float32{1, 0, 0}
		b := []float32{0, 1, 0}
		got, err := distance.DotProduct(a, b)
		require.NoError(t, err)
		require.InDelta(t, float32(0), got, 1e-5)
	})

	t.Run("zero vectors", func(t *testing.T) {
		a := []float32{0, 0, 0}
		b := []float32{0, 0, 0}
		got, err := distance.DotProduct(a, b)
		require.NoError(t, err)
		require.InDelta(t, float32(0), got, 1e-5)
	})

	t.Run("dimension mismatch", func(t *testing.T) {
		a := []float32{1, 2}
		b := []float32{1}
		_, err := distance.DotProduct(a, b)
		require.ErrorIs(t, err, distance.ErrDimensionMismatch)
	})

	t.Run("empty vectors", func(t *testing.T) {
		got, err := distance.DotProduct([]float32{}, []float32{})
		require.NoError(t, err)
		require.Equal(t, float32(0), got)
	})
}

func TestCosine(t *testing.T) {
	t.Run("identical vectors", func(t *testing.T) {
		a := []float32{1, 2, 3}
		got, err := distance.Cosine(a, a)
		require.NoError(t, err)
		require.InDelta(t, float32(0), got, 1e-5)
	})

	t.Run("orthogonal vectors", func(t *testing.T) {
		a := []float32{3, 0}
		b := []float32{0, 4}
		got, err := distance.Cosine(a, b)
		require.NoError(t, err)
		require.InDelta(t, float32(1), got, 1e-5)
	})

	t.Run("opposite vectors", func(t *testing.T) {
		a := []float32{1, 2, 3}
		b := []float32{-1, -2, -3}
		got, err := distance.Cosine(a, b)
		require.NoError(t, err)
		require.InDelta(t, float32(2), got, 1e-5)
	})

	t.Run("zero vector first", func(t *testing.T) {
		a := []float32{0, 0, 0}
		b := []float32{1, 2, 3}
		_, err := distance.Cosine(a, b)
		require.ErrorIs(t, err, distance.ErrZeroVector)
	})

	t.Run("zero vector second", func(t *testing.T) {
		a := []float32{1, 2, 3}
		b := []float32{0, 0, 0}
		_, err := distance.Cosine(a, b)
		require.ErrorIs(t, err, distance.ErrZeroVector)
	})

	t.Run("both zero vectors", func(t *testing.T) {
		a := []float32{0, 0, 0}
		b := []float32{0, 0, 0}
		_, err := distance.Cosine(a, b)
		require.ErrorIs(t, err, distance.ErrZeroVector)
	})

	t.Run("dimension mismatch", func(t *testing.T) {
		a := []float32{1, 2}
		b := []float32{1}
		_, err := distance.Cosine(a, b)
		require.ErrorIs(t, err, distance.ErrDimensionMismatch)
	})

	t.Run("empty vectors", func(t *testing.T) {
		_, err := distance.Cosine([]float32{}, []float32{})
		require.ErrorIs(t, err, distance.ErrZeroVector)
	})
}

func TestNormalize(t *testing.T) {
	t.Run("known vector", func(t *testing.T) {
		v := []float32{3, 4}
		norm, err := distance.Normalize(v)
		require.NoError(t, err)
		require.InDelta(t, float32(0.6), norm[0], 1e-5)
		require.InDelta(t, float32(0.8), norm[1], 1e-5)
		require.Equal(t, []float32{3, 4}, v)
	})

	t.Run("empty vector", func(t *testing.T) {
		_, err := distance.Normalize([]float32{})
		require.ErrorIs(t, err, distance.ErrZeroVector)
	})

	t.Run("zero vector", func(t *testing.T) {
		_, err := distance.Normalize([]float32{0, 0, 0})
		require.ErrorIs(t, err, distance.ErrZeroVector)
	})
}

func TestNormalizeInPlace(t *testing.T) {
	t.Run("known vector", func(t *testing.T) {
		v := []float32{3, 4}
		err := distance.NormalizeInPlace(v)
		require.NoError(t, err)
		require.InDelta(t, float32(0.6), v[0], 1e-5)
		require.InDelta(t, float32(0.8), v[1], 1e-5)
	})

	t.Run("empty vector", func(t *testing.T) {
		err := distance.NormalizeInPlace([]float32{})
		require.ErrorIs(t, err, distance.ErrZeroVector)
	})

	t.Run("zero vector", func(t *testing.T) {
		err := distance.NormalizeInPlace([]float32{0, 0})
		require.ErrorIs(t, err, distance.ErrZeroVector)
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
		a, _ := distance.Normalize(randomVector(dim, uint64(dim*10+1)))
		b, _ := distance.Normalize(randomVector(dim, uint64(dim*10+2)))
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
		v, _ := distance.Normalize(randomVector(dim, uint64(dim*10+1)))
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
			_, err := distance.L2Squared(a, b)
			require.ErrorIs(t, err, distance.ErrDimensionMismatch)
			return
		}
		if len(a) == 0 {
			require.Equal(t, float32(0), distance.L2SquaredPure(a, b))
			require.Equal(t, float32(0), distance.L2SquaredPortable(a, b))
			require.Equal(t, float32(0), distance.L2SquaredArch(a, b))
			got, err := distance.L2Squared(a, b)
			require.NoError(t, err)
			require.Equal(t, float32(0), got)
			return
		}
		if hasNonFinite(a) || hasNonFinite(b) {
			return
		}

		l2Pure := distance.L2SquaredPure(a, b)
		l2Acc := distance.L2SquaredAccelerated(a, b)
		l2Port := distance.L2SquaredPortable(a, b)
		l2Arch := distance.L2SquaredArch(a, b)

		if math.IsInf(float64(l2Pure), 0) || math.IsNaN(float64(l2Pure)) {
			return
		}

		tol := float64(1e-5)
		if mag := math.Abs(float64(l2Pure)); mag > 1.0 {
			tol = 1e-5 * mag
		}
		require.InDelta(t, l2Pure, l2Acc, tol)
		require.InDelta(t, l2Pure, l2Port, tol)
		require.InDelta(t, l2Pure, l2Arch, tol)

		got, err := distance.L2Squared(a, b)
		require.NoError(t, err)
		require.InDelta(t, l2Pure, got, tol)
	})
}

func FuzzDotProduct(f *testing.F) {
	seedVectorPairCorpus(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		a, b := deriveVectors(data)
		if len(a) != len(b) {
			_, err := distance.DotProduct(a, b)
			require.ErrorIs(t, err, distance.ErrDimensionMismatch)
			return
		}
		if len(a) == 0 {
			require.Equal(t, float32(0), distance.DotProductPure(a, b))
			require.Equal(t, float32(0), distance.DotProductPortable(a, b))
			require.Equal(t, float32(0), distance.DotProductArch(a, b))
			got, err := distance.DotProduct(a, b)
			require.NoError(t, err)
			require.Equal(t, float32(0), got)
			return
		}
		if hasNonFinite(a) || hasNonFinite(b) {
			return
		}

		dotPure := distance.DotProductPure(a, b)
		dotAcc := distance.DotProductAccelerated(a, b)
		dotPort := distance.DotProductPortable(a, b)
		dotArch := distance.DotProductArch(a, b)

		if math.IsInf(float64(dotPure), 0) || math.IsNaN(float64(dotPure)) {
			return
		}

		tol := float64(1e-5)
		if mag := math.Abs(float64(dotPure)); mag > 1.0 {
			tol = 1e-5 * mag
		}
		require.InDelta(t, dotPure, dotAcc, tol)
		require.InDelta(t, dotPure, dotPort, tol)
		require.InDelta(t, dotPure, dotArch, tol)

		got, err := distance.DotProduct(a, b)
		require.NoError(t, err)
		require.InDelta(t, dotPure, got, tol)
	})
}

func FuzzCosine(f *testing.F) {
	seedVectorPairCorpus(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		a, b := deriveVectors(data)
		if len(a) != len(b) {
			_, err := distance.Cosine(a, b)
			require.ErrorIs(t, err, distance.ErrDimensionMismatch)
			return
		}
		if len(a) == 0 {
			_, errPure := distance.CosinePure(a, b)
			_, errPort := distance.CosinePortable(a, b)
			_, errArch := distance.CosineArch(a, b)
			require.ErrorIs(t, errPure, distance.ErrZeroVector)
			require.ErrorIs(t, errPort, distance.ErrZeroVector)
			require.ErrorIs(t, errArch, distance.ErrZeroVector)
			_, err := distance.Cosine(a, b)
			require.ErrorIs(t, err, distance.ErrZeroVector)
			return
		}
		if hasNonFinite(a) || hasNonFinite(b) {
			return
		}

		cosPure, errPure := distance.CosinePure(a, b)
		cosAcc, errAcc := distance.CosineAccelerated(a, b)
		cosPort, errPort := distance.CosinePortable(a, b)
		cosArch, errArch := distance.CosineArch(a, b)

		if errPure != nil {
			require.ErrorIs(t, errPure, distance.ErrZeroVector)
			require.ErrorIs(t, errAcc, distance.ErrZeroVector)
			require.ErrorIs(t, errPort, distance.ErrZeroVector)
			require.ErrorIs(t, errArch, distance.ErrZeroVector)
			_, err := distance.Cosine(a, b)
			require.ErrorIs(t, err, distance.ErrZeroVector)
			return
		}
		require.NoError(t, errAcc)
		require.NoError(t, errPort)
		require.NoError(t, errArch)

		if math.IsNaN(float64(cosPure)) || math.IsInf(float64(cosPure), 0) {
			return
		}

		require.InDelta(t, cosPure, cosAcc, 1e-5)
		require.InDelta(t, cosPure, cosPort, 1e-5)
		require.InDelta(t, cosPure, cosArch, 1e-5)

		got, err := distance.Cosine(a, b)
		require.NoError(t, err)
		require.InDelta(t, cosPure, got, 1e-5)
	})
}

func FuzzNormalizeInPlace(f *testing.F) {
	seedSingleVectorCorpus(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		v := deriveSingleVector(data)
		if len(v) == 0 {
			errPure := distance.NormalizeInPlacePure(v)
			errPort := distance.NormalizeInPlacePortable(v)
			errArch := distance.NormalizeInPlaceArch(v)
			require.ErrorIs(t, errPure, distance.ErrZeroVector)
			require.ErrorIs(t, errPort, distance.ErrZeroVector)
			require.ErrorIs(t, errArch, distance.ErrZeroVector)
			err := distance.NormalizeInPlace(v)
			require.ErrorIs(t, err, distance.ErrZeroVector)
			return
		}
		if hasNonFinite(v) {
			return
		}

		normPure := slices.Clone(v)
		normAcc := slices.Clone(v)
		normPort := slices.Clone(v)
		normArch := slices.Clone(v)

		errPure := distance.NormalizeInPlacePure(normPure)
		errAcc := distance.NormalizeInPlaceAccelerated(normAcc)
		errPort := distance.NormalizeInPlacePortable(normPort)
		errArch := distance.NormalizeInPlaceArch(normArch)

		if errPure != nil {
			require.ErrorIs(t, errPure, distance.ErrZeroVector)
			require.ErrorIs(t, errAcc, distance.ErrZeroVector)
			require.ErrorIs(t, errPort, distance.ErrZeroVector)
			require.ErrorIs(t, errArch, distance.ErrZeroVector)
			err := distance.NormalizeInPlace(slices.Clone(v))
			require.ErrorIs(t, err, distance.ErrZeroVector)
			return
		}
		require.NoError(t, errAcc)
		require.NoError(t, errPort)
		require.NoError(t, errArch)

		for i := range normPure {
			require.InDelta(t, normPure[i], normAcc[i], 1e-5)
			require.InDelta(t, normPure[i], normPort[i], 1e-5)
			require.InDelta(t, normPure[i], normArch[i], 1e-5)
		}
	})
}

func BenchmarkDistance(b *testing.B) {
	funcs := []struct {
		name     string
		pure     func(a, b []float32) (float32, error)
		portable func(a, b []float32) (float32, error)
		arch     func(a, b []float32) (float32, error)
	}{
		{
			name:     "L2Squared",
			pure:     func(a, b []float32) (float32, error) { return distance.L2SquaredPure(a, b), nil },
			portable: func(a, b []float32) (float32, error) { return distance.L2SquaredPortable(a, b), nil },
			arch:     func(a, b []float32) (float32, error) { return distance.L2SquaredArch(a, b), nil },
		},
		{
			name:     "DotProduct",
			pure:     func(a, b []float32) (float32, error) { return distance.DotProductPure(a, b), nil },
			portable: func(a, b []float32) (float32, error) { return distance.DotProductPortable(a, b), nil },
			arch:     func(a, b []float32) (float32, error) { return distance.DotProductArch(a, b), nil },
		},
		{
			name:     "Cosine",
			pure:     distance.CosinePure,
			portable: distance.CosinePortable,
			arch:     distance.CosineArch,
		},
	}
	for _, f := range funcs {
		for _, dim := range []int{128, 768, 1024} {
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
					if _, err := f.arch(v1, v2); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
	for _, dim := range []int{128, 768, 1024} {
		b.Run("NormalizeInPlace/pure/"+strconv.Itoa(dim), func(b *testing.B) {
			v := randomVector(dim, 1)
			buf := slices.Clone(v)
			for b.Loop() {
				copy(buf, v)
				if err := distance.NormalizeInPlacePure(buf); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run("NormalizeInPlace/simd-portable/"+strconv.Itoa(dim), func(b *testing.B) {
			v := randomVector(dim, 1)
			buf := slices.Clone(v)
			for b.Loop() {
				copy(buf, v)
				if err := distance.NormalizeInPlacePortable(buf); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run("NormalizeInPlace/simd-arch/"+strconv.Itoa(dim), func(b *testing.B) {
			v := randomVector(dim, 1)
			buf := slices.Clone(v)
			for b.Loop() {
				copy(buf, v)
				if err := distance.NormalizeInPlaceArch(buf); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestImplementation(t *testing.T) {
	impl := distance.Implementation()
	require.Contains(t, []string{"pure", "simd"}, impl)
}
