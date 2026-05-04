package engine

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Adityarya11/StrataKV/internal/memtable"
	"github.com/Adityarya11/StrataKV/internal/storage"
)

type DB struct {
	mem *memtable.Memtable
	wal *storage.WAL
}

func Open(dataDir string) (*DB, error) {
	err := os.MkdirAll(dataDir, 0755)
	if err != nil {
		return nil, fmt.Errorf("failed to create data dir: %w", err)
	}

	walPath := filepath.Join(dataDir, "0001.wal")
	wal, errPath := storage.NewWal(walPath)
	if errPath != nil {
		return nil, fmt.Errorf("failed to open WAL: %w", errPath)
	}
	return &DB{
		mem: memtable.New(),
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
