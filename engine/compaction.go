package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/Adityarya11/StrataKV/v2/internal/memtable"
	"github.com/Adityarya11/StrataKV/v2/internal/storage"
)

// CompactionResult is what a Compact call did.
type CompactionResult struct {
	SegmentsMerged   int
	RecordsRead      int // overwritten versions and tombstones included
	RecordsWritten   int // what survived; the difference is what was reclaimed
	TombstonesPurged int

	BytesBefore int64
	BytesAfter  int64
}

// Compact merges every segment into one. It keeps the newest version of each
// key and drops tombstones. Dropping them is only safe because this is a full
// merge: every segment that could hold an older value is merged in the same
// pass.
//
// It holds the write lock the whole time, so reads and writes wait.
func (db *DB) Compact() (CompactionResult, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	var result CompactionResult

	// flush first so the memtable is part of the merge
	if err := db.flushLocked(); err != nil {
		return result, fmt.Errorf("flush before compaction: %w", err)
	}

	if len(db.segments) < 2 {
		return result, nil
	}

	// oldest first so newer records overwrite older ones (db.segments is
	// newest first)
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
			// a read error must never lead to deleting anything: stop here and
			// leave every file in place
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

	// WriteSegment goes through a temp file and a rename, so the new segment is
	// never half written
	if err := storage.WriteSegment(path, surviving, db.opts.FilterFalsePositiveRate); err != nil {
		return result, fmt.Errorf("write compacted segment %s: %w", name, err)
	}

	compacted, err := storage.OpenSegment(path)
	if err != nil {
		// the new segment doesn't open cleanly, so keep the sources and drop it
		os.Remove(path)
		return result, fmt.Errorf("verify compacted segment %s: %w", name, err)
	}

	if info, err := os.Stat(path); err == nil {
		result.BytesAfter = info.Size()
	}

	// everything in the sources is now in the new segment
	obsolete := db.segments
	db.segments = []*storage.Segment{compacted}

	for _, seg := range obsolete {
		if err := os.Remove(seg.Path()); err != nil && !os.IsNotExist(err) {
			// the index is already right, so a leftover file is only wasted
			// space; report it
			return result, fmt.Errorf("compaction succeeded but %s could not be removed: %w",
				seg.Name(), err)
		}
	}

	return result, nil
}
