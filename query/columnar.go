package query

import (
	"errors"
	"fmt"
	"slices"
	"sync"

	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
)

const (
	DefaultChunkSize    = 65536
	DefaultVectorColumn = "vector"
)

type RowLoc struct {
	Chunk int
	Row   int
}

type Record struct {
	ID         string
	Vectors    map[string][]float32
	Attributes map[string]string
}

type chunk struct {
	docIDs     []string
	tombstones []uint64
	vectors    map[string][]float32
	vectorMask map[string][]uint64
	attrs      map[string][]string
	attrMask   map[string][]uint64
}

func newChunk(size int, vectorDims map[string]int) *chunk {
	words := (size + 63) / 64
	c := &chunk{
		docIDs:     make([]string, 0, size),
		tombstones: make([]uint64, words),
		vectors:    make(map[string][]float32, len(vectorDims)),
		vectorMask: make(map[string][]uint64, len(vectorDims)),
		attrs:      make(map[string][]string),
		attrMask:   make(map[string][]uint64),
	}
	for col, dim := range vectorDims {
		c.vectors[col] = make([]float32, size*dim)
		c.vectorMask[col] = make([]uint64, words)
	}
	return c
}

func (c *chunk) setVector(col string, row, dim int, vec []float32) {
	copy(c.vectors[col][row*dim:(row+1)*dim], vec)
	c.vectorMask[col][row/64] |= (uint64(1) << (row % 64))
}

func (c *chunk) setAttribute(col string, row int, val string, chunkSize int) {
	s, ok := c.attrs[col]
	if !ok {
		s = make([]string, chunkSize)
		c.attrs[col] = s
		c.attrMask[col] = make([]uint64, (chunkSize+63)/64)
	}
	s[row] = val
	c.attrMask[col][row/64] |= (uint64(1) << (row % 64))
}

type Table struct {
	mu         sync.RWMutex
	chunkSize  int
	chunks     []*chunk
	index      map[string]RowLoc
	vectorDims map[string]int
	liveCount  int
}

func NewTable(chunkSize ...int) *Table {
	size := DefaultChunkSize
	if len(chunkSize) > 0 && chunkSize[0] > 0 {
		size = chunkSize[0]
	}
	return &Table{
		chunkSize:  size,
		index:      make(map[string]RowLoc),
		vectorDims: make(map[string]int),
	}
}

func (t *Table) initLocked() {
	if t.chunkSize <= 0 {
		t.chunkSize = DefaultChunkSize
	}
	if t.index == nil {
		t.index = make(map[string]RowLoc)
	}
	if t.vectorDims == nil {
		t.vectorDims = make(map[string]int)
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

func (t *Table) Upsert(id string, vectors map[string][]float32, attrs map[string]string) error {
	if id == "" {
		return errors.New("query: empty doc id")
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.initLocked()

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
		c := t.chunks[loc.Chunk]
		word := loc.Row / 64
		bit := uint64(1) << (loc.Row % 64)
		if c.tombstones[word]&bit != 0 {
			c.tombstones[word] &^= bit
			t.liveCount++
		}

		for col, vec := range vectors {
			if len(vec) == 0 {
				continue
			}
			dim := t.vectorDims[col]
			c.setVector(col, loc.Row, dim, vec)
		}

		for k, v := range attrs {
			c.setAttribute(k, loc.Row, v, t.chunkSize)
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
	t.index[id] = RowLoc{Chunk: chunkIdx, Row: row}
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

func (t *Table) UpsertDoc(doc *cloudyneighpb.Document) error {
	if doc == nil {
		return errors.New("query: nil document")
	}
	var vectors map[string][]float32
	if len(doc.Vector) > 0 {
		vectors = map[string][]float32{DefaultVectorColumn: doc.Vector}
	}
	return t.Upsert(doc.Id, vectors, doc.Attributes)
}

func (t *Table) Delete(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.initLocked()

	loc, ok := t.index[id]
	if !ok {
		return false
	}
	c := t.chunks[loc.Chunk]
	word := loc.Row / 64
	bit := uint64(1) << (loc.Row % 64)
	if c.tombstones[word]&bit != 0 {
		return false
	}
	c.tombstones[word] |= bit
	t.liveCount--
	return true
}

func (t *Table) Get(id string) (Record, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	loc, ok := t.index[id]
	if !ok {
		return Record{}, false
	}
	c := t.chunks[loc.Chunk]
	word := loc.Row / 64
	bit := uint64(1) << (loc.Row % 64)
	if c.tombstones[word]&bit != 0 {
		return Record{}, false
	}

	rec := Record{
		ID:         id,
		Vectors:    make(map[string][]float32),
		Attributes: make(map[string]string),
	}

	for col, mask := range c.vectorMask {
		if mask[word]&bit != 0 {
			dim := t.vectorDims[col]
			offset := loc.Row * dim
			rec.Vectors[col] = slices.Clone(c.vectors[col][offset : offset+dim])
		}
	}

	for col, mask := range c.attrMask {
		if mask[word]&bit != 0 {
			rec.Attributes[col] = c.attrs[col][loc.Row]
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
	c := t.chunks[loc.Chunk]
	word := loc.Row / 64
	bit := uint64(1) << (loc.Row % 64)
	if c.tombstones[word]&bit != 0 {
		return nil, false
	}

	mask, ok := c.vectorMask[col]
	if !ok || mask[word]&bit == 0 {
		return nil, false
	}

	dim := t.vectorDims[col]
	offset := loc.Row * dim
	return slices.Clone(c.vectors[col][offset : offset+dim]), true
}

func (t *Table) Attribute(id, key string) (string, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	loc, ok := t.index[id]
	if !ok {
		return "", false
	}
	c := t.chunks[loc.Chunk]
	word := loc.Row / 64
	bit := uint64(1) << (loc.Row % 64)
	if c.tombstones[word]&bit != 0 {
		return "", false
	}

	mask, ok := c.attrMask[key]
	if !ok || mask[word]&bit == 0 {
		return "", false
	}

	return c.attrs[key][loc.Row], true
}

func (t *Table) Loc(id string) (RowLoc, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	loc, ok := t.index[id]
	if !ok {
		return RowLoc{}, false
	}
	c := t.chunks[loc.Chunk]
	word := loc.Row / 64
	bit := uint64(1) << (loc.Row % 64)
	if c.tombstones[word]&bit != 0 {
		return RowLoc{}, false
	}
	return loc, true
}

func (t *Table) IsTombstoned(loc RowLoc) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if loc.Chunk < 0 || loc.Chunk >= len(t.chunks) {
		return true
	}
	c := t.chunks[loc.Chunk]
	if loc.Row < 0 || loc.Row >= len(c.docIDs) {
		return true
	}
	word := loc.Row / 64
	bit := uint64(1) << (loc.Row % 64)
	return c.tombstones[word]&bit != 0
}

func (t *Table) Len() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.liveCount
}

func (t *Table) TotalRows() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	total := 0
	for _, c := range t.chunks {
		total += len(c.docIDs)
	}
	return total
}

func (t *Table) ChunkCount() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.chunks)
}
