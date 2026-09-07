// This is AI generated Code, I recently got Claude Code and hence using it to
// the fullest.

package storage

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// collect replays a WAL into a slice, which is easier to assert against than
// the callback the engine actually uses.
func collect(t *testing.T, w *WAL) ([]Record, RecoveryReport) {
	t.Helper()

	var got []Record
	report, err := w.Recover(func(tombstone bool, key, value []byte) {
		got = append(got, Record{
			Tombstone: tombstone,
			Key:       append([]byte(nil), key...),
			Value:     append([]byte(nil), value...),
		})
	})
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}

	return got, report
}

func newWAL(t *testing.T) (*WAL, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.wal")
	w, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("OpenWAL: %v", err)
	}
	t.Cleanup(func() { w.Close() })

	return w, path
}

func TestWALRoundTrip(t *testing.T) {
	w, path := newWAL(t)

	if err := w.Write(false, []byte("a"), []byte("1")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Write(false, []byte("b"), []byte("2")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Write(true, []byte("a"), nil); err != nil {
		t.Fatalf("Write tombstone: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	got, report := collect(t, reopened)

	if len(got) != 3 {
		t.Fatalf("replayed %d records, want 3", len(got))
	}
	if report.DiscardedBytes != 0 {
		t.Errorf("discarded %d bytes from an intact log", report.DiscardedBytes)
	}
	if !got[2].Tombstone || string(got[2].Key) != "a" {
		t.Errorf("third record = %+v, want tombstone for \"a\"", got[2])
	}
}

func TestRecoverTornTail(t *testing.T) {
	// The regression that motivated this work. A crash during Write leaves a
	// record ending mid-stream. Recovery used to return that as an error, so
	// Open failed and the database could not be mounted at all -- after
	// exactly the failure a write-ahead log exists to survive.
	//
	// Every byte of the tail is tried, because where a crash lands inside a
	// record is arbitrary.
	for cut := 1; cut <= 12; cut++ {
		t.Run(fmt.Sprintf("truncated by %d bytes", cut), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "torn.wal")

			w, err := OpenWAL(path)
			if err != nil {
				t.Fatalf("OpenWAL: %v", err)
			}
			for i := range 5 {
				if err := w.Write(false, fmt.Appendf(nil, "key%d", i), []byte("value")); err != nil {
					t.Fatalf("Write: %v", err)
				}
			}
			w.Close()

			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat: %v", err)
			}
			if err := os.Truncate(path, info.Size()-int64(cut)); err != nil {
				t.Fatalf("truncate: %v", err)
			}

			reopened, err := OpenWAL(path)
			if err != nil {
				t.Fatalf("a torn tail must not prevent opening the log: %v", err)
			}
			defer reopened.Close()

			got, report := collect(t, reopened)

			// The four records before the damaged one are protected by their
			// checksums and must all survive.
			if len(got) != 4 {
				t.Fatalf("replayed %d records, want the 4 that precede the torn one", len(got))
			}
			if report.DiscardedBytes == 0 {
				t.Error("report claims nothing was discarded, but the tail was cut")
			}
			if report.TruncationCause == nil {
				t.Error("report gives no reason for discarding the tail")
			}

			// The log must be usable for appends immediately afterwards,
			// otherwise recovery has only moved the failure later.
			if err := reopened.Write(false, []byte("after"), []byte("recovery")); err != nil {
				t.Fatalf("append after recovery: %v", err)
			}
		})
	}
}

func TestRecoverDetectsBitFlip(t *testing.T) {
	// Truncation is the common failure; silent corruption is the dangerous
	// one. A single flipped bit in a value must be caught by the checksum
	// rather than replayed as though it were the value that was written.
	path := filepath.Join(t.TempDir(), "flipped.wal")

	w, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("OpenWAL: %v", err)
	}
	if err := w.Write(false, []byte("intact"), []byte("first")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Write(false, []byte("damaged"), []byte("second")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	w.Close()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	raw[len(raw)-1] ^= 0x01 // flip one bit in the last value
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	reopened, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("OpenWAL: %v", err)
	}
	defer reopened.Close()

	got, report := collect(t, reopened)

	if len(got) != 1 || string(got[0].Key) != "intact" {
		t.Fatalf("replayed %d records, want only the undamaged one", len(got))
	}
	if !errors.Is(report.TruncationCause, ErrCorrupt) {
		t.Errorf("TruncationCause = %v, want ErrCorrupt", report.TruncationCause)
	}
}

func TestRecoverZeroFilledTail(t *testing.T) {
	// Some filesystems extend a file with a run of zeros after a crash rather
	// than leaving it short. Those bytes decode as a record with a zero-length
	// key, which the codec rejects outright.
	path := filepath.Join(t.TempDir(), "zeros.wal")

	w, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("OpenWAL: %v", err)
	}
	if err := w.Write(false, []byte("real"), []byte("record")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	w.Close()

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	if _, err := f.Write(make([]byte, 512)); err != nil {
		t.Fatalf("append zeros: %v", err)
	}
	f.Close()

	reopened, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("OpenWAL: %v", err)
	}
	defer reopened.Close()

	got, report := collect(t, reopened)

	if len(got) != 1 {
		t.Fatalf("replayed %d records, want 1", len(got))
	}
	if report.DiscardedBytes != 512 {
		t.Errorf("discarded %d bytes, want the 512 zero bytes", report.DiscardedBytes)
	}
}

