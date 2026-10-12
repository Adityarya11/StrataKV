// refactored using AI.

package storage

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Adityarya11/StrataKV/v2/internal/filter"
	"github.com/Adityarya11/StrataKV/v2/internal/memtable"
)

// Segment file layout:
//
//	[ "STRATASG" 8B | version uint16 2B ]   header
//	[ record ] [ record ] ...               data
//	[ encoded Bloom filter ]                index
//	[ trailer, 28B, fixed width ]           footer

const (
	segmentMagic   = "STRATASG"
	segmentVersion = uint16(1)
	segmentHeader  = len(segmentMagic) + 2

	trailerMagic = "STRATAFT"
	trailerSize  = 8 + 4 + 4 + 4 + len(trailerMagic) // 28

	// SegmentSuffix is the extension of segment files.
	SegmentSuffix = ".seg"
)

var (
	// ErrIncompleteSegment means the trailer is missing or invalid, so the file
	// was never finished.
	ErrIncompleteSegment = errors.New("stratakv/storage: segment is incomplete")

	// ErrUnknownSegmentVersion means the segment came from a newer version.
	ErrUnknownSegmentVersion = errors.New("stratakv/storage: unrecognised segment format version")
)

// Segment is a handle on one immutable segment file. Opening it loads the
// header, trailer and filter. Records are read when needed.
type Segment struct {
	path       string
	name       string
	seq        int64
	bloom      *filter.BloomFilter
	entryCount int
	dataEnd    int64 // offset one past the last record
	dataStart  int64
	legacy     bool
}

// SegmentName builds a segment filename. The zero padding makes name order
// match sequence order.
func SegmentName(seq int64) string {
	return fmt.Sprintf("%020d%s", seq, SegmentSuffix)
}

// ParseSegmentSequence returns the sequence number in a segment's filename, or
// false if the name isn't a segment. Compare these as numbers, not strings:
// older names aren't zero padded, so a string comparison would order them
// wrongly.
func ParseSegmentSequence(name string) (int64, bool) {
	if !strings.HasSuffix(name, SegmentSuffix) {
		return 0, false
	}

	seq, err := strconv.ParseInt(strings.TrimSuffix(name, SegmentSuffix), 10, 64)
	if err != nil || seq < 0 {
		return 0, false
	}

	return seq, true
}

// WriteSegment writes data as a new segment at path. It writes a temp file and
// renames it once the data is on disk, so a reader never sees a partial segment.
func WriteSegment(path string, data map[string]memtable.Entry, falsePositiveRate float64) error {
	tmp := path + ".tmp"

	f, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create segment %s: %w", tmp, err)
	}

	// remove the temp file on failure so a full disk doesn't leave junk behind
	defer func() {
		if f != nil {
			f.Close()
			os.Remove(tmp)
		}
	}()

	if err := writeSegmentTo(f, data, falsePositiveRate); err != nil {
		return err
	}

	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync segment %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close segment %s: %w", tmp, err)
	}
	f = nil // disarm the cleanup; the file is now complete

	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("install segment %s: %w", path, err)
	}
	syncDir(filepath.Dir(path))

	return nil
}

func writeSegmentTo(w io.Writer, data map[string]memtable.Entry, falsePositiveRate float64) error {
	buf := make([]byte, 0, segmentHeader+len(data)*64)
	buf = append(buf, segmentMagic...)
	buf = binary.LittleEndian.AppendUint16(buf, segmentVersion)

	// sized here, when the exact key count is known
	bloom := filter.NewOptimal(len(data), falsePositiveRate)

	for k, entry := range data {
		key := []byte(k)
		buf = AppendRecord(buf, Record{Tombstone: entry.Deleted, Key: key, Value: entry.Value})
		bloom.Add(key)
	}

	dataEnd := len(buf)

	encoded, err := bloom.MarshalBinary()
	if err != nil {
		return fmt.Errorf("encode segment filter: %w", err)
	}
	buf = append(buf, encoded...)

	buf = appendTrailer(buf, uint64(dataEnd), uint32(len(encoded)), uint32(len(data)), encoded)

	if _, err := w.Write(buf); err != nil {
		return fmt.Errorf("write segment body: %w", err)
	}

	return nil
}

