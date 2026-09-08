package engine

import (
	"fmt"

	"github.com/Adityarya11/StrataKV/v2/internal/storage"
)

// Batch groups writes so they are applied atomically and share one fsync.
//
// Two reasons to use one. Cost: an fsync is milliseconds while encoding a
// record is microseconds, so a hundred writes in a batch cost roughly one
// write's worth of durability overhead instead of a hundred. Correctness: the
// group is all-or-nothing, so a crash mid-batch leaves the database as though
// the batch never happened.
//
// A Batch buffers in memory until Commit, so keep them to a size you are happy
// to hold. A Batch is not safe for concurrent use, though the DB it commits to
// is.
type Batch struct {
	db      *DB
	records []storage.Record
	bytes   int
}

// NewBatch starts an empty batch against db.
func (db *DB) NewBatch() *Batch {
	return &Batch{db: db}
}

// Put stages a key-value write. Key and value are copied, so the caller may
// reuse both buffers immediately.
func (b *Batch) Put(key, value []byte) error {
	if len(key) == 0 {
		return ErrEmptyKey
	}

	b.records = append(b.records, storage.Record{
		Key:   append([]byte(nil), key...),
		Value: append([]byte(nil), value...),
	})
	b.bytes += len(key) + len(value)

	return nil
}

// Delete stages a tombstone.
func (b *Batch) Delete(key []byte) error {
	if len(key) == 0 {
		return ErrEmptyKey
	}

	b.records = append(b.records, storage.Record{
		Tombstone: true,
		Key:       append([]byte(nil), key...),
	})
	b.bytes += len(key)

	return nil
}

// Len reports how many writes are staged.
func (b *Batch) Len() int { return len(b.records) }

// Size estimates the staged key and value bytes, excluding record overhead.
func (b *Batch) Size() int { return b.bytes }

// Reset discards everything staged, so the Batch can be reused.
func (b *Batch) Reset() {
	b.records = b.records[:0]
	b.bytes = 0
}

// Commit applies the batch.
//
// Every staged write reaches the log as one atomic group followed by at most
// one fsync. On success the batch is reset and can be filled again. On failure
// nothing is applied and the staged writes are left in place, so the caller can
// retry.
//
// Later writes to the same key within one batch win, exactly as they would if
// the writes were made separately in the same order.
func (b *Batch) Commit() error {
	if len(b.records) == 0 {
		return nil
	}

	db := b.db

	db.mu.Lock()
	defer db.mu.Unlock()

	if err := db.wal.Append(b.records, db.opts.Sync == SyncAlways); err != nil {
		return fmt.Errorf("log batch of %d writes: %w", len(b.records), err)
	}

	for _, rec := range b.records {
		if rec.Tombstone {
			db.mem.Delete(rec.Key)
		} else {
			db.mem.Put(rec.Key, rec.Value)
		}
	}

	b.Reset()

	return db.maybeFlushLocked()
}
