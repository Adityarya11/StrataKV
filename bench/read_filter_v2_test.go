//go:build !v1

package bench

import (
	"fmt"
	"testing"

	stratakv "github.com/Adityarya11/StrataKV/v2/engine"
)

// BenchmarkGetFilter shows what the Bloom filter buys. The same fixtures are
// built with a 1% filter and with a 99% one. A 99% target squeezes the filter
// to a few hundred bits, so it saturates and prunes almost nothing. It stands in
// for "no filter"; there is no real off switch. Its false-positive rate depends
// on keys per segment, so read the whole sweep, not one row.
func BenchmarkGetFilter(b *testing.B) {
	defaultFPR := stratakv.DefaultOptions().FilterFalsePositiveRate

	for _, fpr := range []float64{defaultFPR, 0.99} {
		for _, op := range []string{"miss", "hit"} {
			for _, segs := range cfg.segs {
				for _, val := range cfg.vals {
					b.Run(fmt.Sprintf("fpr=%g/op=%s/segs=%d/val=%d", fpr, op, segs, val), func(b *testing.B) {
						variant := ""
						if fpr != defaultFPR {
							variant = fmt.Sprintf("fpr=%g", fpr)
						}

						dir := fixtureDir(b, fixtureKey{segs: segs, val: val, state: "fragmented", variant: variant}, func(dir string) error {
							return buildFixtureWith(dir, segs, val, fpr)
						})

						runGet(b, dir, op, segs*recordsPerSegment(val))
					})
				}
			}
		}
	}
}
