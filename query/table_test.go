package query_test

import (
	"errors"
	"fmt"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	"github.com/iampat/cloudy-neigh/query"
	"github.com/iampat/cloudy-neigh/vector"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func stringAttr(s string) *cloudyneighpb.AttributeValue {
	return &cloudyneighpb.AttributeValue{Value: &cloudyneighpb.AttributeValue_StringValue{StringValue: s}}
}

func assertAttrsEqual(t *testing.T, want, got map[string]*cloudyneighpb.AttributeValue) {
	t.Helper()
	require.Equal(t, len(want), len(got))
	for k, v := range want {
		require.True(t, proto.Equal(v, got[k]))
	}
}

func TestTable_Upsert(t *testing.T) {
	tests := []struct {
		name     string
		rec      *cloudyneighpb.Record
		wantErr  bool
		wantVec  []float32
		wantAttr map[string]*cloudyneighpb.AttributeValue
	}{
		{
			name: "record with vector and attributes",
			rec: &cloudyneighpb.Record{
				Id: "doc-1",
				Vectors: map[string]*cloudyneighpb.Vector{
					"default": {Values: []float32{1.1, 2.2, 3.3}},
				},
				Attributes: map[string]*cloudyneighpb.AttributeValue{
					"title": stringAttr("hello"),
					"lang":  stringAttr("en"),
				},
			},
			wantVec: []float32{1.1, 2.2, 3.3},
			wantAttr: map[string]*cloudyneighpb.AttributeValue{
				"title": stringAttr("hello"),
				"lang":  stringAttr("en"),
			},
		},
		{
			name: "record without vector",
			rec: &cloudyneighpb.Record{
				Id: "doc-2",
				Attributes: map[string]*cloudyneighpb.AttributeValue{
					"tag": stringAttr("test"),
				},
			},
			wantVec: nil,
			wantAttr: map[string]*cloudyneighpb.AttributeValue{
				"tag": stringAttr("test"),
			},
		},
		{
			name: "record without attributes",
			rec: &cloudyneighpb.Record{
				Id: "doc-3",
				Vectors: map[string]*cloudyneighpb.Vector{
					"default": {Values: []float32{4.4, 5.5, 6.6}},
				},
			},
			wantVec:  []float32{4.4, 5.5, 6.6},
			wantAttr: map[string]*cloudyneighpb.AttributeValue{},
		},
		{
			name: "empty record id rejected",
			rec: &cloudyneighpb.Record{
				Id: "",
				Vectors: map[string]*cloudyneighpb.Vector{
					"default": {Values: []float32{1.0}},
				},
			},
			wantErr: true,
		},
		{
			name: "nil vector in record rejected",
			rec: &cloudyneighpb.Record{
				Id: "doc-nil-vec",
				Vectors: map[string]*cloudyneighpb.Vector{
					"default": nil,
				},
			},
			wantErr: true,
		},
		{
			name:    "nil record rejected",
			rec:     nil,
			wantErr: true,
		},
	}

	b := query.NewTable()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := b.UpsertRecord(tc.rec)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			table := b
			rec, ok := table.Get(tc.rec.Id)
			require.True(t, ok)
			require.Equal(t, tc.rec.Id, rec.Id)
			assertAttrsEqual(t, tc.wantAttr, rec.Attributes)

			if len(tc.wantVec) > 0 {
				require.Equal(t, tc.wantVec, rec.Vectors["default"].Values)
			} else {
				require.Nil(t, rec.Vectors["default"])
			}
			b = table.Clone()
		})
	}

	table := b
	t.Run("mutation isolation on read", func(t *testing.T) {
		rec, ok := table.Get("doc-1")
		require.True(t, ok)
		rec.Attributes["title"] = stringAttr("corrupted")
		rec.Vectors["default"].Values[0] = 999.0

		fresh, ok := table.Get("doc-1")
		require.True(t, ok)
		require.True(t, proto.Equal(stringAttr("hello"), fresh.Attributes["title"]))
		require.Equal(t, float32(1.1), fresh.Vectors["default"].Values[0])
	})
}

