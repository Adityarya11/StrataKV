//go:build !v1

package bench

import (
	"fmt"
	"os"
	"strings"

	stratakv "github.com/Adityarya11/StrataKV/v2/engine"
)

const engineName = "v2"

type v2Store struct{ db *stratakv.DB }

func openStore(dir string) (store, error) {
	db, err := stratakv.Open(dir)
	if err != nil {
		return nil, err
	}

	return v2Store{db}, nil
}

func (s v2Store) Put(k, v []byte) error              { return s.db.Put(k, v) }
func (s v2Store) Get(k []byte) ([]byte, bool, error) { return s.db.Get(k) }
func (s v2Store) Close() error                       { return s.db.Close() }

// buildFixture writes exactly segs default-size segments through the engine.
// SyncNever keeps it quick, and everything is flushed and closed before a
// benchmark reads it.
func buildFixture(dir string, segs, valSize int) error {
	return buildFixtureWith(dir, segs, valSize, stratakv.DefaultOptions().FilterFalsePositiveRate)
}

func buildFixtureWith(dir string, segs, valSize int, fpr float64) error {
	opts := stratakv.DefaultOptions()
	opts.Sync = stratakv.SyncNever
	opts.FilterFalsePositiveRate = fpr

	db, err := stratakv.OpenWith(dir, opts)
	if err != nil {
		return err
	}

	val := valueOf(valSize)
	for i := range segs * recordsPerSegment(valSize) {
		if err := db.Put(keyFor(i), val); err != nil {
			db.Close()
			return err
		}
	}

	if err := db.Flush(); err != nil {
		db.Close()
		return err
	}
	if err := db.Close(); err != nil {
		return err
	}

	got, err := countSegments(dir)
	if err != nil {
		return err
	}
	if got != segs {
		return fmt.Errorf("fixture has %d segments, want %d", got, segs)
	}

	return nil
}

func compactFixture(dir string) error {
	db, err := stratakv.Open(dir)
	if err != nil {
		return err
	}

	if _, err := db.Compact(); err != nil {
		db.Close()
		return err
	}

	return db.Close()
}

func countSegments(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}

	n := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".seg") {
			n++
		}
	}

	return n, nil
}
