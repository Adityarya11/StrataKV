package storage

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/Adityarya11/StrataKV/internal/memtable"
)

type Segment struct {
	file *os.File
}

// WriteSegment takes the in-memory data and stores it in the dense segment file format.
func WriteSegment(path string, data map[string]memtable.Entry) error {
	// os.O_TRUNC is used to rewrite the file completely if it already exists.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to create segment: %w", err)
	}
	defer f.Close()

	for k, v := range data {
		keyBytes := []byte(k)

		header := make([]byte, 9) // 1 -> tombstone, 4 -> key len, 4-> val len

		if v.Deleted {
			header[0] = 1
		} else {
			header[0] = 0
		}

		binary.LittleEndian.PutUint32(header[1:5], uint32(len(keyBytes)))
		binary.LittleEndian.PutUint32(header[5:9], uint32(len(v.Value)))

		if _, err := f.Write(header); err != nil {
			return err
		}
		if _, err := f.Write(keyBytes); err != nil {
			return err
		}
		if !v.Deleted {
			if _, err := f.Write(v.Value); err != nil {
				return err
			}
		}
	}

	return f.Sync()
}

// ReadSegment
func ReadSegment(path string, out map[string]memtable.Entry) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("failed to open the segment: %w", err)
	}

	defer f.Close()

	for {
		header := make([]byte, 9)

		_, err := io.ReadFull(f, header)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil
		}

		isDeleted := header[0] == 1
		keyLen := binary.LittleEndian.Uint32(header[1:5])
		valLen := binary.LittleEndian.Uint32(header[5:9])

		key := make([]byte, keyLen)
		if _, err := io.ReadFull(f, key); err != nil {
			return nil
		}

		var value []byte
		if !isDeleted {
			value = make([]byte, valLen)
			if _, err := io.ReadFull(f, value); err != nil {
				return err
			}
		}
		out[string(key)] = memtable.Entry{Value: value, Deleted: isDeleted}
	}

	return nil
}

func SearchSegment(path string, searchKey []byte) ([]byte, bool, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, false
	}

	defer f.Close()

	for {
		header := make([]byte, 9)
		_, err := io.ReadFull(f, header)
		if err == io.EOF {
			break
		}

		if err != nil {
			return nil, false, false
		}

		isDeleted := header[0] == 1
		keyLen := binary.LittleEndian.Uint32(header[1:5])
		valLen := binary.LittleEndian.Uint32(header[5:9])

		key := make([]byte, keyLen)
		if _, err := io.ReadFull(f, key); err != nil {
			return nil, false, false
		}

		if string(key) == string(searchKey) {
			if isDeleted {
				return nil, true, true // Found, but it's a tombstone
			}

			value := make([]byte, valLen)
			if _, err := io.ReadFull(f, value); err != nil {
				return nil, false, false
			}

			return value, true, false // Found and not deleted
		}

		if !isDeleted {
			if _, err := f.Seek(int64(valLen), io.SeekCurrent); err != nil {
				return nil, false, false
			}
		}
	}

	return nil, false, false
}
