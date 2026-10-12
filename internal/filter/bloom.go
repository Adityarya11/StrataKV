// Package filter is a Bloom filter. It answers "definitely not here" or
// "maybe here", never "definitely here".
//
// There is one filter per segment. A read asks it first and skips the segment
// on a "no", which is what keeps reads cheap as segments pile up.
package filter

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"math/bits"
)

// ErrMalformed is returned for an encoded filter that is truncated or
// inconsistent.
var ErrMalformed = errors.New("stratakv/filter: malformed encoded filter")

const (
	// DefaultFalsePositiveRate is 1%.
	DefaultFalsePositiveRate = 0.01

	// floor for the bit array, so a segment with a handful of keys still gets a
	// usable filter
	minBits = 512

	// 32 MiB of bits, far more than any segment here needs
	maxBits = 1 << 28

	// past about 16 probes the extra accuracy isn't worth the cache misses
	maxHashes = 16

	// m (uint32) and k (uint8) at the front of the encoded form
	encodedHeaderSize = 5
)

// BloomFilter is a fixed-size Bloom filter on a packed bit array. It is sized
// once from the number of keys it will hold and doesn't grow. Adding many more
// keys than that doesn't make it wrong, but the false-positive rate climbs
// until it stops being useful.
//
// A filter is built while its segment is written and never changed after, so
// concurrent MightContain calls are safe.
type BloomFilter struct {
	bits []uint64 // packed bit array, 64 bits per word
	m    uint32   // bit capacity
	k    uint8    // number of probes per key
}

// New returns a filter with a fixed number of bits and probes. NewOptimal is
// usually what you want.
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

// NewOptimal sizes a filter for n keys at false-positive rate p, using the
// standard formulas:
//
//	m = -n * ln(p) / (ln 2)^2     bits
//	k = (m / n) * ln 2            probes
//
// At p = 0.01 that is about 9.6 bits and 7 probes per key. The size has to
// follow n. A fixed size wastes memory on small segments and saturates on big
// ones.
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

// Add puts a key in the filter.
func (b *BloomFilter) Add(key []byte) {
	h1, h2 := seeds(key)
	for i := uint8(0); i < b.k; i++ {
		idx := b.probe(i, h1, h2)
		b.bits[idx>>6] |= 1 << (idx & 63)
	}
}

// MightContain reports whether the key may be present. False is exact: the key
// was never added. True can be a false positive.
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

// probe is the bit index for the i-th probe. It uses double hashing
// (Kirsch-Mitzenmacher): h1 + i*h2 stands in for k separate hash functions.
// The sum is done in 64 bits so it can't wrap before the modulo.
func (b *BloomFilter) probe(i uint8, h1, h2 uint32) uint32 {
	return uint32((uint64(h1) + uint64(i)*uint64(h2)) % uint64(b.m))
}

// seeds splits one 64-bit FNV-1a hash into two. h2 is forced odd: if it shared
// a factor with m, the probes would cycle through a few bits and the
// false-positive rate would creep up.
func seeds(key []byte) (h1, h2 uint32) {
	h := fnv.New64a()
	_, _ = h.Write(key) // hash.Hash.Write never returns an error
	sum := h.Sum64()

	return uint32(sum), uint32(sum>>32) | 1
}

// Bits is the size of the bit array.
func (b *BloomFilter) Bits() uint32 { return b.m }

// Hashes is the number of probes per key.
func (b *BloomFilter) Hashes() uint8 { return b.k }

// FalsePositiveRate estimates the current rate from how full the bit array is.
func (b *BloomFilter) FalsePositiveRate() float64 {
	set := 0
	for _, w := range b.bits {
		set += bits.OnesCount64(w)
	}
	return math.Pow(float64(set)/float64(b.m), float64(b.k))
}

// MarshalBinary encodes the filter as [m uint32][k uint8][packed bits].
// Segments store their filter this way, so opening one doesn't mean rescanning
// it to rebuild the filter.
func (b *BloomFilter) MarshalBinary() ([]byte, error) {
	out := make([]byte, encodedHeaderSize+len(b.bits)*8)

	binary.LittleEndian.PutUint32(out[0:4], b.m)
	out[4] = b.k
	for i, w := range b.bits {
		binary.LittleEndian.PutUint64(out[encodedHeaderSize+i*8:], w)
	}

	return out, nil
}

// Unmarshal decodes the output of MarshalBinary. It checks that the declared
// size matches the bytes given. A short bit array would answer "not here" for
// keys that are there, so a damaged filter has to be rejected.
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
