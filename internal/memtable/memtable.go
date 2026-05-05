package memtable

import (
	"sync"
)

type Entry struct {
	Value   []byte
	Deleted bool
}

type MemTable struct {
	mu   sync.RWMutex
	data map[string]Entry // lowercase -> encapsulation achieved!!
}

func New() *MemTable {
	return &MemTable{
		data: make(map[string]Entry),
	}
}

// put
func (m *MemTable) Put(key, value []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()

	copyVal := append([]byte(nil), value...)
	m.data[string(key)] = Entry{Value: copyVal, Deleted: false}
}

// get
func (m *MemTable) Get(key []byte) ([]byte, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	entry, exists := m.data[string(key)]
	if !exists || entry.Deleted {
		return nil, false
	}
	return entry.Value, true
}

// delete
func (m *MemTable) Delete(key []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.data[string(key)] = Entry{Deleted: true}
}

func (m *MemTable) Export() map[string][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make(map[string][]byte)
	for k, entry := range m.data {
		if !entry.Deleted {
			out[k] = append([]byte(nil), entry.Value...)
		}
	}

	return out
}

func (m *MemTable) ApproximateSize() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	size := 0
	for k, entry := range m.data {
		size += len(k) + len(entry.Value) + 1
	}

	return size
}

func (m *MemTable) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.data = make(map[string]Entry)
}
