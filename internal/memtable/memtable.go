// Package memtable implements the in-memory write buffer that sits in front
// of the on-disk segments.
//
// Every write lands here after the write-ahead log has accepted it, and stays
// until the buffer crosses its size threshold and is flushed to an immutable
// segment. It is the fastest layer of the read path and the only mutable one.
package memtable

import "sync"

// entryOverhead is the per-record cost of a key on disk: the 13-byte record
// header the storage layer writes around every key-value pair.
//
// The size estimate exists to decide when to flush, so it should approximate
// the bytes a flush will produce rather than the bytes Go is holding. Counting
// only key and value length -- as this did before -- understates a segment by
// 13 bytes per record, which for small values means segments materially larger
// than the configured threshold.
const entryOverhead = 13

// Entry is a stored value, or a tombstone marking a deleted key.
type Entry struct {
	Value   []byte
	Deleted bool
}

// MemTable is a concurrent in-memory key-value buffer.
type MemTable struct {
	mu   sync.RWMutex
	data map[string]Entry
	size int // running estimate of the flushed size in bytes
}

// New returns an empty MemTable.
func New() *MemTable {
	return &MemTable{data: make(map[string]Entry)}
}

// Put stores a value, replacing any existing entry for the key.
//
// The value is copied. Callers routinely reuse the buffer they passed in, and
// retaining it would let a later mutation reach in and change data the engine
// has already acknowledged.
func (m *MemTable) Put(key, value []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.replace(string(key), Entry{Value: append([]byte(nil), value...)})
}

// Delete records a tombstone for the key.
//
// The key is not removed from the map. An older segment on disk may still hold
// a value for it, and only an explicit tombstone can shadow that value on the
// way through the read path.
func (m *MemTable) Delete(key []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.replace(string(key), Entry{Deleted: true})
}

// replace installs an entry and keeps the size estimate in step.
// The caller must hold the write lock.
func (m *MemTable) replace(key string, entry Entry) {
	if old, existed := m.data[key]; existed {
		m.size -= len(key) + len(old.Value) + entryOverhead
	}

	m.data[key] = entry
	m.size += len(key) + len(entry.Value) + entryOverhead
}

// Get returns the value for a key.
//
// A tombstone reports as absent: within this layer the key is gone, and the
// caller must not fall through to the segments on disk to find an older value.
func (m *MemTable) Get(key []byte) ([]byte, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	entry, exists := m.data[string(key)]
	if !exists {
		return nil, false
	}

	return entry.Value, !entry.Deleted
}

// Lookup reports an entry along with whether the memtable knows the key at
// all, letting the read path distinguish "deleted here" from "not here".
func (m *MemTable) Lookup(key []byte) (Entry, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	entry, exists := m.data[string(key)]
	return entry, exists
}

// Export returns a copy of the buffer's contents for flushing to disk.
//
// Tombstones are included deliberately. A delete that never reaches a segment
// would be forgotten at the next flush, and the older value it was shadowing
// would come back to life.
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

// ApproximateSize estimates the bytes a flush of this buffer would write.
//
// The estimate is maintained incrementally on every write. Recomputing it by
// walking the map -- as this did before -- made each Put O(n) in the number of
// buffered keys, so filling a memtable cost O(n²) and got quadratically worse
// the smaller the values were.
func (m *MemTable) ApproximateSize() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.size
}

// Len reports the number of buffered keys, tombstones included.
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
