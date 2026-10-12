package engine

import (
	"fmt"

	"github.com/Adityarya11/StrataKV/v2/internal/storage"
)

// Batch collects writes and applies them together: all or nothing, with one
// fsync for the whole group. The fsync is the expensive part of a write, so a
// batch costs far less than the same writes made one at a time.
//
// A Batch lives in memory until Commit. It isn't safe for concurrent use,
// though the DB it commits to is.
type Batch struct {
	db      *DB
	records []storage.Record
	bytes   int
}

// NewBatch returns an empty batch for db.
func (db *DB) NewBatch() *Batch {
	return &Batch{db: db}
}

// Put adds a write to the batch. Key and value are copied.
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

// Delete adds a tombstone to the batch.
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

// Len is the number of writes in the batch.
func (b *Batch) Len() int { return len(b.records) }

// Size is the total size of the keys and values in the batch.
func (b *Batch) Size() int { return b.bytes }

// Reset empties the batch so it can be reused.
func (b *Batch) Reset() {
	b.records = b.records[:0]
	b.bytes = 0
}

// Commit writes the whole batch to the log as one group and, under SyncAlways,
// fsyncs once. On success the batch is emptied for reuse. On failure nothing
// was applied and the writes are still in the batch, so you can retry. If a key
// appears more than once, the last write wins.
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