// appendTrailer writes the fixed-size footer. Its checksum covers the trailer
// fields and the encoded filter.
func appendTrailer(dst []byte, bloomOffset uint64, bloomLen, entryCount uint32, bloomBytes []byte) []byte {
	fields := make([]byte, 0, 16)
	fields = binary.LittleEndian.AppendUint64(fields, bloomOffset)
	fields = binary.LittleEndian.AppendUint32(fields, bloomLen)
	fields = binary.LittleEndian.AppendUint32(fields, entryCount)

	sum := crc32Combine(fields, bloomBytes)

	dst = append(dst, fields...)
	dst = binary.LittleEndian.AppendUint32(dst, sum)
	dst = append(dst, trailerMagic...)

	return dst
}

func crc32Combine(a, b []byte) uint32 {
	h := newCRC()
	_, _ = h.Write(a)
	_, _ = h.Write(b)
	return h.Sum32()
}

// OpenSegment opens a segment and loads its trailer and filter. A file that
// fails any structural check is rejected, not read as far as it happens to
// parse.
func OpenSegment(path string) (*Segment, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open segment %s: %w", path, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat segment %s: %w", path, err)
	}

	name := filepath.Base(path)
	seq, _ := ParseSegmentSequence(name)

	seg := &Segment{path: path, name: name, seq: seq}

	legacy, err := isLegacySegment(f, info.Size())
	if err != nil {
		return nil, err
	}
	if legacy {
		return seg, seg.loadLegacy(f, info.Size())
	}

	if err := seg.loadV1(f, info.Size()); err != nil {
		return nil, err
	}

	return seg, nil
}

func isLegacySegment(f *os.File, size int64) (bool, error) {
	if size < int64(len(segmentMagic)) {
		return true, nil // too small for a magic number
	}

	magic := make([]byte, len(segmentMagic))
	if _, err := f.ReadAt(magic, 0); err != nil {
		return false, fmt.Errorf("read segment magic: %w", err)
	}

	return string(magic) != segmentMagic, nil
}

func (s *Segment) loadV1(f *os.File, size int64) error {
	if size < int64(segmentHeader+trailerSize) {
		return fmt.Errorf("%w: %s is %d bytes, smaller than an empty segment",
			ErrIncompleteSegment, s.name, size)
	}

	var version [2]byte
	if _, err := f.ReadAt(version[:], int64(len(segmentMagic))); err != nil {
		return fmt.Errorf("read segment version: %w", err)
	}
	if v := binary.LittleEndian.Uint16(version[:]); v != segmentVersion {
		return fmt.Errorf("%w: %s declares version %d, this build understands %d",
			ErrUnknownSegmentVersion, s.name, v, segmentVersion)
	}

	trailer := make([]byte, trailerSize)
	if _, err := f.ReadAt(trailer, size-int64(trailerSize)); err != nil {
		return fmt.Errorf("read segment trailer: %w", err)
	}

	if string(trailer[trailerSize-len(trailerMagic):]) != trailerMagic {
		return fmt.Errorf("%w: %s has no trailer, so it was never finished being written",
			ErrIncompleteSegment, s.name)
	}

	var (
		bloomOffset = binary.LittleEndian.Uint64(trailer[0:8])
		bloomLen    = binary.LittleEndian.Uint32(trailer[8:12])
		entryCount  = binary.LittleEndian.Uint32(trailer[12:16])
		want        = binary.LittleEndian.Uint32(trailer[16:20])
	)

	indexEnd := int64(bloomOffset) + int64(bloomLen)
	if int64(bloomOffset) < int64(segmentHeader) || indexEnd != size-int64(trailerSize) {
		return fmt.Errorf("%w: %s trailer points at bytes [%d,%d) outside the file",
			ErrIncompleteSegment, s.name, bloomOffset, indexEnd)
	}

	encoded := make([]byte, bloomLen)
	if _, err := f.ReadAt(encoded, int64(bloomOffset)); err != nil {
		return fmt.Errorf("read segment filter: %w", err)
	}

	if got := crc32Combine(trailer[0:16], encoded); got != want {
		return fmt.Errorf("%w: %s trailer checksum %08x does not match recorded %08x",
			ErrCorrupt, s.name, got, want)
	}

	bloom, err := filter.Unmarshal(encoded)
	if err != nil {
		return fmt.Errorf("decode filter in %s: %w", s.name, err)
	}

	s.bloom = bloom
	s.entryCount = int(entryCount)
	s.dataStart = int64(segmentHeader)
	s.dataEnd = int64(bloomOffset)

	return nil
}

