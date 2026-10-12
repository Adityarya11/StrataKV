# StrataKV benchmark harness

Measures the engine through its public API only. Nothing in here changes the
library; `go build ./...` and `go test ./...` at the repository root are
unaffected, and a plain `go test ./...` does not touch the disk from this
package.

The results of the run quoted in the main README are written up in
[`results/2026-10-06/SUMMARY.md`](results/2026-10-06/SUMMARY.md). Raw output from
`run-report.sh` stays local and is ignored by git; only a hand-written
`SUMMARY.md` per run is kept.

## Safety model

| Guard | What it does |
| --- | --- |
| Levels | `STRATA_BENCH_LEVEL` = `smoke` (default, ~10 MiB of fixtures), `report` (what `run-report.sh` uses, ~350 MiB), `standard`, `full`. |
| Fixture budget | `STRATA_BENCH_MAX_MB` (default 256). A fixture that would exceed it is skipped with a message, never built. |
| Write interlock | Benchmarks that write refuse to run without `-benchtime=Nx`, and skip if N x bytes-per-op exceeds the budget. Disk writes are bounded by the command you typed. |
| Bounded files | The fsync benchmark truncates its file at 8 MiB (64 x block size for large blocks). |
| One run directory | Everything lives under `bench/.fixtures/run-*`, removed on exit. If a run is killed, delete `bench/.fixtures` by hand. |
| Real disk | Defaults to `bench/.fixtures`, on the repository's disk. `/tmp` is often tmpfs, where fsync is free and sync numbers are meaningless. Override with `STRATA_BENCH_DIR`. |

Memory: the largest allocation is fixture *building* for compacted states,
which merges a whole fixture in RAM (roughly 2x its size). At `smoke` that is
~8 MiB. At `full`, the 100-segment compacted fixtures need several hundred MiB;
check free RAM first.

## One command

```bash
bash bench/run-report.sh
```

Runs everything below, saves the output under `results/<date>_<machine>/` along
with a record of the machine (`env.txt`), and writes the v1.0.3 to v2 comparisons
if `benchstat` is installed. It refuses to start on battery power, with under 2 GB of free RAM, or
under 3 GB of free disk. `DRY_RUN=1 bash bench/run-report.sh` prints the checks
and every command without running any. Expect about 20 minutes. Close browsers
first.

## What each benchmark answers

| Benchmark | Question |
| --- | --- |
| `FsyncFloor` | What does the disk charge for a durable write? The floor under every `sync=always` number. |
| `Get` | Point-read cost by hit/miss, fragmented vs compacted, segment count, and value size. |
| `Open` | Startup cost by segment count. v2 reads one trailer per segment; v1.0.3 rescans every byte. |
| `GetFilter` (v2 only) | What does the Bloom filter buy? 1% filter vs a 99% filter that saturates and prunes almost nothing. |
| `Compact` (v2 only) | Cost and payoff of compacting a churned database: time, bytes reclaimed, records dropped, memory allocated. |
| `Put` (v2 only) | `SyncNever` vs `SyncAlways` by value size. |
| `Batch` (v2 only) | How much of a write is the fsync? ns/record by batch size. |
| `workload/` | YCSB-style A, B, C and a cache-fill mix: per-operation latency percentiles and a per-second series that shows flush and compaction stalls. |

Names look like `Get/op=hit/state=compacted/segs=10/val=1024`, so `benchstat`
can group by any of those keys.

## The workload driver

`go run ./bench/workload -workload b -target-ops 10000 -csv out.csv`

A reimplementation of YCSB's core workload definitions (A 50/50, B 95/5,
C read-only), plus `blan`, a cache-fill mix (95% read, 5% insert, small values),
with YCSB's zipfian key generator (Gray et al., theta 0.99). It is **YCSB-style,
not the YCSB tool**; say so. Keys are 64-character hex digests.

- **Closed loop, with an optional throttle.** `-target-ops` fixes the offered
  load so write volume is predictable. Under a stall a closed loop issues fewer
  operations than an open-loop client would, so tail percentiles are a lower
  bound.
- **Caps.** The run stops at the first of `-duration`, `-max-ops`, or
  `-max-write-mb`. The data directory is removed on exit.
- **Clock.** Per-operation latencies use the Windows performance counter, not
  `time.Now()`. On Windows `time.Now()` follows the system timer, which is
  15.6 ms by default and 1 ms while a browser is open; with it, nearly every
  fast operation reads as zero. The run's `env.txt` records the timer resolution.
