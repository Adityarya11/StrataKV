package bench

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"
	"math/rand/v2"
	"testing"
	"time"
)

// store is the part of the engine API the read benchmarks use. Each adapter
// wraps one engine version, so the benchmark files are identical for v2 and for
// a v1.0.3 checkout.
type store interface {
	Put(key, value []byte) error
	Get(key []byte) (value []byte, found bool, err error)
	Close() error
}

const (
	keySize        = 64      // hex SHA-256, the shape blan-backend stores
	segmentTarget  = 1 << 20 // default flush threshold in both engines
	recordOverhead = 13      // v2 record header
)

// recordsPerSegment is how many records fill one default-size segment. It uses
// the memtable's own flush formula, so a fixture built through the engine ends
// up with exactly the segment count asked for.
func recordsPerSegment(valSize int) int {
	rec := keySize + valSize + recordOverhead
	return (segmentTarget + rec - 1) / rec
}

func keyFor(i int) []byte {
	sum := sha256.Sum256(binary.BigEndian.AppendUint64(nil, uint64(i)))
	return hex.AppendEncode(nil, sum[:])
}

// missKeyFor never matches a keyFor key; the hash input has a different prefix.
func missKeyFor(i int) []byte {
	sum := sha256.Sum256(binary.BigEndian.AppendUint64([]byte("miss"), uint64(i)))
	return hex.AppendEncode(nil, sum[:])
}

func valueOf(n int) []byte {
	v := make([]byte, n)
	for i := range v {
		v[i] = byte(i*31 + 7)
	}
	return v
}

// newSeqKey and seqKey make cheap sequential keys for the write benchmarks,
// where hashing every key would add noise to a microsecond operation. The
// engine copies keys on write, so one buffer is reused.
func newSeqKey() []byte {
	k := make([]byte, keySize)
	for i := range k {
		k[i] = '0'
	}
	return k
}

func seqKey(k []byte, i int) {
	const digits = "0123456789abcdef"

	n := uint64(i)
	for j := keySize - 1; j >= keySize-16; j-- {
		k[j] = digits[n&0xf]
		n >>= 4
	}
}

const lookupKeys = 4096

// hitKeys picks keys spread evenly over the fixture from a fixed seed, so every
// run and both engine versions look up the same keys.
func hitKeys(total int) [][]byte {
	r := rand.New(rand.NewPCG(1, 2))

	keys := make([][]byte, lookupKeys)
	for i := range keys {
		keys[i] = keyFor(r.IntN(total))
	}

	return keys
}

func missKeys() [][]byte {
	keys := make([][]byte, lookupKeys)
	for i := range keys {
		keys[i] = missKeyFor(i)
	}

	return keys
}

// percentile uses the nearest-rank method on an ascending slice.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}

	rank := int(math.Ceil(p / 100 * float64(len(sorted))))

	return sorted[min(max(rank-1, 0), len(sorted)-1)]
}

func TestPercentile(t *testing.T) {
	sorted := make([]time.Duration, 100)
	for i := range sorted {
		sorted[i] = time.Duration(i + 1)
	}

	for _, tc := range []struct {
		p    float64
		want time.Duration
	}{
		{0, 1}, {50, 50}, {99, 99}, {99.9, 100}, {100, 100},
	} {
		if got := percentile(sorted, tc.p); got != tc.want {
			t.Errorf("percentile(%v) = %v, want %v", tc.p, got, tc.want)
		}
	}

	if got := percentile(nil, 50); got != 0 {
		t.Errorf("percentile of no samples = %v, want 0", got)
	}
}

func TestKeysAreDistinctAndSized(t *testing.T) {
	if len(keyFor(1)) != keySize || len(missKeyFor(1)) != keySize {
		t.Fatalf("keys must be %d bytes", keySize)
	}
	if string(keyFor(7)) == string(missKeyFor(7)) {
		t.Fatal("a miss key collides with a stored key")
	}

	k := newSeqKey()
	seqKey(k, 255)
	if got := string(k[keySize-16:]); got != "00000000000000ff" {
		t.Fatalf("seqKey(255) tail = %q", got)
	}
}

var _ testing.TB // keep the testing import explicit for build-tag variants
