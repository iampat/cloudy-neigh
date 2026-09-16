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

const (
	chunkSize  = 1024
	chunkMask  = chunkSize - 1
	chunkShift = 10
)

func chunkIndex(i int) (int, int) {
	return i >> chunkShift, i & chunkMask
}

type packedVectorCol struct {
	dim      int
	numVecs  int
	chunks   [][]float32
	vecToRow [][]int
	rowToVec [][]int
}

type Table struct {
	mu         sync.RWMutex
	numRows    int
	docIDs     [][]string
	index      []map[string]int
	tombstones [][]bool
	vectors    map[string]*packedVectorCol
	attrs      map[string][][]*cloudyneighpb.AttributeValue
}

var _ QueryExecutor = (*Table)(nil)

type builderVectorCol struct {
	dim         int
	numVecs     int
	chunks      [][]float32
	chunkShared []bool
	vecToRow    [][]int
	vecShared   []bool
	rowToVec    [][]int
	rowShared   []bool
}

type builderAttrCol struct {
	chunks      [][]*cloudyneighpb.AttributeValue
	chunkShared []bool
}

type Builder struct {
	numRows    int
	docIDs     [][]string
	docShared  []bool
	tombstones [][]bool
	tombShared []bool
	baseIndex  []map[string]int
	deltaIndex map[string]int
	vectors    map[string]*builderVectorCol
	attrs      map[string]*builderAttrCol
}

func newBuilderVectorCol(dim, numRows int) *builderVectorCol {
	numChunks := (numRows + chunkSize - 1) / chunkSize
	rowToVec := make([][]int, numChunks)
	rowShared := make([]bool, numChunks)
	for c := 0; c < numChunks; c++ {
		count := chunkSize
		if (c+1)*chunkSize > numRows {
			count = numRows - c*chunkSize
		}
		chunk := make([]int, count, chunkSize)
		for i := range chunk {
			chunk[i] = -1
		}
		rowToVec[c] = chunk
	}
	return &builderVectorCol{
		dim:       dim,
		rowToVec:  rowToVec,
		rowShared: rowShared,
	}
}

func newBuilderAttrCol(numRows int) *builderAttrCol {
	numChunks := (numRows + chunkSize - 1) / chunkSize
	chunks := make([][]*cloudyneighpb.AttributeValue, numChunks)
	chunkShared := make([]bool, numChunks)
	for c := 0; c < numChunks; c++ {
		count := chunkSize
		if (c+1)*chunkSize > numRows {
			count = numRows - c*chunkSize
		}
		chunks[c] = make([]*cloudyneighpb.AttributeValue, count, chunkSize)
	}
	return &builderAttrCol{
		chunks:      chunks,
		chunkShared: chunkShared,
	}
}

func NewBuilder() *Builder {
	return &Builder{
		deltaIndex: make(map[string]int),
		vectors:    make(map[string]*builderVectorCol),
		attrs:      make(map[string]*builderAttrCol),
	}
}