func TestTable_MultiVector(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		vectors map[string][]float32
		attrs   map[string]*cloudyneighpb.AttributeValue
		wantErr bool
	}{
		{
			name: "multiple named vector columns",
			id:   "doc-mv-1",
			vectors: map[string][]float32{
				"title_emb": {0.1, 0.2},
				"body_emb":  {0.3, 0.4, 0.5, 0.6},
			},
			attrs: map[string]*cloudyneighpb.AttributeValue{"type": stringAttr("article")},
		},
		{
			name: "sparse vector columns",
			id:   "doc-mv-2",
			vectors: map[string][]float32{
				"title_emb": {0.7, 0.8},
			},
			attrs: map[string]*cloudyneighpb.AttributeValue{"type": stringAttr("headline")},
		},
		{
			name: "dimension mismatch rejected",
			id:   "doc-mv-3",
			vectors: map[string][]float32{
				"title_emb": {0.9, 1.0, 1.1},
			},
			wantErr: true,
		},
	}

	b := query.NewTable()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := b.Upsert(tc.id, tc.vectors, tc.attrs)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			table := b
			rec, ok := table.Get(tc.id)
			require.True(t, ok)
			for col, expected := range tc.vectors {
				require.Equal(t, expected, rec.Vectors[col].Values)
			}
			b = table.Clone()
		})
	}

	table := b
	rec, ok := table.Get("doc-mv-2")
	require.True(t, ok)
	require.Nil(t, rec.Vectors["body_emb"])
	require.Nil(t, rec.Vectors["nonexistent"])
}

func TestTable_PartialUpdate(t *testing.T) {
	b := query.NewTable()
	err := b.Upsert("doc-p", map[string][]float32{
		"vec": {1.0, 2.0},
	}, map[string]*cloudyneighpb.AttributeValue{
		"a": stringAttr("initial-a"),
		"b": stringAttr("initial-b"),
	})
	require.NoError(t, err)

	tests := []struct {
		name     string
		vectors  map[string][]float32
		attrs    map[string]*cloudyneighpb.AttributeValue
		wantVec  []float32
		wantAttr map[string]*cloudyneighpb.AttributeValue
	}{
		{
			name: "merge attributes key-by-key preserving existing keys",
			attrs: map[string]*cloudyneighpb.AttributeValue{
				"b": stringAttr("updated-b"),
				"c": stringAttr("new-c"),
			},
			wantVec: []float32{1.0, 2.0},
			wantAttr: map[string]*cloudyneighpb.AttributeValue{
				"a": stringAttr("initial-a"),
				"b": stringAttr("updated-b"),
				"c": stringAttr("new-c"),
			},
		},
		{
			name: "update vector preserves attributes",
			vectors: map[string][]float32{
				"vec": {3.0, 4.0},
			},
			wantVec: []float32{3.0, 4.0},
			wantAttr: map[string]*cloudyneighpb.AttributeValue{
				"a": stringAttr("initial-a"),
				"b": stringAttr("updated-b"),
				"c": stringAttr("new-c"),
			},
		},
		{
			name: "empty vector does not overwrite existing vector",
			vectors: map[string][]float32{
				"vec": {},
			},
			attrs: map[string]*cloudyneighpb.AttributeValue{
				"d": stringAttr("new-d"),
			},
			wantVec: []float32{3.0, 4.0},
			wantAttr: map[string]*cloudyneighpb.AttributeValue{
				"a": stringAttr("initial-a"),
				"b": stringAttr("updated-b"),
				"c": stringAttr("new-c"),
				"d": stringAttr("new-d"),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := b.Upsert("doc-p", tc.vectors, tc.attrs)
			require.NoError(t, err)

			table := b
			rec, ok := table.Get("doc-p")
			require.True(t, ok)
			require.Equal(t, tc.wantVec, rec.Vectors["vec"].Values)
			assertAttrsEqual(t, tc.wantAttr, rec.Attributes)
			b = table.Clone()
		})
	}
}