func TestLegacyWALMigration(t *testing.T) {
	// A database written by the previous release has a headerless, unchecksummed
	// log. Its unflushed writes must survive the upgrade, and the file must
	// come out in the current format so it is protected from then on.
	path := filepath.Join(t.TempDir(), "legacy.wal")

	var legacy []byte
	appendLegacy := func(tombstone bool, key, value string) {
		hdr := make([]byte, 9)
		if tombstone {
			hdr[0] = 1
			value = ""
		}
		binary.LittleEndian.PutUint32(hdr[1:5], uint32(len(key)))
		binary.LittleEndian.PutUint32(hdr[5:9], uint32(len(value)))

		legacy = append(legacy, hdr...)
		legacy = append(legacy, key...)
		legacy = append(legacy, value...)
	}

	appendLegacy(false, "alpha", "one")
	appendLegacy(false, "beta", "two")
	appendLegacy(true, "alpha", "")

	if err := os.WriteFile(path, legacy, 0o644); err != nil {
		t.Fatalf("seed legacy WAL: %v", err)
	}

	w, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("OpenWAL on legacy file: %v", err)
	}

	got, report := collect(t, w)

	if !report.MigratedFromLegacy {
		t.Error("report does not mention the migration")
	}
	if len(got) != 3 {
		t.Fatalf("replayed %d records, want 3", len(got))
	}
	if !got[2].Tombstone {
		t.Error("legacy tombstone did not survive migration")
	}

	if err := w.Write(false, []byte("gamma"), []byte("three")); err != nil {
		t.Fatalf("append after migration: %v", err)
	}
	w.Close()

	// Reopening must now take the v1 path and see all four records.
	reopened, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("reopen migrated WAL: %v", err)
	}
	defer reopened.Close()

	got, report = collect(t, reopened)

	if report.MigratedFromLegacy {
		t.Error("file was migrated twice; the header was not written")
	}
	if len(got) != 4 {
		t.Fatalf("replayed %d records after migration, want 4", len(got))
	}
}

func TestResetKeepsLogUsable(t *testing.T) {
	w, path := newWAL(t)

	if err := w.Write(false, []byte("before"), []byte("flush")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if err := w.Write(false, []byte("after"), []byte("flush")); err != nil {
		t.Fatalf("Write after Reset: %v", err)
	}
	w.Close()

	reopened, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	got, _ := collect(t, reopened)

	if len(got) != 1 || string(got[0].Key) != "after" {
		t.Fatalf("after Reset the log replayed %+v, want only the post-reset write", got)
	}
}

func TestRejectsFutureVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.wal")

	hdr := append([]byte(walMagic), 0, 0)
	binary.LittleEndian.PutUint16(hdr[len(walMagic):], walVersion+1)
	if err := os.WriteFile(path, hdr, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Guessing at an unknown layout is worse than refusing to open it.
	if _, err := OpenWAL(path); !errors.Is(err, ErrUnknownWALVersion) {
		t.Fatalf("OpenWAL error = %v, want ErrUnknownWALVersion", err)
	}
}
