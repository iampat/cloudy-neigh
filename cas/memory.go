package cas

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"sync"
)

type Memory struct {
	mu      sync.Mutex
	blobs   map[Digest][]byte
	root    Digest
	hasRoot bool
}

func NewMemory() *Memory {
	return &Memory{blobs: make(map[Digest][]byte)}
}

// Put and Get both copy. Disk hands out freshly read bytes on every call, and
// the two backends have to be indistinguishable to a caller that mutates what
// it passed in or what it got back.
func (m *Memory) Put(data []byte) (Digest, error) {
	d := Digest(sha256.Sum256(data))

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.blobs[d]; !ok {
		m.blobs[d] = bytes.Clone(data)
	}
	return d, nil
}

func (m *Memory) Get(d Digest) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.blobs[d]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, d)
	}
	return bytes.Clone(data), nil
}

func (m *Memory) SetRoot(d Digest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.root, m.hasRoot = d, true
	return nil
}

func (m *Memory) Root() (Digest, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.root, m.hasRoot, nil
}
