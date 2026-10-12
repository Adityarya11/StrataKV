// Package engine is the public API for StrataKV, an embedded key-value store.
// Writes go to a write-ahead log and an in-memory buffer, which is flushed to
// immutable segment files. Each segment has its own Bloom filter.
package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/Adityarya11/StrataKV/v2/internal/memtable"
	"github.com/Adityarya11/StrataKV/v2/internal/storage"
)

const walFileName = "0001.wal"

// ErrEmptyKey is returned for a zero-length key. A zero key length is how the
// record format recognises bytes that aren't a record, so an empty key can't be
// stored.
var ErrEmptyKey = errors.New("stratakv: key must not be empty")

// DB is an open database. It is safe for concurrent use. Reads share a lock;
// writes, flushes and compaction take it exclusively.
type DB struct {
	mu      sync.RWMutex
	mem     *memtable.MemTable
	wal     *storage.WAL
	dataDir string

	// newest first, so the first segment that has a key has its latest value
	segments []*storage.Segment

	opts Options

	// Always increasing. The clock alone isn't enough: on Windows two flushes
	// can land in the same tick, and the second segment would overwrite the
	// first.
	nextSeq int64

	lastRecovery storage.RecoveryReport
}

// Open opens the database in dataDir with DefaultOptions, creating the
// directory if needed.
func Open(dataDir string) (*DB, error) {
	return OpenWith(dataDir, DefaultOptions())
}

// OpenWith is Open with explicit options. It replays the write-ahead log and
// loads each segment's index. If a segment fails its integrity checks it
// returns an error, rather than serving part of the data.
func OpenWith(dataDir string, opts Options) (*DB, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data directory %s: %w", dataDir, err)
	}

	db := &DB{
		mem:     memtable.New(),
		dataDir: dataDir,
		opts:    opts,
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

// sortSegments puts the newest segment first. The caller holds db.mu.
func (db *DB) sortSegments() {
	slices.SortFunc(db.segments, func(a, b *storage.Segment) int {
		return int(b.Sequence() - a.Sequence())
	})
}

// nextSegmentName builds a filename from the wall clock, nudged so it never
// repeats or goes backwards.
func (db *DB) nextSegmentName() string {
	seq := max(time.Now().UnixNano(), db.nextSeq)
	db.nextSeq = seq + 1

	return storage.SegmentName(seq)
}

// Put stores value under key. The write goes to the log (and is fsynced under
// SyncAlways) before it reaches the in-memory buffer. Both slices are copied,
// so the caller can reuse them.
func (db *DB) Put(key, value []byte) error {
	if len(key) == 0 {
		return ErrEmptyKey
	}

	db.mu.Lock()
	defer db.mu.Unlock()

	if err := db.wal.Append(
		[]storage.Record{{Key: key, Value: value}},
		db.opts.Sync == SyncAlways,
	); err != nil {
		return fmt.Errorf("log put: %w", err)
	}
	db.mem.Put(key, value)

	return db.maybeFlushLocked()
}

// Delete writes a tombstone for key. The old value stays on disk until the next
// compaction.
func (db *DB) Delete(key []byte) error {
	if len(key) == 0 {
		return ErrEmptyKey
	}

	db.mu.Lock()
	defer db.mu.Unlock()

	if err := db.wal.Append(
		[]storage.Record{{Tombstone: true, Key: key}},
		db.opts.Sync == SyncAlways,
	); err != nil {
		return fmt.Errorf("log delete: %w", err)
	}
	db.mem.Delete(key)

	// deletes count towards the flush threshold too
	return db.maybeFlushLocked()
}

// Get returns the value for key. found is false if the key doesn't exist. A
// non-nil error means the read itself failed (an I/O error or a bad checksum),
// which is not the same as a missing key. Don't modify the returned slice.
func (db *DB) Get(key []byte) (value []byte, found bool, err error) {
	if len(key) == 0 {
		return nil, false, ErrEmptyKey
	}

	db.mu.RLock()
	defer db.mu.RUnlock()

	// the memtable has the newest version of anything recent, tombstones
	// included, so whatever it says is final
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

// Has reports whether key exists.
func (db *DB) Has(key []byte) (bool, error) {
	_, found, err := db.Get(key)
	return found, err
}

// maybeFlushLocked flushes once the memtable reaches its size limit. The
// caller holds db.mu.
func (db *DB) maybeFlushLocked() error {
	if db.mem.ApproximateSize() < db.opts.MemTableSize {
		return nil
	}

	return db.flushLocked()
}

// flushLocked writes the memtable out as a new segment, then resets the log.
// The segment is fsynced before the log is reset, so a crash in between leaves
// the data in both places, and replaying a log over a segment that already
// has the same records does no harm.
func (db *DB) flushLocked() error {
	data := db.mem.Export()
	if len(data) == 0 {
		return nil
	}

	name := db.nextSegmentName()
	path := filepath.Join(db.dataDir, name)

	if err := storage.WriteSegment(path, data, db.opts.FilterFalsePositiveRate); err != nil {
		return fmt.Errorf("flush memtable to %s: %w", name, err)
	}

	seg, err := storage.OpenSegment(path)
	if err != nil {
		return fmt.Errorf("open freshly written segment %s: %w", name, err)
	}

	// index the segment before clearing the memtable
	db.segments = append(db.segments, seg)
	db.sortSegments()

	db.mem.Clear()

	if err := db.wal.Reset(); err != nil {
		return fmt.Errorf("reset write-ahead log after flush: %w", err)
	}

	return nil
}

// Flush writes the memtable to a segment now, whatever its size.
func (db *DB) Flush() error {
	db.mu.Lock()
	defer db.mu.Unlock()

	return db.flushLocked()
}

// Stats describes the current shape of the database.
type Stats struct {
	Segments       int // segment files on disk
	SegmentEntries int // records in them, overwritten versions included
	MemTableKeys   int
	MemTableBytes  int // roughly what a flush would write
	LegacySegments int // old format, rewritten by the next compaction

	Recovery storage.RecoveryReport
}

// Stats returns a snapshot of the database's shape. It is meant for deciding
// when to compact.
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

// Close syncs and closes the write-ahead log. Whatever is still in the memtable
// stays in the log and is replayed by the next Open. Call Flush first to move
// it into a segment.
func (db *DB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()

	return db.wal.Close()
}
