package query

import (
	"cmp"
	"container/heap"
	"errors"
	"fmt"
	"slices"
	"strings"

	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	"github.com/iampat/cloudy-neigh/query/distance"
	"google.golang.org/protobuf/proto"
)

var ErrDimensionMismatch = errors.New("query: vector dimension mismatch")

type packedVectorCol struct {
	dim      int
	data     []float32
	rowToVec []int
	vecToRow []int
}

type Table struct {
	docIDs     []string
	index      map[string]int
	tombstones []bool
	vectors    map[string]*packedVectorCol
	attrs      map[string][]*cloudyneighpb.AttributeValue
}

type Builder struct {
	docIDs     []string
	index      map[string]int
	tombstones []bool
	vectors    map[string]*packedVectorCol
	attrs      map[string][]*cloudyneighpb.AttributeValue
}

func NewBuilder() *Builder {
	return &Builder{
		index:   make(map[string]int),
		vectors: make(map[string]*packedVectorCol),
		attrs:   make(map[string][]*cloudyneighpb.AttributeValue),
	}
}

func (t *Table) Builder() *Builder {
	if t == nil {
		return NewBuilder()
	}
	index := make(map[string]int, len(t.index))
	for k, v := range t.index {
		index[k] = v
	}
	vectors := make(map[string]*packedVectorCol, len(t.vectors))
	for col, vCol := range t.vectors {
		vectors[col] = &packedVectorCol{
			dim:      vCol.dim,
			data:     slices.Clone(vCol.data),
			rowToVec: slices.Clone(vCol.rowToVec),
			vecToRow: slices.Clone(vCol.vecToRow),
		}
	}
	attrs := make(map[string][]*cloudyneighpb.AttributeValue, len(t.attrs))
	for k, col := range t.attrs {
		attrs[k] = slices.Clone(col)
	}
	return &Builder{
		docIDs:     slices.Clone(t.docIDs),
		index:      index,
		tombstones: slices.Clone(t.tombstones),
		vectors:    vectors,
		attrs:      attrs,
	}
}

func (b *Builder) Upsert(id string, vectors map[string][]float32, attrs map[string]*cloudyneighpb.AttributeValue) error {
	if id == "" {
		return errors.New("query: empty doc id")
	}

	if b.index == nil {
		b.index = make(map[string]int)
		b.vectors = make(map[string]*packedVectorCol)
		b.attrs = make(map[string][]*cloudyneighpb.AttributeValue)
	}

	for col, vec := range vectors {
		if len(vec) == 0 {
			continue
		}
		if vCol, ok := b.vectors[col]; ok {
			if len(vec) != vCol.dim {
				return fmt.Errorf("%w: column %q has dimension %d, got %d", ErrDimensionMismatch, col, vCol.dim, len(vec))
			}
		}
	}

	for col, vec := range vectors {
		if len(vec) == 0 {
			continue
		}
		if _, ok := b.vectors[col]; !ok {
			vCol := &packedVectorCol{
				dim:      len(vec),
				rowToVec: make([]int, len(b.docIDs)),
			}
			for i := range vCol.rowToVec {
				vCol.rowToVec[i] = -1
			}
			b.vectors[col] = vCol
		}
	}

	for k, v := range attrs {
		if v == nil {
			continue
		}
		if _, ok := b.attrs[k]; !ok {
			b.attrs[k] = make([]*cloudyneighpb.AttributeValue, len(b.docIDs))
		}
	}

	if row, exists := b.index[id]; exists {
		if b.tombstones[row] {
			b.tombstones[row] = false
		}

		for col, vec := range vectors {
			if len(vec) == 0 {
				continue
			}
			vCol := b.vectors[col]
			vecIdx := vCol.rowToVec[row]
			if vecIdx >= 0 {
				offset := vecIdx * vCol.dim
				copy(vCol.data[offset:offset+vCol.dim], vec)
			} else {
				vecIdx = len(vCol.vecToRow)
				vCol.data = append(vCol.data, vec...)
				vCol.vecToRow = append(vCol.vecToRow, row)
				vCol.rowToVec[row] = vecIdx
			}
		}

		for k, v := range attrs {
			if v == nil {
				continue
			}
			b.attrs[k][row] = proto.Clone(v).(*cloudyneighpb.AttributeValue)
		}
		return nil
	}

	row := len(b.docIDs)
	b.docIDs = append(b.docIDs, id)
	b.tombstones = append(b.tombstones, false)
	b.index[id] = row

	for col, vCol := range b.vectors {
		if vec, ok := vectors[col]; ok && len(vec) > 0 {
			vecIdx := len(vCol.vecToRow)
			vCol.data = append(vCol.data, vec...)
			vCol.vecToRow = append(vCol.vecToRow, row)
			vCol.rowToVec = append(vCol.rowToVec, vecIdx)
		} else {
			vCol.rowToVec = append(vCol.rowToVec, -1)
		}
	}

	for k, col := range b.attrs {
		if v, ok := attrs[k]; ok && v != nil {
			b.attrs[k] = append(col, proto.Clone(v).(*cloudyneighpb.AttributeValue))
		} else {
			b.attrs[k] = append(col, nil)
		}
	}
	return nil
}

func (b *Builder) UpsertRecord(rec *cloudyneighpb.Record) error {
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
	return b.Upsert(rec.Id, vectors, rec.Attributes)
}

func (b *Builder) Delete(id string) bool {
	row, ok := b.index[id]
	if !ok || b.tombstones[row] {
		return false
	}
	b.tombstones[row] = true
	return true
}

