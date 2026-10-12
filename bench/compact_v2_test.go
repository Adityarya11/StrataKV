//go:build !v1

package bench

import (
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	stratakv "github.com/Adityarya11/StrataKV/v2/engine"
)

func churnUnique() int {
	switch levelName {
	case "smoke":
		return 2000
	case "report":
		return 10000
	default:
		return 20000
	}
}

// buildChurn makes a database with plenty to reclaim: every key written once,
// three overwrites per key on a hot quarter of the keys, then deletes across
// half of them. Segments pile up and nothing compacts them, like a long-running
// cache that nobody calls Compact on.
func buildChurn(dir string, unique, valSize int) error {
	opts := stratakv.DefaultOptions()
	opts.Sync = stratakv.SyncNever

	db, err := stratakv.OpenWith(dir, opts)
	if err != nil {
		return err
	}

	val := valueOf(valSize)
	r := rand.New(rand.NewPCG(3, 4))

	for i := range unique {
		if err := db.Put(keyFor(i), val); err != nil {
			db.Close()
			return err
		}
	}
	for range 3 * unique {
		if err := db.Put(keyFor(r.IntN(unique/4)), val); err != nil {
			db.Close()
			return err
		}
	}
	for range unique / 4 {
		if err := db.Delete(keyFor(r.IntN(unique / 2))); err != nil {
			db.Close()
			return err
		}
	}

	if err := db.Flush(); err != nil {
		db.Close()
		return err
	}

	return db.Close()
}

// BenchmarkCompact times one full compaction of the churn database and reports
// what it reclaimed. Compaction destroys its input, so each iteration works on
// a fresh copy made outside the timed part. Run it with -benchtime=1x.
//
// reclaimed-% belongs to this workload, so quote it with the workload.
func BenchmarkCompact(b *testing.B) {
	unique := churnUnique()
	const val = 1024

	b.Run(fmt.Sprintf("unique=%d/val=%d", unique, val), func(b *testing.B) {
		segs := (4*unique*(keySize+val+recordOverhead) + segmentTarget - 1) / segmentTarget

		fx := fixtureDir(b, fixtureKey{segs: segs, val: val, state: "churn", variant: fmt.Sprintf("u=%d", unique)},
			func(dir string) error { return buildChurn(dir, unique, val) })

		size, err := dirBytes(fx)
		if err != nil {
			b.Fatalf("size fixture: %v", err)
		}
		guardWrites(b, 2*size) // a copy in, a compacted segment out

		// as in BenchmarkOpen, keep the fixture build out of the timing
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			b.StopTimer()

			work, err := os.MkdirTemp(fixtureRoot(b), "compact-")
			if err != nil {
				b.Fatalf("scratch: %v", err)
			}
			if err := copyDir(fx, work); err != nil {
				b.Fatalf("copy fixture: %v", err)
			}

			db, err := stratakv.Open(work)
			if err != nil {
				b.Fatalf("open: %v", err)
			}

			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)

			b.StartTimer()
			res, err := db.Compact()
			b.StopTimer()

			runtime.ReadMemStats(&after)

			if err != nil {
				b.Fatalf("Compact: %v", err)
			}

			db.Close()
			os.RemoveAll(work)

			if i == b.N-1 {
				const mib = 1 << 20

				b.ReportMetric(float64(res.SegmentsMerged), "segments-merged")
				b.ReportMetric(float64(res.RecordsRead), "records-in")
				b.ReportMetric(float64(res.RecordsWritten), "records-out")
				b.ReportMetric(float64(res.TombstonesPurged), "tombstones-purged")
				b.ReportMetric(float64(res.BytesBefore)/mib, "MiB-before")
				b.ReportMetric(float64(res.BytesAfter)/mib, "MiB-after")
				b.ReportMetric(100*float64(res.BytesBefore-res.BytesAfter)/float64(res.BytesBefore), "reclaimed-%")
				b.ReportMetric(float64(after.TotalAlloc-before.TotalAlloc)/mib, "alloc-MiB")
			}
		}
	})
}

func dirBytes(dir string) (int64, error) {
	var total int64

	err := filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()

		return nil
	})

	return total, err
}

func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := copyFile(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}

	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}

	return out.Close()
}
