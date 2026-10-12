// Package memtable is the in-memory buffer in front of the segment files.
// Writes land here once the write-ahead log has them, and stay until the buffer
// is flushed to a segment.
package memtable

import "sync"

// entryOverhead is the record header the storage layer adds to every entry.
// The size estimate is used to decide when to flush, so it counts what a flush
// would write, not what Go holds in memory.
const entryOverhead = 13

// Entry is a stored value, or a tombstone for a deleted key.
type Entry struct {
	Value   []byte
	Deleted bool
}

// MemTable is an in-memory key-value buffer, safe for concurrent use.
type MemTable struct {
	mu   sync.RWMutex
	data map[string]Entry
	size int // running estimate of the flushed size, in bytes
}

// New returns an empty MemTable.
func New() *MemTable {
	return &MemTable{data: make(map[string]Entry)}
}

// Put stores a copy of value under key, replacing any existing entry.
func (m *MemTable) Put(key, value []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.replace(string(key), Entry{Value: append([]byte(nil), value...)})
}

// Delete stores a tombstone. The key stays in the map because an older segment
// may still have a value for it, and the tombstone is what hides that.
func (m *MemTable) Delete(key []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.replace(string(key), Entry{Deleted: true})
}

// replace sets an entry and updates the size estimate. The caller holds the
// write lock.
func (m *MemTable) replace(key string, entry Entry) {
	if old, existed := m.data[key]; existed {
		m.size -= len(key) + len(old.Value) + entryOverhead
	}

	m.data[key] = entry
	m.size += len(key) + len(entry.Value) + entryOverhead
}

// Get returns the value for key. A tombstone reads as absent.
func (m *MemTable) Get(key []byte) ([]byte, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	entry, exists := m.data[string(key)]
	if !exists {
		return nil, false
	}

	return entry.Value, !entry.Deleted
}

// Lookup returns the entry and whether the memtable has the key at all. That
// tells "deleted here" apart from "not here", which Get can't.
func (m *MemTable) Lookup(key []byte) (Entry, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	entry, exists := m.data[string(key)]
	return entry, exists
}

// Export returns a copy of everything in the buffer, for flushing. Tombstones
// are included. Without them a delete would be forgotten at the next flush.
func (m *MemTable) Export() map[string]Entry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make(map[string]Entry, len(m.data))
	for k, entry := range m.data {
		out[k] = Entry{
			Value:   append([]byte(nil), entry.Value...),
			Deleted: entry.Deleted,
		}
	}

	return out
}

// ApproximateSize is roughly how many bytes a flush would write. It is kept up
// to date on every write, so reading it is cheap.
func (m *MemTable) ApproximateSize() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.size
}

// Len is the number of keys in the buffer, tombstones included.
func (m *MemTable) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return len(m.data)
}

// Clear empties the buffer.
func (m *MemTable) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.data = make(map[string]Entry)
	m.size = 0
}
