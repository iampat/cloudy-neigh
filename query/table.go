package query

import (
	"cmp"
	"container/heap"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	"github.com/iampat/cloudy-neigh/vector"
	"google.golang.org/protobuf/proto"
)

var ErrDimensionMismatch = errors.New("query: vector dimension mismatch")

type flatVectorCol struct {
	dim      int
	data     []float32
	data16   []uint16
	hasVec   []bool
	invNorms []float32
}

func vectorInvNorm(v []float32) float32 {
	var normSq float32
	for _, x := range v {
		normSq += x * x
	}
	if normSq == 0 {
		return 0
	}
	return float32(1 / math.Sqrt(float64(normSq)))
}

type SearchStats struct {
	ScanDuration        time.Duration
	MaterializeDuration time.Duration
}

type Table struct {
	numRows    int
	docIDs     []string
	index      map[string]int
	tombstones []bool
	vectors    map[string]*flatVectorCol
	attrs      map[string][]*cloudyneighpb.AttributeValue
	kernels    vector.Kernels
}

func NewTable(kernels vector.Kernels) *Table {
	return &Table{
		index:   make(map[string]int),
		vectors: make(map[string]*flatVectorCol),
		attrs:   make(map[string][]*cloudyneighpb.AttributeValue),
		kernels: kernels,
	}
}

func (t *Table) Clone() *Table {
	c := &Table{
		numRows:    t.numRows,
		docIDs:     slices.Clone(t.docIDs),
		index:      make(map[string]int, len(t.index)),
		tombstones: slices.Clone(t.tombstones),
		vectors:    make(map[string]*flatVectorCol, len(t.vectors)),
		attrs:      make(map[string][]*cloudyneighpb.AttributeValue, len(t.attrs)),
		kernels:    t.kernels,
	}
	for k, v := range t.index {
		c.index[k] = v
	}
	for k, v := range t.vectors {
		c.vectors[k] = &flatVectorCol{
			dim:      v.dim,
			data:     slices.Clone(v.data),
			data16:   slices.Clone(v.data16),
			hasVec:   slices.Clone(v.hasVec),
			invNorms: slices.Clone(v.invNorms),
		}
	}
	for k, col := range t.attrs {
		newCol := make([]*cloudyneighpb.AttributeValue, len(col))
		for i, a := range col {
			if a != nil {
				newCol[i] = proto.Clone(a).(*cloudyneighpb.AttributeValue)
			}
		}
		c.attrs[k] = newCol
	}
	return c
}

type stagedVector struct {
	values  []float32
	data16  []uint16
	invNorm float32
}

func (t *Table) Upsert(id string, vectors map[string][]float32, attrs map[string]*cloudyneighpb.AttributeValue) error {
	if id == "" {
		return errors.New("query: empty doc id")
	}

	if t.index == nil {
		t.index = make(map[string]int)
		t.vectors = make(map[string]*flatVectorCol)
		t.attrs = make(map[string][]*cloudyneighpb.AttributeValue)
	}
	is16 := t.kernels.Dot16 != nil

	staged := make(map[string]stagedVector, len(vectors))
	for col, vec := range vectors {
		if len(vec) == 0 {
			continue
		}
		if vCol, ok := t.vectors[col]; ok && len(vec) != vCol.dim {
			return fmt.Errorf("%w: column %q has dimension %d, got %d", ErrDimensionMismatch, col, vCol.dim, len(vec))
		}
		sv := stagedVector{values: vec, invNorm: vectorInvNorm(vec)}
		if is16 {
			sv.data16 = make([]uint16, len(vec))
			if err := vector.EncodeFP16(sv.data16, vec); err != nil {
				return fmt.Errorf("query: encode vector %q: %w", col, err)
			}
		}
		staged[col] = sv
	}

	for col, sv := range staged {
		if _, ok := t.vectors[col]; ok {
			continue
		}
		dim := len(sv.values)
		vCol := &flatVectorCol{
			dim:      dim,
			hasVec:   make([]bool, t.numRows),
			invNorms: make([]float32, t.numRows),
		}
		if is16 {
			vCol.data16 = make([]uint16, t.numRows*dim)
		} else {
			vCol.data = make([]float32, t.numRows*dim)
		}
		t.vectors[col] = vCol
	}

	for k, v := range attrs {
		if v == nil {
			continue
		}
		if _, ok := t.attrs[k]; !ok {
			t.attrs[k] = make([]*cloudyneighpb.AttributeValue, t.numRows)
		}
	}

	if row, ok := t.index[id]; ok {
		if t.tombstones[row] {
			for k, aCol := range t.attrs {
				if _, ok := attrs[k]; !ok {
					aCol[row] = nil
				}
			}
			for col, vCol := range t.vectors {
				if _, ok := staged[col]; !ok {
					vCol.hasVec[row] = false
					vCol.invNorms[row] = 0
				}
			}
		}
		t.tombstones[row] = false

		for col, sv := range staged {
			vCol := t.vectors[col]
			offset := row * vCol.dim
			if is16 {
				copy(vCol.data16[offset:offset+vCol.dim], sv.data16)
			} else {
				copy(vCol.data[offset:offset+vCol.dim], sv.values)
			}
			vCol.hasVec[row] = true
			vCol.invNorms[row] = sv.invNorm
		}

		for k, v := range attrs {
			if v == nil {
				continue
			}
			t.attrs[k][row] = proto.Clone(v).(*cloudyneighpb.AttributeValue)
		}
		return nil
	}

	row := t.numRows
	t.docIDs = append(t.docIDs, id)
	t.tombstones = append(t.tombstones, false)
	t.index[id] = row

	for col, vCol := range t.vectors {
		sv, ok := staged[col]
		switch {
		case !ok && is16:
			vCol.data16 = append(vCol.data16, make([]uint16, vCol.dim)...)
		case !ok:
			vCol.data = append(vCol.data, make([]float32, vCol.dim)...)
		case is16:
			vCol.data16 = append(vCol.data16, sv.data16...)
		default:
			vCol.data = append(vCol.data, sv.values...)
		}
		vCol.hasVec = append(vCol.hasVec, ok)
		vCol.invNorms = append(vCol.invNorms, sv.invNorm)
	}

	for k, aCol := range t.attrs {
		if v, ok := attrs[k]; ok && v != nil {
			t.attrs[k] = append(aCol, proto.Clone(v).(*cloudyneighpb.AttributeValue))
		} else {
			t.attrs[k] = append(aCol, nil)
		}
	}

	t.numRows++
	return nil
}