func TestTable_FlatStorage(t *testing.T) {
	table := query.NewTable()
	for i := 0; i < 8; i++ {
		id := string(rune('a' + i))
		err := table.Upsert(id, map[string][]float32{
			"v": {float32(i)},
		}, map[string]*cloudyneighpb.AttributeValue{
			"idx": stringAttr(string(rune('0' + i))),
		})
		require.NoError(t, err)
	}

	tests := []struct {
		id      string
		wantVal float32
		wantIdx *cloudyneighpb.AttributeValue
	}{
		{"a", 0.0, stringAttr("0")},
		{"b", 1.0, stringAttr("1")},
		{"c", 2.0, stringAttr("2")},
		{"d", 3.0, stringAttr("3")},
		{"e", 4.0, stringAttr("4")},
		{"f", 5.0, stringAttr("5")},
		{"g", 6.0, stringAttr("6")},
		{"h", 7.0, stringAttr("7")},
	}

	for _, tc := range tests {
		t.Run("retrieve "+tc.id, func(t *testing.T) {
			rec, ok := table.Get(tc.id)
			require.True(t, ok)
			require.Equal(t, tc.id, rec.Id)
			require.True(t, proto.Equal(tc.wantIdx, rec.Attributes["idx"]))

			require.Equal(t, []float32{tc.wantVal}, rec.Vectors["v"].Values)
		})
	}

	_, ok := table.Get("c")
	require.True(t, ok)
	_, ok = table.Get("e")
	require.True(t, ok)

	table = table.Clone()
	err := table.Upsert("h", nil, map[string]*cloudyneighpb.AttributeValue{"idx": stringAttr("updated")})
	require.NoError(t, err)
	rec, ok := table.Get("h")
	require.True(t, ok)
	require.True(t, proto.Equal(stringAttr("updated"), rec.Attributes["idx"]))
}

func TestTable_ZeroValue(t *testing.T) {
	var table query.Table

	_, ok := table.Get("nonexistent")
	require.False(t, ok)

	b := table.Clone()
	err := b.Upsert("doc-1", map[string][]float32{
		"vec": {1.0, 2.0},
	}, map[string]*cloudyneighpb.AttributeValue{
		"title": stringAttr("zero-value-test"),
	})
	require.NoError(t, err)

	rec, ok := b.Get("doc-1")
	require.True(t, ok)
	require.Equal(t, "doc-1", rec.Id)
	require.True(t, proto.Equal(stringAttr("zero-value-test"), rec.Attributes["title"]))
	require.Equal(t, []float32{1.0, 2.0}, rec.Vectors["vec"].Values)

	var nilTable *query.Table
	cloned := nilTable.Clone()
	require.NotNil(t, cloned)
}

func TestTable_SparseVectors(t *testing.T) {
	table := query.NewTable()
	for i := 0; i < 50; i++ {
		err := table.Upsert("doc-"+strconv.Itoa(i), nil, map[string]*cloudyneighpb.AttributeValue{
			"k": stringAttr("v"),
		})
		require.NoError(t, err)
	}

	err := table.Upsert("doc-50", map[string][]float32{
		"v": {1.0, 2.0, 3.0, 4.0},
	}, nil)
	require.NoError(t, err)

	for i := 0; i < 50; i++ {
		id := "doc-" + strconv.Itoa(i)
		rec, ok := table.Get(id)
		require.True(t, ok)
		require.Nil(t, rec.Vectors["v"])
	}

	rec, ok := table.Get("doc-50")
	require.True(t, ok)
	require.Equal(t, []float32{1.0, 2.0, 3.0, 4.0}, rec.Vectors["v"].Values)
}

func TestTable_Delete(t *testing.T) {
	table := query.NewTable()
	err := table.Upsert("doc-1", map[string][]float32{
		"vec": {1.0, 2.0},
	}, map[string]*cloudyneighpb.AttributeValue{
		"title": stringAttr("test"),
	})
	require.NoError(t, err)

	_, ok := table.Get("doc-1")
	require.True(t, ok)

	deletedTable := table.Clone()
	require.True(t, deletedTable.Delete("doc-1"))
	_, ok = deletedTable.Get("doc-1")
	require.False(t, ok)

	table2 := deletedTable.Clone()
	require.False(t, table2.Delete("doc-1"))
	require.False(t, table2.Delete("nonexistent"))
}

