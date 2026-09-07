// Package engine provides the public API for the StrataKV storage engine: a
// thread-safe key-value interface over an append-only write-ahead log, an
// in-memory write buffer, immutable on-disk segments, and per-segment Bloom
// filters that keep reads off segments which cannot hold the key.
package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/Adityarya11/StrataKV/internal/memtable"
	"github.com/Adityarya11/StrataKV/internal/storage"
)

const (
	// maxMemTableSize is how large the in-memory buffer grows before it is
	// flushed to an immutable segment.
	maxMemTableSize = 1 << 20 // 1 MiB

	walFileName = "0001.wal"
)

// ErrEmptyKey reports a zero-length key. The record format uses a zero key
// length to mean "these bytes are not a record", so storing one would make
// corruption undetectable.
var ErrEmptyKey = errors.New("stratakv: key must not be empty")

// DB is an open StrataKV database.
//
// A DB is safe for concurrent use by multiple goroutines. Reads share a lock;
// writes, flushes, and compaction take it exclusively.
type DB struct {
	mu      sync.RWMutex
	mem     *memtable.MemTable
	wal     *storage.WAL
	dataDir string

	// segments is ordered newest first: the first one holding a key holds its
	// current value. Held in memory because reads used to call os.ReadDir and
	// re-sort on every Get.
	segments []*storage.Segment

	// nextSeq is kept strictly increasing rather than read from the clock,
	// whose resolution on Windows lets two flushes share a tick -- and two
	// segments with one name means the second overwrites the first.
	nextSeq int64

	lastRecovery storage.RecoveryReport
}

// Open mounts a database at dataDir, creating it if needed. Replays the
// write-ahead log and loads every segment's index, failing if any segment does
// not pass its integrity checks.
func Open(dataDir string) (*DB, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data directory %s: %w", dataDir, err)
	}

	db := &DB{
		mem:     memtable.New(),
		dataDir: dataDir,
	}

	if err := db.loadSegments(); err != nil {
		return nil, err
	}

	wal, err := storage.OpenWAL(filepath.Join(dataDir, walFileName))
	if err != nil {
		return nil, err
	}
	db.wal = wal

	report, err := wal.Recover(func(tombstone bool, key, value []byte) {
		if tombstone {
			db.mem.Delete(key)
		} else {
			db.mem.Put(key, value)
		}
	})
	if err != nil {
		wal.Close()
		return nil, fmt.Errorf("recover write-ahead log: %w", err)
	}

	db.lastRecovery = report

	return db, nil
}

func (db *DB) loadSegments() error {
	entries, err := os.ReadDir(db.dataDir)
	if err != nil {
		return fmt.Errorf("read data directory %s: %w", db.dataDir, err)
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}

		seq, ok := storage.ParseSegmentSequence(e.Name())
		if !ok {
			continue
		}

		seg, err := storage.OpenSegment(filepath.Join(db.dataDir, e.Name()))
		if err != nil {
			return fmt.Errorf("open segment %s: %w", e.Name(), err)
		}

		db.segments = append(db.segments, seg)
		db.nextSeq = max(db.nextSeq, seq+1)
	}

	db.sortSegments()

	return nil
}

// sortSegments orders the index newest first. Caller must hold db.mu.
func (db *DB) sortSegments() {
	slices.SortFunc(db.segments, func(a, b *storage.Segment) int {
		return int(b.Sequence() - a.Sequence())
	})
}

// nextSegmentName allocates a filename. The sequence tracks wall-clock
// nanoseconds so names stay meaningful, but never repeats or goes backwards.
func (db *DB) nextSegmentName() string {
	seq := max(time.Now().UnixNano(), db.nextSeq)
	db.nextSeq = seq + 1

	return storage.SegmentName(seq)
}

// Put stores a value for a key. The write is logged and fsynced before the
// buffer is touched, so a Put that returns nil has survived to disk. The value
// is copied; the caller may reuse its buffer.
func (db *DB) Put(key, value []byte) error {
	if len(key) == 0 {
		return ErrEmptyKey
	}

	db.mu.Lock()
	defer db.mu.Unlock()

	if err := db.wal.Write(false, key, value); err != nil {
		return fmt.Errorf("log put: %w", err)
	}
	db.mem.Put(key, value)

	return db.maybeFlushLocked()
}

