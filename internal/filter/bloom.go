// Package filter implements a Bloom filter: a probabilistic set-membership
// structure that answers either "definitely not present" or "possibly present",
// never "definitely present".
//
// StrataKV keeps one filter per segment file. Before a read touches the disk,
// the filter is consulted; a negative answer skips the segment entirely. That
// is what keeps read amplification bounded as segments accumulate, and it is
// why the filter's accuracy has to track the size of the segment it guards.
package filter

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"math/bits"
)

// ErrMalformed reports an encoded filter that is truncated or self-inconsistent.
var ErrMalformed = errors.New("stratakv/filter: malformed encoded filter")

const (
	// DefaultFalsePositiveRate is the target rate used when a caller has no
	// reason to pick its own. At 1%, 99 of every 100 segments that cannot
	// contain a key are skipped without a disk read.
	DefaultFalsePositiveRate = 0.01

	// minBits floors the bit array so that a segment holding a handful of keys
	// still gets a filter with sane behaviour rather than a degenerate one.
	minBits = 512

	// maxBits caps a single filter at 32 MiB of bit array (~28M keys at 1%),
	// well beyond any segment this engine produces.
	maxBits = 1 << 28

	// maxHashes bounds probe cost. Beyond ~16 probes the marginal accuracy is
	// not worth the cache misses.
	maxHashes = 16

	// encodedHeaderSize is the fixed prefix of MarshalBinary: m uint32, k uint8.
	encodedHeaderSize = 5
)

// BloomFilter is a fixed-capacity Bloom filter backed by a packed bit array.
//
// A filter is sized once, at construction, from the number of keys it will
// hold; it does not grow. Adding substantially more keys than it was sized for
// degrades the false-positive rate rather than producing incorrect answers —
// the filter stays correct but stops being useful.
//
// A BloomFilter is not safe for concurrent mutation. In StrataKV a filter is
// built once while its segment is being written and is immutable thereafter,
// so concurrent MightContain calls are safe.
type BloomFilter struct {
	bits []uint64 // packed bit array, 64 bits per word
	m    uint32   // bit capacity
	k    uint8    // number of probes per key
}

// New returns a filter with an explicit bit capacity and probe count.
// Prefer NewOptimal unless you have a specific reason to fix both parameters.
func New(m uint32, k uint8) *BloomFilter {
	switch {
	case m < minBits:
		m = minBits
	case m > maxBits:
		m = maxBits
	}
	if k < 1 {
		k = 1
	} else if k > maxHashes {
		k = maxHashes
	}

	return &BloomFilter{
		bits: make([]uint64, (int(m)+63)/64),
		m:    m,
		k:    k,
	}
}

// NewOptimal sizes a filter to hold n keys at a target false-positive rate p,
// using the standard closed-form optima for a Bloom filter:
//
//	m = -n·ln(p) / (ln 2)²        (bits)
//	k = (m/n)·ln 2                (probes)
//
// At p = 0.01 this works out to roughly 9.6 bits and 7 probes per key. Sizing
// from n is the whole point: a filter that is fixed while n varies is either
// wasting memory on small segments or saturated and useless on large ones.
func NewOptimal(n int, p float64) *BloomFilter {
	if n < 1 {
		n = 1
	}
	if p <= 0 || p >= 1 {
		p = DefaultFalsePositiveRate
	}

	m := math.Ceil(-float64(n) * math.Log(p) / (math.Ln2 * math.Ln2))
	if m > maxBits {
		m = maxBits
	}

	k := math.Round(m / float64(n) * math.Ln2)
	if k < 1 {
		k = 1
	}

	return New(uint32(m), uint8(math.Min(k, maxHashes)))
}

// Add records a key in the filter.
func (b *BloomFilter) Add(key []byte) {
	h1, h2 := seeds(key)
	for i := uint8(0); i < b.k; i++ {
		idx := b.probe(i, h1, h2)
		b.bits[idx>>6] |= 1 << (idx & 63)
	}
}

