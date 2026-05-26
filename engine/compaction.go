package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Adityarya11/StrataKV/internal/memtable"
	"github.com/Adityarya11/StrataKV/internal/storage"
)

// Compact triggers a manual background compaction process.
// It scans all immutable segment files on disk, merges them, and purges
// stale data (overwritten keys and tombstones). This reclaims disk space
// and reduces read amplification by consolidating fragmented segments into a single file.
func (db *DB) Compact() error {
	db.mu.Lock()
	defer db.mu.Unlock()

	files, err := os.ReadDir(db.dataDir)
	if err != nil {
		return err
	}

	var segments []string
	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".seg") {
			segments = append(segments, f.Name())
		}
	}

	if len(segments) < 2 {
		return nil
	}

	fmt.Printf("Compaction started, merging %d segments...\n", len(segments))

	sort.Strings(segments)

	// Merging into memory, older segments first to ensure newer values overwrite older ones.
	mergedData := make(map[string]memtable.Entry)
	for _, seg := range segments {
		segPath := filepath.Join(db.dataDir, seg)
		if err := storage.ReadSegment(segPath, mergedData); err != nil {
			return fmt.Errorf("failed to read segment %s: %w", seg, err)
		}
	}

	// Purge all tombstones: older segments are being deleted, so we can finally drop them!
	finalData := make(map[string]memtable.Entry)
	for k, v := range mergedData {
		if !v.Deleted {
			finalData[k] = v
		}
	}

	newSegName := fmt.Sprintf("%d.seg", time.Now().UnixNano())
	newSegPath := filepath.Join(db.dataDir, newSegName)

	if err := storage.WriteSegment(newSegPath, finalData); err != nil {
		return fmt.Errorf("failed to write compacted segment: %w", err)
	}

	// compact bf
	if bf, err := storage.BuildBloomFilter(newSegPath); err == nil {
		db.segmentFilters[newSegName] = bf
	}

	for _, seg := range segments {
		os.Remove(filepath.Join(db.dataDir, seg))
		delete(db.segmentFilters, seg)
	}

	fmt.Printf("Compaction completed ====> Success..")
	return nil
}
