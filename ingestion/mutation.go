package ingestion

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/iampat/cloudy-neigh/document"
)

// MutationType represents the operation type (PUT/UPSERT or DELETE).
type MutationType string

const (
	MutationPut    MutationType = "PUT"
	MutationDelete MutationType = "DELETE"
)

// Mutation represents a single ingestion operation envelope.
type Mutation struct {
	Type      MutationType       `json:"type"`
	DocID     string             `json:"doc_id"`
	Document  *document.Document `json:"document,omitempty"`
	Timestamp int64              `json:"timestamp"`
}

// NewPutMutation creates a new PUT/UPSERT mutation.
func NewPutMutation(doc *document.Document) *Mutation {
	docID := ""
	if doc != nil {
		docID = doc.Id
	}
	return &Mutation{
		Type:      MutationPut,
		DocID:     docID,
		Document:  doc,
		Timestamp: time.Now().UnixNano(),
	}
}

// NewDeleteMutation creates a new DELETE mutation (tombstone).
func NewDeleteMutation(docID string) *Mutation {
	return &Mutation{
		Type:      MutationDelete,
		DocID:     docID,
		Timestamp: time.Now().UnixNano(),
	}
}

// Driver defines the interface for batch serialization and deserialization.
type Driver interface {
	Name() string
	Extension() string
	MarshalBatch(mutations []*Mutation) ([]byte, error)
	UnmarshalBatch(data []byte) ([]*Mutation, error)
}

// JSONLDriver serializes mutations into JSON Lines format.
type JSONLDriver struct{}

func (d *JSONLDriver) Name() string { return "pb.jsonl" }
func (d *JSONLDriver) Extension() string { return ".jsonl" }

func (d *JSONLDriver) MarshalBatch(mutations []*Mutation) ([]byte, error) {
	var buf []byte
	for _, m := range mutations {
		line, err := json.Marshal(m)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal mutation: %w", err)
		}
		buf = append(buf, line...)
		buf = append(buf, '\n')
	}
	return buf, nil
}

func (d *JSONLDriver) UnmarshalBatch(data []byte) ([]*Mutation, error) {
	var mutations []*Mutation
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			line := data[start:i]
			start = i + 1
			if len(line) == 0 {
				continue
			}
			var m Mutation
			if err := json.Unmarshal(line, &m); err != nil {
				return nil, fmt.Errorf("failed to unmarshal mutation line: %w", err)
			}
			mutations = append(mutations, &m)
		}
	}
	if start < len(data) {
		line := data[start:]
		if len(line) > 0 {
			var m Mutation
			if err := json.Unmarshal(line, &m); err != nil {
				return nil, fmt.Errorf("failed to unmarshal trailing line: %w", err)
			}
			mutations = append(mutations, &m)
		}
	}
	return mutations, nil
}

// RecordIODriver serializes mutations into length-prefixed binary records.
type RecordIODriver struct{}

func (r *RecordIODriver) Name() string { return "recordio" }
func (r *RecordIODriver) Extension() string { return ".bin" }

func (r *RecordIODriver) MarshalBatch(mutations []*Mutation) ([]byte, error) {
	var buf []byte
	lenBuf := make([]byte, 4)
	for _, m := range mutations {
		data, err := json.Marshal(m)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal record: %w", err)
		}
		binary.BigEndian.PutUint32(lenBuf, uint32(len(data)))
		buf = append(buf, lenBuf...)
		buf = append(buf, data...)
	}
	return buf, nil
}

func (r *RecordIODriver) UnmarshalBatch(data []byte) ([]*Mutation, error) {
	var mutations []*Mutation
	offset := 0
	for offset < len(data) {
		if offset+4 > len(data) {
			return nil, io.ErrUnexpectedEOF
		}
		recLen := binary.BigEndian.Uint32(data[offset : offset+4])
		offset += 4
		if offset+int(recLen) > len(data) {
			return nil, io.ErrUnexpectedEOF
		}
		recData := data[offset : offset+int(recLen)]
		offset += int(recLen)

		var m Mutation
		if err := json.Unmarshal(recData, &m); err != nil {
			return nil, fmt.Errorf("failed to unmarshal record data: %w", err)
		}
		mutations = append(mutations, &m)
	}
	return mutations, nil
}