// Delete records a tombstone. The key is not physically removed until the next
// compaction: older segments may still hold a value, and only a tombstone
// shadows them.
func (db *DB) Delete(key []byte) error {
	if len(key) == 0 {
		return ErrEmptyKey
	}

	db.mu.Lock()
	defer db.mu.Unlock()

	if err := db.wal.Write(true, key, nil); err != nil {
		return fmt.Errorf("log delete: %w", err)
	}
	db.mem.Delete(key)

	// Deletes are writes too; checking only on Put let them grow unbounded.
	return db.maybeFlushLocked()
}

// Get returns the value stored for a key.
//
// A nil error with found false means the key is genuinely absent. A non-nil
// error means the read could not be completed -- I/O failure, or a segment
// that fails its checksums -- and must not be treated as absence.
func (db *DB) Get(key []byte) (value []byte, found bool, err error) {
	if len(key) == 0 {
		return nil, false, ErrEmptyKey
	}

	db.mu.RLock()
	defer db.mu.RUnlock()

	// The buffer holds the newest version of anything recently written,
	// tombstones included, so it answers first and answers finally.
	if entry, known := db.mem.Lookup(key); known {
		if entry.Deleted {
			return nil, false, nil
		}
		return entry.Value, true, nil
	}

	for _, seg := range db.segments {
		if !seg.MightContain(key) {
			continue
		}

		val, hit, tombstone, err := seg.Get(key)
		if err != nil {
			return nil, false, fmt.Errorf("read %s: %w", seg.Name(), err)
		}
		if !hit {
			continue // Bloom filter false positive
		}
		if tombstone {
			return nil, false, nil
		}

		return val, true, nil
	}

	return nil, false, nil
}

// Has reports whether a key is present, without returning its value.
func (db *DB) Has(key []byte) (bool, error) {
	_, found, err := db.Get(key)
	return found, err
}

// maybeFlushLocked flushes if the buffer crossed the threshold. Caller holds db.mu.
func (db *DB) maybeFlushLocked() error {
	if db.mem.ApproximateSize() < maxMemTableSize {
		return nil
	}

	return db.flushLocked()
}

// flushLocked writes the buffer to a new segment and resets the log.
//
// Order matters: segment written and fsynced first, log reset second. A crash
// between them leaves the data in both places, and replaying a log over a
// segment holding the same records is harmless. The reverse order opens a
// window where the data lives nowhere.
func (db *DB) flushLocked() error {
	data := db.mem.Export()
	if len(data) == 0 {
		return nil
	}

	name := db.nextSegmentName()
	path := filepath.Join(db.dataDir, name)

	if err := storage.WriteSegment(path, data); err != nil {
		return fmt.Errorf("flush memtable to %s: %w", name, err)
	}

	seg, err := storage.OpenSegment(path)
	if err != nil {
		return fmt.Errorf("open freshly written segment %s: %w", name, err)
	}

	// Register before clearing: until it is indexed, its keys live only in mem.
	db.segments = append(db.segments, seg)
	db.sortSegments()

	db.mem.Clear()

	if err := db.wal.Reset(); err != nil {
		return fmt.Errorf("reset write-ahead log after flush: %w", err)
	}

	return nil
}

// Flush writes the buffer to a segment regardless of the size threshold.
func (db *DB) Flush() error {
	db.mu.Lock()
	defer db.mu.Unlock()

	return db.flushLocked()
}

// Stats describes the database's current on-disk and in-memory shape.
type Stats struct {
	Segments       int // immutable segment files
	SegmentEntries int // records across them, superseded versions included
	MemTableKeys   int
	MemTableBytes  int // what flushing the buffer would write
	LegacySegments int // pre-checksum format; rewritten by the next compaction

	Recovery storage.RecoveryReport
}

// Stats snapshots the database's shape. This is the hook for deciding when to
// compact.
func (db *DB) Stats() Stats {
	db.mu.RLock()
	defer db.mu.RUnlock()

	s := Stats{
		Segments:      len(db.segments),
		MemTableKeys:  db.mem.Len(),
		MemTableBytes: db.mem.ApproximateSize(),
		Recovery:      db.lastRecovery,
	}

	for _, seg := range db.segments {
		s.SegmentEntries += seg.EntryCount()
		if seg.Legacy() {
			s.LegacySegments++
		}
	}

	return s
}

// Close flushes and closes the write-ahead log. Buffered writes are left in
// the log -- already durable, replayed by the next Open. Call Flush first to
// consolidate them into a segment.
func (db *DB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()

	return db.wal.Close()
}
