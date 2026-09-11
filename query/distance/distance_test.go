package distance_test

import (
	"math/rand"
	"slices"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/iampat/cloudy-neigh/query/distance"
)

func randomVector(dim int, seed int64) []float32 {
	rng := rand.New(rand.NewSource(seed))
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
		_, err := distance.L2Squared([]float32{}, []float32{})
		require.ErrorIs(t, err, distance.ErrEmptyVector)
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
		_, err := distance.DotProduct([]float32{}, []float32{})
		require.ErrorIs(t, err, distance.ErrEmptyVector)
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
		require.ErrorIs(t, err, distance.ErrEmptyVector)
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
		require.ErrorIs(t, err, distance.ErrEmptyVector)
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
		require.ErrorIs(t, err, distance.ErrEmptyVector)
	})

	t.Run("zero vector", func(t *testing.T) {
		err := distance.NormalizeInPlace([]float32{0, 0})
		require.ErrorIs(t, err, distance.ErrZeroVector)
	})
}

func TestParity(t *testing.T) {
	dimensions := []int{1, 2, 3, 4, 7, 8, 9, 15, 16, 128, 768, 1024}
	for _, dim := range dimensions {
		t.Run("dim-"+strconv.Itoa(dim), func(t *testing.T) {
			a, err := distance.Normalize(randomVector(dim, int64(dim*10+1)))
			require.NoError(t, err)
			b, err := distance.Normalize(randomVector(dim, int64(dim*10+2)))
			require.NoError(t, err)

			l2Pure := distance.L2SquaredPure(a, b)
			l2Acc := distance.L2SquaredAccelerated(a, b)
			l2Port := distance.L2SquaredPortable(a, b)
			require.InDelta(t, l2Pure, l2Acc, 1e-5)
			require.InDelta(t, l2Pure, l2Port, 1e-5)

			dotPure := distance.DotProductPure(a, b)
			dotAcc := distance.DotProductAccelerated(a, b)
			dotPort := distance.DotProductPortable(a, b)
			require.InDelta(t, dotPure, dotAcc, 1e-5)
			require.InDelta(t, dotPure, dotPort, 1e-5)

			cosPure, errPure := distance.CosinePure(a, b)
			cosAcc, errAcc := distance.CosineAccelerated(a, b)
			cosPort, errPort := distance.CosinePortable(a, b)
			require.NoError(t, errPure)
			require.NoError(t, errAcc)
			require.NoError(t, errPort)
			require.InDelta(t, cosPure, cosAcc, 1e-5)
			require.InDelta(t, cosPure, cosPort, 1e-5)

			normPure := slices.Clone(a)
			normAcc := slices.Clone(a)
			normPort := slices.Clone(a)
			errPure = distance.NormalizeInPlacePure(normPure)
			errAcc = distance.NormalizeInPlaceAccelerated(normAcc)
			errPort = distance.NormalizeInPlacePortable(normPort)
			require.NoError(t, errPure)
			require.NoError(t, errAcc)
			require.NoError(t, errPort)
			for i := range normPure {
				require.InDelta(t, normPure[i], normAcc[i], 1e-5)
				require.InDelta(t, normPure[i], normPort[i], 1e-5)
			}
		})
	}
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
