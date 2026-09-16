package query_test

import (
	"strconv"
	"testing"
	"time"

	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	"github.com/iampat/cloudy-neigh/query"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestQueryExecutor_Conformance(t *testing.T) {
	executors := []struct {
		name string
		exec query.QueryExecutor
	}{
		{name: "chunked_table", exec: query.NewTable()},
		{name: "flat_table", exec: query.NewFlatTable()},
	}

	for _, tc := range executors {
		t.Run(tc.name, func(t *testing.T) {
			require.NotNil(t, tc.exec)
		})
	}
}

func TestFlatTable_UpsertAndGet(t *testing.T) {
	tbl := query.NewFlatTable()

	err := tbl.Upsert("doc-1", map[string][]float32{
		"default": {1.0, 2.0, 3.0},
	}, map[string]*cloudyneighpb.AttributeValue{
		"category": stringAttr("electronics"),
	})
	require.NoError(t, err)

	rec, ok := tbl.Get("doc-1")
	require.True(t, ok)
	require.Equal(t, "doc-1", rec.Id)
	require.Equal(t, []float32{1.0, 2.0, 3.0}, rec.Vectors["default"].Values)
	require.True(t, proto.Equal(stringAttr("electronics"), rec.Attributes["category"]))

	vec, ok := tbl.Vector("doc-1", "default")
	require.True(t, ok)
	require.Equal(t, []float32{1.0, 2.0, 3.0}, vec)

	attr, ok := tbl.Attribute("doc-1", "category")
	require.True(t, ok)
	require.True(t, proto.Equal(stringAttr("electronics"), attr))

	_, ok = tbl.Get("doc-missing")
	require.False(t, ok)

	_, ok = tbl.Vector("doc-1", "missing_col")
	require.False(t, ok)

	_, ok = tbl.Attribute("doc-1", "missing_attr")
	require.False(t, ok)
}

func TestFlatTable_Validation(t *testing.T) {
	tbl := query.NewFlatTable()

	require.Error(t, tbl.Upsert("", map[string][]float32{"default": {1.0}}, nil))
	require.Error(t, tbl.UpsertRecord(nil))

	require.NoError(t, tbl.Upsert("doc-1", map[string][]float32{"default": {1.0, 2.0}}, nil))
	require.ErrorIs(t, tbl.Upsert("doc-2", map[string][]float32{"default": {1.0}}, nil), query.ErrDimensionMismatch)

	require.Error(t, tbl.UpsertRecord(&cloudyneighpb.Record{
		Id: "doc-nil-vec",
		Vectors: map[string]*cloudyneighpb.Vector{
			"default": nil,
		},
	}))
}

func TestFlatTable_Delete(t *testing.T) {
	tbl := query.NewFlatTable()

	require.False(t, tbl.Delete("nonexistent"))

	require.NoError(t, tbl.Upsert("doc-1", map[string][]float32{"default": {1.0, 0.0}}, nil))
	require.True(t, tbl.Delete("doc-1"))
	require.False(t, tbl.Delete("doc-1"))

	_, ok := tbl.Get("doc-1")
	require.False(t, ok)

	_, ok = tbl.Vector("doc-1", "default")
	require.False(t, ok)

	_, ok = tbl.Attribute("doc-1", "any")
	require.False(t, ok)
}

func TestFlatTable_ReupsertDeletedDoc(t *testing.T) {
	tbl := query.NewFlatTable()

	require.NoError(t, tbl.Upsert("doc-1", map[string][]float32{
		"v1": {1.0, 2.0},
		"v2": {3.0, 4.0},
	}, map[string]*cloudyneighpb.AttributeValue{
		"tag": stringAttr("old"),
	}))

	require.True(t, tbl.Delete("doc-1"))

	require.NoError(t, tbl.Upsert("doc-1", map[string][]float32{
		"v1": {5.0, 6.0},
	}, map[string]*cloudyneighpb.AttributeValue{
		"tag": stringAttr("new"),
	}))

	rec, ok := tbl.Get("doc-1")
	require.True(t, ok)
	require.Equal(t, []float32{5.0, 6.0}, rec.Vectors["v1"].Values)
	require.Nil(t, rec.Vectors["v2"])
	require.True(t, proto.Equal(stringAttr("new"), rec.Attributes["tag"]))

	_, ok = tbl.Vector("doc-1", "v2")
	require.False(t, ok)
}

func TestFlatTable_SearchWithStats(t *testing.T) {
	tbl := query.NewFlatTable()

	docs := []struct {
		id  string
		vec []float32
		cat string
	}{
		{id: "d1", vec: []float32{1.0, 0.0}, cat: "a"},
		{id: "d2", vec: []float32{0.0, 1.0}, cat: "b"},
		{id: "d3", vec: []float32{0.7, 0.7}, cat: "a"},
	}

	for _, d := range docs {
		require.NoError(t, tbl.Upsert(d.id, map[string][]float32{"default": d.vec}, map[string]*cloudyneighpb.AttributeValue{
			"cat": stringAttr(d.cat),
		}))
	}

	hits, stats, err := tbl.SearchWithStats("default", []float32{1.0, 0.0}, 2, cloudyneighpb.DistanceMetric_DISTANCE_METRIC_COSINE, nil)
	require.NoError(t, err)
	require.Len(t, hits, 2)
	require.Equal(t, "d1", hits[0].Record.Id)
	require.Equal(t, "d3", hits[1].Record.Id)
	require.GreaterOrEqual(t, stats.ScanDuration, time.Duration(0))
	require.GreaterOrEqual(t, stats.MaterializeDuration, time.Duration(0))

	filter := &cloudyneighpb.EqualityFilter{
		Field: "cat",
		Value: stringAttr("b"),
	}
	filteredHits, err := tbl.Search("default", []float32{1.0, 0.0}, 10, cloudyneighpb.DistanceMetric_DISTANCE_METRIC_COSINE, filter)
	require.NoError(t, err)
	require.Len(t, filteredHits, 1)
	require.Equal(t, "d2", filteredHits[0].Record.Id)
}

func TestQueryExecutor_Equivalence(t *testing.T) {
	chunked := query.NewTable()
	flat := query.NewFlatTable()

	tables := []query.QueryExecutor{chunked, flat}

	for i := 0; i < 50; i++ {
		id := strconv.Itoa(i)
		v := []float32{float32(i), float32(50 - i), float32(i * 2)}
		attrs := map[string]*cloudyneighpb.AttributeValue{
			"parity": stringAttr(strconv.Itoa(i % 2)),
		}
		for _, tbl := range tables {
			require.NoError(t, tbl.Upsert(id, map[string][]float32{"v": v}, attrs))
		}
	}

	for _, tbl := range tables {
		require.True(t, tbl.Delete("10"))
		require.True(t, tbl.Delete("25"))
	}

	queryVec := []float32{20.0, 30.0, 40.0}
	chunkedHits, _, err1 := chunked.SearchWithStats("v", queryVec, 10, cloudyneighpb.DistanceMetric_DISTANCE_METRIC_COSINE, nil)
	require.NoError(t, err1)

	flatHits, _, err2 := flat.SearchWithStats("v", queryVec, 10, cloudyneighpb.DistanceMetric_DISTANCE_METRIC_COSINE, nil)
	require.NoError(t, err2)

	require.Equal(t, len(chunkedHits), len(flatHits))
	for i := range chunkedHits {
		require.Equal(t, chunkedHits[i].Record.Id, flatHits[i].Record.Id)
		require.InDelta(t, chunkedHits[i].Score, flatHits[i].Score, 1e-5)
	}

	filter := &cloudyneighpb.EqualityFilter{
		Field: "parity",
		Value: stringAttr("1"),
	}
	cFilterHits, err1 := chunked.Search("v", queryVec, 5, cloudyneighpb.DistanceMetric_DISTANCE_METRIC_EUCLIDEAN_SQUARED, filter)
	require.NoError(t, err1)

	fFilterHits, err2 := flat.Search("v", queryVec, 5, cloudyneighpb.DistanceMetric_DISTANCE_METRIC_EUCLIDEAN_SQUARED, filter)
	require.NoError(t, err2)

	require.Equal(t, len(cFilterHits), len(fFilterHits))
	for i := range cFilterHits {
		require.Equal(t, cFilterHits[i].Record.Id, fFilterHits[i].Record.Id)
		require.InDelta(t, cFilterHits[i].Score, fFilterHits[i].Score, 1e-5)
	}
}
