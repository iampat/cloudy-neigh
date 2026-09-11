package query

import (
	"errors"
	"fmt"
	"slices"
	"sync"

	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	"google.golang.org/protobuf/proto"
)

type rowLoc struct {
	chunk int
	row   int
}

type chunk struct {
	docIDs     []string
	tombstones []uint64
	vectors    map[string][]float32
	vectorMask map[string][]uint64
	attrs      map[string][]*cloudyneighpb.AttributeValue
	attrMask   map[string][]uint64
}

func newChunk(size int, vectorDims map[string]int) *chunk {
	words := (size + 63) / 64
	c := &chunk{
		docIDs:     make([]string, 0, size),
		tombstones: make([]uint64, words),
		vectors:    make(map[string][]float32, len(vectorDims)),
		vectorMask: make(map[string][]uint64, len(vectorDims)),
		attrs:      make(map[string][]*cloudyneighpb.AttributeValue),
		attrMask:   make(map[string][]uint64),
	}
	for col, dim := range vectorDims {
		c.vectors[col] = make([]float32, size*dim)
		c.vectorMask[col] = make([]uint64, words)
	}
	return c
}

func (c *chunk) dead(row int) bool {
	return c.tombstones[row/64]&(uint64(1)<<(row%64)) != 0
}

func (c *chunk) setVector(col string, row, dim int, vec []float32) {
	copy(c.vectors[col][row*dim:(row+1)*dim], vec)
	c.vectorMask[col][row/64] |= (uint64(1) << (row % 64))
}

func (c *chunk) setAttribute(col string, row int, val *cloudyneighpb.AttributeValue, chunkSize int) {
	s, ok := c.attrs[col]
	if !ok {
		s = make([]*cloudyneighpb.AttributeValue, chunkSize)
		c.attrs[col] = s
		c.attrMask[col] = make([]uint64, (chunkSize+63)/64)
	}
	if val != nil {
		val = proto.Clone(val).(*cloudyneighpb.AttributeValue)
	}
	s[row] = val
	c.attrMask[col][row/64] |= (uint64(1) << (row % 64))
}

type Table struct {
	mu         sync.RWMutex
	chunkSize  int
	chunks     []*chunk
	index      map[string]rowLoc
	vectorDims map[string]int
	liveCount  int
}

func NewTable(chunkSize int) *Table {
	if chunkSize <= 0 {
		chunkSize = 65536
	}
	return &Table{
		chunkSize:  chunkSize,
		index:      make(map[string]rowLoc),
		vectorDims: make(map[string]int),
	}
}

func (t *Table) ensureVectorColLocked(col string, dim int) {
	words := (t.chunkSize + 63) / 64
	for _, c := range t.chunks {
		if c.vectors[col] == nil {
			c.vectors[col] = make([]float32, t.chunkSize*dim)
			c.vectorMask[col] = make([]uint64, words)
		}
	}
}

func (t *Table) Upsert(id string, vectors map[string][]float32, attrs map[string]*cloudyneighpb.AttributeValue) error {
	if id == "" {
		return errors.New("query: empty doc id")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

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
			t.ensureVectorColLocked(col, len(vec))
		}
	}

	loc, exists := t.index[id]
	if exists {
		c := t.chunks[loc.chunk]
		word := loc.row / 64
		bit := uint64(1) << (loc.row % 64)
		if c.dead(loc.row) {
			c.tombstones[word] &^= bit
			t.liveCount++
		}

		for col, vec := range vectors {
			if len(vec) == 0 {
				continue
			}
			dim := t.vectorDims[col]
			c.setVector(col, loc.row, dim, vec)
		}

		for k, v := range attrs {
			c.setAttribute(k, loc.row, v, t.chunkSize)
		}
		return nil
	}

	if len(t.chunks) == 0 || len(t.chunks[len(t.chunks)-1].docIDs) >= t.chunkSize {
		t.chunks = append(t.chunks, newChunk(t.chunkSize, t.vectorDims))
	}

	chunkIdx := len(t.chunks) - 1
	c := t.chunks[chunkIdx]
	row := len(c.docIDs)
	c.docIDs = append(c.docIDs, id)
	t.index[id] = rowLoc{chunk: chunkIdx, row: row}
	t.liveCount++

	for col, vec := range vectors {
		if len(vec) == 0 {
			continue
		}
		dim := t.vectorDims[col]
		c.setVector(col, row, dim, vec)
	}

	for k, v := range attrs {
		c.setAttribute(k, row, v, t.chunkSize)
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

	loc, ok := t.index[id]
	if !ok {
		return false
	}
	c := t.chunks[loc.chunk]
	if c.dead(loc.row) {
		return false
	}
	c.tombstones[loc.row/64] |= uint64(1) << (loc.row % 64)
	t.liveCount--
	return true
}

func (t *Table) Get(id string) (*cloudyneighpb.Record, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	loc, ok := t.index[id]
	if !ok {
		return nil, false
	}
	c := t.chunks[loc.chunk]
	if c.dead(loc.row) {
		return nil, false
	}

	word := loc.row / 64
	bit := uint64(1) << (loc.row % 64)

	rec := &cloudyneighpb.Record{
		Id:         id,
		Vectors:    make(map[string]*cloudyneighpb.Vector),
		Attributes: make(map[string]*cloudyneighpb.AttributeValue),
	}

	for col, mask := range c.vectorMask {
		if mask[word]&bit != 0 {
			dim := t.vectorDims[col]
			offset := loc.row * dim
			rec.Vectors[col] = &cloudyneighpb.Vector{
				Values: slices.Clone(c.vectors[col][offset : offset+dim]),
			}
		}
	}

	for col, mask := range c.attrMask {
		if mask[word]&bit != 0 {
			val := c.attrs[col][loc.row]
			if val != nil {
				val = proto.Clone(val).(*cloudyneighpb.AttributeValue)
			}
			rec.Attributes[col] = val
		}
	}

	return rec, true
}

func (t *Table) Vector(id, col string) ([]float32, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	loc, ok := t.index[id]
	if !ok {
		return nil, false
	}
	c := t.chunks[loc.chunk]
	if c.dead(loc.row) {
		return nil, false
	}

	word := loc.row / 64
	bit := uint64(1) << (loc.row % 64)

	mask, ok := c.vectorMask[col]
	if !ok || mask[word]&bit == 0 {
		return nil, false
	}

	dim := t.vectorDims[col]
	offset := loc.row * dim
	return slices.Clone(c.vectors[col][offset : offset+dim]), true
}

func (t *Table) Attribute(id, key string) (*cloudyneighpb.AttributeValue, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	loc, ok := t.index[id]
	if !ok {
		return nil, false
	}
	c := t.chunks[loc.chunk]
	if c.dead(loc.row) {
		return nil, false
	}

	word := loc.row / 64
	bit := uint64(1) << (loc.row % 64)

	mask, ok := c.attrMask[key]
	if !ok || mask[word]&bit == 0 {
		return nil, false
	}

	val := c.attrs[key][loc.row]
	if val != nil {
		val = proto.Clone(val).(*cloudyneighpb.AttributeValue)
	}
	return val, true
}

func (t *Table) Len() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.liveCount
}
