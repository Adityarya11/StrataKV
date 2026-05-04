package storage

import (
	"encoding/binary"
	"os"
	"sync"
)

type WAL struct {
	mu   sync.Mutex
	file *os.File
}

// WAL -> write - ahead - log file for the logs (this will be appended)
func NewWal(path string) (*WAL, error) {
	/*
	* // os.O_APPEND: Only write to the end of the file.
	* // os.O_CREATE: Create it if it doesn't exist.
	* // os.O_WRONLY: We only need to write during normal operation.
	 */

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0544)
	if err != nil {
		return nil, err
	}

	return &WAL{file: f}, nil
}

// WriteEntry for entring the file
func (w *WAL) WriteEntry(isDelete bool, key, value []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	header := make([]byte, 9) // 1 byte for bool, 4 for key and value = 9

	if isDelete {
		header[0] = 1
	} else {
		header[0] = 0
	}

	// Little-endian is a byte-ordering format where the least significant byte (LSB)—the "little end"—is stored at the lowest memory address
	binary.LittleEndian.PutUint32(header[1:5], uint32(len(key)))
	binary.LittleEndian.PutUint32(header[5:9], uint32(len(value)))

	_, errH := w.file.Write(header)
	if errH != nil {
		return errH
	}

	_, errK := w.file.Write(key)
	if errK != nil {
		return errK
	}

	if isDelete {
		_, errV := w.file.Write(value)
		if errV != nil {
			return errV
		}
	}

	return nil
}

// close
func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.file.Close()
}