func (b *Builder) Build() *Table {
	t := &Table{
		docIDs:     b.docIDs,
		index:      b.index,
		tombstones: b.tombstones,
		vectors:    b.vectors,
		attrs:      b.attrs,
	}
	b.docIDs = nil
	b.index = nil
	b.tombstones = nil
	b.vectors = nil
	b.attrs = nil
	return t
}

func NewTable() *Table {
	return NewBuilder().Build()
}

func (t *Table) recordAt(row int, id string) *cloudyneighpb.Record {
	rec := &cloudyneighpb.Record{
		Id:         id,
		Vectors:    make(map[string]*cloudyneighpb.Vector, len(t.vectors)),
		Attributes: make(map[string]*cloudyneighpb.AttributeValue, len(t.attrs)),
	}

	for col, vCol := range t.vectors {
		if row < len(vCol.rowToVec) {
			vecIdx := vCol.rowToVec[row]
			if vecIdx >= 0 {
				offset := vecIdx * vCol.dim
				rec.Vectors[col] = &cloudyneighpb.Vector{
					Values: slices.Clone(vCol.data[offset : offset+vCol.dim]),
				}
			}
		}
	}

	for k, col := range t.attrs {
		if val := col[row]; val != nil {
			rec.Attributes[k] = proto.Clone(val).(*cloudyneighpb.AttributeValue)
		}
	}

	return rec
}

func (t *Table) Get(id string) (*cloudyneighpb.Record, bool) {
	row, ok := t.index[id]
	if !ok || t.tombstones[row] {
		return nil, false
	}
	return t.recordAt(row, id), true
}

func (t *Table) Vector(id, col string) ([]float32, bool) {
	row, ok := t.index[id]
	if !ok || t.tombstones[row] {
		return nil, false
	}
	vCol, ok := t.vectors[col]
	if !ok || row >= len(vCol.rowToVec) {
		return nil, false
	}
	vecIdx := vCol.rowToVec[row]
	if vecIdx < 0 {
		return nil, false
	}
	offset := vecIdx * vCol.dim
	return slices.Clone(vCol.data[offset : offset+vCol.dim]), true
}

func (t *Table) Attribute(id, key string) (*cloudyneighpb.AttributeValue, bool) {
	row, ok := t.index[id]
	if !ok || t.tombstones[row] {
		return nil, false
	}
	col, ok := t.attrs[key]
	if !ok {
		return nil, false
	}
	val := col[row]
	if val == nil {
		return nil, false
	}
	return proto.Clone(val).(*cloudyneighpb.AttributeValue), true
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

func (t *Table) Search(
	col string,
	query []float32,
	topK int,
	metric cloudyneighpb.DistanceMetric,
	filter *cloudyneighpb.EqualityFilter,
) ([]*cloudyneighpb.ScoredRecord, error) {
	if topK <= 0 {
		return nil, nil
	}

	vCol, ok := t.vectors[col]
	if !ok {
		return nil, nil
	}

	if len(query) != vCol.dim {
		return nil, ErrDimensionMismatch
	}

	var cmpFunc func(a, b searchHit) int
	switch metric {
	case cloudyneighpb.DistanceMetric_DISTANCE_METRIC_COSINE:
		normSq, err := distance.DotProduct(query, query)
		if err != nil {
			return nil, err
		}
		if normSq == 0 {
			return nil, distance.ErrZeroVector
		}
		cmpFunc = cmpAsc
	case cloudyneighpb.DistanceMetric_DISTANCE_METRIC_EUCLIDEAN_SQUARED:
		cmpFunc = cmpAsc
	case cloudyneighpb.DistanceMetric_DISTANCE_METRIC_DOT_PRODUCT:
		cmpFunc = cmpDesc
	default:
		return nil, fmt.Errorf("query: unknown metric %v", metric)
	}

	heapCap := topK
	if len(vCol.vecToRow) < heapCap {
		heapCap = len(vCol.vecToRow)
	}
	h := hitHeap{
		hits: make([]searchHit, 0, heapCap),
		cmp:  cmpFunc,
	}

	for vecIdx, row := range vCol.vecToRow {
		if t.tombstones[row] {
			continue
		}
		if filter != nil {
			attrCol, ok := t.attrs[filter.Field]
			if !ok || attrCol[row] == nil || !proto.Equal(attrCol[row], filter.Value) {
				continue
			}
		}

		offset := vecIdx * vCol.dim
		storedVec := vCol.data[offset : offset+vCol.dim]

		var score float32
		switch metric {
		case cloudyneighpb.DistanceMetric_DISTANCE_METRIC_COSINE:
			var err error
			score, err = distance.Cosine(query, storedVec)
			if err != nil {
				if errors.Is(err, distance.ErrZeroVector) {
					continue
				}
				return nil, err
			}
		case cloudyneighpb.DistanceMetric_DISTANCE_METRIC_EUCLIDEAN_SQUARED:
			var err error
			score, err = distance.L2Squared(query, storedVec)
			if err != nil {
				return nil, err
			}
		case cloudyneighpb.DistanceMetric_DISTANCE_METRIC_DOT_PRODUCT:
			var err error
			score, err = distance.DotProduct(query, storedVec)
			if err != nil {
				return nil, err
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

	hits := make([]*cloudyneighpb.ScoredRecord, len(h.hits))
	for i, hit := range h.hits {
		hits[i] = &cloudyneighpb.ScoredRecord{
			Record: t.recordAt(hit.row, hit.id),
			Score:  hit.score,
		}
	}
	return hits, nil
}
