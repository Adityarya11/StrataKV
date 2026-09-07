package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/Adityarya11/StrataKV/internal/memtable"
	"github.com/Adityarya11/StrataKV/internal/storage"
)

// CompactionResult reports what a compaction cycle did.
type CompactionResult struct {
	SegmentsMerged   int
	RecordsRead      int // superseded versions and tombstones included
	RecordsWritten   int // the gap against RecordsRead is what was reclaimed
	TombstonesPurged int

	BytesBefore int64
	BytesAfter  int64
}

// Compact merges every segment into one, keeping only the newest version of
// each key and dropping tombstones outright.
//
// Purging is safe only because this is a full merge: every segment that could
// hold a shadowed value is consumed in the same pass. The invariant would not
// hold for a partial or levelled compaction.
//
// Stop-the-world: holds the write lock for its full duration.
func (db *DB) Compact() (CompactionResult, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	var result CompactionResult

	// Include the buffer, so the merge is a complete picture and the memtable
	// cannot reintroduce versions it just resolved.
	if err := db.flushLocked(); err != nil {
		return result, fmt.Errorf("flush before compaction: %w", err)
	}

	if len(db.segments) < 2 {
		return result, nil
	}

	// Oldest first so newer records overwrite older; the index is newest first.
	merged := make(map[string]memtable.Entry)
	for _, segment := range slices.Backward(db.segments) {
		if err := segment.ForEach(func(rec storage.Record) error {
			merged[string(rec.Key)] = memtable.Entry{
				Value:   rec.Value,
				Deleted: rec.Tombstone,
			}
			result.RecordsRead++
			return nil
		}); err != nil {
			// Abort without touching anything. This is the failure that used to
			// be swallowed: a short read passed for a successful end of file.
			return result, fmt.Errorf("compaction aborted, no segments removed: %w", err)
		}

		if info, err := os.Stat(segment.Path()); err == nil {
			result.BytesBefore += info.Size()
		}
	}

	surviving := make(map[string]memtable.Entry, len(merged))
	for k, entry := range merged {
		if entry.Deleted {
			result.TombstonesPurged++
			continue
		}
		surviving[k] = entry
	}
	result.RecordsWritten = len(surviving)
	result.SegmentsMerged = len(db.segments)

	name := db.nextSegmentName()
	path := filepath.Join(db.dataDir, name)

	// Written under a temp name and renamed, so the sources are never removed
	// against a partial file.
	if err := storage.WriteSegment(path, surviving); err != nil {
		return result, fmt.Errorf("write compacted segment %s: %w", name, err)
	}

	compacted, err := storage.OpenSegment(path)
	if err != nil {
		// Output does not verify. Leave the sources; the database is still
		// consistent, just un-compacted.
		os.Remove(path)
		return result, fmt.Errorf("verify compacted segment %s: %w", name, err)
	}

	if info, err := os.Stat(path); err == nil {
		result.BytesAfter = info.Size()
	}

	// Every source was read in full and is durably in the replacement.
	obsolete := db.segments
	db.segments = []*storage.Segment{compacted}

	for _, seg := range obsolete {
		if err := os.Remove(seg.Path()); err != nil && !os.IsNotExist(err) {
			// The index is already correct, so this is wasted space rather than
			// a correctness problem. Report it without failing the compaction.
			return result, fmt.Errorf("compaction succeeded but %s could not be removed: %w",
				seg.Name(), err)
		}
	}

	return result, nil
}
