package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Adityarya11/StrataKV/internal/memtable"
	"github.com/Adityarya11/StrataKV/internal/storage"
)

const (
	maxMemTableSize = 1 << 20 // 1 MiB flush threshold
	maxSegmentSize  = 64 << 20
	walFileName     = "0001.wal"
)

type DB struct {
	mu      sync.RWMutex
	mem     *memtable.MemTable
	wal     *storage.WAL
	dataDir string
}

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

	return &DB{
		mem:     mem,
		wal:     wal,
		dataDir: dataDir,
	}, nil
}

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
		segPath := filepath.Join(db.dataDir, seg)

		if val, found := storage.SearchSegment(segPath, key); found {
			return val, true
		}

	}

	return nil, false

}

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

func (db *DB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.wal.Close()
}
