// This is AI generated Code, I recently got Claude Code and hence using it to
// the fullest.

// Package integration exercises StrataKV through its public API only, the way
// a consumer such as blan-backend does. Nothing here reaches into internals.
//
// Unit tests that need unexported identifiers live beside the code they cover
// (internal/filter, internal/storage); Go requires same-package tests to sit in
// the same directory. This package holds the end-to-end proofs instead.
package integration

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Adityarya11/StrataKV/engine"
)

func open(t *testing.T, dir string) *engine.DB {
	t.Helper()

	db, err := engine.Open(dir)
	if err != nil {
		t.Fatalf("Open(%s): %v", dir, err)
	}

	return db
}

func mustPut(t *testing.T, db *engine.DB, key, value string) {
	t.Helper()

	if err := db.Put([]byte(key), []byte(value)); err != nil {
		t.Fatalf("Put(%q): %v", key, err)
	}
}

func mustGet(t *testing.T, db *engine.DB, key string) (string, bool) {
	t.Helper()

	value, found, err := db.Get([]byte(key))
	if err != nil {
		t.Fatalf("Get(%q): %v", key, err)
	}

	return string(value), found
}

// segments lists the segment files in a data directory.
func segments(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".seg") {
			out = append(out, e.Name())
		}
	}

	return out
}

// fillSegments writes enough data to force at least n memtable flushes.
func fillSegments(t *testing.T, db *engine.DB, n int) {
	t.Helper()

	payload := strings.Repeat("x", 4096)
	for i := range n * 300 {
		mustPut(t, db, fmt.Sprintf("key%05d", i), payload)
	}
}

func TestPutGetDelete(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir)
	defer db.Close()

	mustPut(t, db, "user:101", "aditya")

	if got, found := mustGet(t, db, "user:101"); !found || got != "aditya" {
		t.Fatalf("Get = %q, %v; want \"aditya\", true", got, found)
	}

	mustPut(t, db, "user:101", "arya")
	if got, _ := mustGet(t, db, "user:101"); got != "arya" {
		t.Errorf("after overwrite Get = %q, want \"arya\"", got)
	}

	if err := db.Delete([]byte("user:101")); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, found := mustGet(t, db, "user:101"); found {
		t.Error("deleted key still reads as present")
	}

	if _, found := mustGet(t, db, "never-written"); found {
		t.Error("absent key reads as present")
	}

	mustPut(t, db, "present", "value")
	for key, want := range map[string]bool{"present": true, "never-written": false} {
		got, err := db.Has([]byte(key))
		if err != nil {
			t.Fatalf("Has(%q): %v", key, err)
		}
		if got != want {
			t.Errorf("Has(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestEmptyKeyRejected(t *testing.T) {
	db := open(t, t.TempDir())
	defer db.Close()

	if err := db.Put(nil, []byte("v")); !errors.Is(err, engine.ErrEmptyKey) {
		t.Errorf("Put(nil) = %v, want ErrEmptyKey", err)
	}
	if _, _, err := db.Get([]byte("")); !errors.Is(err, engine.ErrEmptyKey) {
		t.Errorf("Get(\"\") = %v, want ErrEmptyKey", err)
	}
	if err := db.Delete([]byte("")); !errors.Is(err, engine.ErrEmptyKey) {
		t.Errorf("Delete(\"\") = %v, want ErrEmptyKey", err)
	}
}

func TestDurabilityAcrossReopen(t *testing.T) {
	dir := t.TempDir()

	db := open(t, dir)
	mustPut(t, db, "persisted", "value")
	mustPut(t, db, "removed", "value")
	if err := db.Delete([]byte("removed")); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := open(t, dir)
	defer reopened.Close()

	if got, found := mustGet(t, reopened, "persisted"); !found || got != "value" {
		t.Errorf("after reopen Get = %q, %v; want \"value\", true", got, found)
	}
	if _, found := mustGet(t, reopened, "removed"); found {
		t.Error("a delete did not survive the reopen")
	}
}

func TestOpenSurvivesTornWAL(t *testing.T) {
	// A crash during Put leaves a record ending mid-stream. The database must
	// still mount, keep every record that precedes the damage, and accept new
	// writes. Before this fix, Open returned an error and blan-backend's
	// log.Fatalf turned that into a backend that would not boot.
	dir := t.TempDir()

	db := open(t, dir)
	for i := range 20 {
		mustPut(t, db, fmt.Sprintf("key%02d", i), "value")
	}
	db.Close()

	wal := filepath.Join(dir, "0001.wal")
	info, err := os.Stat(wal)
	if err != nil {
		t.Fatalf("stat WAL: %v", err)
	}
	if err := os.Truncate(wal, info.Size()-5); err != nil {
		t.Fatalf("truncate WAL: %v", err)
	}

	reopened, err := engine.Open(dir)
	if err != nil {
		t.Fatalf("Open after a torn WAL write: %v", err)
	}
	defer reopened.Close()

	survived := 0
	for i := range 20 {
		if _, found := mustGet(t, reopened, fmt.Sprintf("key%02d", i)); found {
			survived++
		}
	}
	if survived < 19 {
		t.Errorf("%d of 20 keys survived; only the torn record should be lost", survived)
	}

	if stats := reopened.Stats(); stats.Recovery.DiscardedBytes == 0 {
		t.Error("Stats does not report the discarded tail")
	}

	mustPut(t, reopened, "after-recovery", "ok")
	if _, found := mustGet(t, reopened, "after-recovery"); !found {
		t.Error("database is not writable after recovery")
	}
}

func TestCompactionMergesAndPurges(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir)
	defer db.Close()

	fillSegments(t, db, 3)

	// Overwrite the first quarter of the key range so the merge has genuinely
	// superseded versions to discard, not just distinct keys to copy across.
	payload := strings.Repeat("y", 4096)
	for i := range 225 {
		mustPut(t, db, fmt.Sprintf("key%05d", i), payload)
	}

	mustPut(t, db, "survivor", "keep me")
	mustPut(t, db, "doomed", "delete me")
	if err := db.Delete([]byte("doomed")); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	before := len(segments(t, dir))
	if before < 2 {
		t.Fatalf("only %d segments; the workload did not force enough flushes", before)
	}

	result, err := db.Compact()
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}

	if after := len(segments(t, dir)); after != 1 {
		t.Errorf("%d segments after compaction, want 1", after)
	}
	if result.TombstonesPurged < 1 {
		t.Error("compaction purged no tombstones")
	}
	if result.RecordsWritten >= result.RecordsRead {
		t.Errorf("wrote %d of %d records; compaction reclaimed nothing",
			result.RecordsWritten, result.RecordsRead)
	}

	if got, found := mustGet(t, db, "survivor"); !found || got != "keep me" {
		t.Errorf("Get(survivor) = %q, %v after compaction", got, found)
	}
	if _, found := mustGet(t, db, "doomed"); found {
		t.Error("a purged tombstone let the old value come back to life")
	}

	t.Logf("merged %d segments, %d records in, %d out, %.1f%% of bytes reclaimed",
		result.SegmentsMerged, result.RecordsRead, result.RecordsWritten,
		100*float64(result.BytesBefore-result.BytesAfter)/float64(result.BytesBefore))
}

