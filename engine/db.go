// Package engine provides the public API for the StrataKV storage engine.
// It exposes a clean, thread-safe interface for key-value operations while
// encapsulating the underlying Log-Structured Merge (LSM) tree mechanics,
// MemTables, and Write-Ahead Logging (WAL).
package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Adityarya11/StrataKV/internal/filter"
	"github.com/Adityarya11/StrataKV/internal/memtable"
	"github.com/Adityarya11/StrataKV/internal/storage"
)

const (
	maxMemTableSize = 1 << 20 // 1 MiB flush threshold
	maxSegmentSize  = 64 << 20
	walFileName     = "0001.wal"
)

// DB represents an active instance of the StrataKV storage engine.
// It manages the active MemTable, Write-Ahead Log, and orchestrates
// disk flushes. DB is thread-safe and safe for concurrent use across multiple goroutines.
type DB struct {
	mu             sync.RWMutex
	mem            *memtable.MemTable
	wal            *storage.WAL
	dataDir        string
	segmentFilters map[string]*filter.BloomFilter
}

// Open initializes and mounts the StrataKV engine at the specified directory.
// If the directory does not exist, it will be created. During initialization,
// Open will automatically recover any un-flushed data by replaying the Write-Ahead Log (WAL)
// and reconstruct the in-memory Bloom filters for fast reads.
func Open(dataDir string) (*DB, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create the data dir: %w", err)
	}

	walPath := filepath.Join(dataDir, walFileName)
	wal, err := storage.NewWAL(walPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open wal: %w", err)
	}

	mem := memtable.New()

	err = wal.Recover(func(isDelete bool, key []byte, val []byte) {
		if isDelete {
			mem.Delete(key)
		} else {
			mem.Put(key, val)
		}
	})

	if err != nil {
		return nil, fmt.Errorf("failed to recover from Wal: %w", err)
	}

	db := &DB{
		mem:            mem,
		wal:            wal,
		dataDir:        dataDir,
		segmentFilters: make(map[string]*filter.BloomFilter),
	}

	// bloom filter for all segmensts
	files, _ := os.ReadDir(dataDir)
	for _, f := range files {
		if len(f.Name()) > 4 && f.Name()[len(f.Name())-4:] == ".seg" {
			segPath := filepath.Join(dataDir, f.Name())

			bf, err := storage.BuildBloomFilter(segPath)
			if err == nil {
				db.segmentFilters[f.Name()] = bf
			}
		}
	}

	return db, nil
}

// Put inserts or updates a key-value pair in the database.
// The operation is synchronous; it first appends the entry to the WAL for strict
// durability before mutating the in-memory MemTable.
func (db *DB) Put(key, val []byte) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	err := db.wal.WriteEntry(false, key, val)
	if err != nil {
		return fmt.Errorf("failed to write the WAL: %w", err)
	}

	db.mem.Put(key, val)

	if db.mem.ApproximateSize() >= maxMemTableSize {
		if err := db.flushLocked(); err != nil {
			return fmt.Errorf("failed to flush the memtable: %w", err)
		}
	}

	return nil
}

func (db *DB) flushLocked() error {
	fmt.Println("Memtable is full, starting flush process...")

	data := db.mem.Export()
	if len(data) == 0 {
		return nil
	}

	segName := fmt.Sprintf("%d.seg", time.Now().UnixNano())
	segPath := filepath.Join(db.dataDir, segName)
	if err := storage.WriteSegment(segPath, data); err != nil {
		return fmt.Errorf("failed to write segment %s: %w", segName, err)
	}

	// bf for new segments
	if bf, err := storage.BuildBloomFilter(segPath); err == nil {
		db.segmentFilters[segName] = bf
	}

	db.mem.Clear()

	if err := db.wal.Close(); err != nil {
		return fmt.Errorf("failed to close WAL before rotation: %w", err)
	}

	walPath := filepath.Join(db.dataDir, walFileName)
	if err := os.Remove(walPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove old WAL: %w", err)
	}

	newWAL, err := storage.NewWAL(walPath)
	if err != nil {
		return fmt.Errorf("failed to create new WAL: %w", err)
	}

	db.wal = newWAL
	fmt.Printf("Flush complete. Created segment: %s\n", segName)
	return nil
}

// Get retrieves the value associated with a given key.
// It follows a hierarchy of reads: checking the MemTable first, then probing
// the in-memory Bloom filters to prune disk I/O, and finally searching the
// immutable segment files on disk. Returns false if the key does not exist or was deleted.
func (db *DB) Get(key []byte) ([]byte, bool) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	val, found := db.mem.Get(key) // check the memtable -> fastest
	if found {
		return val, true
	}

	files, err := os.ReadDir(db.dataDir)
	if err != nil {
		return nil, false
	}

	var segments []string
	for _, f := range files {
		if len(f.Name()) > 4 && f.Name()[len(f.Name())-4:] == ".seg" {
			segments = append(segments, f.Name())
		}
	}

	// sort desc
	sort.Slice(segments, func(i, j int) bool {
		return segments[i] > segments[j]
	})

	for _, seg := range segments {

		// bloom check
		bf, exists := db.segmentFilters[seg]
		if exists && !bf.MightContain(key) {
			continue
		}

		segPath := filepath.Join(db.dataDir, seg)

		if val, found, isDeleted := storage.SearchSegment(segPath, key); found {
			if isDeleted {
				return nil, false // The key was explicitly deleted (tombstone)
			}
			return val, true
		}

	}

	return nil, false

}

// Delete marks a key as deleted using a tombstone.
// The key is not immediately removed from disk; instead, a tombstone record is
// appended to the WAL and MemTable. The physical deletion occurs during the next Compaction cycle.
func (db *DB) Delete(key []byte) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	err := db.wal.WriteEntry(true, key, nil)
	if err != nil {
		return fmt.Errorf("WAL delete failed: %w", err)
	}

	db.mem.Delete(key)
	return nil
}

// Close safely shuts down the database.
// It ensures that the Write-Ahead Log (WAL) is properly synchronized and closed,
// preventing data corruption. This should always be called (usually via defer)
// before an application exits.
func (db *DB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.wal.Close()
}
