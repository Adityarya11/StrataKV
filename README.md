# StrataKV

An embedded, LSM-tree-inspired key-value storage engine written in Go — Write-Ahead Log durability, immutable segment files, tombstone-based deletes, Bloom-filter-accelerated reads, and background compaction, exposed both as a Go library and a minimal HTTP server.

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

	val, found := db.Get([]byte("user:101"))
	fmt.Println(string(val), found) // aditya true

	db.Delete([]byte("user:101"))
}
```

To run StrataKV as a standalone HTTP server instead of embedding it:

```bash
go run cmd/stratakv/main.go
# Server starts on http://localhost:8080
```

See [API.md](API.md) for the full HTTP reference.

---

## Architecture

```
Client
  │
  ▼
HTTP Server Layer (cmd/stratakv, internal/server)
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

**Write path:** every `Put`/`Delete` is appended to the WAL and fsync'd before the MemTable is mutated, guaranteeing crash recovery. Once the MemTable crosses a 1 MiB threshold, it flushes to disk as a new immutable `.seg` file and a Bloom filter is built for it.

**Read path:** `Get` checks the MemTable first, then walks segment files newest-to-oldest. For each segment, a Bloom filter check runs first — if the filter says "definitely not present," the segment is skipped entirely, avoiding a disk scan.

**Compaction:** merges all segments into one, keeping only the latest version of each key and dropping tombstones whose deletes have now been physically applied. Triggered manually via `db.Compact()` or `POST /compact`.

---

## Public API and Encapsulation

StrataKV is usable two ways: as an embedded library, or as a standalone HTTP server.

- **`engine/`** — the public package. `Open`, `Put`, `Get`, `Delete`, `Compact`, `Close`. This is what you import.
- **`internal/`** — WAL, MemTable, segment, and Bloom filter internals. Go's compiler enforces that nothing outside this module can import these packages, so the storage internals can change without breaking consumers of `engine`.
- **`cmd/stratakv` + `internal/server`** — an optional HTTP adapter wrapping the engine in REST endpoints. Skip this entirely if you're embedding StrataKV directly (see the real integration example below).

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
	val, found := DB.Get([]byte(hashKey))
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

Full methodology and raw numbers are in [BENCHMARKS.md](BENCHMARKS.md). Headline results from a sustained append-heavy workload (50K writes, 200K overwrites, 80K tombstone deletes):

| Metric                    | Before      | After                                   |
| ------------------------- | ----------- | --------------------------------------- |
| Segment count             | 1,945       | 2 (post-compaction)                     |
| Disk usage                | 1,953.79 MB | 393.42 MB (-79.86%)                     |
| GET latency, existing key | 1.23s       | 175 µs (post-compaction + Bloom filter) |
| GET latency, missing key  | 2.07s       | 163 µs (post-compaction + Bloom filter) |
| WAL crash recovery        | —           | 446.8 ms for the full workload          |

---

## Known Limitations

These are intentional, documented tradeoffs of a learning-focused implementation, not oversights:

- **Stop-the-world compaction and flush** — reads and writes block while either runs, under a single `sync.RWMutex`.
- **Linear scan within a segment** — Bloom filters skip whole segments but don't avoid a sequential scan inside a candidate segment; no sparse index exists yet.
- **Bit-per-bool Bloom filter** — `bitset []bool` uses 8x more memory than a packed bitset would.
- **Fixed Bloom filter sizing** — every segment gets the same 10,000-bit / 3-hash filter regardless of how many keys it holds, so false-positive rates vary with segment size.
- **No checksums** — segment files have no corruption detection; a partial write from a crash mid-flush would currently fail or misread silently rather than being caught explicitly.
- **No replication, no transactions, single-node only.**

---

## Project Structure

```
StrataKV/
├── cmd/stratakv/main.go        — HTTP server entrypoint
├── engine/
│   ├── db.go                   — Open, Put, Get, Delete, Close, flush
│   └── compaction.go           — Compact
├── internal/
│   ├── filter/bloom.go         — Bloom filter
│   ├── memtable/memtable.go    — in-memory write buffer
│   ├── server/http.go          — REST handlers
│   └── storage/
│       ├── wal.go              — Write-Ahead Log
│       └── segment.go          — segment read/write/search
├── scripts/benchmark.go        — benchmark harness
├── API.md
├── BENCHMARKS.md
└── go.mod
```

---

## License

Apache 2.0 — see [LICENSE](LICENSE).