func TestCompactionRefusesToDestroyData(t *testing.T) {
	// The silent data loss regression, end to end.
	//
	// Truncating a segment used to be invisible: ReadSegment returned nil on a
	// short read, so compaction merged the surviving prefix, wrote it out, and
	// deleted the originals -- losing 128 of 600 keys while returning success.
	// Compaction must now abort and leave every source file untouched.
	dir := t.TempDir()

	db := open(t, dir)
	fillSegments(t, db, 3)
	db.Close()

	names := segments(t, dir)
	if len(names) < 2 {
		t.Fatalf("only %d segments; need at least 2 to compact", len(names))
	}

	victim := filepath.Join(dir, names[0])
	intact, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("read segment: %v", err)
	}

	if err := os.WriteFile(victim, intact[:len(intact)/2], 0o644); err != nil {
		t.Fatalf("truncate segment: %v", err)
	}

	// A truncated segment has no trailer, so Open refuses to mount rather than
	// serving a silently shortened view of the data.
	if _, err := engine.Open(dir); err == nil {
		t.Fatal("Open accepted a database containing a truncated segment")
	}

	// Restore the real bytes, then corrupt one in place. This is damage Open
	// cannot see -- header and trailer still verify -- so the protection has to
	// hold inside Compact, on the record checksum.
	damaged := append([]byte(nil), intact...)
	damaged[64] ^= 0xFF
	if err := os.WriteFile(victim, damaged, 0o644); err != nil {
		t.Fatalf("write segment: %v", err)
	}

	reopened, err := engine.Open(dir)
	if err != nil {
		t.Fatalf("Open with an intact trailer: %v", err)
	}
	defer reopened.Close()

	if _, err := reopened.Compact(); err == nil {
		t.Fatal("Compact reported success over a corrupt segment")
	}

	// The critical assertion: every source file is still on disk. Compact
	// flushes the recovered buffer before merging, so the segment count can
	// legitimately grow -- what must never happen is a source going missing.
	for _, name := range names {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("a failed compaction removed source segment %s: %v", name, err)
		}
	}
}

func TestReadsSpanMemtableAndSegments(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir)
	defer db.Close()

	// Written first, so it ends up in an early segment.
	mustPut(t, db, "old-key", "original")
	fillSegments(t, db, 2)

	// Overwritten last, so the newest version is still in the buffer.
	mustPut(t, db, "old-key", "updated")

	if got, _ := mustGet(t, db, "old-key"); got != "updated" {
		t.Errorf("Get = %q, want \"updated\"; the read path served a stale version", got)
	}

	if err := db.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Now both versions are on disk in different segments, and newest-first
	// ordering is the only thing that resolves them correctly.
	if got, _ := mustGet(t, db, "old-key"); got != "updated" {
		t.Errorf("after flush Get = %q, want \"updated\"", got)
	}
}

func TestStatsReflectDatabaseShape(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir)
	defer db.Close()

	mustPut(t, db, "buffered", "value")

	stats := db.Stats()
	if stats.MemTableKeys != 1 {
		t.Errorf("MemTableKeys = %d, want 1", stats.MemTableKeys)
	}
	if stats.Segments != 0 {
		t.Errorf("Segments = %d before any flush, want 0", stats.Segments)
	}

	fillSegments(t, db, 2)

	if stats = db.Stats(); stats.Segments == 0 {
		t.Error("Segments is still 0 after the buffer should have flushed")
	}
	if stats.LegacySegments != 0 {
		t.Errorf("LegacySegments = %d for a freshly created database", stats.LegacySegments)
	}
}
