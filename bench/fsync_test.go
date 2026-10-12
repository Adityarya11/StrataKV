package bench

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// BenchmarkFsyncFloor measures the disk, not the engine: write a block and
// optionally fsync it. A SyncAlways Put can't be faster than the sync=true row
// of a similar size. The file is truncated at a small cap so disk use stays
// bounded, while writes still extend it the way a WAL does.
func BenchmarkFsyncFloor(b *testing.B) {
	sizes := []int{100, 4096}
	if levelName != "smoke" {
		sizes = append(sizes, 1<<20)
	}

	for _, size := range sizes {
		for _, doSync := range []bool{true, false} {
			b.Run(fmt.Sprintf("sync=%t/size=%d", doSync, size), func(b *testing.B) {
				n := guardWrites(b, int64(size))

				path := filepath.Join(fixtureRoot(b), fmt.Sprintf("floor-%d-%t.dat", size, doSync))
				f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
				if err != nil {
					b.Fatalf("create %s: %v", path, err)
				}
				defer func() {
					f.Close()
					os.Remove(path)
				}()

				buf := make([]byte, size)
				capBytes := max(8<<20, 64*size)
				pos := 0

				var samples []time.Duration
				if doSync {
					samples = make([]time.Duration, 0, n+1)
				}

				for b.Loop() {
					if pos+size > capBytes {
						if err := f.Truncate(0); err != nil {
							b.Fatalf("truncate: %v", err)
						}
						pos = 0
					}

					if !doSync {
						if _, err := f.WriteAt(buf, int64(pos)); err != nil {
							b.Fatalf("write: %v", err)
						}
						pos += size
						continue
					}

					start := nanotime()
					if _, err := f.WriteAt(buf, int64(pos)); err != nil {
						b.Fatalf("write: %v", err)
					}
					if err := f.Sync(); err != nil {
						b.Fatalf("sync: %v", err)
					}
					samples = append(samples, time.Duration(nanotime()-start))
					pos += size
				}

				if len(samples) > 0 {
					slices.Sort(samples)
					b.ReportMetric(float64(percentile(samples, 50)), "p50-ns")
					b.ReportMetric(float64(percentile(samples, 99)), "p99-ns")
					b.ReportMetric(float64(samples[len(samples)-1]), "max-ns")
				}
			})
		}
	}
}
