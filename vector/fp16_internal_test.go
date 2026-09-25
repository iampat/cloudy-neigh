package vector

import (
	"math"
	"math/rand/v2"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func randomFloats(dim int, seed uint64) []float32 {
	rng := rand.New(rand.NewPCG(seed, seed+1))
	v := make([]float32, dim)
	for i := range v {
		v[i] = rng.Float32()*2 - 1
	}
	return v
}

func dotFP16Pure(q []float32, row []uint16) float32 {
	var sum float32
	for i := range q {
		sum += q[i] * fp16ToFloat32(row[i])
	}
	return sum
}

func l2FP16Pure(q []float32, row []uint16) float32 {
	var sum float32
	for i := range q {
		d := q[i] - fp16ToFloat32(row[i])
		sum += d * d
	}
	return sum
}

func TestKernels16(t *testing.T) {
	k, err := FP16.Kernels()
	if err != nil {
		t.Skip(err)
	}
	for _, dim := range []int{1, 2, 12, 15, 16, 17, 31, 32, 64, 100, 128, 129, 200, 256, 1024, 1031} {
		t.Run(strconv.Itoa(dim), func(t *testing.T) {
			q := randomFloats(dim, uint64(dim)*3+1)
			row := make([]uint16, dim)
			require.NoError(t, EncodeFP16(row, randomFloats(dim, uint64(dim)*3+2)))

			dot := dotFP16Pure(q, row)
			require.InDelta(t, dot, k.Dot16(q, row), 1e-4*(1+math.Abs(float64(dot))))
			l2 := l2FP16Pure(q, row)
			require.InDelta(t, l2, k.L216(q, row), 1e-4*(1+math.Abs(float64(l2))))
		})
	}
}

func TestDecode16(t *testing.T) {
	k, err := FP16.Kernels()
	if err != nil {
		t.Skip(err)
	}
	decode := k.Decode16
	for _, dim := range []int{1, 15, 16, 17, 100, 1024, 1031} {
		t.Run(strconv.Itoa(dim), func(t *testing.T) {
			enc := make([]uint16, dim)
			require.NoError(t, EncodeFP16(enc, randomFloats(dim, uint64(dim)*5+1)))
			got := make([]float32, dim)
			decode(got, enc)
			for i, h := range enc {
				require.Equal(t, fp16ToFloat32(h), got[i], "index %d", i)
			}
		})
	}

	t.Run("short dst", func(t *testing.T) {
		enc := make([]uint16, 32)
		short := make([]float32, 16)
		require.Panics(t, func() {
			decode(short, enc)
		})
		buf := make([]float32, 64)
		shortWithCap := buf[:10]
		require.Panics(t, func() {
			decode(shortWithCap, enc)
		})
	})
}

func TestFloat16RoundTrip(t *testing.T) {
	decode := func(enc []uint16) []float32 {
		out := make([]float32, len(enc))
		for i, h := range enc {
			out[i] = fp16ToFloat32(h)
		}
		return out
	}
	t.Run("exact", func(t *testing.T) {
		exact := []float32{0, 1, -1, 0.5, 65504, -65504}
		enc := make([]uint16, len(exact))
		require.NoError(t, EncodeFP16(enc, exact))
		require.Equal(t, exact, decode(enc))
	})
	t.Run("random", func(t *testing.T) {
		src := append(randomFloats(1000, 7), 1e-5, -1e-5, 0.1, 0.3)
		enc := make([]uint16, len(src))
		require.NoError(t, EncodeFP16(enc, src))
		got := decode(enc)
		for i, x := range src {
			tol := math.Abs(float64(x))/(1<<11) + 1.0/(1<<25)
			require.InDelta(t, x, got[i], tol, "value %v", x)
		}
	})
}