func TestTable_Search_Metrics(t *testing.T) {
	table := query.NewTable()
	docs := []struct {
		id  string
		vec []float32
	}{
		{"doc-1", []float32{1.0, 0.0}},
		{"doc-2", []float32{0.0, 1.0}},
		{"doc-3", []float32{-1.0, 0.0}},
		{"doc-4", []float32{0.6, 0.8}},
	}
	for _, d := range docs {
		err := table.Upsert(d.id, map[string][]float32{"v": d.vec}, nil)
		require.NoError(t, err)
	}

	tests := []struct {
		name      string
		metric    cloudyneighpb.DistanceMetric
		wantIDs   []string
		wantScore []float32
	}{
		{
			name:      "cosine distance ascending",
			metric:    cloudyneighpb.DistanceMetric_DISTANCE_METRIC_COSINE,
			wantIDs:   []string{"doc-1", "doc-4", "doc-2", "doc-3"},
			wantScore: []float32{0.0, 0.4, 1.0, 2.0},
		},
		{
			name:      "l2 squared distance ascending",
			metric:    cloudyneighpb.DistanceMetric_DISTANCE_METRIC_EUCLIDEAN_SQUARED,
			wantIDs:   []string{"doc-1", "doc-4", "doc-2", "doc-3"},
			wantScore: []float32{0.0, 0.8, 2.0, 4.0},
		},
		{
			name:      "dot product descending",
			metric:    cloudyneighpb.DistanceMetric_DISTANCE_METRIC_DOT_PRODUCT,
			wantIDs:   []string{"doc-1", "doc-4", "doc-2", "doc-3"},
			wantScore: []float32{1.0, 0.6, 0.0, -1.0},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hits, _, err := table.Search("v", []float32{1.0, 0.0}, 4, tc.metric, nil)
			require.NoError(t, err)
			require.Len(t, hits, len(tc.wantIDs))

			for i, hit := range hits {
				require.Equal(t, tc.wantIDs[i], hit.Record.Id)
				require.InDelta(t, tc.wantScore[i], hit.Score, 1e-5)
			}
		})
	}
}

func TestTable_Search_TopKBounds(t *testing.T) {
	table := query.NewTable()
	for i := 1; i <= 5; i++ {
		err := table.Upsert("doc-"+strconv.Itoa(i), map[string][]float32{
			"v": {float32(i), 0.0},
		}, nil)
		require.NoError(t, err)
	}

	tests := []struct {
		name    string
		topK    int
		wantIDs []string
	}{
		{
			name:    "k less than N",
			topK:    2,
			wantIDs: []string{"doc-1", "doc-2"},
		},
		{
			name:    "k equal to N",
			topK:    5,
			wantIDs: []string{"doc-1", "doc-2", "doc-3", "doc-4", "doc-5"},
		},
		{
			name:    "k greater than N",
			topK:    10,
			wantIDs: []string{"doc-1", "doc-2", "doc-3", "doc-4", "doc-5"},
		},
		{
			name:    "k is zero",
			topK:    0,
			wantIDs: nil,
		},
		{
			name:    "k is negative",
			topK:    -1,
			wantIDs: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hits, _, err := table.Search("v", []float32{0.0, 0.0}, tc.topK, cloudyneighpb.DistanceMetric_DISTANCE_METRIC_EUCLIDEAN_SQUARED, nil)
			require.NoError(t, err)
			if len(tc.wantIDs) == 0 {
				require.Empty(t, hits)
				return
			}
			require.Len(t, hits, len(tc.wantIDs))
			for i, hit := range hits {
				require.Equal(t, tc.wantIDs[i], hit.Record.Id)
			}
		})
	}
}

func TestTable_Search_Ties(t *testing.T) {
	table := query.NewTable()
	docs := []string{"doc-c", "doc-a", "doc-d", "doc-b"}
	for _, id := range docs {
		err := table.Upsert(id, map[string][]float32{
			"v": {1.0, 0.0},
		}, nil)
		require.NoError(t, err)
	}

	tests := []struct {
		name    string
		topK    int
		metric  cloudyneighpb.DistanceMetric
		wantIDs []string
	}{
		{
			name:    "cosine ties broken by document ID ascending with k less than N",
			topK:    2,
			metric:  cloudyneighpb.DistanceMetric_DISTANCE_METRIC_COSINE,
			wantIDs: []string{"doc-a", "doc-b"},
		},
		{
			name:    "dot product ties broken by document ID ascending with k less than N",
			topK:    3,
			metric:  cloudyneighpb.DistanceMetric_DISTANCE_METRIC_DOT_PRODUCT,
			wantIDs: []string{"doc-a", "doc-b", "doc-c"},
		},
		{
			name:    "ties with k equal to N sorted completely by document ID",
			topK:    4,
			metric:  cloudyneighpb.DistanceMetric_DISTANCE_METRIC_EUCLIDEAN_SQUARED,
			wantIDs: []string{"doc-a", "doc-b", "doc-c", "doc-d"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hits, _, err := table.Search("v", []float32{1.0, 0.0}, tc.topK, tc.metric, nil)
			require.NoError(t, err)
			require.Len(t, hits, len(tc.wantIDs))
			for i, hit := range hits {
				require.Equal(t, tc.wantIDs[i], hit.Record.Id)
			}
		})
	}
}

