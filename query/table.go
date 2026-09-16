package query

import (
	"cmp"
	"container/heap"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	"github.com/iampat/cloudy-neigh/query/distance"
	"google.golang.org/protobuf/proto"
)

var ErrDimensionMismatch = errors.New("query: vector dimension mismatch")

type flatVectorCol struct {
	dim    int
	data   []float32
	hasVec []bool
}

type Table struct {
	mu         sync.Mutex
	numRows    int
	docIDs     []string
	index      map[string]int
	tombstones []bool
	vectors    map[string]*flatVectorCol
	attrs      map[string][]*cloudyneighpb.AttributeValue
}

var _ QueryExecutor = (*Table)(nil)

func NewTable() *Table {
	return NewTableWithCapacity(0, 0)
}

func NewTableWithCapacity(capacity, dim int) *Table {
	t := &Table{
		docIDs:     make([]string, 0, capacity),
		index:      make(map[string]int, capacity),
		tombstones: make([]bool, 0, capacity),
		vectors:    make(map[string]*flatVectorCol),
		attrs:      make(map[string][]*cloudyneighpb.AttributeValue),
	}
	if dim > 0 {
		t.vectors["default"] = &flatVectorCol{
			dim:    dim,
			data:   make([]float32, 0, capacity*dim),
			hasVec: make([]bool, 0, capacity),
		}
	}
	return t
}

