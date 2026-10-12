# StrataKV

An embedded key-value store for Go, built like an LSM tree: a write-ahead log, an in-memory buffer, immutable checksummed segment files with a Bloom filter each, and compaction. No dependencies outside the standard library.

[![Go Reference](https://pkg.go.dev/badge/github.com/Adityarya11/StrataKV/v2.svg)](https://pkg.go.dev/github.com/Adityarya11/StrataKV/v2)

I wrote it to understand how engines like LevelDB and RocksDB work, then used it as the cache for my compiler backend, [Blan](https://github.com/Adityarya11/blan-backend). Once something depends on it, the crash and corruption cases matter, so most of v2 is about those.

## Install

```bash
go get github.com/Adityarya11/StrataKV/v2@latest
```

The `/v2` is part of the import path. Without it you get the old v1 API.

```go
package main

import (
	"fmt"

	stratakv "github.com/Adityarya11/StrataKV/v2/engine"
)

func main() {
	db, err := stratakv.Open("./data")
	if err != nil {
		panic(err)
	}
	defer db.Close()

	db.Put([]byte("user:101"), []byte("aditya"))

	val, found, err := db.Get([]byte("user:101"))
	if err != nil {
		panic(err) // the read failed, which is different from a missing key
	}
	fmt.Println(string(val), found) // aditya true

	db.Delete([]byte("user:101"))
}
```

`Get` returns an error separately from `found` on purpose. A checksum failure or an I/O error is not a cache miss, and treating it as one hides corruption.

## How it works

```
engine.DB
  ├── MemTable          recent writes, in memory
  ├── WAL (0001.wal)    every write is logged here first
  └── Segments (*.seg)  immutable files, newest first, one Bloom filter each
```

- **Write.** The record goes to the WAL and is fsynced, then into the MemTable. At 1 MiB the MemTable is flushed to a new segment (written to a temp file, fsynced, renamed into place).
- **Read.** MemTable first, then segments from newest to oldest. A segment whose Bloom filter says "not here" is skipped without touching disk.
- **Delete.** Writes a tombstone. The key is physically removed at the next compaction.
- **Recovery.** Every record has a CRC-32C. On open, the WAL is replayed up to the first record that does not verify, and the rest is truncated and reported in `Stats().Recovery`. A segment is only accepted if its trailer is present and valid; the trailer is written last.
- **Compaction.** `Compact()` merges every segment into one, keeping the newest version of each key and dropping tombstones. It aborts without deleting anything if a segment fails to read.

## Options

```go
opts := stratakv.DefaultOptions()
opts.MemTableSize = 4 << 20            // flush at 4 MiB instead of 1
opts.FilterFalsePositiveRate = 0.001   // bigger filters, fewer wasted segment scans
opts.Sync = stratakv.SyncNever         // see below

db, err := stratakv.OpenWith("./data", opts)
```

| Option | Default | |
| --- | --- | --- |
| `MemTableSize` | 1 MiB | Larger means fewer, bigger segments and more memory. |
| `Sync` | `SyncAlways` | `SyncAlways` fsyncs before each write returns. `SyncNever` fsyncs on `Flush`, `Compact` and `Close` only. |
| `FilterFalsePositiveRate` | 0.01 | About 9.6 bits per key at 0.01, 14.4 at 0.001. |

`SyncNever` is for data you can regenerate, like a compile cache. Writes since the last sync are lost if the machine loses power or the process is killed. A clean `Close` loses nothing.

## Batches

```go
batch := db.NewBatch()
for _, item := range items {
	batch.Put(item.Key, item.Value)
}
if err := batch.Commit(); err != nil {
	return err
}
```

A batch is one write and one fsync, and it is all or nothing: if the process dies mid-write, recovery drops the whole batch.

## Performance

One Windows laptop (Ryzen 5 6600H, NVMe SSD, Go 1.27), plugged in, Balanced power plan, Defender on. Warm page cache, synthetic data (SHA-256 keys, 64 B to 8 KB values), 5 to 6 runs per number. v2 is compared with my own v1.0.3, and nothing here compares StrataKV with another database.

| What | Result | Setup |
| --- | --- | --- |
| fsync | 2.3 ms median, over 99% of a durable `Put` | 100 B write, `SyncAlways` |
| Batched writes | 346 to 45,867 records/s (133x) | 1,000 records per batch vs one, 1 KB values |
| Startup | 825 ms (v1.0.3) to 2.9 ms (v2) | 25 segments, 64 B values |
| Missing-key lookup | 59 µs vs 6.2 ms with a saturated filter (105x) | 10 segments, 1 KB values |
| Bloom false positives | 85.6% (v1.0.3) to 1.0% (v2) | 10,000 keys in a segment |
| Compaction | 35.6 MiB to 8.5 MiB, median 70 ms | 36 segments, a workload with many overwrites |
| Point read after compaction | 585 µs to 5.6 ms (9.6x slower) | 10 segments merged into one, 1 KB values |

The last row is the main weakness. A segment has no index, so a hit scans its segment, and compaction turns many small segments into one large one. Space comes back, reads get slower. Sorted segments with a sparse index are the next step.

v2 is faster than v1.0.3 on most reads, but slower with 8 KB values, because it reads and checksums every value in a segment it scans.

The harness and a YCSB-style workload driver are in [`bench/`](bench). The full tables and caveats for this run are in [`bench/results/2026-10-06/SUMMARY.md`](bench/results/2026-10-06/SUMMARY.md), and the method is in [`docs/BENCHMARKS.md`](docs/BENCHMARKS.md). The raw output isn't published; `bench/run-report.sh` regenerates it in about 20 minutes.

## Limitations

- No index inside a segment, so reads scan linearly (see above).
- Nothing compacts for you. Call `Compact()` yourself, using `Stats()` to decide when.
- Compaction blocks all reads and writes, and builds the merged data in memory.
- No TTL, eviction, range scans or iteration. Single node, one process per directory, no file lock.
- With `SyncAlways`, fsync dominates write cost. Use batches or `SyncNever`.

More detail, including known issues, is in [`docs/INTEGRATION.md`](docs/INTEGRATION.md).

## Layout

```
engine/            the public API (Open, Put, Get, Delete, Batch, Compact, Stats)
internal/storage/  record codec, WAL, segment files
internal/filter/   Bloom filter
internal/memtable/ in-memory buffer
test/integration/  tests through the public API
bench/             benchmark harness, workload driver, results
docs/              integration guide, benchmark method
```

Apache 2.0, see [LICENSE](LICENSE).
