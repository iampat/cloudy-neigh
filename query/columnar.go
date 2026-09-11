package query

import (
	"errors"
	"fmt"
	"slices"
	"sync"

	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	"google.golang.org/protobuf/proto"
)

type Table struct {
	mu         sync.RWMutex
	docIDs     []string
	index      map[string]int // docID -> row
	tombstones []bool
	vectors    map[string][]float32 // col -> contiguous flat slice (row * dim)
	vectorDims map[string]int
	attrs      map[string][]*cloudyneighpb.AttributeValue
}

func NewTable() *Table {
	return &Table{
		index:      make(map[string]int),
		vectors:    make(map[string][]float32),
		vectorDims: make(map[string]int),
		attrs:      make(map[string][]*cloudyneighpb.AttributeValue),
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
		t.vectors = make(map[string][]float32)
		t.vectorDims = make(map[string]int)
		t.attrs = make(map[string][]*cloudyneighpb.AttributeValue)
	}

	for col, vec := range vectors {
		if len(vec) == 0 {
			continue
		}
		dim, ok := t.vectorDims[col]
		if ok && len(vec) != dim {
			return fmt.Errorf("query: vector column %q dimension mismatch: got %d, want %d", col, len(vec), dim)
		}
	}

	for col, vec := range vectors {
		if len(vec) == 0 {
			continue
		}
		if _, ok := t.vectorDims[col]; !ok {
			t.vectorDims[col] = len(vec)
			t.vectors[col] = make([]float32, len(t.docIDs)*len(vec))
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
			dim := t.vectorDims[col]
			offset := row * dim
			copy(t.vectors[col][offset:offset+dim], vec)
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

	for col, dim := range t.vectorDims {
		if vec, ok := vectors[col]; ok && len(vec) > 0 {
			t.vectors[col] = append(t.vectors[col], vec...)
		} else {
			t.vectors[col] = append(t.vectors[col], make([]float32, dim)...)
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

func (t *Table) Get(id string) (*cloudyneighpb.Record, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	row, ok := t.index[id]
	if !ok || t.tombstones[row] {
		return nil, false
	}

	rec := &cloudyneighpb.Record{
		Id:         id,
		Vectors:    make(map[string]*cloudyneighpb.Vector, len(t.vectorDims)),
		Attributes: make(map[string]*cloudyneighpb.AttributeValue, len(t.attrs)),
	}

	for col, dim := range t.vectorDims {
		offset := row * dim
		rec.Vectors[col] = &cloudyneighpb.Vector{
			Values: slices.Clone(t.vectors[col][offset : offset+dim]),
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
	dim, ok := t.vectorDims[col]
	if !ok {
		return nil, false
	}
	offset := row * dim
	return slices.Clone(t.vectors[col][offset : offset+dim]), true
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
