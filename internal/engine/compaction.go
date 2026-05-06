package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Adityarya11/StrataKV/internal/storage"
)

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
	mergedData := make(map[string][]byte)
	for _, seg := range segments {
		segPath := filepath.Join(db.dataDir, seg)
		if err := storage.ReadSegment(segPath, mergedData); err != nil {
			return fmt.Errorf("failed to read segment %s: %w", seg, err)
		}
	}

	newSegName := fmt.Sprintf("%d.seg", time.Now().UnixNano())
	newSegPath := filepath.Join(db.dataDir, newSegName)

	if err := storage.WriteSegment(newSegPath, mergedData); err != nil {
		return fmt.Errorf("failed to write compacted segment: %w", err)
	}

	for _, seg := range segments {
		os.Remove(filepath.Join(db.dataDir, seg))
	}

	fmt.Printf("Compaction completed ====> Success..")
	return nil
}
