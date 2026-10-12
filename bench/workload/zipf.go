package main

import (
	"hash/fnv"
	"math"
	"math/rand/v2"
)

// zipfian draws from a Zipf distribution over [0, n) with the algorithm from
// Gray et al., "Quickly Generating Billion-Record Synthetic Databases" (1994),
// which is what YCSB uses. Go's rand.Zipf needs an exponent above 1 and YCSB's
// is 0.99, so the standard library can't do it.
type zipfian struct {
	n            uint64
	alpha, zetan float64
	eta          float64
	halfPowTheta float64
}

const zipfTheta = 0.99

func newZipfian(n uint64) *zipfian {
	zetan := zeta(n, zipfTheta)
	zeta2 := zeta(2, zipfTheta)

	return &zipfian{
		n:            n,
		alpha:        1 / (1 - zipfTheta),
		zetan:        zetan,
		eta:          (1 - math.Pow(2/float64(n), 1-zipfTheta)) / (1 - zeta2/zetan),
		halfPowTheta: math.Pow(0.5, zipfTheta),
	}
}

func zeta(n uint64, theta float64) float64 {
	var sum float64
	for i := uint64(1); i <= n; i++ {
		sum += 1 / math.Pow(float64(i), theta)
	}

	return sum
}

// next returns an item rank: 0 is the most popular and popularity falls with
// rank.
func (z *zipfian) next(r *rand.Rand) uint64 {
	u := r.Float64()
	uz := u * z.zetan

	if uz < 1 {
		return 0
	}
	if uz < 1+z.halfPowTheta {
		return 1
	}

	return min(uint64(float64(z.n)*math.Pow(z.eta*u-z.eta+1, z.alpha)), z.n-1)
}

// scramble spreads popular ranks over the keyspace with an FNV hash, as YCSB's
// ScrambledZipfian does, so the hot keys aren't all next to each other. YCSB
// hashes into a much larger item space first, so the skew matches but the exact
// key identities differ.
func scramble(rank, n uint64) uint64 {
	h := fnv.New64a()
	var b [8]byte
	for i := range b {
		b[i] = byte(rank >> (8 * i))
	}
	h.Write(b[:])

	return h.Sum64() % n
}

// keyChooser returns a function picking a key index in [0, n).
func keyChooser(dist string, n uint64) func(*rand.Rand) uint64 {
	if dist == "uniform" || n < 2 {
		return func(r *rand.Rand) uint64 { return r.Uint64N(n) }
	}

	z := newZipfian(n)

	return func(r *rand.Rand) uint64 { return scramble(z.next(r), n) }
}
