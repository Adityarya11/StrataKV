package storage

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
)

// The WAL and the segment files share one record layout:
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

	// Set on every record of a batch except the last. The unset one commits
	// the group. Older records never have it, so each one commits itself.
	flagPending = 1 << 1

	// MaxRecordSize is the most a key and value can add up to. It mostly exists
	// for decoding: garbage bytes often declare a huge length.
	MaxRecordSize = 64 << 20
)

// CRC-32C has a hardware instruction on amd64 and arm64.
var crcTable = crc32.MakeTable(crc32.Castagnoli)

func newCRC() hash.Hash32 { return crc32.New(crcTable) }

var (
	// ErrTorn means a record ends before its declared length, which is what a
	// crash mid-write leaves. Normal at the end of a WAL, a problem anywhere else.
	ErrTorn = errors.New("stratakv/storage: incomplete record")

	// ErrCorrupt means a complete record failed its checks.
	ErrCorrupt = errors.New("stratakv/storage: corrupt record")
)

// Record is one decoded operation. A tombstone has no value. Pending means the
// record belongs to a batch whose commit hasn't been seen yet; recovery holds
// those back and drops them if the commit never arrives.
type Record struct {
	Tombstone bool
	Pending   bool
	Key       []byte
	Value     []byte
}

// EncodedLen is the size AppendRecord will produce.
func EncodedLen(key, value []byte) int {
	return recordHeaderSize + len(key) + len(value)
}

// AppendRecord appends the encoded record to dst. Building it in one buffer
// means one write call instead of three, and a smaller window for a crash to
// split a record.
func AppendRecord(dst []byte, rec Record) []byte {
	key, value := rec.Key, rec.Value
	if rec.Tombstone {
		value = nil
	}

	start := len(dst)
	dst = append(dst, make([]byte, recordHeaderSize)...)

	var flags byte
	if rec.Tombstone {
		flags |= flagTombstone
	}
	if rec.Pending {
		flags |= flagPending
	}
	dst[start+4] = flags
	binary.LittleEndian.PutUint32(dst[start+5:start+9], uint32(len(key)))
	binary.LittleEndian.PutUint32(dst[start+9:start+13], uint32(len(value)))

	dst = append(dst, key...)
	dst = append(dst, value...)

	// checksum everything after the checksum field, then fill it in
	binary.LittleEndian.PutUint32(dst[start:start+4], crc32.Checksum(dst[start+4:], crcTable))

	return dst
}

// ReadRecord decodes one record and returns how many bytes it used. It ends
// three ways, and callers treat them differently: io.EOF for a clean end,
// ErrTorn for a record cut short, ErrCorrupt for one that doesn't check out.
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

	// a zero-length key is never written, so this isn't a record. NTFS can
	// leave a zero-filled tail after a crash, and this gives a clearer error
	// than a checksum mismatch would
	if keyLen == 0 {
		return Record{}, 0, fmt.Errorf("%w: zero-length key", ErrCorrupt)
	}
	if uint64(keyLen)+uint64(valLen) > MaxRecordSize {
		return Record{}, 0, fmt.Errorf("%w: declared length %d exceeds %d byte maximum",
			ErrCorrupt, uint64(keyLen)+uint64(valLen), MaxRecordSize)
	}

	// the checksummed bytes in one buffer: header after the CRC, then the payload
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
		Pending:   flags&flagPending != 0,
		Key:       body[checksummed : checksummed+int(keyLen)],
	}
	if !rec.Tombstone {
		rec.Value = body[checksummed+int(keyLen):]
	}

	return rec, recordHeaderSize + int(keyLen) + int(valLen), nil
}
