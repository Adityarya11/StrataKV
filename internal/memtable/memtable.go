package memtable

import "sync"

type Memtable struct {
	mu   sync.RWMutex
	data map[string][]byte // lowercase -> encapsulation achieved!!
}

func New() *Memtable {
	return &Memtable{
		data: make(map[string][]byte),
	}
}

// put
func (m *Memtable) Put(key, value []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.data[string(key)] = value
}

// get
func (m *Memtable) Get(key []byte) ([]byte, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	value, exists := m.data[string(key)]
	return value, exists
}

// delete
func (m *Memtable) Delete(key []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.data, string(key))
}