// MightContain reports whether the key may be present.
//
// False means the key is definitely absent — that answer is exact, and it is
// the one the read path relies on. True means the key is probably present, at
// the filter's configured false-positive rate.
func (b *BloomFilter) MightContain(key []byte) bool {
	h1, h2 := seeds(key)
	for i := uint8(0); i < b.k; i++ {
		idx := b.probe(i, h1, h2)
		if b.bits[idx>>6]&(1<<(idx&63)) == 0 {
			return false
		}
	}
	return true
}

// probe returns the bit index of the i'th probe for a key.
//
// This is Kirsch-Mitzenmacher double hashing: k independent-enough probes are
// synthesised from two hashes as h1 + i·h2, which is provably as accurate as k
// real hash functions for Bloom filter purposes and far cheaper. The
// arithmetic widens to uint64 so that h1 + i·h2 cannot wrap before the modulo
// and skew the distribution.
func (b *BloomFilter) probe(i uint8, h1, h2 uint32) uint32 {
	return uint32((uint64(h1) + uint64(i)*uint64(h2)) % uint64(b.m))
}

// seeds derives two hashes from one 64-bit FNV-1a digest.
//
// h2 is forced odd. If h2 shared a factor with m the probe sequence
// h1 + i·h2 would revisit a short cycle of bit positions instead of spreading
// across the array, quietly inflating the false-positive rate.
func seeds(key []byte) (h1, h2 uint32) {
	h := fnv.New64a()
	_, _ = h.Write(key) // hash.Hash.Write never returns an error
	sum := h.Sum64()

	return uint32(sum), uint32(sum>>32) | 1
}

// Bits reports the filter's bit capacity.
func (b *BloomFilter) Bits() uint32 { return b.m }

// Hashes reports the number of probes performed per key.
func (b *BloomFilter) Hashes() uint8 { return b.k }

// FalsePositiveRate estimates the filter's current false-positive rate from
// how full the bit array actually is, rather than from the key count it was
// sized for. Useful for asserting in tests and for reporting metrics.
func (b *BloomFilter) FalsePositiveRate() float64 {
	set := 0
	for _, w := range b.bits {
		set += bits.OnesCount64(w)
	}
	return math.Pow(float64(set)/float64(b.m), float64(b.k))
}

// MarshalBinary encodes the filter as [m uint32][k uint8][packed bits].
//
// Segment files carry their filter in a trailer so that opening a database
// costs one small read per segment instead of a full rescan of every byte on
// disk to rebuild filters from scratch.
func (b *BloomFilter) MarshalBinary() ([]byte, error) {
	out := make([]byte, encodedHeaderSize+len(b.bits)*8)

	binary.LittleEndian.PutUint32(out[0:4], b.m)
	out[4] = b.k
	for i, w := range b.bits {
		binary.LittleEndian.PutUint64(out[encodedHeaderSize+i*8:], w)
	}

	return out, nil
}

// Unmarshal decodes a filter produced by MarshalBinary. It validates that the
// declared bit capacity matches the number of bytes supplied, so a truncated
// or garbled trailer is rejected rather than silently producing a filter that
// answers "definitely absent" for keys that are present.
func Unmarshal(data []byte) (*BloomFilter, error) {
	if len(data) < encodedHeaderSize {
		return nil, fmt.Errorf("%w: %d bytes is shorter than the %d byte header",
			ErrMalformed, len(data), encodedHeaderSize)
	}

	m := binary.LittleEndian.Uint32(data[0:4])
	k := data[4]
	if m == 0 || k == 0 || k > maxHashes {
		return nil, fmt.Errorf("%w: m=%d k=%d", ErrMalformed, m, k)
	}

	words := (int(m) + 63) / 64
	if got, want := len(data)-encodedHeaderSize, words*8; got != want {
		return nil, fmt.Errorf("%w: m=%d needs %d bytes of bit array, got %d",
			ErrMalformed, m, want, got)
	}

	b := &BloomFilter{bits: make([]uint64, words), m: m, k: k}
	for i := range b.bits {
		b.bits[i] = binary.LittleEndian.Uint64(data[encodedHeaderSize+i*8:])
	}

	return b, nil
}
