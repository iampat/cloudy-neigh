package query

import (
	"container/heap"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	"github.com/iampat/cloudy-neigh/query/distance"
	"google.golang.org/protobuf/proto"
)

type flatVectorCol struct {
	dim    int
	data   []float32
	hasVec []bool
}

type FlatTable struct {
	mu         sync.Mutex
	numRows    int
	docIDs     []string
	index      map[string]int
	tombstones []bool
	vectors    map[string]*flatVectorCol
	attrs      map[string][]*cloudyneighpb.AttributeValue
}

var _ QueryExecutor = (*FlatTable)(nil)

func NewFlatTable() *FlatTable {
	return &FlatTable{
		index:   make(map[string]int),
		vectors: make(map[string]*flatVectorCol),
		attrs:   make(map[string][]*cloudyneighpb.AttributeValue),
	}
}

func NewFlatTableWithCapacity(capacity, dim int) *FlatTable {
	t := &FlatTable{
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

func (t *FlatTable) Reserve(capacity int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if capacity <= t.numRows {
		return
	}
	if cap(t.docIDs) < capacity {
		newDocIDs := make([]string, len(t.docIDs), capacity)
		copy(newDocIDs, t.docIDs)
		t.docIDs = newDocIDs
	}
	if cap(t.tombstones) < capacity {
		newTombs := make([]bool, len(t.tombstones), capacity)
		copy(newTombs, t.tombstones)
		t.tombstones = newTombs
	}
	if t.index == nil {
		t.index = make(map[string]int, capacity)
	}
	for _, vCol := range t.vectors {
		targetCap := capacity * vCol.dim
		if cap(vCol.data) < targetCap {
			newData := make([]float32, len(vCol.data), targetCap)
			copy(newData, vCol.data)
			vCol.data = newData
		}
		if cap(vCol.hasVec) < capacity {
			newHasVec := make([]bool, len(vCol.hasVec), capacity)
			copy(newHasVec, vCol.hasVec)
			vCol.hasVec = newHasVec
		}
	}
}

func (t *FlatTable) Upsert(id string, vectors map[string][]float32, attrs map[string]*cloudyneighpb.AttributeValue) error {
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

	if row, exists := t.index[id]; exists {
		wasDeleted := t.tombstones[row]
		if wasDeleted {
			t.tombstones[row] = false
			for _, aCol := range t.attrs {
				if row < len(aCol) {
					aCol[row] = nil
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

func (t *FlatTable) Delete(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	row, ok := t.index[id]
	if !ok || t.tombstones[row] {
		return false
	}
	t.tombstones[row] = true
	return true
}

func (t *FlatTable) recordAtLocked(row int, id string) *cloudyneighpb.Record {
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

func (t *FlatTable) Get(id string) (*cloudyneighpb.Record, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	row, ok := t.index[id]
	if !ok || t.tombstones[row] {
		return nil, false
	}
	return t.recordAtLocked(row, id), true
}

func (t *FlatTable) Search(
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
	default:
		return nil, SearchStats{}, fmt.Errorf("query: unknown metric %v", metric)
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
		if filter != nil {
			attrs, ok := t.attrs[filter.Field]
			if !ok || row >= len(attrs) || attrs[row] == nil || !proto.Equal(attrs[row], filter.Value) {
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