func TestTable_Search_Filters(t *testing.T) {
	table := query.NewTable()
	records := []struct {
		id    string
		vec   []float32
		attrs map[string]*cloudyneighpb.AttributeValue
	}{
		{
			id:    "doc-1",
			vec:   []float32{1.0, 0.0},
			attrs: map[string]*cloudyneighpb.AttributeValue{"cat": stringAttr("books"), "tag": stringAttr("fav")},
		},
		{
			id:    "doc-2",
			vec:   []float32{0.9, 0.1},
			attrs: map[string]*cloudyneighpb.AttributeValue{"cat": stringAttr("electronics")},
		},
		{
			id:    "doc-3",
			vec:   []float32{0.8, 0.2},
			attrs: map[string]*cloudyneighpb.AttributeValue{"cat": stringAttr("books")},
		},
		{
			id:    "doc-4",
			vec:   []float32{0.95, 0.05},
			attrs: map[string]*cloudyneighpb.AttributeValue{"tag": stringAttr("fav")},
		},
	}
	for _, r := range records {
		err := table.Upsert(r.id, map[string][]float32{"v": r.vec}, r.attrs)
		require.NoError(t, err)
	}

	tests := []struct {
		name    string
		filter  *cloudyneighpb.EqualityFilter
		wantIDs []string
	}{
		{
			name: "filter hit returns only matching documents",
			filter: &cloudyneighpb.EqualityFilter{
				Field: "cat",
				Value: stringAttr("books"),
			},
			wantIDs: []string{"doc-1", "doc-3"},
		},
		{
			name: "filter miss on known field returns empty",
			filter: &cloudyneighpb.EqualityFilter{
				Field: "cat",
				Value: stringAttr("clothing"),
			},
			wantIDs: nil,
		},
		{
			name: "filter on unknown field returns empty",
			filter: &cloudyneighpb.EqualityFilter{
				Field: "missing_field",
				Value: stringAttr("val"),
			},
			wantIDs: nil,
		},
		{
			name:    "nil filter returns all documents",
			filter:  nil,
			wantIDs: []string{"doc-1", "doc-4", "doc-2", "doc-3"},
		},
		{
			name: "nil filter value matches nothing",
			filter: &cloudyneighpb.EqualityFilter{
				Field: "cat",
				Value: nil,
			},
			wantIDs: nil,
		},
		{
			name: "empty string filter value matches nothing when no empty string attributes exist",
			filter: &cloudyneighpb.EqualityFilter{
				Field: "cat",
				Value: stringAttr(""),
			},
			wantIDs: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hits, _, err := table.Search("v", []float32{1.0, 0.0}, 10, cloudyneighpb.DistanceMetric_DISTANCE_METRIC_COSINE, tc.filter)
			require.NoError(t, err)
			if len(tc.wantIDs) == 0 {
				require.Empty(t, hits)
				return
			}
			require.Len(t, hits, len(tc.wantIDs))
			for i, hit := range hits {
				require.Equal(t, tc.wantIDs[i], hit.Record.Id)
			}
		})
	}
}

func TestTable_Search_Skips(t *testing.T) {
	table := query.NewTable()

	err := table.Upsert("doc-live", map[string][]float32{"v": {1.0, 0.0}}, nil)
	require.NoError(t, err)

	err = table.Upsert("doc-tombstoned", map[string][]float32{"v": {1.0, 0.0}}, nil)
	require.NoError(t, err)
	require.True(t, table.Delete("doc-tombstoned"))

	err = table.Upsert("doc-other-col", map[string][]float32{"other": {1.0, 0.0}}, nil)
	require.NoError(t, err)

	err = table.Upsert("doc-no-vec", nil, map[string]*cloudyneighpb.AttributeValue{"a": stringAttr("b")})
	require.NoError(t, err)

	err = table.Upsert("doc-zero-vec", map[string][]float32{"v": {0.0, 0.0}}, nil)
	require.NoError(t, err)

	t.Run("cosine skips tombstones missing vectors and zero stored vectors", func(t *testing.T) {
		hits, _, err := table.Search("v", []float32{1.0, 0.0}, 10, cloudyneighpb.DistanceMetric_DISTANCE_METRIC_COSINE, nil)
		require.NoError(t, err)
		require.Len(t, hits, 1)
		require.Equal(t, "doc-live", hits[0].Record.Id)
	})

	t.Run("l2 squared includes zero stored vector", func(t *testing.T) {
		hits, _, err := table.Search("v", []float32{1.0, 0.0}, 10, cloudyneighpb.DistanceMetric_DISTANCE_METRIC_EUCLIDEAN_SQUARED, nil)
		require.NoError(t, err)
		require.Len(t, hits, 2)
		require.Equal(t, "doc-live", hits[0].Record.Id)
		require.Equal(t, "doc-zero-vec", hits[1].Record.Id)
	})
}

