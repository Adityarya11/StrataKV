//go:build v1

package bench

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"

	stratakv "github.com/Adityarya11/StrataKV/engine"
)

// Only used in a v1.0.3 checkout, built with -tags v1 (see bench/README.md).

const engineName = "v1.0.3"

type v1Store struct{ db *stratakv.DB }

func openStore(dir string) (store, error) {
	db, err := stratakv.Open(dir)
	if err != nil {
		return nil, err
	}

	return v1Store{db}, nil
}

func (s v1Store) Put(k, v []byte) error { return s.db.Put(k, v) }
func (s v1Store) Close() error          { return s.db.Close() }

func (s v1Store) Get(k []byte) ([]byte, bool, error) {
	v, found := s.db.Get(k)
	return v, found, nil
}

// buildFixture writes v1's file format directly. v1 fsyncs every Put, so filling
// a few hundred segments through the engine would take hours. The layout is
// v1.0.3's WriteSegment: a 9-byte header (tombstone flag, key length, value
// length), then key and value. The real v1 engine opens the files and builds
// its own filters.
func buildFixture(dir string, segs, valSize int) error {
	per := recordsPerSegment(valSize)
	val := valueOf(valSize)

	// 19-digit names like v1 makes; v1 sorts them as strings
	const base = int64(1_700_000_000_000_000_000)

	for s := range segs {
		path := filepath.Join(dir, fmt.Sprintf("%d.seg", base+int64(s)))
		if err := writeLegacySegment(path, s*per, per, val); err != nil {
			return err
		}
	}

	return nil
}

func writeLegacySegment(path string, first, count int, val []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := bufio.NewWriterSize(f, 1<<20)

	var hdr [9]byte
	for i := first; i < first+count; i++ {
		key := keyFor(i)

		hdr[0] = 0
		binary.LittleEndian.PutUint32(hdr[1:5], uint32(len(key)))
		binary.LittleEndian.PutUint32(hdr[5:9], uint32(len(val)))

		if _, err := w.Write(hdr[:]); err != nil {
			return err
		}
		if _, err := w.Write(key); err != nil {
			return err
		}
		if _, err := w.Write(val); err != nil {
			return err
		}
	}

	if err := w.Flush(); err != nil {
		return err
	}

	return f.Sync()
}

func compactFixture(dir string) error {
	db, err := stratakv.Open(dir)
	if err != nil {
		return err
	}

	if err := db.Compact(); err != nil {
		db.Close()
		return err
	}

	return db.Close()
}
