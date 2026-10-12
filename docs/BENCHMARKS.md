# Benchmarks

How the numbers in the README were produced, and what they leave out. The full tables for the run are in [`bench/results/2026-10-06/SUMMARY.md`](../bench/results/2026-10-06/SUMMARY.md). The raw output isn't published; [`bench/run-report.sh`](../bench/run-report.sh) regenerates it.

An earlier version of this page had figures from an ad-hoc harness (a 2,196x Bloom filter speedup and an 80% disk saving). They came from two different runs on different machine states and did not reproduce, so they were removed.

## Machine

Lenovo 82SB, Ryzen 5 6600H (6 cores), 15 GB RAM, Micron NVMe SSD, Windows 11, Go 1.27. Plugged in, Balanced power plan, Defender real-time protection on. The system timer was 15.6 ms, which is why per-operation latencies use the Windows performance counter instead of `time.Now()`. The full machine record is in `SUMMARY.md`.

## What was run

| Benchmark | What it measures |
| --- | --- |
| `FsyncFloor` | A write plus fsync, with and without the fsync, at 100 B, 4 KB and 1 MiB. The floor under every durable write. |
| `Get` | Point reads (hit and miss) against 1, 10 and 25 segments of 64 B, 1 KB and 8 KB values, fragmented and compacted. Run on v2 and on v1.0.3. |
| `Open` | Startup time for the same fixtures. Run on v2 and on v1.0.3. |
| `GetFilter` | The same reads with a 1% Bloom filter and with a 99% one, which saturates and prunes almost nothing. |
| `Compact` | One compaction of a database with many overwrites and some deletes. |
| `Put`, `Batch` | Write cost by sync mode, value size and batch size. |
| `workload/` | YCSB-style mixes (A 50/50, B 95/5, C read-only, and a cache-fill mix) with zipfian keys. Per-operation latency percentiles and a per-second series. |

Each benchmark ran 5 or 6 times. v1.0.3 and v2 were compared with `benchstat` on the same machine in the same session. Every read checks its result (a hit must be found, a miss must not), so a broken fixture fails instead of timing an error path.

## Data

Synthetic. Keys are the 64-character hex SHA-256 of an integer, which is the shape the compiler cache stores. Values are a fixed byte pattern. A 1 MiB flush holds 7,437 records at 64 B values, 953 at 1 KB and 127 at 8 KB, and that count decides how useful a Bloom filter is.

v1.0.3 fixtures were written directly in its on-disk format, since v1.0.3 fsyncs every `Put` and filling hundreds of segments through it would take hours. The real v1.0.3 engine then opened them.

## What it leaves out

- Warm page cache only. Cold reads would be slower.
- One machine, one disk, one OS. fsync cost varies a lot between setups. An earlier run on this laptop implied about 22 ms per fsync against 2.3 ms here.
- Databases of a few dozen MiB. Read cost grows with segment size, so larger databases will be slower.
- No comparison with other databases.
- The workload driver is closed loop, so under a stall it issues fewer requests than real clients would, and its tail percentiles are a lower bound. It is a reimplementation of YCSB's workload definitions and zipfian generator, not the YCSB tool.
- Max latency was around 20 ms in every `SyncNever` workload including the read-only one, so at that level it is noise from the machine, not from flushes.
- The 99% filter stands in for "no filter". There is no switch to turn the filter off.

## Reproduce

```bash
bash bench/run-report.sh
```

It checks that the machine is on AC power with enough free RAM and disk, runs everything (about 20 minutes), and writes a dated directory under `bench/results/`. `DRY_RUN=1` prints the plan without running it. See [`bench/README.md`](../bench/README.md).