func (t *Table) Clone() *Table {
	t.mu.Lock()
	defer t.mu.Unlock()

	c := &Table{
		numRows:    t.numRows,
		docIDs:     slices.Clone(t.docIDs),
		index:      make(map[string]int, len(t.index)),
		tombstones: slices.Clone(t.tombstones),
		vectors:    make(map[string]*flatVectorCol, len(t.vectors)),
		attrs:      make(map[string][]*cloudyneighpb.AttributeValue, len(t.attrs)),
	}
	for k, v := range t.index {
		c.index[k] = v
	}
	for k, v := range t.vectors {
		c.vectors[k] = &flatVectorCol{
			dim:    v.dim,
			data:   slices.Clone(v.data),
			hasVec: slices.Clone(v.hasVec),
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

func (t *Table) Upsert(id string, vectors map[string][]float32, attrs map[string]*cloudyneighpb.AttributeValue) error {
	if id == "" {
		return errors.New("query: empty doc id")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.index == nil {
		t.index = make(map[string]int)
		t.vectors = make(map[string]*flatVectorCol)
		t.attrs = make(map[string][]*cloudyneighpb.AttributeValue)
	}

	for col, vec := range vectors {
		if len(vec) == 0 {
			continue
		}
		if vCol, ok := t.vectors[col]; ok {
			if len(vec) != vCol.dim {
				return fmt.Errorf("%w: column %q has dimension %d, got %d", ErrDimensionMismatch, col, vCol.dim, len(vec))
			}
		}
	}

	for col, vec := range vectors {
		if len(vec) == 0 {
			continue
		}
		if _, ok := t.vectors[col]; !ok {
			dim := len(vec)
			data := make([]float32, t.numRows*dim)
			hasVec := make([]bool, t.numRows)
			t.vectors[col] = &flatVectorCol{
				dim:    dim,
				data:   data,
				hasVec: hasVec,
			}
		}
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
		wasDeleted := t.tombstones[row]
		t.tombstones[row] = false

		if wasDeleted {
			for k, aCol := range t.attrs {
				if _, ok := attrs[k]; !ok {
					if row < len(aCol) {
						aCol[row] = nil
					}
				}
			}
			for col, vCol := range t.vectors {
				if vec, ok := vectors[col]; !ok || len(vec) == 0 {
					if row < len(vCol.hasVec) {
						vCol.hasVec[row] = false
					}
				}
			}
		}

		for col, vec := range vectors {
			if len(vec) == 0 {
				continue
			}
			vCol := t.vectors[col]
			offset := row * vCol.dim
			copy(vCol.data[offset:offset+vCol.dim], vec)
			vCol.hasVec[row] = true
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
		if vec, ok := vectors[col]; ok && len(vec) > 0 {
			vCol.data = append(vCol.data, vec...)
			vCol.hasVec = append(vCol.hasVec, true)
		} else {
			vCol.data = append(vCol.data, make([]float32, vCol.dim)...)
			vCol.hasVec = append(vCol.hasVec, false)
		}
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
	t.mu.Lock()
	defer t.mu.Unlock()
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

func (t *Table) recordAtLocked(row int, id string) *cloudyneighpb.Record {
	rec := &cloudyneighpb.Record{
		Id:         id,
		Vectors:    make(map[string]*cloudyneighpb.Vector, len(t.vectors)),
		Attributes: make(map[string]*cloudyneighpb.AttributeValue, len(t.attrs)),
	}
	for col, vCol := range t.vectors {
		if row < len(vCol.hasVec) && vCol.hasVec[row] {
			offset := row * vCol.dim
			rec.Vectors[col] = &cloudyneighpb.Vector{
				Values: slices.Clone(vCol.data[offset : offset+vCol.dim]),
			}
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
	t.mu.Lock()
	defer t.mu.Unlock()
	row, ok := t.index[id]
	if !ok || t.isTombstoned(row) {
		return nil, false
	}
	return t.recordAtLocked(row, id), true
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

	t.mu.Lock()
	defer t.mu.Unlock()

	vCol, ok := t.vectors[col]
	if !ok || t.numRows == 0 {
		return nil, SearchStats{}, nil
	}
	if len(query) != vCol.dim {
		return nil, SearchStats{}, ErrDimensionMismatch
	}

	var cmpFunc func(a, b searchHit) int
	isDesc := false
	switch metric {
	case cloudyneighpb.DistanceMetric_DISTANCE_METRIC_COSINE:
		normSq, err := distance.DotProduct(query, query)
		if err != nil {
			return nil, SearchStats{}, err
		}
		if normSq == 0 {
			return nil, SearchStats{}, distance.ErrZeroVector
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
		storedVec := data[offset : offset+dim]

		var score float32
		switch metric {
		case cloudyneighpb.DistanceMetric_DISTANCE_METRIC_COSINE:
			var err error
			score, err = distance.Cosine(query, storedVec)
			if err != nil {
				if errors.Is(err, distance.ErrZeroVector) {
					continue
				}
				return nil, SearchStats{}, err
			}
		case cloudyneighpb.DistanceMetric_DISTANCE_METRIC_EUCLIDEAN_SQUARED:
			var err error
			score, err = distance.L2Squared(query, storedVec)
			if err != nil {
				return nil, SearchStats{}, err
			}
		case cloudyneighpb.DistanceMetric_DISTANCE_METRIC_DOT_PRODUCT:
			var err error
			score, err = distance.DotProduct(query, storedVec)
			if err != nil {
				return nil, SearchStats{}, err
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
			Record: t.recordAtLocked(hit.row, hit.id),
			Score:  hit.score,
		}
	}
	matDur := time.Since(matStart)

	return hits, SearchStats{
		ScanDuration:        scanDur,
		MaterializeDuration: matDur,
	}, nil
}

type Builder struct {
	table *Table
}

func NewBuilder() *Builder {
	return &Builder{table: NewTable()}
}

func (t *Table) Builder() *Builder {
	if t == nil {
		return NewBuilder()
	}
	return &Builder{table: t.Clone()}
}

func (b *Builder) Upsert(id string, vectors map[string][]float32, attrs map[string]*cloudyneighpb.AttributeValue) error {
	return b.table.Upsert(id, vectors, attrs)
}

func (b *Builder) UpsertRecord(rec *cloudyneighpb.Record) error {
	return b.table.UpsertRecord(rec)
}

func (b *Builder) Delete(id string) bool {
	return b.table.Delete(id)
}

func (b *Builder) Build() *Table {
	return b.table
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
