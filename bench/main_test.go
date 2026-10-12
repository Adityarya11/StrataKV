package bench

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Safety limits:
//
//   - STRATA_BENCH_MAX_MB (default 256) caps fixture size. A fixture that
//     would go over is skipped, not built.
//   - Benchmarks that write need -benchtime=Nx, so disk writes are bounded by
//     the command and not by how fast the machine is.
//   - The default level (smoke) needs about 10 MiB of fixtures.
//   - Everything lives in one run directory that is removed on exit.

type levelConfig struct {
	segs []int
	vals []int
}

var levels = map[string]levelConfig{
	"smoke": {segs: []int{1, 4}, vals: []int{1024}},
	// report is what run-report.sh uses: wide enough to show the trends, small
	// enough for a laptop (about 350 MiB of fixtures).
	"report":   {segs: []int{1, 10, 25}, vals: []int{64, 1024, 8192}},
	"standard": {segs: []int{1, 10, 50}, vals: []int{64, 1024, 8192}},
	"full":     {segs: []int{1, 10, 50, 100}, vals: []int{64, 1024, 8192}},
}

var (
	levelName string
	cfg       levelConfig
	budget    int64
)

func TestMain(m *testing.M) {
	levelName = envOr("STRATA_BENCH_LEVEL", "smoke")

	var ok bool
	if cfg, ok = levels[levelName]; !ok {
		fmt.Fprintf(os.Stderr, "unknown STRATA_BENCH_LEVEL %q: want smoke, report, standard or full\n", levelName)
		os.Exit(2)
	}

	mb, err := strconv.Atoi(envOr("STRATA_BENCH_MAX_MB", "256"))
	if err != nil || mb <= 0 {
		fmt.Fprintln(os.Stderr, "STRATA_BENCH_MAX_MB must be a positive integer")
		os.Exit(2)
	}
	budget = int64(mb) << 20

	// benchstat treats "key: value" lines before the first benchmark as
	// configuration
	fmt.Printf("strata-bench-engine: %s\n", engineName)
	fmt.Printf("strata-bench-level: %s\n", levelName)
	fmt.Printf("strata-bench-budget-mb: %d\n", mb)

	code := m.Run()
	removeRoot()
	os.Exit(code)
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}

	return fallback
}

var (
	rootOnce sync.Once
	rootDir  string
	rootErr  error
)

// fixtureRoot creates the run directory on first use, so a plain go test ./...
// doesn't touch the disk. It defaults to bench/.fixtures, on the repo's disk;
// /tmp is often tmpfs, where fsync is free.
func fixtureRoot(tb testing.TB) string {
	tb.Helper()

	rootOnce.Do(func() {
		base := envOr("STRATA_BENCH_DIR", ".fixtures")
		if rootErr = os.MkdirAll(base, 0o755); rootErr != nil {
			return
		}

		if entries, err := os.ReadDir(base); err == nil {
			stale := 0
			for _, e := range entries {
				if e.IsDir() && strings.HasPrefix(e.Name(), "run-") {
					stale++
				}
			}
			if stale > 0 {
				fmt.Fprintf(os.Stderr, "note: %d leftover run directories in %s from an interrupted run; delete them if unneeded\n", stale, base)
			}
		}

		rootDir, rootErr = os.MkdirTemp(base, "run-")
	})

	if rootErr != nil {
		tb.Fatalf("create fixture root: %v", rootErr)
	}

	return rootDir
}

func removeRoot() {
	if rootDir != "" {
		os.RemoveAll(rootDir)
	}
}

// scratchDir returns an empty directory that is removed when the benchmark ends.
func scratchDir(b *testing.B) string {
	b.Helper()

	dir, err := os.MkdirTemp(fixtureRoot(b), "scratch-")
	if err != nil {
		b.Fatalf("create scratch dir: %v", err)
	}
	b.Cleanup(func() { os.RemoveAll(dir) })

	return dir
}

type fixtureKey struct {
	segs, val int
	state     string // "fragmented" or "compacted"
	variant   string // empty for the default configuration
}

var (
	fixtureMu sync.Mutex
	fixtures  = map[fixtureKey]string{}
	usedBytes int64
)

func fixtureBytes(segs int) int64 {
	return int64(segs) * (segmentTarget + segmentTarget/16)
}

// fixtureDir builds each fixture once and reuses it across sub-benchmarks and
// -count runs. Reads don't change a fixture, so sharing is safe. The build
// happens before the timed part.
func fixtureDir(b *testing.B, k fixtureKey, build func(dir string) error) string {
	b.Helper()

	fixtureMu.Lock()
	defer fixtureMu.Unlock()

	if dir, ok := fixtures[k]; ok {
		return dir
	}

	need := fixtureBytes(k.segs)
	if usedBytes+need > budget {
		b.Skipf("fixture budget exceeded (%d MiB used + %d MiB needed > %d MiB); raise STRATA_BENCH_MAX_MB if your disk can take it",
			usedBytes>>20, need>>20, budget>>20)
	}

	dir := filepath.Join(fixtureRoot(b), fmt.Sprintf("fx-%03d", len(fixtures)))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		b.Fatalf("create fixture dir: %v", err)
	}

	fmt.Fprintf(os.Stderr, "building fixture: segs=%d val=%d state=%s variant=%q (~%d MiB)\n",
		k.segs, k.val, k.state, k.variant, need>>20)

	if err := build(dir); err != nil {
		os.RemoveAll(dir)
		b.Fatalf("build fixture: %v", err)
	}

	fixtures[k] = dir
	usedBytes += need

	return dir
}

// fixedIterations reports the N from -benchtime=Nx.
func fixedIterations() (int, bool) {
	f := flag.Lookup("test.benchtime")
	if f == nil {
		return 0, false
	}

	s := f.Value.String()
	if !strings.HasSuffix(s, "x") {
		return 0, false
	}

	n, err := strconv.Atoi(strings.TrimSuffix(s, "x"))

	return n, err == nil && n > 0
}

// guardWrites skips a write benchmark unless an explicit iteration count bounds
// its disk writes and they fit the budget. The framework runs one extra
// iteration first, hence n+1.
func guardWrites(b *testing.B, bytesPerOp int64) int {
	b.Helper()

	n, ok := fixedIterations()
	if !ok {
		b.Skip("skipped: write benchmarks need a fixed iteration count so disk writes stay bounded, e.g. -benchtime=2000x (see bench/README.md)")
	}

	if planned := int64(n+1) * bytesPerOp; planned > budget {
		b.Skipf("skipped: %d iterations x %d bytes = %d MiB exceeds the %d MiB budget; lower -benchtime or raise STRATA_BENCH_MAX_MB",
			n+1, bytesPerOp, planned>>20, budget>>20)
	}

	return n
}
