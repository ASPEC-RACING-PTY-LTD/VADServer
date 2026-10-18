package repo

import (
	"context"
	"sync"
)

// MemBackend is an in-memory Backend for tests.
type MemBackend struct {
	LockTable
	mu      sync.RWMutex
	objects map[Kind]map[string][]byte
}

// NewMemBackend returns an empty in-memory backend.
func NewMemBackend() *MemBackend {
	return &MemBackend{objects: map[Kind]map[string][]byte{}}
}

// Put implements Backend.
func (m *MemBackend) Put(_ context.Context, kind Kind, oid string, data []byte) error {
	if err := CheckObject(kind, oid, data); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.objects[kind] == nil {
		m.objects[kind] = map[string][]byte{}
	}
	m.objects[kind][oid] = append([]byte(nil), data...)
	return nil
}

func (m *MemBackend) get(kind Kind, oid string) ([]byte, error) {
	if err := CheckKey(kind, oid); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.objects[kind][oid]
	if !ok {
		return nil, ErrNotFound
	}
	return b, nil
}

// Get implements Backend.
func (m *MemBackend) Get(_ context.Context, kind Kind, oid string) ([]byte, error) {
	b, err := m.get(kind, oid)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), b...), nil
}

// GetRange implements Backend.
func (m *MemBackend) GetRange(_ context.Context, kind Kind, oid string, offset, length int64) ([]byte, error) {
	b, err := m.get(kind, oid)
	if err != nil {
		return nil, err
	}
	if offset < 0 || length < 0 || offset > int64(len(b)) || length > int64(len(b))-offset {
		return nil, ErrRange
	}
	return append([]byte(nil), b[offset:offset+length]...), nil
}

// Stat implements Backend.
func (m *MemBackend) Stat(_ context.Context, kind Kind, oid string) (int64, error) {
	b, err := m.get(kind, oid)
	if err != nil {
		return 0, err
	}
	return int64(len(b)), nil
}

// List implements Backend.
func (m *MemBackend) List(_ context.Context, kind Kind) ([]ObjectInfo, error) {
	if !kind.Valid() {
		return nil, ErrInvalidObject
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ObjectInfo, 0, len(m.objects[kind]))
	for oid, b := range m.objects[kind] {
		out = append(out, ObjectInfo{OID: oid, Size: int64(len(b))})
	}
	return out, nil
}

// Delete implements Backend.
func (m *MemBackend) Delete(_ context.Context, kind Kind, oid string) error {
	if err := CheckKey(kind, oid); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects[kind], oid)
	return nil
}

// Raw returns the stored bytes without copying, for tests that tamper with
// objects; ok is false when the object is missing.
func (m *MemBackend) Raw(kind Kind, oid string) (b []byte, ok bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok = m.objects[kind][oid]
	return b, ok
}

// SetRaw stores bytes without validation, for tamper tests.
func (m *MemBackend) SetRaw(kind Kind, oid string, data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.objects[kind] == nil {
		m.objects[kind] = map[string][]byte{}
	}
	m.objects[kind][oid] = data
}