func TestTable_Search_Errors(t *testing.T) {
	table := query.NewTable()
	err := table.Upsert("doc-1", map[string][]float32{"v": {1.0, 2.0, 3.0}}, nil)
	require.NoError(t, err)

	tests := []struct {
		name      string
		col       string
		query     []float32
		metric    cloudyneighpb.DistanceMetric
		wantErrIs error
		wantEmpty bool
	}{
		{
			name:      "missing column returns empty slice without error",
			col:       "missing",
			query:     []float32{1.0, 2.0, 3.0},
			metric:    cloudyneighpb.DistanceMetric_DISTANCE_METRIC_COSINE,
			wantEmpty: true,
		},
		{
			name:      "dimension mismatch returns ErrDimensionMismatch",
			col:       "v",
			query:     []float32{1.0, 2.0},
			metric:    cloudyneighpb.DistanceMetric_DISTANCE_METRIC_COSINE,
			wantErrIs: query.ErrDimensionMismatch,
		},
		{
			name:      "zero query vector under cosine returns ErrZeroVector",
			col:       "v",
			query:     []float32{0.0, 0.0, 0.0},
			metric:    cloudyneighpb.DistanceMetric_DISTANCE_METRIC_COSINE,
			wantErrIs: vector.ErrZeroVector,
		},
		{
			name:      "zero query vector under l2 squared is allowed",
			col:       "v",
			query:     []float32{0.0, 0.0, 0.0},
			metric:    cloudyneighpb.DistanceMetric_DISTANCE_METRIC_EUCLIDEAN_SQUARED,
			wantErrIs: nil,
		},
		{
			name:      "zero query vector under dot product is allowed",
			col:       "v",
			query:     []float32{0.0, 0.0, 0.0},
			metric:    cloudyneighpb.DistanceMetric_DISTANCE_METRIC_DOT_PRODUCT,
			wantErrIs: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hits, _, err := table.Search(tc.col, tc.query, 5, tc.metric, nil)
			if tc.wantErrIs != nil {
				require.ErrorIs(t, err, tc.wantErrIs)
				return
			}
			require.NoError(t, err)
			if tc.wantEmpty {
				require.Empty(t, hits)
			} else {
				require.NotEmpty(t, hits)
			}
		})
	}

	t.Run("unspecified metric returns error", func(t *testing.T) {
		_, _, err := table.Search("v", []float32{1.0, 2.0, 3.0}, 5, cloudyneighpb.DistanceMetric_DISTANCE_METRIC_UNSPECIFIED, nil)
		require.Error(t, err)
	})

	t.Run("unknown metric returns error", func(t *testing.T) {
		_, _, err := table.Search("v", []float32{1.0, 2.0, 3.0}, 5, cloudyneighpb.DistanceMetric(999), nil)
		require.Error(t, err)
	})
}

