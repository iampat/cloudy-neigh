package query

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	"github.com/iampat/cloudy-neigh/query/distance"
	"google.golang.org/protobuf/proto"
)

var ErrDimensionMismatch = errors.New("query: vector dimension mismatch")

type Metric int

const (
	MetricCosine Metric = iota
	MetricL2Squared
	MetricDotProduct
)

type MutationOp int

const (
	OpUpsert MutationOp = iota
	OpDelete
)

type Mutation struct {
	Op     MutationOp
	Record *cloudyneighpb.Record
	ID     string
}

type packedVectorCol struct {
	dim      int
	data     []float32
	rowToVec []int
	vecToRow []int
}

type Table struct {
	mu         sync.RWMutex
	docIDs     []string
	index      map[string]int
	tombstones []bool
	vectors    map[string]*packedVectorCol
	attrs      map[string][]*cloudyneighpb.AttributeValue
}

func NewTable() *Table {
	return &Table{
		index:   make(map[string]int),
		vectors: make(map[string]*packedVectorCol),
		attrs:   make(map[string][]*cloudyneighpb.AttributeValue),
	}
}

func (t *Table) upsertLocked(id string, vectors map[string][]float32, attrs map[string]*cloudyneighpb.AttributeValue) error {
	if id == "" {
		return errors.New("query: empty doc id")
	}

	if t.index == nil {
		t.index = make(map[string]int)
		t.vectors = make(map[string]*packedVectorCol)
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
			vCol := &packedVectorCol{
				dim:      len(vec),
				rowToVec: make([]int, len(t.docIDs)),
			}
			for i := range vCol.rowToVec {
				vCol.rowToVec[i] = -1
			}
			t.vectors[col] = vCol
		}
	}

	for k, v := range attrs {
		if v == nil {
			continue
		}
		if _, ok := t.attrs[k]; !ok {
			t.attrs[k] = make([]*cloudyneighpb.AttributeValue, len(t.docIDs))
		}
	}

	if row, exists := t.index[id]; exists {
		if t.tombstones[row] {
			t.tombstones[row] = false
		}

		for col, vec := range vectors {
			if len(vec) == 0 {
				continue
			}
			vCol := t.vectors[col]
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
			t.attrs[k][row] = proto.Clone(v).(*cloudyneighpb.AttributeValue)
		}
		return nil
	}

	row := len(t.docIDs)
	t.docIDs = append(t.docIDs, id)
	t.tombstones = append(t.tombstones, false)
	t.index[id] = row

	for col, vCol := range t.vectors {
		if vec, ok := vectors[col]; ok && len(vec) > 0 {
			vecIdx := len(vCol.vecToRow)
			vCol.data = append(vCol.data, vec...)
			vCol.vecToRow = append(vCol.vecToRow, row)
			vCol.rowToVec = append(vCol.rowToVec, vecIdx)
		} else {
			vCol.rowToVec = append(vCol.rowToVec, -1)
		}
	}

	for k, col := range t.attrs {
		if v, ok := attrs[k]; ok && v != nil {
			t.attrs[k] = append(col, proto.Clone(v).(*cloudyneighpb.AttributeValue))
		} else {
			t.attrs[k] = append(col, nil)
		}
	}
	return nil
}

func (t *Table) Upsert(id string, vectors map[string][]float32, attrs map[string]*cloudyneighpb.AttributeValue) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.upsertLocked(id, vectors, attrs)
}

func (t *Table) upsertRecordLocked(rec *cloudyneighpb.Record) error {
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
	return t.upsertLocked(rec.Id, vectors, rec.Attributes)
}

func (t *Table) UpsertRecord(rec *cloudyneighpb.Record) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.upsertRecordLocked(rec)
}

func (t *Table) deleteLocked(id string) bool {
	row, ok := t.index[id]
	if !ok || t.tombstones[row] {
		return false
	}
	t.tombstones[row] = true
	return true
}

func (t *Table) Delete(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.deleteLocked(id)
}

func (t *Table) ApplyMutations(mutations []Mutation) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, m := range mutations {
		switch m.Op {
		case OpUpsert:
			if err := t.upsertRecordLocked(m.Record); err != nil {
				return err
			}
		case OpDelete:
			t.deleteLocked(m.ID)
		default:
			return fmt.Errorf("query: unknown mutation op %v", m.Op)
		}
	}
	return nil
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
	t.mu.RLock()
	defer t.mu.RUnlock()

	row, ok := t.index[id]
	if !ok || t.tombstones[row] {
		return nil, false
	}
	return t.recordAt(row, id), true
}

func (t *Table) Vector(id, col string) ([]float32, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

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
	t.mu.RLock()
	defer t.mu.RUnlock()

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

func worseAsc(a, b searchHit) bool {
	if a.score != b.score {
		return a.score > b.score
	}
	return a.id > b.id
}

func worseDesc(a, b searchHit) bool {
	if a.score != b.score {
		return a.score < b.score
	}
	return a.id > b.id
}

func (t *Table) Search(
	col string,
	query []float32,
	topK int,
	metric Metric,
	filter *cloudyneighpb.EqualityFilter,
) ([]*cloudyneighpb.ScoredRecord, error) {
	if topK <= 0 {
		return nil, nil
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	vCol, ok := t.vectors[col]
	if !ok {
		return nil, nil
	}

	if len(query) != vCol.dim {
		return nil, ErrDimensionMismatch
	}

	switch metric {
	case MetricCosine:
		var sum float32
		for _, x := range query {
			sum += x * x
		}
		if sum == 0 {
			return nil, distance.ErrZeroVector
		}
	case MetricL2Squared, MetricDotProduct:
	default:
		return nil, fmt.Errorf("query: unknown metric %v", metric)
	}

	worse := worseAsc
	if metric == MetricDotProduct {
		worse = worseDesc
	}

	heapCap := topK
	if len(vCol.vecToRow) < heapCap {
		heapCap = len(vCol.vecToRow)
	}
	h := make([]searchHit, 0, heapCap)

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
		case MetricCosine:
			var err error
			score, err = distance.Cosine(query, storedVec)
			if err != nil {
				if errors.Is(err, distance.ErrZeroVector) {
					continue
				}
				return nil, err
			}
		case MetricL2Squared:
			var err error
			score, err = distance.L2Squared(query, storedVec)
			if err != nil {
				return nil, err
			}
		case MetricDotProduct:
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

		if len(h) < topK {
			h = append(h, cand)
			i := len(h) - 1
			for i > 0 {
				p := (i - 1) / 2
				if !worse(h[i], h[p]) {
					break
				}
				h[i], h[p] = h[p], h[i]
				i = p
			}
		} else if worse(h[0], cand) {
			h[0] = cand
			i := 0
			for {
				worst := i
				left := 2*i + 1
				right := 2*i + 2
				if left < len(h) && worse(h[left], h[worst]) {
					worst = left
				}
				if right < len(h) && worse(h[right], h[worst]) {
					worst = right
				}
				if worst == i {
					break
				}
				h[i], h[worst] = h[worst], h[i]
				i = worst
			}
		}
	}

	slices.SortFunc(h, func(a, b searchHit) int {
		if a.score != b.score {
			if metric == MetricDotProduct {
				return cmp.Compare(b.score, a.score)
			}
			return cmp.Compare(a.score, b.score)
		}
		return strings.Compare(a.id, b.id)
	})

	hits := make([]*cloudyneighpb.ScoredRecord, len(h))
	for i, hit := range h {
		hits[i] = &cloudyneighpb.ScoredRecord{
			Record: t.recordAt(hit.row, hit.id),
			Score:  hit.score,
		}
	}
	return hits, nil
}
