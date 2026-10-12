# StrataKV benchmark results, 2026-10-06

One run of `bench/run-report.sh`, written up by hand. The raw output isn't
published (it's about 150 KB of text); the script regenerates it in about 20
minutes. Nothing here is mocked: the real engine, real files on the NVMe drive,
real fsync. The data is synthetic (SHA-256 keys, fixed byte values), as in YCSB.

## Machine and conditions

| | |
| --- | --- |
| Machine | Lenovo 82SB, Ryzen 5 6600H (6 cores / 12 threads), 15.2 GB RAM |
| Disk | Micron MTFDKCD512TFK, 477 GB NVMe SSD |
| OS, Go | Windows 11 build 26300, Go 1.27.0 |
| Power | plugged in (battery 24% and charging), Balanced power plan |
| Defender | real-time protection on (exclusions unreadable without admin) |
| System timer | 15.6 ms, so per-operation latencies use the performance counter, not `time.Now()` |
| Code | commit `097f183` (v2.0.0) plus the uncommitted `bench/` harness |
| Level | `report`: 1, 10 and 25 segments of 64 B, 1 KB and 8 KB values, warm page cache |

5 to 6 runs per number. v1.0.3 and v2 were compared with `benchstat`.

## Findings

**1. fsync is the cost of a durable write** (`FsyncFloor`, `Put`).
A 100-byte write plus fsync: p50 2.34 ms, p99 4.7 ms, worst 51 ms. Without the
fsync: 6.3 µs, about 390x less. A `SyncAlways` Put costs 2.39 to 2.46 ms at 64 B,
1 KB and 8 KB values, the same as the fsync floor whatever the value size.

**2. Batching amortises it** (`Batch`, `SyncAlways`, 1 KB values).

| batch | per record | records/s |
| --- | --- | --- |
| 1 | 2.89 ms | 346 |
| 10 | 292 µs | 3,424 |
| 100 | 43.8 µs | 22,836 |
| 1000 | 21.8 µs | 45,867 (133x batch of 1) |

`SyncNever` Put: 2.9 µs (64 B), 24 µs (1 KB), 149 µs (8 KB).

**3. Startup, v1.0.3 to v2** (`Open`, n=6, p=0.002 in every row).
Faster in all 9 configurations, from -56% to -99.65%, geomean -96.2%. v2 reads one
trailer per segment, so startup depends on the segment count, not the bytes: 0.37 ms
with 1 segment, 1.4 ms with 10, 2.7 to 2.9 ms with 25, whatever the value size.
v1.0.3 rescans every byte: 25 segments of 64 B values took 825 ms against 2.9 ms.

**4. Point reads, v1.0.3 to v2** (`Get`).
Geomean -93.6% over 30 configurations. Faster in 23, no significant difference in
2, **slower in 5, all with 8 KB values** (+45% to +172%). Likely cause: v1 seeks
past values while v2 reads and checksums every value in a segment it scans, so at
8 KB the bytes dominate. At small values v1's per-record syscalls and its
directory listing on every `Get` dominate instead. This is reasoned from the
code, not profiled.

**5. Bloom filters** (`GetFilter`: a 1% filter against a saturated 99% one, which
stands in for "no filter").
Missing-key lookups are 96 to 132x faster at 64 B and 1 KB values, and 23 to 27x
faster at 8 KB. Hits are 1.1x faster with 1 segment and up to 18x with 25.
At 8 KB values a 1% filter costs more than v1's oversized one: a false positive
scans about 1 MB, and 25 segments x 1% x ~0.94 ms is ~235 µs, close to the 253 µs
measured. A lower target rate would help there.

**6. Compaction** (`Compact`; churn workload of 10k keys, 30k overwrites of a hot
quarter, 2.5k deletes).
36 segments, 35.6 MiB to 8.5 MiB (76.2% reclaimed), 35,663 records to 8,049,
1,951 tombstones purged, 66 to 85 ms (median 70), 88.9 MiB allocated. The
percentage belongs to this workload, so quote it with the workload.

**7. Compaction makes point reads slower, because segments have no index.**
Hit latency, fragmented against compacted (medians): 10 segments of 1 KB values,
585 µs against 5.6 ms (9.6x); 25 segments of 64 B values, 1.35 ms against 20.8 ms
(15x). YCSB-style workload C on 20k x 1 KB records: 5,751 ops/s and p50 657 µs
fragmented, against 417 ops/s and p50 9.1 ms after one compaction. Workload A with
compaction at 8 segments: p99 read 12.4 ms against 1.35 ms without.

**8. YCSB-style workloads** (20k records, 4 clients, zipfian, `SyncNever`; throttled
so write volume is bounded; closed loop, so the tails are a lower bound).

| workload | read p50 / p99 / p99.9 | write p50 / p99 / p99.9 |
| --- | --- | --- |
| A 50/50 @ 6k ops/s | 173 µs / 1.35 ms / 3.5 ms | 34 µs / 1.63 ms / 15.8 ms |
| B 95/5 @ 10k ops/s | 129 µs / 1.36 ms / 2.1 ms | 333 µs / 1.42 ms / 3.0 ms |
| C read-only | 657 µs / 2.09 ms / 2.7 ms | |
| blan 95/5 inserts, 256 B | 657 µs / 1.86 ms / 2.7 ms | 593 µs / 1.70 ms / 2.2 ms |
| B, `SyncAlways` @ 200 ops/s | 662 µs / 4.3 ms / 5.0 ms | 3.5 ms / 6.5 ms / 6.7 ms |

Max latency was 17.5 to 22.3 ms in the four `SyncNever` runs, **including the
read-only one**, so at that level it is noise from the machine and shouldn't be
blamed on flushes. The `SyncAlways` run peaked at 6.7 ms and the compaction run at
34.5 ms. The write p99.9 of 15.8 ms in A, against 3.0 ms in B, fits flush stalls
but wasn't isolated.

## Notes on the run

- `Open` and `Compact` were run twice. The first run's timer counted the fixture
  build in its first sample (one Open cell read 1.395 s against ~3 ms, the first
  compaction 868 ms against ~70 ms). I added `b.ResetTimer()` and re-ran those two;
  the numbers above are from the re-run. The other benchmarks use `b.Loop()`, which
  resets the timer itself, and weren't affected.
- v1.0.3 prints "Compaction started/completed" to stdout, which landed mid-line and
  cost one sample in each of the 6 v1 `hit/compacted` cells (n=5, not 6). Medians
  are unaffected.
- Some 1 KB and 8 KB read cells vary by 20 to 30% between runs: sub-millisecond
  operations on a laptop.
- v1.0.3 fixtures were written directly in its file format, because v1 fsyncs every
  `Put` and filling hundreds of segments through it would take hours. The real v1
  engine opened them.
- Not measured: cold-cache reads, databases beyond a few dozen MiB, other machines,
  a Defender exclusion, the High performance power plan, other databases.
