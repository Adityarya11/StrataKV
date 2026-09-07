package storage

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// WAL file layout:
//
//	[ "STRATAWL" 8B | version uint16 2B ] [ record ] [ record ] ...

const (
	walMagic      = "STRATAWL"
	walVersion    = uint16(1)
	walHeaderSize = len(walMagic) + 2
)

// ErrUnknownWALVersion reports a log written by a newer build of StrataKV.
var ErrUnknownWALVersion = errors.New("stratakv/storage: unrecognised WAL format version")

// WAL is an append-only log of key-value mutations. Safe for concurrent use.
type WAL struct {
	mu   sync.Mutex
	file *os.File
	path string
	size int64 // bytes currently in the file, header included
}

// RecoveryReport describes what Recover found. DiscardedBytes above zero means
// a previous process died mid-write: normal once, alarming if it repeats.
type RecoveryReport struct {
	RecordsApplied     int
	DiscardedBytes     int64
	TruncationCause    error
	MigratedFromLegacy bool
}

// OpenWAL opens or creates the log at path. A new file gets a format header, an
// existing v1 file is validated, and a pre-v1 file is flagged for migration.
func OpenWAL(path string) (*WAL, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open WAL %s: %w", path, err)
	}

	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("stat WAL %s: %w", path, err)
	}

	w := &WAL{file: f, path: path, size: info.Size()}

	if info.Size() == 0 {
		if err := w.writeHeader(); err != nil {
			f.Close()
			return nil, err
		}
	} else if err := w.validateHeader(); err != nil {
		f.Close()
		return nil, err
	}

	// Leave the handle positioned to append. writeHeader uses WriteAt, which
	// does not move the offset, so without this a caller that writes before
	// recovering would overwrite the header it just wrote.
	if err := w.seekToEnd(); err != nil {
		f.Close()
		return nil, err
	}

	return w, nil
}

func (w *WAL) writeHeader() error {
	hdr := make([]byte, 0, walHeaderSize)
	hdr = append(hdr, walMagic...)
	hdr = binary.LittleEndian.AppendUint16(hdr, walVersion)

	if _, err := w.file.WriteAt(hdr, 0); err != nil {
		return fmt.Errorf("write WAL header: %w", err)
	}
	if err := w.file.Sync(); err != nil {
		return fmt.Errorf("sync WAL header: %w", err)
	}

	w.size = int64(walHeaderSize)
	return nil
}

// validateHeader checks magic and version. A missing magic is a pre-v1 log,
// which is a migration rather than an error.
func (w *WAL) validateHeader() error {
	hdr := make([]byte, walHeaderSize)
	if _, err := w.file.ReadAt(hdr, 0); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read WAL header: %w", err)
	}

	if string(hdr[:len(walMagic)]) != walMagic {
		return nil // legacy; Recover acts on it
	}

	if v := binary.LittleEndian.Uint16(hdr[len(walMagic):]); v != walVersion {
		return fmt.Errorf("%w: file declares version %d, this build understands %d",
			ErrUnknownWALVersion, v, walVersion)
	}

	return nil
}

func (w *WAL) isLegacy() (bool, error) {
	if w.size < int64(len(walMagic)) {
		return false, nil
	}

	magic := make([]byte, len(walMagic))
	if _, err := w.file.ReadAt(magic, 0); err != nil {
		return false, fmt.Errorf("read WAL magic: %w", err)
	}

	return string(magic) != walMagic, nil
}

// Write appends one mutation and fsyncs before returning. A write that is only
// in the page cache is a write the OS is free to lose.
func (w *WAL) Write(tombstone bool, key, value []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	buf := AppendRecord(make([]byte, 0, EncodedLen(key, value)), tombstone, key, value)

	n, err := w.file.Write(buf)
	w.size += int64(n)
	if err != nil {
		return fmt.Errorf("append to WAL: %w", err)
	}

	if err := w.file.Sync(); err != nil {
		return fmt.Errorf("sync WAL: %w", err)
	}

	return nil
}

// Recover replays the log into apply, truncating an unreadable tail rather than
// failing. A torn tail is what a crash during Write leaves behind; refusing to
// open because of it makes the database unmountable after exactly the failure
// the log exists to survive. Earlier records are protected by their checksums.
func (w *WAL) Recover(apply func(tombstone bool, key, value []byte)) (RecoveryReport, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	legacy, err := w.isLegacy()
	if err != nil {
		return RecoveryReport{}, err
	}
	if legacy {
		return w.recoverLegacy(apply)
	}

	report, err := w.replay(int64(walHeaderSize), apply)
	if err != nil {
		return report, err
	}

	return report, w.seekToEnd()
}

