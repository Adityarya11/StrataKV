package main

import (
	"math/rand/v2"
	"testing"
)

func TestZipfianIsSkewedAndInRange(t *testing.T) {
	const (
		n     = 1000
		draws = 200_000
	)

	z := newZipfian(n)
	r := rand.New(rand.NewPCG(1, 2))
	counts := make([]int, n)

	for range draws {
		k := z.next(r)
		if k >= n {
			t.Fatalf("rank %d out of range [0,%d)", k, n)
		}
		counts[k]++
	}

	// with theta 0.99 over 1000 items the top item takes about 13%
	if top := float64(counts[0]) / draws; top < 0.08 || top > 0.20 {
		t.Errorf("rank 0 drew %.1f%% of requests, want roughly 13%%", top*100)
	}
	if !(counts[0] > counts[99] && counts[99] > counts[999]) {
		t.Errorf("popularity should fall with rank: %d, %d, %d", counts[0], counts[99], counts[999])
	}
}

func TestScrambleStaysInRangeAndMoves(t *testing.T) {
	const n = 5000

	seen := map[uint64]bool{}
	for rank := range uint64(n) {
		k := scramble(rank, n)
		if k >= n {
			t.Fatalf("scramble(%d) = %d out of range", rank, k)
		}
		seen[k] = true
	}

	if len(seen) < n/2 {
		t.Errorf("only %d distinct keys from %d ranks; hash is clumping", len(seen), n)
	}
}

func TestUniformCoversTheKeyspace(t *testing.T) {
	choose := keyChooser("uniform", 100)
	r := rand.New(rand.NewPCG(5, 6))

	seen := map[uint64]bool{}
	for range 10_000 {
		seen[choose(r)] = true
	}

	if len(seen) != 100 {
		t.Errorf("uniform chooser reached %d of 100 keys", len(seen))
	}
}

// A clock that can't tell two back-to-back reads apart makes every latency
// zero, which is what time.Now() did on this machine. This keeps that from
// coming back.
func TestNanotimeHasUsableResolution(t *testing.T) {
	const pairs = 100_000

	zero, prev := 0, nanotime()
	for range pairs {
		start := nanotime()
		if nanotime()-start == 0 {
			zero++
		}
		if start < prev {
			t.Fatal("nanotime went backwards")
		}
		prev = start
	}

	if zero > pairs/2 {
		t.Errorf("%d of %d empty measurements read zero; clock is too coarse to time operations", zero, pairs)
	}
}

func TestPercentileNearestRank(t *testing.T) {
	sorted := make([]int64, 100)
	for i := range sorted {
		sorted[i] = int64(i + 1)
	}

	for p, want := range map[float64]int64{50: 50, 99: 99, 99.9: 100, 100: 100} {
		if got := percentile(sorted, p); got != want {
			t.Errorf("percentile(%v) = %d, want %d", p, got, want)
		}
	}
}
