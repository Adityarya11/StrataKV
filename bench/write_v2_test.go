//go:build !v1

package bench

import (
	"fmt"
	"testing"

	stratakv "github.com/Adityarya11/StrataKV/v2/engine"
)

// Both write benchmarks need -benchtime=Nx. Without it the framework keeps
// writing for a full second, which on a fast disk is hundreds of MB per run.
// With it, total writes are N times the bytes per op.

// BenchmarkPut compares sync modes across value sizes. Put the sync=always rows
// next to BenchmarkFsyncFloor to see how much of a write is the fsync.
func BenchmarkPut(b *testing.B) {
	for _, mode := range []stratakv.SyncMode{stratakv.SyncNever, stratakv.SyncAlways} {
		for _, val := range []int{64, 1024, 8192} {
			b.Run(fmt.Sprintf("sync=%s/val=%d", mode, val), func(b *testing.B) {
				guardWrites(b, int64(keySize+val+recordOverhead))

				opts := stratakv.DefaultOptions()
				opts.Sync = mode

				db, err := stratakv.OpenWith(scratchDir(b), opts)
				if err != nil {
					b.Fatalf("open: %v", err)
				}
				defer db.Close()

				value := valueOf(val)
				key := newSeqKey()

				i := 0
				for b.Loop() {
					seqKey(key, i)
					i++

					if err := db.Put(key, value); err != nil {
						b.Fatalf("Put: %v", err)
					}
				}
			})
		}
	}
}

// BenchmarkBatch commits batches of different sizes under SyncAlways, where the
// fsync dominates. ns/op is per batch; ns/record makes sizes comparable.
// Building the batch is inside the timed loop.
func BenchmarkBatch(b *testing.B) {
	const val = 1024

	for _, size := range []int{1, 10, 100, 1000} {
		b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
			guardWrites(b, int64(size*(keySize+val+recordOverhead)))

			db, err := stratakv.Open(scratchDir(b))
			if err != nil {
				b.Fatalf("open: %v", err)
			}
			defer db.Close()

			value := valueOf(val)
			key := newSeqKey()
			batch := db.NewBatch()

			i, commits := 0, 0
			for b.Loop() {
				for range size {
					seqKey(key, i)
					i++

					if err := batch.Put(key, value); err != nil {
						b.Fatalf("Batch.Put: %v", err)
					}
				}

				if err := batch.Commit(); err != nil {
					b.Fatalf("Commit: %v", err)
				}
				commits++
			}

			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(commits*size), "ns/record")
		})
	}
}