func (w *WAL) replay(offset int64, apply func(bool, []byte, []byte)) (RecoveryReport, error) {
	var report RecoveryReport

	if _, err := w.file.Seek(offset, io.SeekStart); err != nil {
		return report, fmt.Errorf("seek past WAL header: %w", err)
	}

	reader := io.Reader(w.file)
	good := offset

	for {
		rec, n, err := ReadRecord(reader)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			report.TruncationCause = err
			break
		}

		apply(rec.Tombstone, rec.Key, rec.Value)
		report.RecordsApplied++
		good += int64(n)
	}

	if good < w.size {
		report.DiscardedBytes = w.size - good
		if err := w.file.Truncate(good); err != nil {
			return report, fmt.Errorf("truncate WAL to last valid record at %d: %w", good, err)
		}
		w.size = good
	}

	return report, nil
}

func (w *WAL) seekToEnd() error {
	if _, err := w.file.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("seek to WAL end: %w", err)
	}
	return nil
}

// recoverLegacy replays a pre-v1 log -- [ flags 1B | keyLen 4B | valLen 4B |
// key | value ], no magic, no checksums -- and rewrites it in the current
// format so an existing database keeps its unflushed writes across the upgrade.
func (w *WAL) recoverLegacy(apply func(bool, []byte, []byte)) (RecoveryReport, error) {
	report := RecoveryReport{MigratedFromLegacy: true}

	if _, err := w.file.Seek(0, io.SeekStart); err != nil {
		return report, fmt.Errorf("rewind legacy WAL: %w", err)
	}

	var (
		records []Record
		good    int64
		reader  = io.Reader(w.file)
	)

	for {
		rec, n, err := readLegacyRecord(reader)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			report.TruncationCause = err
			break
		}

		records = append(records, rec)
		good += int64(n)
	}

	report.DiscardedBytes = w.size - good
	report.RecordsApplied = len(records)

	for _, rec := range records {
		apply(rec.Tombstone, rec.Key, rec.Value)
	}

	if err := w.rewrite(records); err != nil {
		return report, err
	}

	return report, nil
}

func readLegacyRecord(r io.Reader) (Record, int, error) {
	const legacyHeaderSize = 9

	var hdr [legacyHeaderSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return Record{}, 0, io.EOF
		}
		return Record{}, 0, fmt.Errorf("%w: legacy header: %v", ErrTorn, err)
	}

	tombstone := hdr[0] == 1
	keyLen := binary.LittleEndian.Uint32(hdr[1:5])
	valLen := binary.LittleEndian.Uint32(hdr[5:9])

	if keyLen == 0 || uint64(keyLen)+uint64(valLen) > MaxRecordSize {
		return Record{}, 0, fmt.Errorf("%w: legacy lengths key=%d val=%d",
			ErrCorrupt, keyLen, valLen)
	}

	key := make([]byte, keyLen)
	if _, err := io.ReadFull(r, key); err != nil {
		return Record{}, 0, fmt.Errorf("%w: legacy key: %v", ErrTorn, err)
	}

	rec := Record{Tombstone: tombstone, Key: key}
	if !tombstone {
		rec.Value = make([]byte, valLen)
		if _, err := io.ReadFull(r, rec.Value); err != nil {
			return Record{}, 0, fmt.Errorf("%w: legacy value: %v", ErrTorn, err)
		}
	}

	return rec, legacyHeaderSize + int(keyLen) + int(valLen), nil
}

// rewrite replaces the log's contents via a temporary file and a rename, so a
// crash midway leaves the original intact.
func (w *WAL) rewrite(records []Record) error {
	tmp := w.path + ".rewrite"

	f, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create WAL rewrite file: %w", err)
	}

	buf := make([]byte, 0, walHeaderSize+4096)
	buf = append(buf, walMagic...)
	buf = binary.LittleEndian.AppendUint16(buf, walVersion)
	for _, rec := range records {
		buf = AppendRecord(buf, rec.Tombstone, rec.Key, rec.Value)
	}

	if _, err := f.Write(buf); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write WAL rewrite file: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("sync WAL rewrite file: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close WAL rewrite file: %w", err)
	}

	// Windows refuses to replace a file that is still open, so close first.
	if err := w.file.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close WAL before rewrite: %w", err)
	}
	if err := os.Rename(tmp, w.path); err != nil {
		return fmt.Errorf("install rewritten WAL: %w", err)
	}
	syncDir(filepath.Dir(w.path))

	reopened, err := os.OpenFile(w.path, os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("reopen rewritten WAL: %w", err)
	}

	w.file = reopened
	w.size = int64(len(buf))

	return nil
}

// Reset empties the log once its contents are durable in a segment. Truncating
// in place keeps the same file rather than opening a window with no log at all.
func (w *WAL) Reset() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.file.Truncate(0); err != nil {
		return fmt.Errorf("truncate WAL: %w", err)
	}
	if _, err := w.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind WAL: %w", err)
	}
	if err := w.writeHeader(); err != nil {
		return err
	}

	return w.seekToEnd()
}

// Close flushes and closes the log.
func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.file.Sync(); err != nil {
		w.file.Close()
		return fmt.Errorf("sync WAL on close: %w", err)
	}

	return w.file.Close()
}