func TestTable_Search_Concurrent(t *testing.T) {
	table := query.NewTable()
	for i := 0; i < 20; i++ {
		err := table.Upsert("doc-"+strconv.Itoa(i), map[string][]float32{
			"v": {float32(i), float32(i * 2)},
		}, map[string]*cloudyneighpb.AttributeValue{
			"tag": stringAttr("num"),
		})
		require.NoError(t, err)
	}

	var current atomic.Pointer[query.Table]
	current.Store(table)

	oldSnap := current.Load()

	const readers = 4
	const iters = 50
	errCh := make(chan error, readers+1)

	for r := 0; r < readers; r++ {
		go func() {
			for i := 0; i < iters; i++ {
				hits, _, err := oldSnap.Search("v", []float32{1.0, 2.0}, 5, cloudyneighpb.DistanceMetric_DISTANCE_METRIC_EUCLIDEAN_SQUARED, nil)
				if err != nil {
					errCh <- err
					return
				}
				if len(hits) != 5 {
					errCh <- fmt.Errorf("expected 5 hits on old snapshot, got %d", len(hits))
					return
				}
				if _, ok := oldSnap.Get("doc-1"); !ok {
					errCh <- errors.New("doc-1 not found on old snapshot")
					return
				}
				if _, ok := oldSnap.Get("doc-new"); ok {
					errCh <- errors.New("doc-new should not be visible on old snapshot")
					return
				}
			}
			errCh <- nil
		}()
	}

	go func() {
		for i := 0; i < iters; i++ {
			prev := current.Load()
			wb := prev.Clone()
			if err := wb.Upsert("doc-new", map[string][]float32{"v": {100.0, 200.0}}, nil); err != nil {
				errCh <- err
				return
			}
			wb.Delete("doc-1")
			current.Store(wb)
		}
		errCh <- nil
	}()

	for i := 0; i < readers+1; i++ {
		require.NoError(t, <-errCh)
	}

	newSnap := current.Load()
	require.NotEqual(t, oldSnap, newSnap)
	_, ok := oldSnap.Get("doc-1")
	require.True(t, ok)
	_, ok = oldSnap.Get("doc-new")
	require.False(t, ok)
	_, ok = newSnap.Get("doc-1")
	require.False(t, ok)
	_, ok = newSnap.Get("doc-new")
	require.True(t, ok)
}

func TestTable_ChunkBoundary(t *testing.T) {
	table := query.NewTable()
	const count = 2500
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("doc-%04d", i)
		err := table.Upsert(id, map[string][]float32{
			"v": {float32(i), float32(i + 1)},
		}, map[string]*cloudyneighpb.AttributeValue{
			"idx": stringAttr(strconv.Itoa(i)),
		})
		require.NoError(t, err)
	}

	for i := 0; i < count; i += 250 {
		id := fmt.Sprintf("doc-%04d", i)
		rec, ok := table.Get(id)
		require.True(t, ok)
		require.Equal(t, id, rec.Id)
		require.Equal(t, []float32{float32(i), float32(i + 1)}, rec.Vectors["v"].Values)
	}

	hits, _, err := table.Search("v", []float32{1000.0, 1001.0}, 3, cloudyneighpb.DistanceMetric_DISTANCE_METRIC_EUCLIDEAN_SQUARED, nil)
	require.NoError(t, err)
	require.Len(t, hits, 3)
	require.Equal(t, "doc-1000", hits[0].Record.Id)
	require.Equal(t, float32(0.0), hits[0].Score)
}

func TestTable_SnapshotIsolation_Delta(t *testing.T) {
	snap1 := query.NewTable()
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("doc-%02d", i)
		err := snap1.Upsert(id, map[string][]float32{
			"v": {float32(i), float32(i * 2)},
		}, map[string]*cloudyneighpb.AttributeValue{
			"tag": stringAttr("initial"),
		})
		require.NoError(t, err)
	}

	snap2 := snap1.Clone()
	err := snap2.Upsert("doc-delta", map[string][]float32{
		"v": {500.0, 1000.0},
	}, map[string]*cloudyneighpb.AttributeValue{
		"tag": stringAttr("delta"),
	})
	require.NoError(t, err)

	err = snap2.Upsert("doc-05", map[string][]float32{
		"v": {999.0, 999.0},
	}, map[string]*cloudyneighpb.AttributeValue{
		"tag": stringAttr("updated"),
	})
	require.NoError(t, err)

	deleted := snap2.Delete("doc-10")
	require.True(t, deleted)

	_, ok := snap1.Get("doc-delta")
	require.False(t, ok)

	rec5Old, ok := snap1.Get("doc-05")
	require.True(t, ok)
	require.Equal(t, []float32{5.0, 10.0}, rec5Old.Vectors["v"].Values)
	require.True(t, proto.Equal(stringAttr("initial"), rec5Old.Attributes["tag"]))

	rec10Old, ok := snap1.Get("doc-10")
	require.True(t, ok)
	require.Equal(t, "doc-10", rec10Old.Id)

	hitsOld, _, err := snap1.Search("v", []float32{999.0, 999.0}, 1, cloudyneighpb.DistanceMetric_DISTANCE_METRIC_EUCLIDEAN_SQUARED, nil)
	require.NoError(t, err)
	require.NotEmpty(t, hitsOld)
	require.NotEqual(t, float32(0.0), hitsOld[0].Score)

	recDelta, ok := snap2.Get("doc-delta")
	require.True(t, ok)
	require.Equal(t, "doc-delta", recDelta.Id)

	rec5New, ok := snap2.Get("doc-05")
	require.True(t, ok)
	require.Equal(t, []float32{999.0, 999.0}, rec5New.Vectors["v"].Values)
	require.True(t, proto.Equal(stringAttr("updated"), rec5New.Attributes["tag"]))

	_, ok = snap2.Get("doc-10")
	require.False(t, ok)

	hitsNew, _, err := snap2.Search("v", []float32{999.0, 999.0}, 1, cloudyneighpb.DistanceMetric_DISTANCE_METRIC_EUCLIDEAN_SQUARED, nil)
	require.NoError(t, err)
	require.Len(t, hitsNew, 1)
	require.Equal(t, "doc-05", hitsNew[0].Record.Id)
	require.Equal(t, float32(0.0), hitsNew[0].Score)
}

