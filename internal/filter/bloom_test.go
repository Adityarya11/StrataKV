// This is AI generated Code, I recently got Claude Code and hence using it to
// the fullest.

package filter

import (
	"fmt"
	"math"
	"testing"
)

// key makes a 64-character hex key, the shape of the SHA-256 digests this is
// used with.
func key(i int) []byte { return fmt.Appendf(nil, "%064x", i) }

func TestNoFalseNegatives(t *testing.T) {
	// the one thing a Bloom filter must never do: every key that was added has
	// to come back as possibly present, or a segment holding live data gets
	// skipped
	const n = 5000

	b := NewOptimal(n, DefaultFalsePositiveRate)
	for i := range n {
		b.Add(key(i))
	}

	for i := range n {
		if !b.MightContain(key(i)) {
			t.Fatalf("false negative for key %d: added key reported absent", i)
		}
	}
}

func TestOptimalSizingHoldsTargetRate(t *testing.T) {
	// The old filter used 10000 bits and 3 hashes for every segment. It
	// measured 31% false positives at 4000 keys and 86% at 10000, which is a
	// filter that prunes nothing.
	for _, n := range []int{100, 1000, 4000, 10000, 50000} {
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			b := NewOptimal(n, DefaultFalsePositiveRate)
			for i := range n {
				b.Add(key(i))
			}

			const trials = 20000
			fp := 0
			for i := range trials {
				if b.MightContain(fmt.Appendf(nil, "absent-%064x", i)) {
					fp++
				}
			}

			rate := float64(fp) / trials

			// some headroom over 1%, since double hashing only approximates
			// independent hashes. What matters is that the rate stays flat as
			// n grows
			if rate > 0.03 {
				t.Errorf("n=%d: false-positive rate %.2f%% exceeds bound (target %.0f%%)",
					n, rate*100, DefaultFalsePositiveRate*100)
			}

			t.Logf("n=%-6d m=%-8d k=%d  measured=%.2f%%  estimated=%.2f%%",
				n, b.Bits(), b.Hashes(), rate*100, b.FalsePositiveRate()*100)
		})
	}
}

func TestPackedBitsetMemory(t *testing.T) {
	// the old filter used a bool (one byte) per bit; packing into uint64 words
	// is 8x smaller, and every open segment keeps its filter in memory
	const n = 100000

	b := NewOptimal(n, DefaultFalsePositiveRate)
	packed := len(b.bits) * 8
	unpacked := int(b.Bits()) // one byte per bit, as before

	// the array rounds up to a whole 64-bit word
	if packed*8 > unpacked+63 {
		t.Errorf("packed bitset uses %d bytes, expected ~%d (1/8th of %d)",
			packed, unpacked/8, unpacked)
	}

	t.Logf("n=%d: %d bits -> %d bytes packed vs %d bytes as []bool",
		n, b.Bits(), packed, unpacked)
}

func TestRoundTrip(t *testing.T) {
	const n = 2000

	orig := NewOptimal(n, DefaultFalsePositiveRate)
	for i := range n {
		orig.Add(key(i))
	}

	encoded, err := orig.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}

	got, err := Unmarshal(encoded)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.Bits() != orig.Bits() || got.Hashes() != orig.Hashes() {
		t.Fatalf("parameters not preserved: got m=%d k=%d, want m=%d k=%d",
			got.Bits(), got.Hashes(), orig.Bits(), orig.Hashes())
	}

	// a decoded filter has to answer exactly like the one that was encoded, or
	// segments that hold a key would get skipped
	for i := range n {
		if !got.MightContain(key(i)) {
			t.Fatalf("decoded filter lost key %d", i)
		}
	}
	for i := range 5000 {
		probe := fmt.Appendf(nil, "absent-%064x", i)
		if orig.MightContain(probe) != got.MightContain(probe) {
			t.Fatalf("decoded filter disagrees with original on probe %d", i)
		}
	}
}

func TestUnmarshalRejectsMalformed(t *testing.T) {
	b := NewOptimal(1000, DefaultFalsePositiveRate)
	encoded, err := b.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}

	cases := map[string][]byte{
		"empty":            {},
		"header only":      encoded[:encodedHeaderSize],
		"truncated bits":   encoded[:len(encoded)-8],
		"trailing garbage": append(append([]byte{}, encoded...), 0xFF),
		"zero m":           append([]byte{0, 0, 0, 0}, encoded[4:]...),
	}

	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			// a short bit array would say "not here" for keys that are there,
			// so malformed input has to be rejected
			if _, err := Unmarshal(data); err == nil {
				t.Errorf("Unmarshal accepted malformed input")
			}
		})
	}
}

func TestNewClampsParameters(t *testing.T) {
	if got := New(0, 0); got.Bits() != minBits || got.Hashes() != 1 {
		t.Errorf("New(0,0) = m %d k %d, want m %d k 1", got.Bits(), got.Hashes(), minBits)
	}
	if got := New(minBits, 200); got.Hashes() != maxHashes {
		t.Errorf("New clamped k to %d, want %d", got.Hashes(), maxHashes)
	}
}

func TestNewOptimalDegenerateInputs(t *testing.T) {
	for _, tc := range []struct {
		name string
		n    int
		p    float64
	}{
		{"zero keys", 0, DefaultFalsePositiveRate},
		{"negative keys", -5, DefaultFalsePositiveRate},
		{"p of zero", 100, 0},
		{"p of one", 100, 1},
		{"p out of range", 100, 17},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := NewOptimal(tc.n, tc.p)
			if b.Bits() < minBits || b.Hashes() < 1 {
				t.Fatalf("degenerate filter: m=%d k=%d", b.Bits(), b.Hashes())
			}
			b.Add([]byte("x"))
			if !b.MightContain([]byte("x")) {
				t.Error("filter lost a key it just stored")
			}
		})
	}
}

func TestFalsePositiveRateTracksFill(t *testing.T) {
	b := New(minBits*16, 7)
	if got := b.FalsePositiveRate(); got != 0 {
		t.Errorf("empty filter reports %v, want 0", got)
	}

	for i := range 500 {
		b.Add(key(i))
	}
	if got := b.FalsePositiveRate(); got <= 0 || math.IsNaN(got) {
		t.Errorf("populated filter reports %v", got)
	}
}

func BenchmarkAdd(b *testing.B) {
	const n = 10000

	f := NewOptimal(n, DefaultFalsePositiveRate)
	keys := make([][]byte, 1024)
	for i := range keys {
		keys[i] = key(i)
	}

	i := 0
	for b.Loop() {
		f.Add(keys[i%len(keys)])
		i++
	}
}

func BenchmarkMightContain(b *testing.B) {
	const n = 10000

	f := NewOptimal(n, DefaultFalsePositiveRate)
	for i := range n {
		f.Add(key(i))
	}

	keys := make([][]byte, 1024)
	for i := range keys {
		keys[i] = key(i)
	}

	i := 0
	for b.Loop() {
		f.MightContain(keys[i%len(keys)])
		i++
	}
}
