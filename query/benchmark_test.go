package query_test

import (
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strconv"
	"testing"

	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	"github.com/iampat/cloudy-neigh/query"
)

func BenchmarkSearch1M1024_FlatTable(b *testing.B) {
	const numVecs = 1_000_000
	const dim = 1024

	ft := query.NewFlatTableWithCapacity(numVecs, dim)
	vec := make([]float32, dim)
	rng := rand.New(rand.NewPCG(42, 100))
	for i := range vec {
		vec[i] = rng.Float32() + 0.01
	}

	vecMap := map[string][]float32{"default": vec}
	for i := 0; i < numVecs; i++ {
		id := strconv.Itoa(i)
		vec[0] = float32(i%1000) + 0.1
		if err := ft.Upsert(id, vecMap, nil); err != nil {
			b.Fatal(err)
		}
	}

	queryVec := make([]float32, dim)
	for i := range queryVec {
		queryVec[i] = rng.Float32() + 0.01
	}

	for b.Loop() {
		hits, _, err := ft.Search("default", queryVec, 10, cloudyneighpb.DistanceMetric_DISTANCE_METRIC_COSINE, nil)
		if err != nil {
			b.Fatal(err)
		}
		if len(hits) == 0 {
			b.Fatal("no hits")
		}
	}
}

func BenchmarkSearch1M1024_ChunkedTable(b *testing.B) {
	const numVecs = 1_000_000
	const dim = 1024

	builder := query.NewBuilder()
	vec := make([]float32, dim)
	rng := rand.New(rand.NewPCG(42, 100))
	for i := range vec {
		vec[i] = rng.Float32() + 0.01
	}

	vecMap := map[string][]float32{"default": vec}
	for i := 0; i < numVecs; i++ {
		id := strconv.Itoa(i)
		vec[0] = float32(i%1000) + 0.1
		if err := builder.Upsert(id, vecMap, nil); err != nil {
			b.Fatal(err)
		}
	}
	table := builder.Build()

	queryVec := make([]float32, dim)
	for i := range queryVec {
		queryVec[i] = rng.Float32() + 0.01
	}

	for b.Loop() {
		hits, _, err := table.Search("default", queryVec, 10, cloudyneighpb.DistanceMetric_DISTANCE_METRIC_COSINE, nil)
		if err != nil {
			b.Fatal(err)
		}
		if len(hits) == 0 {
			b.Fatal("no hits")
		}
	}
}

func TestProfile1M1024(t *testing.T) {
	if os.Getenv("RUN_1M_PROFILE") != "1" {
		t.Skip("skipping 1M profile; set RUN_1M_PROFILE=1 to enable")
	}

	const numVecs = 1_000_000
	const dim = 1024
	const iters = 20

	outDir := os.Getenv("PROFILE_OUT_DIR")
	if outDir == "" {
		outDir = "."
	}

	t.Run("FlatTable", func(t *testing.T) {
		ft := query.NewFlatTableWithCapacity(numVecs, dim)
		vec := make([]float32, dim)
		rng := rand.New(rand.NewPCG(42, 100))
		for i := range vec {
			vec[i] = rng.Float32() + 0.01
		}
		vecMap := map[string][]float32{"default": vec}
		for i := 0; i < numVecs; i++ {
			id := strconv.Itoa(i)
			vec[0] = float32(i%1000) + 0.1
			if err := ft.Upsert(id, vecMap, nil); err != nil {
				t.Fatal(err)
			}
		}

		queryVec := make([]float32, dim)
		for i := range queryVec {
			queryVec[i] = rng.Float32() + 0.01
		}

		f, err := os.Create(filepath.Join(outDir, "flat_clean_cpu.pprof"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()

		if err := pprof.StartCPUProfile(f); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < iters; i++ {
			hits, _, err := ft.Search("default", queryVec, 10, cloudyneighpb.DistanceMetric_DISTANCE_METRIC_COSINE, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(hits) == 0 {
				t.Fatal("no hits")
			}
		}
		pprof.StopCPUProfile()
	})

	t.Run("ChunkedTable", func(t *testing.T) {
		builder := query.NewBuilder()
		vec := make([]float32, dim)
		rng := rand.New(rand.NewPCG(42, 100))
		for i := range vec {
			vec[i] = rng.Float32() + 0.01
		}
		vecMap := map[string][]float32{"default": vec}
		for i := 0; i < numVecs; i++ {
			id := strconv.Itoa(i)
			vec[0] = float32(i%1000) + 0.1
			if err := builder.Upsert(id, vecMap, nil); err != nil {
				t.Fatal(err)
			}
		}
		table := builder.Build()

		queryVec := make([]float32, dim)
		for i := range queryVec {
			queryVec[i] = rng.Float32() + 0.01
		}

		f, err := os.Create(filepath.Join(outDir, "chunked_clean_cpu.pprof"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()

		if err := pprof.StartCPUProfile(f); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < iters; i++ {
			hits, _, err := table.Search("default", queryVec, 10, cloudyneighpb.DistanceMetric_DISTANCE_METRIC_COSINE, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(hits) == 0 {
				t.Fatal("no hits")
			}
		}
		pprof.StopCPUProfile()
	})
}
