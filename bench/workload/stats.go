package main

import (
	"fmt"
	"io"
	"math"
	"slices"
	"time"
)

type opKind uint8

const (
	opRead opKind = iota
	opWrite
	opKinds
)

func (k opKind) String() string {
	if k == opRead {
		return "read"
	}

	return "write"
}

// sample is one operation's latency and the second of the window it started in.
type sample struct {
	sec  int32
	kind opKind
	ns   int64
}

// percentile uses the nearest-rank method on an ascending slice.
func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}

	rank := int(math.Ceil(p / 100 * float64(len(sorted))))

	return sorted[min(max(rank-1, 0), len(sorted)-1)]
}

func us(ns int64) float64 { return float64(ns) / 1e3 }

func printSummary(w io.Writer, kind opKind, ns []int64, elapsed time.Duration) {
	if len(ns) == 0 {
		fmt.Fprintf(w, "%-6s no operations\n", kind)
		return
	}

	slices.Sort(ns)

	var sum int64
	for _, v := range ns {
		sum += v
	}

	fmt.Fprintf(w, "%-6s n=%-9d ops/s=%-9.0f mean=%-9.1f p50=%-9.1f p95=%-9.1f p99=%-10.1f p99.9=%-11.1f max=%-12.1f (us)\n",
		kind, len(ns), float64(len(ns))/elapsed.Seconds(),
		us(sum/int64(len(ns))), us(percentile(ns, 50)), us(percentile(ns, 95)),
		us(percentile(ns, 99)), us(percentile(ns, 99.9)), us(ns[len(ns)-1]))
}

// writeSeries writes one CSV row per second of the measured window.
func writeSeries(w io.Writer, samples []sample, seconds int, segments, compactMs []int) {
	type bucket [opKinds][]int64

	buckets := make([]bucket, seconds)
	for _, s := range samples {
		if int(s.sec) < seconds {
			buckets[s.sec][s.kind] = append(buckets[s.sec][s.kind], s.ns)
		}
	}

	fmt.Fprintln(w, "second,reads,writes,read_p50_us,read_p99_us,read_max_us,write_p99_us,write_max_us,segments,compaction_ms")

	for sec := range seconds {
		r, wr := buckets[sec][opRead], buckets[sec][opWrite]
		slices.Sort(r)
		slices.Sort(wr)

		maxOf := func(v []int64) int64 {
			if len(v) == 0 {
				return 0
			}
			return v[len(v)-1]
		}

		fmt.Fprintf(w, "%d,%d,%d,%.1f,%.1f,%.1f,%.1f,%.1f,%d,%d\n", sec, len(r), len(wr),
			us(percentile(r, 50)), us(percentile(r, 99)), us(maxOf(r)),
			us(percentile(wr, 99)), us(maxOf(wr)), segments[sec], compactMs[sec])
	}
}
