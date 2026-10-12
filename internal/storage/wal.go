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

// ErrUnknownWALVersion is returned for a log written by a newer version.
var ErrUnknownWALVersion = errors.New("stratakv/storage: unrecognised WAL format version")

// WAL is an append-only log of key-value mutations. Safe for concurrent use.
type WAL struct {
	mu   sync.Mutex
	file *os.File
	path string
	size int64 // bytes currently in the file, header included
}

// RecoveryReport is what Recover found. DiscardedBytes above zero means an
// earlier run died mid-write: expected now and then, a worry if it keeps
// happening.
type RecoveryReport struct {
	RecordsApplied     int
	DiscardedBytes     int64
	TruncationCause    error
	MigratedFromLegacy bool
}

// OpenWAL opens the log at path, creating it with a header if it's new. An old
// log without a header is accepted and migrated during Recover.
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

	// park the offset at the end. writeHeader uses WriteAt, which doesn't move
	// it, so a write before Recover would land on top of the header
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

// validateHeader checks the magic and version. No magic means a log from before
// headers existed, which gets migrated, not rejected.
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

// Write appends one record and fsyncs before returning.
func (w *WAL) Write(tombstone bool, key, value []byte) error {
	return w.Append([]Record{{Tombstone: tombstone, Key: key, Value: value}}, true)
}

// Append writes the records as one group. Every record but the last is marked
// pending, so recovery applies all of them or none. It is a single write and at
// most one fsync, which is why a batch is cheaper than separate writes. With
// sync false the data sits in the page cache until the next Sync, Reset or
// Close.
func (w *WAL) Append(recs []Record, sync bool) error {
	if len(recs) == 0 {
		return nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	size := 0
	for _, rec := range recs {
		size += EncodedLen(rec.Key, rec.Value)
	}

	buf := make([]byte, 0, size)
	for i, rec := range recs {
		rec.Pending = i < len(recs)-1
		buf = AppendRecord(buf, rec)
	}

	n, err := w.file.Write(buf)
	w.size += int64(n)
	if err != nil {
		return fmt.Errorf("append to WAL: %w", err)
	}

	if !sync {
		return nil
	}
	if err := w.file.Sync(); err != nil {
		return fmt.Errorf("sync WAL: %w", err)
	}

	return nil
}

// Sync flushes the log to disk.
func (w *WAL) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.file.Sync(); err != nil {
		return fmt.Errorf("sync WAL: %w", err)
	}

	return nil
}

// Recover replays the log through apply. If the tail can't be read it truncates
// it and carries on. A torn tail is what a crash mid-write leaves, and
// refusing to open because of it would defeat the point of the log. Earlier
// records are covered by their checksums.
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

	// pending records belong to a batch whose commit hasn't been read yet.
	// They're held back so a log that ends mid-batch applies none of it.
	var (
		pending  []Record
		uncommit int64
	)

	for {
		rec, n, err := ReadRecord(reader)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			report.TruncationCause = err
			break
		}

		if rec.Pending {
			pending = append(pending, rec)
			uncommit += int64(n)
			continue
		}

		for _, held := range pending {
			apply(held.Tombstone, held.Key, held.Value)
			report.RecordsApplied++
		}
		apply(rec.Tombstone, rec.Key, rec.Value)
		report.RecordsApplied++

		good += uncommit + int64(n)
		pending, uncommit = nil, 0
	}

	// anything still held never got its commit, so the batch was cut off.
	// good stops before it and the truncation below drops it
	if len(pending) > 0 && report.TruncationCause == nil {
		report.TruncationCause = fmt.Errorf("%w: batch of %d records has no commit",
			ErrTorn, len(pending))
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

// recoverLegacy replays a log from before headers and checksums existed
// ([ flags 1B | keyLen 4B | valLen 4B | key | value ]) and rewrites it in the
// current format, so unflushed writes survive the upgrade.
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

// rewrite replaces the log through a temp file and a rename, so a crash partway
// leaves the original alone.
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
		buf = AppendRecord(buf, rec)
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

	// Windows won't replace a file that's still open
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

// Reset empties the log once its contents are safe in a segment. It truncates
// in place, so there is never a moment with no log file.
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

// Close syncs and closes the log.
func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.file.Sync(); err != nil {
		w.file.Close()
		return fmt.Errorf("sync WAL on close: %w", err)
	}

	return w.file.Close()
}
