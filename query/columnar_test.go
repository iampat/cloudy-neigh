package query_test

import (
	"testing"

	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	"github.com/iampat/cloudy-neigh/query"
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
			wantVec: []float32{0.0, 0.0, 0.0},
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

	table := query.NewTable()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := table.UpsertRecord(tc.rec)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			rec, ok := table.Get(tc.rec.Id)
			require.True(t, ok)
			require.Equal(t, tc.rec.Id, rec.Id)
			assertAttrsEqual(t, tc.wantAttr, rec.Attributes)

			if len(tc.wantVec) > 0 {
				require.Equal(t, tc.wantVec, rec.Vectors["default"].Values)
				vec, ok := table.Vector(tc.rec.Id, "default")
				require.True(t, ok)
				require.Equal(t, tc.wantVec, vec)
			} else {
				_, ok := table.Vector(tc.rec.Id, "default")
				require.False(t, ok)
			}
		})
	}

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

	table := query.NewTable()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := table.Upsert(tc.id, tc.vectors, tc.attrs)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			rec, ok := table.Get(tc.id)
			require.True(t, ok)
			for col, expected := range tc.vectors {
				require.Equal(t, expected, rec.Vectors[col].Values)
				vec, ok := table.Vector(tc.id, col)
				require.True(t, ok)
				require.Equal(t, expected, vec)
			}
		})
	}

	vec, ok := table.Vector("doc-mv-2", "body_emb")
	require.True(t, ok)
	require.Equal(t, []float32{0.0, 0.0, 0.0, 0.0}, vec)

	vec, ok = table.Vector("doc-mv-2", "nonexistent")
	require.False(t, ok)
	require.Nil(t, vec)
}

func TestTable_PartialUpdate(t *testing.T) {
	table := query.NewTable()
	err := table.Upsert("doc-p", map[string][]float32{
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
			err := table.Upsert("doc-p", tc.vectors, tc.attrs)
			require.NoError(t, err)

			rec, ok := table.Get("doc-p")
			require.True(t, ok)
			require.Equal(t, tc.wantVec, rec.Vectors["vec"].Values)
			assertAttrsEqual(t, tc.wantAttr, rec.Attributes)
		})
	}
}

func TestTable_DeleteTombstones(t *testing.T) {
	table := query.NewTable()
	require.NoError(t, table.Upsert("doc-1", map[string][]float32{"vec": {1.0}}, map[string]*cloudyneighpb.AttributeValue{"k": stringAttr("v1")}))
	require.NoError(t, table.Upsert("doc-2", map[string][]float32{"vec": {2.0}}, map[string]*cloudyneighpb.AttributeValue{"k": stringAttr("v2")}))
	require.Equal(t, 2, table.Len())

	tests := []struct {
		name       string
		op         string
		id         string
		vectors    map[string][]float32
		attrs      map[string]*cloudyneighpb.AttributeValue
		wantOk     bool
		wantLen    int
		wantExists bool
	}{
		{
			name:       "delete doc-1 marks tombstone",
			op:         "delete",
			id:         "doc-1",
			wantOk:     true,
			wantLen:    1,
			wantExists: false,
		},
		{
			name:       "repeated delete of doc-1 returns false",
			op:         "delete",
			id:         "doc-1",
			wantOk:     false,
			wantLen:    1,
			wantExists: false,
		},
		{
			name:       "delete nonexistent doc returns false",
			op:         "delete",
			id:         "nonexistent",
			wantOk:     false,
			wantLen:    1,
			wantExists: false,
		},
		{
			name:       "re-upsert doc-1 clears tombstone and merges attributes",
			op:         "upsert",
			id:         "doc-1",
			vectors:    map[string][]float32{"vec": {10.0}},
			attrs:      map[string]*cloudyneighpb.AttributeValue{"extra": stringAttr("e")},
			wantOk:     true,
			wantLen:    2,
			wantExists: true,
		},
		{
			name:       "re-delete doc-1 marks tombstone again",
			op:         "delete",
			id:         "doc-1",
			wantOk:     true,
			wantLen:    1,
			wantExists: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.op == "delete" {
				ok := table.Delete(tc.id)
				require.Equal(t, tc.wantOk, ok)
			} else {
				err := table.Upsert(tc.id, tc.vectors, tc.attrs)
				require.NoError(t, err)
			}

			require.Equal(t, tc.wantLen, table.Len())
			_, exists := table.Get(tc.id)
			require.Equal(t, tc.wantExists, exists)
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

	require.Equal(t, 8, table.Len())

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

			vec, ok := table.Vector(tc.id, "v")
			require.True(t, ok)
			require.Equal(t, []float32{tc.wantVal}, vec)
		})
	}

	require.True(t, table.Delete("d"))
	require.False(t, table.Delete("d"))
	require.Equal(t, 7, table.Len())

	_, ok := table.Get("d")
	require.False(t, ok)
	_, ok = table.Get("c")
	require.True(t, ok)
	_, ok = table.Get("e")
	require.True(t, ok)

	err := table.Upsert("h", nil, map[string]*cloudyneighpb.AttributeValue{"idx": stringAttr("updated")})
	require.NoError(t, err)
	attr, ok := table.Attribute("h", "idx")
	require.True(t, ok)
	require.True(t, proto.Equal(stringAttr("updated"), attr))
}

func TestTable_ZeroValue(t *testing.T) {
	var table query.Table

	require.Equal(t, 0, table.Len())
	_, ok := table.Get("nonexistent")
	require.False(t, ok)
	require.False(t, table.Delete("nonexistent"))

	err := table.Upsert("doc-1", map[string][]float32{
		"vec": {1.0, 2.0},
	}, map[string]*cloudyneighpb.AttributeValue{
		"title": stringAttr("zero-value-test"),
	})
	require.NoError(t, err)
	require.Equal(t, 1, table.Len())

	rec, ok := table.Get("doc-1")
	require.True(t, ok)
	require.Equal(t, "doc-1", rec.Id)
	require.True(t, proto.Equal(stringAttr("zero-value-test"), rec.Attributes["title"]))
	require.Equal(t, []float32{1.0, 2.0}, rec.Vectors["vec"].Values)

	require.True(t, table.Delete("doc-1"))
	require.Equal(t, 0, table.Len())
	_, ok = table.Get("doc-1")
	require.False(t, ok)
}
