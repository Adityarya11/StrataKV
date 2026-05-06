package storage

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

type Segment struct {
	file *os.File
}

// Write Segment takes the in memory data and stores in the dense segmet file.
func WriteSegment(path string, data map[string][]byte) error {
	// O_trunc is used for the fresh start in the file if it exists

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to create Segment: %w", err)
	}
	defer f.Close()

	for k, v := range data {
		keyBytes := []byte(k)

		header := make([]byte, 8) // here 4 -> key len & 4-> val len
		binary.LittleEndian.PutUint32(header[0:4], uint32(len(keyBytes)))
		binary.LittleEndian.PutUint32(header[4:8], uint32(len(v)))

		if _, err := f.Write(header); err != nil {
			return err
		}
		if _, err := f.Write(keyBytes); err != nil {
			return err
		}
		if _, err := f.Write(v); err != nil {
			return err
		}
	}

	return f.Sync()
}

// Readsgment
func Readsgment(path string, out map[string][]byte) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("failed to open the segment: %w", err)
	}

	defer f.Close()

	for {
		header := make([]byte, 8)

		_, err := io.ReadFull(f, header)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil
		}

		keyLen := binary.LittleEndian.Uint32(header[0:4])
		valLen := binary.LittleEndian.Uint32(header[4:8])

		key := make([]byte, keyLen)
		if _, err := io.ReadFull(f, key); err != nil {
			return nil
		}

		value := make([]byte, valLen)
		if _, err := io.ReadFull(f, key); err != nil {
			return err
		}
		out[string(key)] = value
	}

	return nil
}

func SearchSegment(path string, searchKey []byte) ([]byte, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}

	defer f.Close()

	for {
		header := make([]byte, 8)
		_, err := io.ReadFull(f, header)
		if err == io.EOF {
			break
		}

		if err != nil {
			return nil, false
		}

		keyLen := binary.LittleEndian.Uint32(header[0:4])
		valLen := binary.LittleEndian.Uint32(header[4:8])

		key := make([]byte, keyLen)
		if _, err := io.ReadFull(f, key); err != nil {
			return nil, false
		}

		if string(key) == string(searchKey) {
			value := make([]byte, valLen)
			if _, err := io.ReadFull(f, value); err != nil {
				return nil, false
			}

			return value, true
		}

		if _, err := f.Seek(int64(valLen), io.SeekCurrent); err != nil {
			return nil, false
		}
	}

	return nil, false
}
