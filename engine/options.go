package engine

import (
	"errors"
	"fmt"
)

// SyncMode controls when the write-ahead log is flushed to stable storage.
type SyncMode int

const (
	// SyncAlways fsyncs on every write before it returns. A Put that returns
	// nil has survived a power loss. This is the default, and it is also the
	// dominant cost of every write -- an fsync is milliseconds where the rest
	// of the write is microseconds.
	SyncAlways SyncMode = iota

	// SyncNever leaves writes in the operating system's page cache and fsyncs
	// only on Flush, Compact, and Close. Substantially faster, and writes made
	// since the last sync are lost if the machine loses power or the process is
	// killed. A clean Close loses nothing.
	//
	// Reasonable when the data can be regenerated -- a compile cache, a
	// derived index, a scratch store. Not reasonable when it is the only copy.
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

// ErrInvalidOptions reports an Options value that cannot be used.
var ErrInvalidOptions = errors.New("stratakv: invalid options")

// Options configures a database. The zero value is not valid; start from
// DefaultOptions and change what you need.
type Options struct {
	// MemTableSize is how many bytes the in-memory write buffer accumulates
	// before flushing to an immutable segment.
	//
	// Larger buffers mean fewer, bigger segments: less read amplification and
	// less compaction, at the cost of more memory and a longer pause on each
	// flush. Default 1 MiB.
	MemTableSize int

	// Sync controls write durability. Default SyncAlways.
	Sync SyncMode

	// FilterFalsePositiveRate is the target rate for each segment's Bloom
	// filter, which decides how often a read touches a segment that cannot
	// contain the key.
	//
	// Lower is more accurate and uses more memory: roughly 9.6 bits per key at
	// 0.01, 14.4 at 0.001. Must be in (0, 1). Default 0.01.
	FilterFalsePositiveRate float64
}

// DefaultOptions returns the configuration Open uses.
func DefaultOptions() Options {
	return Options{
		MemTableSize:            1 << 20, // 1 MiB
		Sync:                    SyncAlways,
		FilterFalsePositiveRate: 0.01,
	}
}

// validate rejects configurations that would produce a broken database rather
// than quietly substituting a default the caller did not ask for.
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