func (t *Table) Builder() *Builder {
	if t == nil {
		return NewBuilder()
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.builderLocked()
}

func (t *Table) builderLocked() *Builder {
	docIDs := slices.Clone(t.docIDs)
	docShared := make([]bool, len(docIDs))
	for i := range docShared {
		docShared[i] = true
	}

	tombstones := slices.Clone(t.tombstones)
	tombShared := make([]bool, len(tombstones))
	for i := range tombShared {
		tombShared[i] = true
	}

	vectors := make(map[string]*builderVectorCol, len(t.vectors))
	for col, vCol := range t.vectors {
		chunks := slices.Clone(vCol.chunks)
		chunkShared := make([]bool, len(chunks))
		for i := range chunkShared {
			chunkShared[i] = true
		}

		vecToRow := slices.Clone(vCol.vecToRow)
		vecShared := make([]bool, len(vecToRow))
		for i := range vecShared {
			vecShared[i] = true
		}

		rowToVec := slices.Clone(vCol.rowToVec)
		rowShared := make([]bool, len(rowToVec))
		for i := range rowShared {
			rowShared[i] = true
		}

		vectors[col] = &builderVectorCol{
			dim:         vCol.dim,
			numVecs:     vCol.numVecs,
			chunks:      chunks,
			chunkShared: chunkShared,
			vecToRow:    vecToRow,
			vecShared:   vecShared,
			rowToVec:    rowToVec,
			rowShared:   rowShared,
		}
	}

	attrs := make(map[string]*builderAttrCol, len(t.attrs))
	for k, col := range t.attrs {
		chunks := slices.Clone(col)
		chunkShared := make([]bool, len(chunks))
		for i := range chunkShared {
			chunkShared[i] = true
		}
		attrs[k] = &builderAttrCol{
			chunks:      chunks,
			chunkShared: chunkShared,
		}
	}

	return &Builder{
		numRows:    t.numRows,
		docIDs:     docIDs,
		docShared:  docShared,
		tombstones: tombstones,
		tombShared: tombShared,
		baseIndex:  t.index,
		deltaIndex: make(map[string]int),
		vectors:    vectors,
		attrs:      attrs,
	}
}

func (t *Table) Upsert(id string, vectors map[string][]float32, attrs map[string]*cloudyneighpb.AttributeValue) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	b := t.builderLocked()
	if err := b.Upsert(id, vectors, attrs); err != nil {
		return err
	}
	next := b.Build()
	t.numRows = next.numRows
	t.docIDs = next.docIDs
	t.index = next.index
	t.tombstones = next.tombstones
	t.vectors = next.vectors
	t.attrs = next.attrs
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
	b := t.builderLocked()
	if !b.Delete(id) {
		return false
	}
	next := b.Build()
	t.numRows = next.numRows
	t.docIDs = next.docIDs
	t.index = next.index
	t.tombstones = next.tombstones
	t.vectors = next.vectors
	t.attrs = next.attrs
	return true
}

func (b *Builder) rowOf(id string) (int, bool) {
	if row, ok := b.deltaIndex[id]; ok {
		return row, true
	}
	for i := len(b.baseIndex) - 1; i >= 0; i-- {
		if row, ok := b.baseIndex[i][id]; ok {
			return row, true
		}
	}
	return -1, false
}

