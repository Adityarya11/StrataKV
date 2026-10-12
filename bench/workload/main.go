// Command workload drives a StrataKV database with YCSB-style operation mixes
// and reports latency percentiles and a per-second series.
//
// The mixes follow YCSB's core set: A (50% read, 50% update), B (95/5),
// C (read only), plus "blan", a cache-fill shape (95% read, 5% insert, small
// values). Keys are 64-character hex SHA-256 digests, like the compiler cache
// stores. This reimplements YCSB's workload definitions and isn't the YCSB tool,
// so call it YCSB-style.
//
// The loop is closed: each client issues its next operation when the last one
// returns. During a stall (a flush or a compaction) a closed loop issues fewer
// operations than real clients would, so the tail percentiles are a lower
// bound.
//
// The run stops at the first of -duration, -max-ops or -max-write-mb, and the
// data directory is removed on exit.
package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	stratakv "github.com/Adityarya11/StrataKV/v2/engine"
)

type mix struct {
	label  string
	read   int // percent
	update int
	insert int
	value  int // default value size
}

var mixes = map[string]mix{
	"a":    {"A: update heavy, 50% read / 50% update", 50, 50, 0, 1024},
	"b":    {"B: read mostly, 95% read / 5% update", 95, 5, 0, 1024},
	"c":    {"C: read only", 100, 0, 0, 1024},
	"blan": {"blan: cache fill, 95% read / 5% insert", 95, 0, 5, 256},
}

const (
	keySize        = 64
	recordOverhead = 13
)

type config struct {
	workload    string
	records     int
	value       int
	threads     int
	duration    time.Duration
	warmup      time.Duration
	dist        string
	sync        string
	compactLoad bool
	compactAt   int
	targetOps   int
	maxOps      int64
	maxWriteMB  int64
	dir         string
	csvPath     string
}