- `-compact-at N` compacts in the background whenever N segments exist, which
  shows the stop-the-world stalls. `-compact-after-load` compacts once before
  the run, which shows the cost of one large segment.

## Running it

Set a results directory once per session:

```bash
mkdir -p bench/results/2026-10-xx_mymachine
```

Then, one command at a time:

```bash
go test ./bench/ -count=1
```

(harness self-tests; no disk)

```bash
go test ./bench/ -run '^$' -bench FsyncFloor -benchtime=500x -count=5 -timeout 0 > bench/results/2026-10-xx_mymachine/v2-fsync.txt
```

```bash
go test ./bench/ -run '^$' -bench 'Get$' -count=10 -timeout 0 > bench/results/2026-10-xx_mymachine/v2-get.txt
```

```bash
go test ./bench/ -run '^$' -bench GetFilter -count=10 -timeout 0 > bench/results/2026-10-xx_mymachine/v2-filter.txt
```

```bash
go test ./bench/ -run '^$' -bench 'Put/sync=never' -benchtime=5000x -count=5 -timeout 0 > bench/results/2026-10-xx_mymachine/v2-put-never.txt
```

```bash
go test ./bench/ -run '^$' -bench 'Put/sync=always' -benchtime=200x -count=5 -timeout 0 > bench/results/2026-10-xx_mymachine/v2-put-always.txt
```

```bash
go test ./bench/ -run '^$' -bench Batch -benchtime=50x -count=5 -timeout 0 > bench/results/2026-10-xx_mymachine/v2-batch.txt
```

`-run '^$'` skips the unit tests. `-timeout 0` disables the 10-minute default,
which would otherwise kill a slow run.

Estimated cost of the whole list above (computed from the iteration counts, not
measured): about 0.6 GB of cumulative writes spread over the run, never more
than ~60 MB on disk at once, and a few minutes of wall time dominated by fsync
speed. The `Put/sync=always` and `Batch` runs are the slow ones on NTFS.

Larger sweeps: `STRATA_BENCH_LEVEL=standard` needs about 600 MiB of fixtures
(`STRATA_BENCH_MAX_MB=640`). In PowerShell:

```powershell
$env:STRATA_BENCH_LEVEL = "standard"; $env:STRATA_BENCH_MAX_MB = "640"
```

## A/B against v1.0.3

The same files run unchanged against the old engine, which settles "did v2
change read cost?" on one machine in one session. `git archive` extracts a
clean tree without touching your repository:

```bash
git archive v1.0.3 --prefix=strata-v103/ | tar -x -C ..
```

```bash
cp -r bench ../strata-v103/
```

Then, from `../strata-v103`:

```bash
go test -tags v1 ./bench/ -run '^$' -bench 'Get$' -count=10 -timeout 0 > ../StrataKV/bench/results/2026-10-xx_mymachine/v103-get.txt
```

Compare (`go install golang.org/x/perf/cmd/benchstat@latest` once):

```bash
benchstat bench/results/2026-10-xx_mymachine/v103-get.txt bench/results/2026-10-xx_mymachine/v2-get.txt
```

Only `FsyncFloor` and `Get` exist under `-tags v1`; v1 has no options, batches,
or `SyncMode`.

## Caveats that belong next to any number

- **Warm page cache.** Fixtures fit in RAM, so reads measure CPU and syscalls,
  not cold-disk latency. Say so.
- **Different fixtures per engine.** v2 fixtures are built through the engine.
  v1 fixtures are written directly in v1's on-disk format (v1 fsyncs every
  `Put`, which makes building by Put take hours). Same keys, same values, same
  segment sizes, but v1 then builds its own filters when it opens them.
- **The 99% filter is a proxy for "filter off", not a switch.** Its real
  false-positive rate depends on keys per segment (~84% at 1,000 keys, ~100% by
  10,000).
- **A miss costs about `false-positive-rate x scan cost`.** With a 1% filter,
  most misses are nanoseconds and a few are milliseconds, so a small `-benchtime`
  can land on a lucky or unlucky stretch of the 4,096 miss keys. Use `-count=10`
  or more and read the variance `benchstat` reports.
- **Windows.** Defender real-time scanning and NTFS journaling inflate fsync.
  Record whether the data directory is excluded, and ideally run both ways.
- **Smoke-size runs prove the plumbing, not the engine.** Publish only
  `-count>=10` results from a quiet machine.

## Not here yet

In-engine `Metrics()` (counters and histograms inside the library), and
external baselines (Pebble/Badger/bbolt, which belong in a separate module so
the library stays dependency-free).
