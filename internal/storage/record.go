package storage

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
)

// One record layout, shared by the WAL and by segment files:
//
//	+--------+-------+--------+--------+-----+-------+
//	| crc32c | flags | keyLen | valLen | key | value |
//	|   4B   |  1B   |   4B   |   4B   |  n  |   m   |
//	+--------+-------+--------+--------+-----+-------+
//
// The checksum covers every byte after it.
const (
	recordHeaderSize = 13
	flagTombstone    = 1 << 0

	// MaxRecordSize bounds one key-value pair. Its real job is on decode:
	// garbage bytes routinely declare enormous lengths.
	MaxRecordSize = 64 << 20
)

// CRC-32C: hardware-accelerated on amd64 and arm64, unlike the IEEE polynomial.
var crcTable = crc32.MakeTable(crc32.Castagnoli)

func newCRC() hash.Hash32 { return crc32.New(crcTable) }

var (
	// ErrTorn reports a record that ends before its declared length: a process
	// died mid-write. Expected at a WAL tail, corruption anywhere else.
	ErrTorn = errors.New("stratakv/storage: incomplete record")

	// ErrCorrupt reports a structurally complete record that fails verification.
	ErrCorrupt = errors.New("stratakv/storage: corrupt record")
)

// Record is one decoded key-value operation. A tombstone carries no value.
type Record struct {
	Tombstone bool
	Key       []byte
	Value     []byte
}

// EncodedLen reports the byte count AppendRecord will produce.
func EncodedLen(key, value []byte) int {
	return recordHeaderSize + len(key) + len(value)
}

// AppendRecord appends one encoded record to dst.
//
// Assembled in a single buffer so it costs one write syscall rather than three,
// which also narrows the window in which a crash can tear a record apart.
func AppendRecord(dst []byte, tombstone bool, key, value []byte) []byte {
	if tombstone {
		value = nil
	}

	start := len(dst)
	dst = append(dst, make([]byte, recordHeaderSize)...)

	if tombstone {
		dst[start+4] = flagTombstone
	}
	binary.LittleEndian.PutUint32(dst[start+5:start+9], uint32(len(key)))
	binary.LittleEndian.PutUint32(dst[start+9:start+13], uint32(len(value)))

	dst = append(dst, key...)
	dst = append(dst, value...)

	// Checksum everything past the checksum field, then backfill it.
	binary.LittleEndian.PutUint32(dst[start:start+4], crc32.Checksum(dst[start+4:], crcTable))

	return dst
}

// ReadRecord decodes one record and reports the bytes consumed.
//
// The three failure modes are distinct and callers act on them differently:
// io.EOF (clean boundary), ErrTorn (unusable tail), ErrCorrupt (bad data).
func ReadRecord(r io.Reader) (Record, int, error) {
	var hdr [recordHeaderSize]byte

	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return Record{}, 0, io.EOF
		}
		return Record{}, 0, fmt.Errorf("%w: reading header: %v", ErrTorn, err)
	}

	var (
		want   = binary.LittleEndian.Uint32(hdr[0:4])
		flags  = hdr[4]
		keyLen = binary.LittleEndian.Uint32(hdr[5:9])
		valLen = binary.LittleEndian.Uint32(hdr[9:13])
	)

	// A zero-length key is never written, so this is not a record. Catches the
	// zero-filled tail NTFS can leave behind, with a clearer diagnosis than a
	// checksum mismatch would give.
	if keyLen == 0 {
		return Record{}, 0, fmt.Errorf("%w: zero-length key", ErrCorrupt)
	}
	if uint64(keyLen)+uint64(valLen) > MaxRecordSize {
		return Record{}, 0, fmt.Errorf("%w: declared length %d exceeds %d byte maximum",
			ErrCorrupt, uint64(keyLen)+uint64(valLen), MaxRecordSize)
	}

	// Verify over one contiguous buffer: header bytes after the CRC, then payload.
	const checksummed = recordHeaderSize - 4

	body := make([]byte, checksummed+int(keyLen)+int(valLen))
	copy(body, hdr[4:])

	if _, err := io.ReadFull(r, body[checksummed:]); err != nil {
		return Record{}, 0, fmt.Errorf("%w: reading %d byte body: %v",
			ErrTorn, len(body)-checksummed, err)
	}

	if got := crc32.Checksum(body, crcTable); got != want {
		return Record{}, 0, fmt.Errorf("%w: checksum %08x does not match recorded %08x",
			ErrCorrupt, got, want)
	}

	rec := Record{
		Tombstone: flags&flagTombstone != 0,
		Key:       body[checksummed : checksummed+int(keyLen)],
	}
	if !rec.Tombstone {
		rec.Value = body[checksummed+int(keyLen):]
	}

	return rec, recordHeaderSize + int(keyLen) + int(valLen), nil
}