// loadLegacy handles segments from before headers existed. They have no
// checksums and no filter, so the filter is rebuilt by reading the whole file.
// The first compaction rewrites them and the cost goes away.
func (s *Segment) loadLegacy(f *os.File, size int64) error {
	s.legacy = true
	s.dataStart = 0
	s.dataEnd = size

	var keys [][]byte
	err := scanLegacy(io.NewSectionReader(f, 0, size), func(rec Record) error {
		keys = append(keys, rec.Key)
		return nil
	})
	if err != nil {
		return fmt.Errorf("scan legacy segment %s: %w", s.name, err)
	}

	s.bloom = filter.NewOptimal(len(keys), filter.DefaultFalsePositiveRate)
	for _, k := range keys {
		s.bloom.Add(k)
	}
	s.entryCount = len(keys)

	return nil
}

// Name returns the segment's filename.
func (s *Segment) Name() string { return s.name }

// Path returns the segment's full path.
func (s *Segment) Path() string { return s.path }

// Sequence returns the segment's ordering number. Higher is newer.
func (s *Segment) Sequence() int64 { return s.seq }

// EntryCount returns the number of records the segment holds.
func (s *Segment) EntryCount() int { return s.entryCount }

// Legacy reports whether the segment predates the checksummed format.
func (s *Segment) Legacy() bool { return s.legacy }

// MightContain reports whether the segment may hold the key. False is exact, so
// the file doesn't need to be opened.
func (s *Segment) MightContain(key []byte) bool {
	return s.bloom == nil || s.bloom.MightContain(key)
}

// Get looks for key in the segment. It returns the value, whether the key was
// found, and whether what it found is a tombstone. I/O and checksum errors are
// returned, not reported as "not found".
func (s *Segment) Get(key []byte) (value []byte, found, tombstone bool, err error) {
	f, err := os.Open(s.path)
	if err != nil {
		return nil, false, false, fmt.Errorf("open segment %s: %w", s.name, err)
	}
	defer f.Close()

	section := io.NewSectionReader(f, s.dataStart, s.dataEnd-s.dataStart)
	reader := bufio.NewReaderSize(section, 64<<10)

	visit := func(rec Record) error {
		if string(rec.Key) != string(key) {
			return nil
		}
		value, found, tombstone = rec.Value, true, rec.Tombstone
		return errStopScan
	}

	if s.legacy {
		err = scanLegacy(reader, visit)
	} else {
		err = scanRecords(reader, visit)
	}
	if err != nil && !errors.Is(err, errStopScan) {
		return nil, false, false, fmt.Errorf("search segment %s: %w", s.name, err)
	}

	return value, found, tombstone, nil
}

// ForEach calls fn for every record. A decode error stops the walk and is
// returned; compaction relies on that.
func (s *Segment) ForEach(fn func(Record) error) error {
	f, err := os.Open(s.path)
	if err != nil {
		return fmt.Errorf("open segment %s: %w", s.name, err)
	}
	defer f.Close()

	section := io.NewSectionReader(f, s.dataStart, s.dataEnd-s.dataStart)
	reader := bufio.NewReaderSize(section, 64<<10)

	if s.legacy {
		err = scanLegacy(reader, fn)
	} else {
		err = scanRecords(reader, fn)
	}
	if err != nil {
		return fmt.Errorf("read segment %s: %w", s.name, err)
	}

	return nil
}

var errStopScan = errors.New("stratakv/storage: scan stopped")

func scanRecords(r io.Reader, fn func(Record) error) error {
	for {
		rec, _, err := ReadRecord(r)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := fn(rec); err != nil {
			return err
		}
	}
}

func scanLegacy(r io.Reader, fn func(Record) error) error {
	for {
		rec, _, err := readLegacyRecord(r)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := fn(rec); err != nil {
			return err
		}
	}
}

// syncDir fsyncs a directory so a rename survives a power cut. Errors are
// ignored. Windows can't fsync a directory, so this does nothing there, and I
// haven't tested whether a rename survives power loss on Windows.
func syncDir(dir string) {
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	defer f.Close()

	_ = f.Sync()
}