func (b *Builder) Upsert(id string, vectors map[string][]float32, attrs map[string]*cloudyneighpb.AttributeValue) error {
	if id == "" {
		return errors.New("query: empty doc id")
	}

	if b.deltaIndex == nil {
		b.deltaIndex = make(map[string]int)
		b.vectors = make(map[string]*builderVectorCol)
		b.attrs = make(map[string]*builderAttrCol)
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
			b.vectors[col] = newBuilderVectorCol(len(vec), b.numRows)
		}
	}

	for k, v := range attrs {
		if v == nil {
			continue
		}
		if _, ok := b.attrs[k]; !ok {
			b.attrs[k] = newBuilderAttrCol(b.numRows)
		}
	}

	if row, exists := b.rowOf(id); exists {
		rc, rs := chunkIndex(row)
		wasDeleted := b.tombstones[rc][rs]
		if wasDeleted {
			if b.tombShared[rc] {
				b.tombstones[rc] = slices.Clone(b.tombstones[rc])
				b.tombShared[rc] = false
			}
			b.tombstones[rc][rs] = false

			for _, aCol := range b.attrs {
				if rc < len(aCol.chunks) && rs < len(aCol.chunks[rc]) && aCol.chunks[rc][rs] != nil {
					if aCol.chunkShared[rc] {
						aCol.chunks[rc] = slices.Clone(aCol.chunks[rc])
						aCol.chunkShared[rc] = false
					}
					aCol.chunks[rc][rs] = nil
				}
			}
			for col, vCol := range b.vectors {
				if vec, ok := vectors[col]; !ok || len(vec) == 0 {
					if rc < len(vCol.rowToVec) && rs < len(vCol.rowToVec[rc]) && vCol.rowToVec[rc][rs] >= 0 {
						if vCol.rowShared[rc] {
							vCol.rowToVec[rc] = slices.Clone(vCol.rowToVec[rc])
							vCol.rowShared[rc] = false
						}
						vCol.rowToVec[rc][rs] = -1
					}
				}
			}
		}

		for col, vec := range vectors {
			if len(vec) == 0 {
				continue
			}
			vCol := b.vectors[col]
			vecIdx := vCol.rowToVec[rc][rs]
			if vecIdx >= 0 {
				vc, vs := chunkIndex(vecIdx)
				if vCol.chunkShared[vc] {
					vCol.chunks[vc] = slices.Clone(vCol.chunks[vc])
					vCol.chunkShared[vc] = false
				}
				offset := vs * vCol.dim
				copy(vCol.chunks[vc][offset:offset+vCol.dim], vec)
			} else {
				vecIdx = vCol.numVecs
				vc, _ := chunkIndex(vecIdx)
				if vc == len(vCol.chunks) {
					vCol.chunks = append(vCol.chunks, make([]float32, 0, chunkSize*vCol.dim))
					vCol.chunkShared = append(vCol.chunkShared, false)
					vCol.vecToRow = append(vCol.vecToRow, make([]int, 0, chunkSize))
					vCol.vecShared = append(vCol.vecShared, false)
				} else {
					if vCol.chunkShared[vc] {
						vCol.chunks[vc] = slices.Clone(vCol.chunks[vc])
						vCol.chunkShared[vc] = false
					}
					if vCol.vecShared[vc] {
						vCol.vecToRow[vc] = slices.Clone(vCol.vecToRow[vc])
						vCol.vecShared[vc] = false
					}
				}
				vCol.chunks[vc] = append(vCol.chunks[vc], vec...)
				vCol.vecToRow[vc] = append(vCol.vecToRow[vc], row)
				vCol.numVecs++

				if vCol.rowShared[rc] {
					vCol.rowToVec[rc] = slices.Clone(vCol.rowToVec[rc])
					vCol.rowShared[rc] = false
				}
				vCol.rowToVec[rc][rs] = vecIdx
			}
		}

		for k, v := range attrs {
			if v == nil {
				continue
			}
			aCol := b.attrs[k]
			if aCol.chunkShared[rc] {
				aCol.chunks[rc] = slices.Clone(aCol.chunks[rc])
				aCol.chunkShared[rc] = false
			}
			aCol.chunks[rc][rs] = proto.Clone(v).(*cloudyneighpb.AttributeValue)
		}
		return nil
	}

	row := b.numRows
	rc, _ := chunkIndex(row)

	if rc == len(b.docIDs) {
		b.docIDs = append(b.docIDs, make([]string, 0, chunkSize))
		b.docShared = append(b.docShared, false)
	} else if b.docShared[rc] {
		b.docIDs[rc] = slices.Clone(b.docIDs[rc])
		b.docShared[rc] = false
	}
	b.docIDs[rc] = append(b.docIDs[rc], id)

	if rc == len(b.tombstones) {
		b.tombstones = append(b.tombstones, make([]bool, 0, chunkSize))
		b.tombShared = append(b.tombShared, false)
	} else if b.tombShared[rc] {
		b.tombstones[rc] = slices.Clone(b.tombstones[rc])
		b.tombShared[rc] = false
	}
	b.tombstones[rc] = append(b.tombstones[rc], false)

	b.deltaIndex[id] = row

	for col, vCol := range b.vectors {
		if rc == len(vCol.rowToVec) {
			vCol.rowToVec = append(vCol.rowToVec, make([]int, 0, chunkSize))
			vCol.rowShared = append(vCol.rowShared, false)
		} else if vCol.rowShared[rc] {
			vCol.rowToVec[rc] = slices.Clone(vCol.rowToVec[rc])
			vCol.rowShared[rc] = false
		}

		if vec, ok := vectors[col]; ok && len(vec) > 0 {
			vecIdx := vCol.numVecs
			vc, _ := chunkIndex(vecIdx)
			if vc == len(vCol.chunks) {
				vCol.chunks = append(vCol.chunks, make([]float32, 0, chunkSize*vCol.dim))
				vCol.chunkShared = append(vCol.chunkShared, false)
				vCol.vecToRow = append(vCol.vecToRow, make([]int, 0, chunkSize))
				vCol.vecShared = append(vCol.vecShared, false)
			} else {
				if vCol.chunkShared[vc] {
					vCol.chunks[vc] = slices.Clone(vCol.chunks[vc])
					vCol.chunkShared[vc] = false
				}
				if vCol.vecShared[vc] {
					vCol.vecToRow[vc] = slices.Clone(vCol.vecToRow[vc])
					vCol.vecShared[vc] = false
				}
			}
			vCol.chunks[vc] = append(vCol.chunks[vc], vec...)
			vCol.vecToRow[vc] = append(vCol.vecToRow[vc], row)
			vCol.numVecs++
			vCol.rowToVec[rc] = append(vCol.rowToVec[rc], vecIdx)
		} else {
			vCol.rowToVec[rc] = append(vCol.rowToVec[rc], -1)
		}
	}

	for k, aCol := range b.attrs {
		if rc == len(aCol.chunks) {
			aCol.chunks = append(aCol.chunks, make([]*cloudyneighpb.AttributeValue, 0, chunkSize))
			aCol.chunkShared = append(aCol.chunkShared, false)
		} else if aCol.chunkShared[rc] {
			aCol.chunks[rc] = slices.Clone(aCol.chunks[rc])
			aCol.chunkShared[rc] = false
		}
		if v, ok := attrs[k]; ok && v != nil {
			aCol.chunks[rc] = append(aCol.chunks[rc], proto.Clone(v).(*cloudyneighpb.AttributeValue))
		} else {
			aCol.chunks[rc] = append(aCol.chunks[rc], nil)
		}
	}

	b.numRows++
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
	row, ok := b.rowOf(id)
	if !ok {
		return false
	}
	rc, rs := chunkIndex(row)
	if b.tombstones[rc][rs] {
		return false
	}
	if b.tombShared[rc] {
		b.tombstones[rc] = slices.Clone(b.tombstones[rc])
		b.tombShared[rc] = false
	}
	b.tombstones[rc][rs] = true
	return true
}