func main() {
	var cfg config

	flag.StringVar(&cfg.workload, "workload", "b", "a, b, c or blan")
	flag.IntVar(&cfg.records, "records", 20000, "records loaded before the run")
	flag.IntVar(&cfg.value, "value", 0, "value size in bytes (0 = the workload's default)")
	flag.IntVar(&cfg.threads, "threads", 4, "concurrent clients")
	flag.DurationVar(&cfg.duration, "duration", 15*time.Second, "measured window")
	flag.DurationVar(&cfg.warmup, "warmup", 3*time.Second, "unmeasured warm-up before the window")
	flag.StringVar(&cfg.dist, "dist", "zipfian", "key distribution: zipfian or uniform")
	flag.StringVar(&cfg.sync, "sync", "never", "never or always")
	flag.BoolVar(&cfg.compactLoad, "compact-after-load", false, "compact once after the load phase")
	flag.IntVar(&cfg.compactAt, "compact-at", 0, "compact whenever this many segments exist (0 = never)")
	flag.IntVar(&cfg.targetOps, "target-ops", 0, "throttle to this many operations/s across all clients (0 = unthrottled); keeps write volume predictable")
	flag.Int64Var(&cfg.maxOps, "max-ops", 3_000_000, "stop after this many operations")
	flag.Int64Var(&cfg.maxWriteMB, "max-write-mb", 128, "stop after writing this many MiB")
	flag.StringVar(&cfg.dir, "dir", "", "data directory (default: a fresh directory under bench/.fixtures)")
	flag.StringVar(&cfg.csvPath, "csv", "", "write the per-second series here")
	flag.Parse()

	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func (c *config) validate() (mix, error) {
	m, ok := mixes[c.workload]
	if !ok {
		return m, fmt.Errorf("unknown -workload %q (a, b, c, blan)", c.workload)
	}

	switch {
	case c.records < 2 || c.records > 2_000_000:
		return m, fmt.Errorf("-records must be in [2, 2000000]")
	case c.threads < 1 || c.threads > 64:
		return m, fmt.Errorf("-threads must be in [1, 64]")
	case c.duration < time.Second || c.duration > 5*time.Minute:
		return m, fmt.Errorf("-duration must be in [1s, 5m]")
	case c.dist != "zipfian" && c.dist != "uniform":
		return m, fmt.Errorf("-dist must be zipfian or uniform")
	case c.sync != "never" && c.sync != "always":
		return m, fmt.Errorf("-sync must be never or always")
	case c.targetOps < 0:
		return m, fmt.Errorf("-target-ops must be >= 0")
	case c.maxWriteMB < 1 || c.maxWriteMB > 2048:
		return m, fmt.Errorf("-max-write-mb must be in [1, 2048]")
	}

	if c.value == 0 {
		c.value = m.value
	}
	if c.value < 1 || c.value > 1<<20 {
		return m, fmt.Errorf("-value must be in [1, 1048576]")
	}

	return m, nil
}

func targetLabel(n int) string {
	if n == 0 {
		return "unthrottled"
	}

	return fmt.Sprintf("%d ops/s", n)
}

func keyFor(i uint64) []byte {
	sum := sha256.Sum256(binary.BigEndian.AppendUint64(nil, i))
	return hex.AppendEncode(nil, sum[:])
}

func run(cfg config) error {
	m, err := cfg.validate()
	if err != nil {
		return err
	}

	dir := cfg.dir
	if dir == "" {
		base := filepath.Join("bench", ".fixtures")
		if err := os.MkdirAll(base, 0o755); err != nil {
			return err
		}
		if dir, err = os.MkdirTemp(base, "workload-"); err != nil {
			return err
		}
	}
	defer os.RemoveAll(dir)

	opts := stratakv.DefaultOptions()
	if cfg.sync == "always" {
		opts.Sync = stratakv.SyncAlways
	} else {
		opts.Sync = stratakv.SyncNever
	}

	db, err := stratakv.OpenWith(dir, opts)
	if err != nil {
		return err
	}
	defer db.Close()

	value := make([]byte, cfg.value)
	for i := range value {
		value[i] = byte(i*31 + 7)
	}

	fmt.Printf("workload   : %s\n", m.label)
	fmt.Printf("records    : %d x %d B values, keys %d B, dist=%s, sync=%s\n", cfg.records, cfg.value, keySize, cfg.dist, cfg.sync)
	fmt.Printf("clients    : %d (closed loop), warmup %v, window %v, GOMAXPROCS=%d, %s\n",
		cfg.threads, cfg.warmup, cfg.duration, runtime.GOMAXPROCS(0), runtime.Version())
	fmt.Printf("caps       : %d ops, %d MiB written, target %s\n", cfg.maxOps, cfg.maxWriteMB, targetLabel(cfg.targetOps))

	// load in batches so one fsync covers many records, even under SyncAlways
	loadStart := time.Now()
	batch := db.NewBatch()
	for i := range uint64(cfg.records) {
		if err := batch.Put(keyFor(i), value); err != nil {
			return fmt.Errorf("load: %w", err)
		}
		if batch.Len() == 500 {
			if err := batch.Commit(); err != nil {
				return fmt.Errorf("load: %w", err)
			}
		}
	}
	if err := batch.Commit(); err != nil {
		return fmt.Errorf("load: %w", err)
	}
	if err := db.Flush(); err != nil {
		return fmt.Errorf("load flush: %w", err)
	}

	fmt.Printf("load       : %v (%.0f records/s), %d segments\n",
		time.Since(loadStart).Round(time.Millisecond),
		float64(cfg.records)/time.Since(loadStart).Seconds(), db.Stats().Segments)

	if cfg.compactLoad {
		start := time.Now()
		res, err := db.Compact()
		if err != nil {
			return fmt.Errorf("compact after load: %w", err)
		}
		fmt.Printf("compaction : after load, %v, %d -> 1 segments, %d records\n",
			time.Since(start).Round(time.Millisecond), res.SegmentsMerged, res.RecordsWritten)
	}

	return drive(cfg, m, db, value)
}

type runState struct {
	stop       atomic.Bool
	ops        atomic.Int64
	written    atomic.Int64
	insertNext atomic.Uint64
	misses     atomic.Int64
	readErrs   atomic.Int64
	reason     atomic.Value
}

func (s *runState) halt(why string) {
	if s.stop.CompareAndSwap(false, true) {
		s.reason.Store(why)
	}
}

func drive(cfg config, m mix, db *stratakv.DB, value []byte) error {
	var st runState
	st.insertNext.Store(uint64(cfg.records))
	st.reason.Store("duration elapsed")

	choose := keyChooser(cfg.dist, uint64(cfg.records))

	// wall-clock bounds drive the sampler and the wait loop; operation timing
	// uses nanotime with the same bounds
	measureFrom := time.Now().Add(cfg.warmup)
	measureTo := measureFrom.Add(cfg.duration)
	fromNs := nanotime() + cfg.warmup.Nanoseconds()
	toNs := fromNs + cfg.duration.Nanoseconds()
	seconds := int(cfg.duration/time.Second) + 1

	segments := make([]int, seconds)
	compactMs := make([]int, seconds)
	var seriesMu sync.Mutex

	done := make(chan struct{})
	var aux sync.WaitGroup

	// sample the segment count once a second
	aux.Add(1)
	go func() {
		defer aux.Done()

		tick := time.NewTicker(time.Second)
		defer tick.Stop()

		for {
			select {
			case <-done:
				return
			case now := <-tick.C:
				if sec := int(now.Sub(measureFrom) / time.Second); sec >= 0 && sec < seconds {
					n := db.Stats().Segments
					seriesMu.Lock()
					segments[sec] = n
					seriesMu.Unlock()
				}
			}
		}
	}()

	var (
		compactions  int
		compactTotal time.Duration
		compactMax   time.Duration
	)

	if cfg.compactAt > 0 {
		aux.Add(1)
		go func() {
			defer aux.Done()

			tick := time.NewTicker(50 * time.Millisecond)
			defer tick.Stop()

			for {
				select {
				case <-done:
					return
				case <-tick.C:
					if db.Stats().Segments < cfg.compactAt {
						continue
					}

					start := time.Now()
					if _, err := db.Compact(); err != nil {
						fmt.Fprintln(os.Stderr, "compaction failed:", err)
						st.halt("compaction failed")
						return
					}
					d := time.Since(start)

					seriesMu.Lock()
					compactions++
					compactTotal += d
					compactMax = max(compactMax, d)
					if sec := int(start.Sub(measureFrom) / time.Second); sec >= 0 && sec < seconds {
						compactMs[sec] += int(d.Milliseconds())
					}
					seriesMu.Unlock()
				}
			}
		}()
	}

	perWorker := make([][]sample, cfg.threads)

	var workers sync.WaitGroup
	for w := range cfg.threads {
		workers.Add(1)
		go func() {
			defer workers.Done()

			r := rand.New(rand.NewPCG(uint64(w)+1, 0x9e3779b97f4a7c15))
			local := make([]sample, 0, 1<<16)
			perOp := int64(keySize + len(value) + recordOverhead)

			// throttle against a schedule, not a fixed sleep, so a slow operation
			// is followed by a shorter wait. Sleeping happens outside the timed
			// part. After a long stall the backlog is dropped instead of being
			// paid back in a burst of writes.
			var interval time.Duration
			if cfg.targetOps > 0 {
				interval = time.Duration(float64(time.Second) * float64(cfg.threads) / float64(cfg.targetOps))
			}
			next := time.Now()

			for !st.stop.Load() {
				if interval > 0 {
					next = next.Add(interval)
					if d := time.Until(next); d > 0 {
						time.Sleep(d)
					} else if d < -time.Second {
						next = time.Now()
					}
				}

				kind := opRead
				var key []byte

				switch pick := r.IntN(100); {
				case pick < m.read:
					key = keyFor(choose(r))
				case pick < m.read+m.update:
					kind, key = opWrite, keyFor(choose(r))
				default:
					kind, key = opWrite, keyFor(st.insertNext.Add(1)-1)
				}

				start := nanotime()
				if kind == opRead {
					_, found, err := db.Get(key)
					if err != nil {
						st.readErrs.Add(1)
						st.halt("read error: " + err.Error())
						break
					}
					if !found {
						st.misses.Add(1)
					}
				} else {
					if err := db.Put(key, value); err != nil {
						st.halt("write error: " + err.Error())
						break
					}
					if st.written.Add(perOp) > cfg.maxWriteMB<<20 {
						st.halt("write budget reached")
					}
				}
				elapsed := nanotime() - start

				if start >= fromNs && start < toNs {
					local = append(local, sample{
						sec:  int32((start - fromNs) / int64(time.Second)),
						kind: kind,
						ns:   elapsed,
					})
				}

				if st.ops.Add(1) >= cfg.maxOps {
					st.halt("operation cap reached")
				}
			}

			perWorker[w] = local
		}()
	}

	// wait out the window, but stop early if a cap is hit
	for time.Now().Before(measureTo) && !st.stop.Load() {
		time.Sleep(25 * time.Millisecond)
	}
	st.halt("duration elapsed")
	workers.Wait()
	close(done)
	aux.Wait()

	var samples []sample
	for _, p := range perWorker {
		samples = append(samples, p...)
	}

	// if a cap ended the run early, divide by the time actually measured
	measured := cfg.duration
	if st.reason.Load() != "duration elapsed" {
		var last int32
		for _, s := range samples {
			last = max(last, s.sec)
		}
		measured = time.Duration(last+1) * time.Second
	}

	var byKind [opKinds][]int64
	for _, s := range samples {
		byKind[s.kind] = append(byKind[s.kind], s.ns)
	}

	fmt.Printf("\nstopped    : %v after %d operations, %.1f MiB written\n",
		st.reason.Load(), st.ops.Load(), float64(st.written.Load())/(1<<20))
	fmt.Printf("window     : %v measured, %d samples\n\n", measured, len(samples))

	if len(samples) == 0 {
		fmt.Println("nothing was measured: a cap was reached during warm-up. Lower -target-ops or raise -max-write-mb.")
	}

	printSummary(os.Stdout, opRead, byKind[opRead], measured)
	printSummary(os.Stdout, opWrite, byKind[opWrite], measured)

	stats := db.Stats()
	fmt.Printf("\nend state  : %d segments, %d segment records, memtable %d keys\n",
		stats.Segments, stats.SegmentEntries, stats.MemTableKeys)

	if cfg.compactAt > 0 {
		fmt.Printf("compaction : %d during the window, %v total, longest %v (every read and write blocks while one runs)\n",
			compactions, compactTotal.Round(time.Millisecond), compactMax.Round(time.Millisecond))
	}
	if n := st.misses.Load(); n > 0 {
		fmt.Printf("WARNING    : %d reads of loaded keys returned not-found\n", n)
	}
	if n := st.readErrs.Load(); n > 0 {
		return fmt.Errorf("%d read errors", n)
	}

	if cfg.csvPath != "" {
		// carry the last value forward for seconds the sampler was stuck behind a
		// compaction
		for i := 1; i < seconds; i++ {
			if segments[i] == 0 {
				segments[i] = segments[i-1]
			}
		}

		f, err := os.Create(cfg.csvPath)
		if err != nil {
			return err
		}
		defer f.Close()

		writeSeries(f, samples, int(measured/time.Second), segments, compactMs)
	}

	return nil
}
