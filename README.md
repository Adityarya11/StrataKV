# StrataKV

An embedded, LSM-tree-inspired key-value storage engine written in Go — Write-Ahead Log durability, checksummed immutable segment files, tombstone-based deletes, Bloom-filter-accelerated reads, and compaction. Zero external dependencies.

[![Go Reference](https://pkg.go.dev/badge/github.com/Adityarya11/StrataKV.svg)](https://pkg.go.dev/github.com/Adityarya11/StrataKV)

StrataKV exists to explore the core mechanics behind log-structured storage engines like LevelDB and RocksDB — durability, compaction, and the write/read amplification tradeoff — by building them from first principles rather than treating a database as a black box.

---

## Quickstart

```bash
go get github.com/Adityarya11/StrataKV@latest
```

```go
package main

import (
	"fmt"
	stratakv "github.com/Adityarya11/StrataKV/engine"
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
		panic(err) // an unreadable disk, not a missing key
	}
	fmt.Println(string(val), found) // aditya true

	db.Delete([]byte("user:101"))
}
```

---

## Architecture

```
Application (imports engine)
  │
  ▼
Storage Engine (engine)
  │
  ├── Write-Ahead Log (WAL)      — durability backbone, fsync on every write
  ├── MemTable                   — in-memory write buffer, map[string]Entry
  ├── Bloom Filters               — per-segment, prune negative lookups
  ├── Segment Files (.seg)       — immutable, sequential binary records
  └── Compaction Engine          — merges segments, purges tombstones
```

**Write path:** every `Put`/`Delete` is appended to the WAL and fsync'd before the MemTable is mutated, guaranteeing crash recovery. Once the MemTable crosses a 1 MiB threshold, it flushes to disk as a new immutable `.seg` file, written under a temporary name and renamed into place so a reader never sees a partial segment.

**Read path:** `Get` checks the MemTable first, then walks segment files newest-to-oldest. For each segment, a Bloom filter check runs first — if the filter says "definitely not present," the segment is skipped entirely, avoiding a disk scan.

**Integrity:** every record carries a CRC-32C checksum, and every segment ends with a fixed-width trailer holding its Bloom filter. The trailer is written last, so its absence proves the file was never finished. A torn WAL tail — the ordinary artifact of a crash mid-write — is truncated and reported at startup rather than preventing the database from opening.

**Compaction:** merges all segments into one, keeping only the latest version of each key and dropping tombstones whose deletes have now been physically applied. Triggered via `db.Compact()`.

---

## Public API and Encapsulation

StrataKV is an embedded library. You import it into your process; there is no server, no wire protocol, and no network hop.

- **`engine/`** — the public package. `Open`, `Put`, `Get`, `Delete`, `Compact`, `Close`. This is what you import.
- **`internal/`** — WAL, MemTable, segment, and Bloom filter internals. Go's compiler enforces that nothing outside this module can import these packages, so the storage internals can change without breaking consumers of `engine`.

---

## Real-World Usage: Compiler Execution Cache

StrataKV runs in production as the execution-cache layer for the [Blan Compiler Backend](https://github.com/Adityarya11/blan-backend), replacing an external Redis dependency. Incoming C++ source is hashed with SHA-256; the backend checks StrataKV for a cached result before re-executing the code.

```go
package cache

import (
	"log"
	stratakv "github.com/Adityarya11/StrataKV/engine"
)

var DB *stratakv.DB

func InitStrataKV(dataDir string) {
	var err error
	DB, err = stratakv.Open(dataDir)
	if err != nil {
		log.Fatalf("StrataKV failed to open: %v", err)
	}
}

func GetCachedOutput(hashKey string) (string, bool) {
	val, found, err := DB.Get([]byte(hashKey))
	if err != nil {
		// A read error is not a cache miss — surface it, don't swallow it.
		log.Printf("StrataKV read error for %s: %v", hashKey[:8], err)
		return "", false
	}
	return string(val), found
}

func SaveCacheOutput(hashKey, output string) {
	if err := DB.Put([]byte(hashKey), []byte(output)); err != nil {
		log.Printf("StrataKV write error for %s: %v", hashKey[:8], err)
	}
}
```

---

## Performance

Full methodology and raw numbers are in [docs/BENCHMARKS.md](docs/BENCHMARKS.md). Headline results from a sustained append-heavy workload (50K writes, 200K overwrites, 80K tombstone deletes):

| Metric                    | Before      | After                                   |
| ------------------------- | ----------- | --------------------------------------- |
| Segment count             | 1,945       | 2 (post-compaction)                     |
| Disk usage                | 1,953.79 MB | 393.42 MB (-79.86%)                     |
| GET latency, existing key | 1.23s       | 175 µs (post-compaction + Bloom filter) |
| GET latency, missing key  | 2.07s       | 163 µs (post-compaction + Bloom filter) |
| WAL crash recovery        | —           | 446.8 ms for the full workload          |

---

## Known Limitations

Known and prioritised, not discovered in review:

- **Compaction is never triggered automatically** — nothing calls `Compact()` on your behalf. Left alone, segments accumulate and read latency degrades. Drive it from `db.Stats()` until this is built in.
- **No TTL or eviction** — StrataKV stores keys until you delete them. If you use it as a cache, bounding it is your job for now.
- **Stop-the-world compaction and flush** — reads and writes block while either runs, under a single `sync.RWMutex`.
- **Compaction merges in memory** — the whole database passes through one map, so peak memory scales with total data, not with segment count.
- **Linear scan within a segment** — Bloom filters skip whole segments but don't avoid a sequential scan inside a candidate; no sparse index exists yet.
- **One `fsync` per write** — durable, and the dominant cost of every write. There is no group commit or relaxed sync mode yet.
- **No range scans or iteration** — the MemTable is a hash map, so keys have no order.
- **No transactions, no MVCC, no replication. Single node.**

---

## Project Structure

```
StrataKV/
├── engine/
│   ├── db.go                   — Open, Put, Get, Delete, Close, flush
│   └── compaction.go           — Compact
├── internal/
│   ├── filter/bloom.go         — Bloom filter
│   ├── memtable/memtable.go    — in-memory write buffer
│   └── storage/
│       ├── record.go           — shared checksummed record codec
│       ├── wal.go              — Write-Ahead Log
│       └── segment.go          — segment read/write/search
├── test/integration/           — black-box tests through the public API
├── scripts/benchmark.go        — benchmark harness
├── docs/BENCHMARKS.md
└── go.mod
```

---

## License

Apache 2.0 — see [LICENSE](LICENSE).