func (b *Builder) Build() *Table {
	var index []map[string]int
	if len(b.deltaIndex) > 0 {
		if len(b.baseIndex) >= 16 {
			total := len(b.deltaIndex)
			for _, m := range b.baseIndex {
				total += len(m)
			}
			merged := make(map[string]int, total)
			for _, m := range b.baseIndex {
				for k, v := range m {
					merged[k] = v
				}
			}
			for k, v := range b.deltaIndex {
				merged[k] = v
			}
			index = []map[string]int{merged}
		} else {
			index = make([]map[string]int, len(b.baseIndex)+1)
			copy(index, b.baseIndex)
			index[len(b.baseIndex)] = b.deltaIndex
		}
	} else {
		index = b.baseIndex
	}

	vectors := make(map[string]*packedVectorCol, len(b.vectors))
	for col, bCol := range b.vectors {
		vectors[col] = &packedVectorCol{
			dim:      bCol.dim,
			numVecs:  bCol.numVecs,
			chunks:   bCol.chunks,
			vecToRow: bCol.vecToRow,
			rowToVec: bCol.rowToVec,
		}
	}

	attrs := make(map[string][][]*cloudyneighpb.AttributeValue, len(b.attrs))
	for k, aCol := range b.attrs {
		attrs[k] = aCol.chunks
	}

	t := &Table{
		numRows:    b.numRows,
		docIDs:     b.docIDs,
		index:      index,
		tombstones: b.tombstones,
		vectors:    vectors,
		attrs:      attrs,
	}

	b.docIDs = nil
	b.docShared = nil
	b.tombstones = nil
	b.tombShared = nil
	b.baseIndex = nil
	b.deltaIndex = nil
	b.vectors = nil
	b.attrs = nil
	return t
}

func NewTable() *Table {
	return NewBuilder().Build()
}

func (t *Table) rowOf(id string) (int, bool) {
	for i := len(t.index) - 1; i >= 0; i-- {
		if row, ok := t.index[i][id]; ok {
			return row, true
		}
	}
	return -1, false
}

func (t *Table) recordAt(row int, id string) *cloudyneighpb.Record {
	rec := &cloudyneighpb.Record{
		Id:         id,
		Vectors:    make(map[string]*cloudyneighpb.Vector, len(t.vectors)),
		Attributes: make(map[string]*cloudyneighpb.AttributeValue, len(t.attrs)),
	}

	rc, rs := chunkIndex(row)
	for col, vCol := range t.vectors {
		if rc < len(vCol.rowToVec) && rs < len(vCol.rowToVec[rc]) {
			vecIdx := vCol.rowToVec[rc][rs]
			if vecIdx >= 0 {
				vc, vs := chunkIndex(vecIdx)
				offset := vs * vCol.dim
				rec.Vectors[col] = &cloudyneighpb.Vector{
					Values: slices.Clone(vCol.chunks[vc][offset : offset+vCol.dim]),
				}
			}
		}
	}

	for k, col := range t.attrs {
		if rc < len(col) && rs < len(col[rc]) {
			if val := col[rc][rs]; val != nil {
				rec.Attributes[k] = proto.Clone(val).(*cloudyneighpb.AttributeValue)
			}
		}
	}

	return rec
}

