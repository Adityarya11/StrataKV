package engine

import (
	"errors"
	"fmt"
)

// SyncMode says when the write-ahead log is flushed to disk.
type SyncMode int

const (
	// SyncAlways fsyncs before every write returns, so a Put that returned nil
	// survives a power cut. It is the default. The fsync is nearly all of what
	// a write costs.
	SyncAlways SyncMode = iota

	// SyncNever leaves writes in the OS page cache and only fsyncs on Flush,
	// Compact and Close. It is much faster, but writes since the last sync are
	// lost if the machine loses power or the process is killed. A clean Close
	// loses nothing. Fine for data you can regenerate, like a compile cache;
	// not for the only copy of anything.
	SyncNever
)

func (m SyncMode) String() string {
	switch m {
	case SyncAlways:
		return "always"
	case SyncNever:
		return "never"
	default:
		return fmt.Sprintf("SyncMode(%d)", int(m))
	}
}

// ErrInvalidOptions is returned when Options has a value that can't be used.
var ErrInvalidOptions = errors.New("stratakv: invalid options")

// Options configures a database. The zero value isn't valid; start from
// DefaultOptions.
type Options struct {
	// MemTableSize is how many bytes the memtable holds before it is flushed
	// to a segment. Larger means fewer, bigger segments and more memory.
	// Default 1 MiB.
	MemTableSize int

	// Sync is the durability mode. Default SyncAlways.
	Sync SyncMode

	// FilterFalsePositiveRate is the Bloom filter's target false-positive rate
	// for each segment. Lower is more accurate and uses more memory: about 9.6
	// bits per key at 0.01 and 14.4 at 0.001. It must be between 0 and 1.
	// Default 0.01.
	FilterFalsePositiveRate float64
}

// DefaultOptions is the configuration Open uses.
func DefaultOptions() Options {
	return Options{
		MemTableSize:            1 << 20, // 1 MiB
		Sync:                    SyncAlways,
		FilterFalsePositiveRate: 0.01,
	}
}

// validate rejects bad values instead of quietly swapping in a default the
// caller didn't ask for.
func (o Options) validate() error {
	if o.MemTableSize <= 0 {
		return fmt.Errorf("%w: MemTableSize must be positive, got %d",
			ErrInvalidOptions, o.MemTableSize)
	}
	if o.FilterFalsePositiveRate <= 0 || o.FilterFalsePositiveRate >= 1 {
		return fmt.Errorf("%w: FilterFalsePositiveRate must be in (0,1), got %v",
			ErrInvalidOptions, o.FilterFalsePositiveRate)
	}
	if o.Sync != SyncAlways && o.Sync != SyncNever {
		return fmt.Errorf("%w: unknown Sync mode %d", ErrInvalidOptions, int(o.Sync))
	}

	return nil
}