func (t *Table) UpsertRecord(rec *cloudyneighpb.Record) error {
	if rec == nil {
		return errors.New("query: nil record")
	}
	var vectors map[string][]float32
	if len(rec.Vectors) > 0 {
		vectors = make(map[string][]float32, len(rec.Vectors))
		for name, vec := range rec.Vectors {
			if vec == nil {
				return fmt.Errorf("query: nil vector %q", name)
			}
			vectors[name] = vec.Values
		}
	}
	return t.Upsert(rec.Id, vectors, rec.Attributes)
}

func (t *Table) Delete(id string) bool {
	row, ok := t.index[id]
	if !ok || t.tombstones[row] {
		return false
	}
	t.tombstones[row] = true
	return true
}

func (t *Table) isTombstoned(row int) bool {
	return row < len(t.tombstones) && t.tombstones[row]
}

func (t *Table) recordAt(row int, id string) *cloudyneighpb.Record {
	rec := &cloudyneighpb.Record{
		Id:         id,
		Vectors:    make(map[string]*cloudyneighpb.Vector, len(t.vectors)),
		Attributes: make(map[string]*cloudyneighpb.AttributeValue, len(t.attrs)),
	}
	for col, vCol := range t.vectors {
		if row < len(vCol.hasVec) && vCol.hasVec[row] {
			offset := row * vCol.dim
			values := make([]float32, vCol.dim)
			if vCol.data16 != nil {
				t.kernels.Decode16(values, vCol.data16[offset:offset+vCol.dim])
			} else {
				copy(values, vCol.data[offset:offset+vCol.dim])
			}
			rec.Vectors[col] = &cloudyneighpb.Vector{Values: values}
		}
	}
	for k, col := range t.attrs {
		if row < len(col) && col[row] != nil {
			rec.Attributes[k] = proto.Clone(col[row]).(*cloudyneighpb.AttributeValue)
		}
	}
	return rec
}

func (t *Table) Get(id string) (*cloudyneighpb.Record, bool) {
	row, ok := t.index[id]
	if !ok || t.isTombstoned(row) {
		return nil, false
	}
	return t.recordAt(row, id), true
}