func (t *Table) Get(id string) (*cloudyneighpb.Record, bool) {
	if t == nil {
		return nil, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	row, ok := t.rowOf(id)
	if !ok {
		return nil, false
	}
	rc, rs := chunkIndex(row)
	if t.tombstones[rc][rs] {
		return nil, false
	}
	return t.recordAt(row, id), true
}

func (t *Table) Vector(id, col string) ([]float32, bool) {
	if t == nil {
		return nil, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	row, ok := t.rowOf(id)
	if !ok {
		return nil, false
	}
	rc, rs := chunkIndex(row)
	if t.tombstones[rc][rs] {
		return nil, false
	}
	vCol, ok := t.vectors[col]
	if !ok || rc >= len(vCol.rowToVec) || rs >= len(vCol.rowToVec[rc]) {
		return nil, false
	}
	vecIdx := vCol.rowToVec[rc][rs]
	if vecIdx < 0 {
		return nil, false
	}
	vc, vs := chunkIndex(vecIdx)
	offset := vs * vCol.dim
	return slices.Clone(vCol.chunks[vc][offset : offset+vCol.dim]), true
}

func (t *Table) Attribute(id, key string) (*cloudyneighpb.AttributeValue, bool) {
	if t == nil {
		return nil, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	row, ok := t.rowOf(id)
	if !ok {
		return nil, false
	}
	rc, rs := chunkIndex(row)
	if t.tombstones[rc][rs] {
		return nil, false
	}
	col, ok := t.attrs[key]
	if !ok || rc >= len(col) || rs >= len(col[rc]) {
		return nil, false
	}
	val := col[rc][rs]
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

type SearchStats struct {
	ScanDuration        time.Duration
	MaterializeDuration time.Duration
}

func (t *Table) Search(
	col string,
	query []float32,
	topK int,
	metric cloudyneighpb.DistanceMetric,
	filter *cloudyneighpb.EqualityFilter,
) ([]*cloudyneighpb.ScoredRecord, error) {
	hits, _, err := t.SearchWithStats(col, query, topK, metric, filter)
	return hits, err
}

func (t *Table) SearchWithStats(
	col string,
	query []float32,
	topK int,
	metric cloudyneighpb.DistanceMetric,
	filter *cloudyneighpb.EqualityFilter,
) ([]*cloudyneighpb.ScoredRecord, SearchStats, error) {
	if t == nil || topK <= 0 {
		return nil, SearchStats{}, nil
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	vCol, ok := t.vectors[col]
	if !ok || vCol.numVecs == 0 {
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
	if vCol.numVecs < heapCap {
		heapCap = vCol.numVecs
	}
	h := hitHeap{
		hits: make([]searchHit, 0, heapCap),
		cmp:  cmpFunc,
	}

	scanStart := time.Now()
	for chunkIdx, chunk := range vCol.chunks {
		vecToRowChunk := vCol.vecToRow[chunkIdx]
		for vs, row := range vecToRowChunk {
			rc, rs := chunkIndex(row)
			if t.tombstones[rc][rs] {
				continue
			}
			if rc >= len(vCol.rowToVec) || rs >= len(vCol.rowToVec[rc]) || vCol.rowToVec[rc][rs] != (chunkIdx<<chunkShift)|vs {
				continue
			}
			if filter != nil {
				attrChunks, ok := t.attrs[filter.Field]
				if !ok || rc >= len(attrChunks) || rs >= len(attrChunks[rc]) || attrChunks[rc][rs] == nil || !proto.Equal(attrChunks[rc][rs], filter.Value) {
					continue
				}
			}

			offset := vs * vCol.dim
			storedVec := chunk[offset : offset+vCol.dim]

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
				id:    t.docIDs[rc][rs],
				score: score,
			}

			if h.Len() < topK {
				heap.Push(&h, cand)
			} else if h.cmp(cand, h.hits[0]) < 0 {
				h.hits[0] = cand
				heap.Fix(&h, 0)
			}
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
