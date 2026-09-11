package query

import (
	"errors"
	"fmt"
	"slices"
	"sync"

	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	"google.golang.org/protobuf/proto"
)

var ErrDimensionMismatch = errors.New("query: vector dimension mismatch")

type vectorCol struct {
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
	vectors    map[string]*vectorCol
	attrs      map[string][]*cloudyneighpb.AttributeValue
}

func NewTable() *Table {
	return &Table{
		index:   make(map[string]int),
		vectors: make(map[string]*vectorCol),
		attrs:   make(map[string][]*cloudyneighpb.AttributeValue),
	}
}

func (t *Table) Upsert(id string, vectors map[string][]float32, attrs map[string]*cloudyneighpb.AttributeValue) error {
	if id == "" {
		return errors.New("query: empty doc id")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.index == nil {
		t.index = make(map[string]int)
		t.vectors = make(map[string]*vectorCol)
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
			vCol := &vectorCol{
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

func (t *Table) delete(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	row, ok := t.index[id]
	if !ok || t.tombstones[row] {
		return false
	}
	t.tombstones[row] = true
	return true
}

func (t *Table) Get(id string) (*cloudyneighpb.Record, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	row, ok := t.index[id]
	if !ok || t.tombstones[row] {
		return nil, false
	}

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

	return rec, true
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
