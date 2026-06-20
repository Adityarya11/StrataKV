# Benchmarks

StrataKV was evaluated with a synthetic, append-heavy workload designed to stress the specific failure modes an LSM-tree architecture is built to handle: stale data accumulation, segment fragmentation, read amplification, and crash recovery.

## Environment

| Component       | Details                                           |
| --------------- | ------------------------------------------------- |
| OS / Filesystem | Windows 11, NTFS                                  |
| Language        | Go                                                |
| Storage model   | Append-only WAL + immutable segments              |
| Benchmark type  | Local synthetic workload (`scripts/benchmark.go`) |

## Workload

- 50,000 initial key insertions
- 200,000 overwrites, concentrated on the first quarter of the key range to force high overwrite density
- 80,000 tombstone deletes
- 8 KB average payload size
- 1,000 reads against existing keys + 1,000 reads against guaranteed-missing keys, run both before and after compaction

This produced ~1,945 immutable segment files prior to compaction — the condition under which read amplification is worst.

---

## Results: Compaction Impact

| Metric                                      | Value       |
| ------------------------------------------- | ----------- |
| Total workload execution time               | 6m 37.5s    |
| Pre-compaction disk usage                   | 1,953.79 MB |
| Post-compaction disk usage                  | 393.42 MB   |
| Storage reclaimed                           | 79.86%      |
| Segment count                               | 1,945 → 2   |
| GET latency, existing key (pre-compaction)  | 1.230s      |
| GET latency, existing key (post-compaction) | 186.1 ms    |
| Compaction duration                         | 9.22s       |
| WAL recovery time (full workload replay)    | 446.8 ms    |

**Why pre-compaction reads are slow:** the read path scans segments newest-to-oldest with no sparse index, so with ~1,945 segments on disk, a single `Get` can mean traversing nearly 2,000 files sequentially before finding (or ruling out) a key. Compaction collapses that to 2 segments, cutting GET latency by roughly 6.6x.

**Why compaction is expensive but worth it:** it reads every segment fully into memory, keeps only the latest non-deleted version of each key, and rewrites one file — a 9.2s pause that buys back 80% of disk space and a 6.6x read speedup. This is the classic LSM-tree space-amplification-vs-compaction-cost tradeoff.

---

## Results: Bloom Filter Impact

Same workload, run twice — once with Bloom filters disabled, once enabled — isolating their effect on read latency.

| Metric (post-compaction)  | Without Bloom Filter | With Bloom Filter | Speedup |
| ------------------------- | -------------------- | ----------------- | ------- |
| GET latency, existing key | 186.1 ms             | 175.0 µs          | ~1,063x |
| GET latency, missing key  | 358.7 ms             | 163.3 µs          | ~2,196x |

At 2 segments post-compaction, a Bloom filter check converts a worst-case full segment scan into a single in-memory bit-array lookup. Missing keys benefit most, since without a filter, ruling out a key still requires scanning every segment to confirm absence.

### The bug this benchmark caught

The first Bloom filter run showed _pre-compaction_ reads getting slower with filters enabled (1.378s) than without (1.230s) — the opposite of the expected effect. Tracing the read path found the cause in `flushLocked()`: segments created by a MemTable flush were written to disk correctly, but their Bloom filter was never registered into `db.segmentFilters`. Only filters built at startup or after compaction were registered. So every read against a freshly flushed, pre-compaction segment silently fell back to a full linear scan — paying the cost of a filter check that never actually filtered anything.

The fix was a single registration call inside `flushLocked()`, mirroring what `Compact()` and `Open()` already did. Post-fix, the numbers above are consistent with expectations. This is also why the benchmark suite runs reads both before and after every structural change — the regression would have been invisible without a before/after comparison.

---

## Limitations of This Benchmark

This benchmark validates architecture, not production-scale performance:

- Synthetic, uniform-ish access pattern — no Zipfian hot-key skew
- Single-node, single-machine execution
- No sparse segment indexing — within a candidate segment, search is still linear
- Stop-the-world compaction — not measuring concurrent read/write throughput during compaction
- Windows NTFS specifically; synchronous `fsync()` overhead is filesystem-dependent, and this workload was notably slower than expected during write generation, likely due to NTFS journaling and Windows Defender real-time scanning intercepting file writes

Treat these numbers as evidence of correct LSM-tree behavior under stress, not as a competitive benchmark against RocksDB or LevelDB.

---

## Next Steps Suggested by These Results

1. **Sparse segment indexing** — Bloom filters eliminate whole-segment scans for absent keys; a sparse offset index would cut the remaining linear-scan cost for keys that _do_ exist in a candidate segment.
2. **Group-commit WAL** — every write currently calls `fsync()` individually. Batching syncs on a timer or write-count threshold would reduce the synchronous I/O overhead visible in the workload generation phase.
3. **Background, non-blocking compaction** — current compaction holds the database lock for its full 9.2s duration; moving this off the request path would remove the largest source of write/read unavailability.
