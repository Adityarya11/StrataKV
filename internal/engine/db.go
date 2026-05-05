package engine

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Adityarya11/StrataKV/internal/memtable"
	"github.com/Adityarya11/StrataKV/internal/storage"
)

type DB struct {
	mem *memtable.MemTable
	wal *storage.WAL
}

func Open(dataDir string) (*DB, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create the data dir: %w", err)
	}

	walPath := filepath.Join(dataDir, "0001.wal")
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
		mem: mem,
		wal: wal,
	}, nil
}

func (db *DB) Put(key, val []byte) error {
	err := db.wal.WriteEntry(false, key, val)
	if err != nil {
		return fmt.Errorf("Wal write failed: %w", err)
	}

	db.mem.Put(key, val)
	return nil
}

func (db *DB) Get(key []byte) ([]byte, bool) {
	return db.mem.Get(key)
}

func (db *DB) Delete(key []byte) error {
	err := db.wal.WriteEntry(true, key, nil)
	if err != nil {
		return fmt.Errorf("WAL delete failed: %w", err)
	}

	db.mem.Delete(key)
	return nil
}

func (db *DB) Close() error {
	return db.wal.Close()
}
