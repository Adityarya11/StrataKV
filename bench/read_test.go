package bench

import (
	"fmt"
	"testing"
)

// sink keeps Get results alive so the compiler can't drop the call.
var sink []byte

// BenchmarkGet measures point reads on fixtures of a known shape:
//
//	op     hit (key exists) or miss (key was never written)
//	state  fragmented (one segment per flush) or compacted (one segment)
//	segs   segments in the fixture before compaction
//	val    value size; with a fixed 1 MiB flush it sets keys per segment, which
//	       decides how much a Bloom filter can save
//
// These are warm page cache numbers. The fixtures fit in RAM, so this measures
// CPU and syscall cost, not cold-disk latency.
func BenchmarkGet(b *testing.B) {
	for _, op := range []string{"hit", "miss"} {
		for _, state := range []string{"fragmented", "compacted"} {
			for _, segs := range cfg.segs {
				for _, val := range cfg.vals {
					b.Run(fmt.Sprintf("op=%s/state=%s/segs=%d/val=%d", op, state, segs, val), func(b *testing.B) {
						if state == "compacted" && segs == 1 {
							b.Skip("one segment is already compacted")
						}

						dir := fixtureDir(b, fixtureKey{segs: segs, val: val, state: state}, func(dir string) error {
							if err := buildFixture(dir, segs, val); err != nil {
								return err
							}
							if state == "compacted" {
								return compactFixture(dir)
							}

							return nil
						})

						runGet(b, dir, op, segs*recordsPerSegment(val))
					})
				}
			}
		}
	}
}

// BenchmarkOpen measures startup with N segments. v2 reads one trailer per
// segment; v1.0.3 rescans every byte to rebuild filters. Close isn't timed: v2
// fsyncs the log on close and v1 doesn't, and that shouldn't count as Open.
func BenchmarkOpen(b *testing.B) {
	for _, segs := range cfg.segs {
		for _, val := range cfg.vals {
			b.Run(fmt.Sprintf("segs=%d/val=%d", segs, val), func(b *testing.B) {
				dir := fixtureDir(b, fixtureKey{segs: segs, val: val, state: "fragmented"}, func(dir string) error {
					return buildFixture(dir, segs, val)
				})

				// this loop uses StopTimer instead of b.Loop, so the timer has been
				// running since the function started and counted the fixture build.
				// Reset it.
				b.ResetTimer()

				for i := 0; i < b.N; i++ {
					st, err := openStore(dir)
					if err != nil {
						b.Fatalf("open %s: %v", dir, err)
					}

					b.StopTimer()
					st.Close()
					b.StartTimer()
				}
			})
		}
	}
}

// runGet is the timed loop shared by the read benchmarks. It checks each
// result, so a broken fixture fails instead of timing a fast error path.
func runGet(b *testing.B, dir, op string, total int) {
	b.Helper()

	st, err := openStore(dir)
	if err != nil {
		b.Fatalf("open %s: %v", dir, err)
	}
	defer st.Close()

	keys := missKeys()
	if op == "hit" {
		keys = hitKeys(total)
	}
	wantFound := op == "hit"

	i := 0
	for b.Loop() {
		v, found, err := st.Get(keys[i%len(keys)])
		i++

		if err != nil {
			b.Fatalf("Get: %v", err)
		}
		if found != wantFound {
			b.Fatalf("op=%s but found=%v", op, found)
		}

		sink = v
	}
}
