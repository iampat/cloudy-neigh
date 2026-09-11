package query_test

import (
	"testing"

	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	"github.com/iampat/cloudy-neigh/query"
	"github.com/stretchr/testify/require"
)

func TestTable_Upsert(t *testing.T) {
	tests := []struct {
		name     string
		doc      *cloudyneighpb.Document
		wantErr  bool
		wantVec  []float32
		wantAttr map[string]string
	}{
		{
			name: "doc with vector and attributes",
			doc: &cloudyneighpb.Document{
				Id:         "doc-1",
				Vector:     []float32{1.1, 2.2, 3.3},
				Attributes: map[string]string{"title": "hello", "lang": "en"},
			},
			wantVec:  []float32{1.1, 2.2, 3.3},
			wantAttr: map[string]string{"title": "hello", "lang": "en"},
		},
		{
			name: "doc without vector",
			doc: &cloudyneighpb.Document{
				Id:         "doc-2",
				Attributes: map[string]string{"tag": "test"},
			},
			wantVec:  nil,
			wantAttr: map[string]string{"tag": "test"},
		},
		{
			name: "doc without attributes",
			doc: &cloudyneighpb.Document{
				Id:     "doc-3",
				Vector: []float32{4.4, 5.5, 6.6},
			},
			wantVec:  []float32{4.4, 5.5, 6.6},
			wantAttr: map[string]string{},
		},
		{
			name: "empty doc id rejected",
			doc: &cloudyneighpb.Document{
				Id:     "",
				Vector: []float32{1.0},
			},
			wantErr: true,
		},
		{
			name:    "nil doc rejected",
			doc:     nil,
			wantErr: true,
		},
	}

	table := query.NewTable(0)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := table.UpsertDoc(tc.doc)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			rec, ok := table.Get(tc.doc.Id)
			require.True(t, ok)
			require.Equal(t, tc.doc.Id, rec.ID)
			require.Equal(t, tc.wantAttr, rec.Attributes)

			if len(tc.wantVec) > 0 {
				require.Equal(t, tc.wantVec, rec.Vectors[query.DefaultVectorColumn])
				vec, ok := table.Vector(tc.doc.Id, query.DefaultVectorColumn)
				require.True(t, ok)
				require.Equal(t, tc.wantVec, vec)
			} else {
				_, ok := table.Vector(tc.doc.Id, query.DefaultVectorColumn)
				require.False(t, ok)
			}
		})
	}

	t.Run("mutation isolation on read", func(t *testing.T) {
		rec, ok := table.Get("doc-1")
		require.True(t, ok)
		rec.Attributes["title"] = "corrupted"
		rec.Vectors[query.DefaultVectorColumn][0] = 999.0

		fresh, ok := table.Get("doc-1")
		require.True(t, ok)
		require.Equal(t, "hello", fresh.Attributes["title"])
		require.Equal(t, float32(1.1), fresh.Vectors[query.DefaultVectorColumn][0])
	})
}

func TestTable_MultiVector(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		vectors map[string][]float32
		attrs   map[string]string
		wantErr bool
	}{
		{
			name: "multiple named vector columns",
			id:   "doc-mv-1",
			vectors: map[string][]float32{
				"title_emb": {0.1, 0.2},
				"body_emb":  {0.3, 0.4, 0.5, 0.6},
			},
			attrs: map[string]string{"type": "article"},
		},
		{
			name: "sparse vector columns",
			id:   "doc-mv-2",
			vectors: map[string][]float32{
				"title_emb": {0.7, 0.8},
			},
			attrs: map[string]string{"type": "headline"},
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

	table := query.NewTable(0)
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
				require.Equal(t, expected, rec.Vectors[col])
				vec, ok := table.Vector(tc.id, col)
				require.True(t, ok)
				require.Equal(t, expected, vec)
			}
		})
	}

	vec, ok := table.Vector("doc-mv-2", "body_emb")
	require.False(t, ok)
	require.Nil(t, vec)
}