func (t *Table) Search(
	col string,
	query []float32,
	topK int,
	metric cloudyneighpb.DistanceMetric,
	filter *cloudyneighpb.EqualityFilter,
) ([]*cloudyneighpb.ScoredRecord, SearchStats, error) {
	if topK <= 0 {
		return nil, SearchStats{}, nil
	}

	vCol, ok := t.vectors[col]
	if !ok || t.numRows == 0 {
		return nil, SearchStats{}, nil
	}
	if len(query) != vCol.dim {
		return nil, SearchStats{}, ErrDimensionMismatch
	}
	var cmpFunc func(a, b searchHit) int
	isDesc := false
	var invQ float32
	switch metric {
	case cloudyneighpb.DistanceMetric_DISTANCE_METRIC_COSINE:
		invQ = vectorInvNorm(query)
		if invQ == 0 {
			return nil, SearchStats{}, vector.ErrZeroVector
		}
		cmpFunc = cmpAsc
	case cloudyneighpb.DistanceMetric_DISTANCE_METRIC_EUCLIDEAN_SQUARED:
		cmpFunc = cmpAsc
	case cloudyneighpb.DistanceMetric_DISTANCE_METRIC_DOT_PRODUCT:
		cmpFunc = cmpDesc
		isDesc = true
	default:
		return nil, SearchStats{}, fmt.Errorf("query: unknown metric %v", metric)
	}

	var filterAttrs []*cloudyneighpb.AttributeValue
	if filter != nil {
		colAttrs, ok := t.attrs[filter.Field]
		if !ok {
			return nil, SearchStats{}, nil
		}
		filterAttrs = colAttrs
	}

	heapCap := topK
	if t.numRows < heapCap {
		heapCap = t.numRows
	}
	h := hitHeap{
		hits: make([]searchHit, 0, heapCap),
		cmp:  cmpFunc,
	}

	dim := vCol.dim
	data := vCol.data
	data16 := vCol.data16
	scanStart := time.Now()

	for row := 0; row < t.numRows; row++ {
		if t.tombstones[row] || !vCol.hasVec[row] {
			continue
		}
		if filterAttrs != nil {
			if row >= len(filterAttrs) || filterAttrs[row] == nil || !proto.Equal(filterAttrs[row], filter.Value) {
				continue
			}
		}

		offset := row * dim

		var score float32
		switch metric {
		case cloudyneighpb.DistanceMetric_DISTANCE_METRIC_COSINE:
			if vCol.invNorms[row] == 0 {
				continue
			}
			var dot float32
			if data16 != nil {
				dot = t.kernels.Dot16(query, data16[offset:offset+dim])
			} else {
				dot = t.kernels.Dot(query, data[offset:offset+dim])
			}
			sim := dot * invQ * vCol.invNorms[row]
			switch {
			case sim > 1:
				sim = 1
			case sim < -1:
				sim = -1
			}
			score = 1 - sim
		case cloudyneighpb.DistanceMetric_DISTANCE_METRIC_EUCLIDEAN_SQUARED:
			if data16 != nil {
				score = t.kernels.L216(query, data16[offset:offset+dim])
			} else {
				score = t.kernels.L2(query, data[offset:offset+dim])
			}
		case cloudyneighpb.DistanceMetric_DISTANCE_METRIC_DOT_PRODUCT:
			if data16 != nil {
				score = t.kernels.Dot16(query, data16[offset:offset+dim])
			} else {
				score = t.kernels.Dot(query, data[offset:offset+dim])
			}
		}

		if h.Len() == topK {
			if isDesc {
				if score < h.hits[0].score {
					continue
				}
			} else {
				if score > h.hits[0].score {
					continue
				}
			}
		}

		cand := searchHit{
			row:   row,
			id:    t.docIDs[row],
			score: score,
		}

		if h.Len() < topK {
			heap.Push(&h, cand)
		} else if h.cmp(cand, h.hits[0]) < 0 {
			h.hits[0] = cand
			heap.Fix(&h, 0)
		}
	}

	slices.SortFunc(h.hits, h.cmp)
	scanDur := time.Since(scanStart)

	matStart := time.Now()
	hits := make([]*cloudyneighpb.ScoredRecord, len(h.hits))
	for i, hit := range h.hits {
		hits[i] = &cloudyneighpb.ScoredRecord{
			Record: t.recordAt(hit.row, hit.id),
			Score:  hit.score,
		}
	}
	matDur := time.Since(matStart)

	return hits, SearchStats{
		ScanDuration:        scanDur,
		MaterializeDuration: matDur,
	}, nil
}

type searchHit struct {
	row   int
	id    string
	score float32
}

func cmpAsc(a, b searchHit) int {
	if a.score != b.score {
		return cmp.Compare(a.score, b.score)
	}
	return strings.Compare(a.id, b.id)
}

func cmpDesc(a, b searchHit) int {
	if a.score != b.score {
		return cmp.Compare(b.score, a.score)
	}
	return strings.Compare(a.id, b.id)
}

type hitHeap struct {
	hits []searchHit
	cmp  func(a, b searchHit) int
}

func (h *hitHeap) Len() int           { return len(h.hits) }
func (h *hitHeap) Less(i, j int) bool { return h.cmp(h.hits[i], h.hits[j]) > 0 }
func (h *hitHeap) Swap(i, j int)      { h.hits[i], h.hits[j] = h.hits[j], h.hits[i] }
func (h *hitHeap) Push(x any)         { h.hits = append(h.hits, x.(searchHit)) }
func (h *hitHeap) Pop() any {
	n := len(h.hits)
	x := h.hits[n-1]
	h.hits = h.hits[:n-1]
	return x
}
