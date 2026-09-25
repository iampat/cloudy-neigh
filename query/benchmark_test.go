package query_test

import (
	"math/rand/v2"
	"strconv"
	"testing"

	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	"github.com/iampat/cloudy-neigh/query"
	"github.com/iampat/cloudy-neigh/vector"
)

func BenchmarkSearch10K1024_Table(b *testing.B) {
	benchmarkSearch(b, 10_000)
}

func BenchmarkSearch256x1024_Table(b *testing.B) {
	benchmarkSearch(b, 256)
}

func BenchmarkScan1M_Table(b *testing.B) {
	if testing.Short() {
		b.Skip("needs 4 GB of rows")
	}
	benchmarkSearch(b, 1_000_000)
}

func benchmarkSearch(b *testing.B, numVecs int) {
	const dim = 1024

	vec := make([]float32, dim)
	rng := rand.New(rand.NewPCG(42, 100))
	for i := range vec {
		vec[i] = rng.Float32() + 0.01
	}

	queryVec := make([]float32, dim)
	for i := range queryVec {
		queryVec[i] = rng.Float32() + 0.01
	}

	for _, v := range vector.Variants() {
		b.Run(v.String(), func(b *testing.B) {
			k, err := v.Kernels()
			if err != nil {
				b.Skip(err)
			}

			tbl := query.NewTable(k)
			vecMap := map[string][]float32{"default": vec}
			for i := 0; i < numVecs; i++ {
				id := strconv.Itoa(i)
				vec[0] = float32(i%1000) + 0.1
				if err := tbl.Upsert(id, vecMap, nil); err != nil {
					b.Fatal(err)
				}
			}

			for b.Loop() {
				hits, _, err := tbl.Search("default", queryVec, 10, cloudyneighpb.DistanceMetric_DISTANCE_METRIC_COSINE, nil)
				if err != nil {
					b.Fatal(err)
				}
				if len(hits) == 0 {
					b.Fatal("no hits")
				}
			}
		})
	}
}