func TestTable_PartialUpdate(t *testing.T) {
	table := query.NewTable(0)
	err := table.Upsert("doc-p", map[string][]float32{
		"vec": {1.0, 2.0},
	}, map[string]string{
		"a": "initial-a",
		"b": "initial-b",
	})
	require.NoError(t, err)

	tests := []struct {
		name     string
		vectors  map[string][]float32
		attrs    map[string]string
		wantVec  []float32
		wantAttr map[string]string
	}{
		{
			name: "merge attributes key-by-key preserving existing keys",
			attrs: map[string]string{
				"b": "updated-b",
				"c": "new-c",
			},
			wantVec: []float32{1.0, 2.0},
			wantAttr: map[string]string{
				"a": "initial-a",
				"b": "updated-b",
				"c": "new-c",
			},
		},
		{
			name: "update vector preserves attributes",
			vectors: map[string][]float32{
				"vec": {3.0, 4.0},
			},
			wantVec: []float32{3.0, 4.0},
			wantAttr: map[string]string{
				"a": "initial-a",
				"b": "updated-b",
				"c": "new-c",
			},
		},
		{
			name: "empty vector does not overwrite existing vector",
			vectors: map[string][]float32{
				"vec": {},
			},
			attrs: map[string]string{
				"d": "new-d",
			},
			wantVec: []float32{3.0, 4.0},
			wantAttr: map[string]string{
				"a": "initial-a",
				"b": "updated-b",
				"c": "new-c",
				"d": "new-d",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := table.Upsert("doc-p", tc.vectors, tc.attrs)
			require.NoError(t, err)

			rec, ok := table.Get("doc-p")
			require.True(t, ok)
			require.Equal(t, tc.wantVec, rec.Vectors["vec"])
			require.Equal(t, tc.wantAttr, rec.Attributes)
		})
	}
}

func TestTable_DeleteTombstones(t *testing.T) {
	table := query.NewTable(0)
	require.NoError(t, table.Upsert("doc-1", map[string][]float32{"vec": {1.0}}, map[string]string{"k": "v1"}))
	require.NoError(t, table.Upsert("doc-2", map[string][]float32{"vec": {2.0}}, map[string]string{"k": "v2"}))
	require.Equal(t, 2, table.Len())

	tests := []struct {
		name       string
		op         string
		id         string
		vectors    map[string][]float32
		attrs      map[string]string
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
			attrs:      map[string]string{"extra": "e"},
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

func TestTable_ChunkBoundaries(t *testing.T) {
	table := query.NewTable(3)

	for i := 0; i < 8; i++ {
		id := string(rune('a' + i))
		err := table.Upsert(id, map[string][]float32{
			"v": {float32(i)},
		}, map[string]string{
			"idx": string(rune('0' + i)),
		})
		require.NoError(t, err)
	}

	require.Equal(t, 8, table.Len())

	tests := []struct {
		id      string
		wantVal float32
		wantIdx string
	}{
		{"a", 0.0, "0"},
		{"b", 1.0, "1"},
		{"c", 2.0, "2"},
		{"d", 3.0, "3"},
		{"e", 4.0, "4"},
		{"f", 5.0, "5"},
		{"g", 6.0, "6"},
		{"h", 7.0, "7"},
	}

	for _, tc := range tests {
		t.Run("retrieve "+tc.id, func(t *testing.T) {
			rec, ok := table.Get(tc.id)
			require.True(t, ok)
			require.Equal(t, tc.id, rec.ID)
			require.Equal(t, tc.wantIdx, rec.Attributes["idx"])

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

	err := table.Upsert("h", nil, map[string]string{"idx": "updated"})
	require.NoError(t, err)
	attr, ok := table.Attribute("h", "idx")
	require.True(t, ok)
	require.Equal(t, "updated", attr)
}