func TestTable_Search_Stats(t *testing.T) {
	tbl := query.NewTable()
	require.NoError(t, tbl.Upsert("doc-1", map[string][]float32{
		"default": {1.0, 0.0},
	}, nil))

	hits, stats, err := tbl.Search("default", []float32{1.0, 0.0}, 1, cloudyneighpb.DistanceMetric_DISTANCE_METRIC_COSINE, nil)
	require.NoError(t, err)
	require.Len(t, hits, 1)
	require.GreaterOrEqual(t, stats.ScanDuration, time.Duration(0))
	require.GreaterOrEqual(t, stats.MaterializeDuration, time.Duration(0))
}

func TestTable_ReupsertDeletedDocClearsPreviousAttributes(t *testing.T) {
	table := query.NewTable()
	err := table.Upsert("doc-1", map[string][]float32{
		"vec": {1.0, 2.0},
	}, map[string]*cloudyneighpb.AttributeValue{
		"title": stringAttr("initial"),
		"tag":   stringAttr("old"),
	})
	require.NoError(t, err)

	rec, ok := table.Get("doc-1")
	require.True(t, ok)
	require.NotNil(t, rec.Attributes["title"])
	require.NotNil(t, rec.Attributes["tag"])

	table = table.Clone()
	require.True(t, table.Delete("doc-1"))
	_, ok = table.Get("doc-1")
	require.False(t, ok)

	table = table.Clone()
	err = table.Upsert("doc-1", map[string][]float32{
		"vec": {3.0, 4.0},
	}, map[string]*cloudyneighpb.AttributeValue{
		"tag": stringAttr("new"),
	})
	require.NoError(t, err)

	rec, ok = table.Get("doc-1")
	require.True(t, ok)
	require.Nil(t, rec.Attributes["title"])
	require.True(t, proto.Equal(stringAttr("new"), rec.Attributes["tag"]))
	require.Equal(t, []float32{3.0, 4.0}, rec.Vectors["vec"].Values)
}

func TestTable_ReupsertDeletedDocClearsOmittedVectors(t *testing.T) {
	table := query.NewTable()
	err := table.Upsert("doc-1", map[string][]float32{
		"vec1": {1.0, 2.0},
		"vec2": {5.0, 6.0},
	}, nil)
	require.NoError(t, err)

	rec, ok := table.Get("doc-1")
	require.True(t, ok)
	require.Equal(t, []float32{1.0, 2.0}, rec.Vectors["vec1"].Values)

	table = table.Clone()
	require.True(t, table.Delete("doc-1"))

	table = table.Clone()
	err = table.Upsert("doc-1", map[string][]float32{
		"vec1": {3.0, 4.0},
		"vec2": {},
	}, nil)
	require.NoError(t, err)

	rec, ok = table.Get("doc-1")
	require.True(t, ok)
	require.Equal(t, []float32{3.0, 4.0}, rec.Vectors["vec1"].Values)
	require.Nil(t, rec.Vectors["vec2"])
}
