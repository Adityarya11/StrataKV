# StrataKV — Integration Reference

**Version documented: v2.0.0** · Module: `github.com/Adityarya11/StrataKV/v2`

A self-contained reference for integrating StrataKV into another Go project.
Written to be handed to a coding agent working in a _consumer_ repository — it
covers the entire public API, the semantics that are easy to get wrong, the
operational responsibilities the library does **not** handle for you, and an
explicit list of what does not exist so nothing gets invented.

---

## 1. What StrataKV is

An **embedded, LSM-tree-inspired key-value storage engine** in Go. It runs
in-process, like SQLite rather than Redis — there is no server, no wire
protocol, no network hop, and no daemon to manage. A database is a directory of
files.

```
Your Go process
  └── engine.DB
        ├── Write-Ahead Log (0001.wal)   durability; fsync before memory
        ├── MemTable (in RAM)            write buffer, map[string]Entry
        └── Segments (*.seg)             immutable, checksummed, Bloom-indexed
```

**Zero external dependencies.** `go.mod` has no `require` block at all.

### Use it when

- You need persistent key→value storage inside one process.
- Keys are opaque bytes; you look them up by exact key.
- Writes are frequent, reads are by exact key, and the dataset fits on one disk.
- You want to avoid operating Redis/Postgres for something that is really a
  local cache or derived store.

### Do **not** use it when

- You need range scans, prefix scans, or ordered iteration — **not supported**.
- You need transactions across multiple keys beyond a single batch.
- You need multiple processes accessing the same directory — **single process
  only**; there is no file locking, and two processes will corrupt each other.
- You need replication, failover, or multi-node anything.
- You need TTL/expiry — **not supported**, you must delete keys yourself.

---

## 2. Install

```bash
go get github.com/Adityarya11/StrataKV/v2@v2.0.0
```

```go
import stratakv "github.com/Adityarya11/StrataKV/v2/engine"
```

**The `/v2` is mandatory.** The module path contains the major version, per Go
module rules. `github.com/Adityarya11/StrataKV/engine` (no `/v2`) resolves to
the old v1.0.4 API, where `Get` returns two values instead of three. If you see
`assignment mismatch: 3 variables but db.Get returns 2 values`, the import path
is missing `/v2`.

Only `engine` is importable. Everything under `internal/` is blocked by the Go
compiler.

**Go version:** `go.mod` currently declares `go 1.26.4`, so consumers need Go
1.26.4 or newer. The library's actual language requirement is lower (~1.24), so
this floor is stricter than necessary; see §12.

---

## 3. Complete API

Every exported symbol. Nothing else exists.

### Opening and closing

```go
func Open(dataDir string) (*DB, error)
func OpenWith(dataDir string, opts Options) (*DB, error)
func (db *DB) Close() error
```

`Open` uses `DefaultOptions()`. Both create `dataDir` if it does not exist, replay
the write-ahead log, and validate every segment's integrity. `Open` **fails** if
any segment is corrupt or truncated — it will not mount a database it cannot
vouch for.

`Close` fsyncs and closes the log. It does **not** flush the MemTable into a
segment; buffered writes stay in the log and are replayed by the next `Open`.
Call `Flush()` first if you want them consolidated.

### Reads and writes

```go
func (db *DB) Put(key, value []byte) error
func (db *DB) Get(key []byte) (value []byte, found bool, err error)
func (db *DB) Has(key []byte) (bool, error)
func (db *DB) Delete(key []byte) error
```

Keys and values are arbitrary bytes. Both are copied on write, so you may reuse
your buffers immediately. Empty keys are rejected (§8).

The returned `value` from `Get` must be treated as read-only; do not mutate it.

### Batches

```go
func (db *DB) NewBatch() *Batch
func (b *Batch) Put(key, value []byte) error
func (b *Batch) Delete(key []byte) error
func (b *Batch) Commit() error
func (b *Batch) Reset()
func (b *Batch) Len() int
func (b *Batch) Size() int
```

### Maintenance and introspection

```go
func (db *DB) Flush() error
func (db *DB) Compact() (CompactionResult, error)
func (db *DB) Stats() Stats
```

### Configuration

```go
type SyncMode int
const (
    SyncAlways SyncMode = iota  // fsync every write (default)
    SyncNever                    // fsync only on Flush, Compact, Close
)
func (m SyncMode) String() string

type Options struct {
    MemTableSize            int      // default 1<<20 (1 MiB)
    Sync                    SyncMode // default SyncAlways
    FilterFalsePositiveRate float64  // default 0.01, must be in (0,1)
}
func DefaultOptions() Options
```

`Options`' zero value is **invalid**. Always start from `DefaultOptions()` and
modify. Passing an invalid value returns `ErrInvalidOptions` rather than
silently substituting a default you did not choose.

### Result types

```go
type Stats struct {
    Segments       int // immutable segment files on disk
    SegmentEntries int // records across them, superseded versions included
    MemTableKeys   int // keys buffered in memory
    MemTableBytes  int // estimated bytes a flush would write
    LegacySegments int // pre-v2 format; rewritten by the next compaction
    Recovery       storage.RecoveryReport
}

type CompactionResult struct {
    SegmentsMerged   int
    RecordsRead      int   // includes superseded versions and tombstones
    RecordsWritten   int   // survivors; the gap is what was reclaimed
    TombstonesPurged int
    BytesBefore      int64
    BytesAfter        int64
}
```

`Stats.Recovery` has these fields, populated at `Open`:

```go
RecordsApplied     int    // records replayed from the log
DiscardedBytes     int64  // trailing bytes dropped as unreadable
TruncationCause    error  // why, or nil if the log was clean
MigratedFromLegacy bool   // a pre-v2 log was rewritten in place
```

> **API wart:** `Stats.Recovery`'s type lives in `internal/storage`, so you can
> _read_ its fields (`db.Stats().Recovery.DiscardedBytes` compiles fine) but you
> **cannot name the type** — `var r storage.RecoveryReport` will not compile from
> outside the module. Assign fields to local variables instead of passing the
> struct around.

### Errors

```go
var ErrEmptyKey       = errors.New("stratakv: key must not be empty")
var ErrInvalidOptions = errors.New("stratakv: invalid options")
```

These are the **only two** sentinels available. See §8 for what that means for
error handling.

---

## 4. The three-value `Get` — the thing to get right

```go
value, found, err := db.Get(key)
```

Three outcomes, and conflating the last two is the single most likely
integration bug:

| `err`   | `found` | Meaning                             | What to do                       |
| ------- | ------- | ----------------------------------- | -------------------------------- |
| `nil`   | `true`  | Key exists, `value` is it           | Use it                           |
| `nil`   | `false` | Key genuinely does not exist        | Treat as a miss                  |
| non-nil | `false` | **The read could not be completed** | Log it. Do _not_ treat as a miss |

A non-nil error means an I/O failure or a failed checksum — the disk is
unreadable or lying. If you write `if !found { recompute() }` and ignore `err`,
a corrupt segment silently becomes a cache miss, and then becomes wrong data
served as though it were right.

**Correct:**

```go
value, found, err := db.Get(key)
if err != nil {
    log.Printf("stratakv read error for %q: %v", key, err)
    return nil, false // fail closed, and make the failure visible
}
if !found {
    return nil, false
}
return value, true
```

**Wrong:**

```go
value, found, _ := db.Get(key) // discards corruption
```

---

## 5. Configuration by use case

### Durable store (the default)

```go
db, err := stratakv.Open("./data")
```

Every write is fsynced before `Put` returns. A write that returned `nil` has
survived a power cut. This is also the slowest mode: an fsync costs
**milliseconds** where the rest of a write costs microseconds, so it dominates
write throughput entirely.

### Regenerable cache — much faster

```go
opts := stratakv.DefaultOptions()
opts.Sync = stratakv.SyncNever
db, err := stratakv.OpenWith("./cache", opts)
```

`SyncNever` leaves writes in the OS page cache, fsyncing only on `Flush`,
`Compact`, and `Close`. Writes since the last sync are lost if the machine loses
power or the process is killed; **a clean `Close` loses nothing.**

Correct when the data can be rebuilt — a compile cache, a derived index, a
memoisation table. Wrong when it is the only copy of anything.

### Write-heavy, memory available

```go
opts := stratakv.DefaultOptions()
opts.MemTableSize = 16 << 20 // 16 MiB
```

Bigger buffer → fewer, larger segments → less read amplification and less
compaction. Costs memory, and each flush pauses longer (flush is
stop-the-world).

### Read-heavy with many misses

```go
opts := stratakv.DefaultOptions()
opts.FilterFalsePositiveRate = 0.001 // 1 in 1000 instead of 1 in 100
```

Bloom filters let a read skip a segment without touching disk. Lower rate = more
accurate = more memory (~9.6 bits/key at 0.01, ~14.4 at 0.001). Every open
segment holds its filter resident for the life of the `DB`.

---

## 6. Batches — atomic _and_ cheaper

```go
batch := db.NewBatch()
for _, item := range items {
    if err := batch.Put(item.Key, item.Value); err != nil {
        return err
    }
}
if err := batch.Commit(); err != nil {
    return err // nothing was applied; staged writes remain, you may retry
}
```

Two independent guarantees:

**Cost.** The whole group reaches the log as one buffer, one write syscall, and
**one fsync**. A thousand writes in a batch pay roughly one write's worth of
durability overhead rather than a thousand. This is the main reason to use one.

**Atomicity.** Every record but the last carries a _pending_ marker. Recovery
buffers pending records and applies them only when the committing record
arrives. A crash mid-batch therefore applies **none** of it — never a prefix.

Semantics:

- Within one batch, later writes to the same key win, exactly as separate
  ordered writes would.
- `Commit` resets the batch on success, so you can refill and reuse it.
- On failure, staged writes are **left in place** so you can retry.
- A batch buffers entirely in memory until `Commit` — keep sizes sane (a few
  thousand records, not millions).
- A `Batch` is **not** safe for concurrent use, though the `DB` is.

---

## 7. Operational responsibility: you must compact

**StrataKV never compacts on its own. Nothing calls `Compact()` for you.**

This is the most important operational fact in this document. Every MemTable
flush creates a new segment. Segments accumulate. Reads walk them newest-first,
so read latency grows with segment count, and disk usage grows with every
superseded version and every tombstone that was never physically applied.

Left alone, a busy database degrades without bound.

`Stats()` exists to drive this. A reasonable background worker:

```go
func startCompactor(db *stratakv.DB, every time.Duration, maxSegments int) (stop func()) {
    done := make(chan struct{})

    go func() {
        ticker := time.NewTicker(every)
        defer ticker.Stop()

        for {
            select {
            case <-done:
                return
            case <-ticker.C:
                if db.Stats().Segments < maxSegments {
                    continue
                }
                result, err := db.Compact()
                if err != nil {
                    log.Printf("stratakv compaction failed: %v", err)
                    continue
                }
                log.Printf("stratakv compacted %d segments, reclaimed %.1f MiB",
                    result.SegmentsMerged,
                    float64(result.BytesBefore-result.BytesAfter)/(1<<20))
            }
        }
    }()

    return func() { close(done) }
}
```

Call `stop()` before `db.Close()`.

**What `Compact()` does:** flushes the MemTable, merges _every_ segment into one,
keeps only the newest version of each key, and physically drops tombstones.

**Cost:** it is **stop-the-world** — it holds the write lock for its entire
duration, so every read and write blocks. It also builds the merge in memory, so
**peak memory scales with total database size**, not segment count. On a large
database this is a long pause and a large allocation. Schedule accordingly:
off-peak, or at a segment threshold rather than on a short timer.

**Safety:** if any source segment fails to read, compaction aborts and **no
source file is removed**. A failed compaction leaves the database consistent,
just un-compacted.

---

## 8. Error handling

### What you can branch on

```go
if errors.Is(err, stratakv.ErrEmptyKey)       { /* zero-length key */ }
if errors.Is(err, stratakv.ErrInvalidOptions) { /* bad Options */ }
```

**Empty keys are rejected everywhere** — `Put`, `Get`, `Delete`, `Batch.Put`,
`Batch.Delete`. This is not arbitrary: the record format uses a zero key length
as its "these bytes are not a record" marker, so allowing an empty key would
make a zero-filled region indistinguishable from real data. If your keys can be
empty, prefix them.

### What you cannot branch on

> **API gap:** the corruption sentinels (`ErrCorrupt`, `ErrTorn`,
> `ErrIncompleteSegment`, `ErrUnknownWALVersion`, `ErrUnknownSegmentVersion`)
> live in `internal/storage` and are **not importable**. You cannot
> programmatically distinguish "this segment is corrupt" from "the disk is
> full". You get a wrapped error with a descriptive message and nothing to match
> against.
>
> Treat any non-nil error from `Get` as "read failed, reason unknown", log the
> full message, and fail closed. If you need to detect corruption specifically,
> string-matching on `"corrupt record"` / `"segment is incomplete"` is the only
> option today, and it is fragile.

### Errors at `Open`

`Open` fails if a segment does not pass its integrity checks. This is
deliberate — serving part of a corrupt database is worse than refusing to start.

**Do not wrap `Open` in `log.Fatalf` without thinking.** That turns a single bad
segment into a process that will not boot. Decide what your application should
do: refuse to start, start with the cache disabled, or (if the data is
regenerable) move the directory aside and start fresh.

### Recovery reporting

A database can open _successfully_ and still have discarded data:

```go
if rec := db.Stats().Recovery; rec.DiscardedBytes > 0 {
    log.Printf("stratakv discarded %d bytes of unreadable log tail: %v",
        rec.DiscardedBytes, rec.TruncationCause)
}
```

A torn log tail is the normal artifact of a crash mid-write and is handled
automatically. Seeing it **once** after an unclean shutdown is expected. Seeing
it repeatedly is a hardware or configuration signal worth investigating.

---

## 9. Concurrency and process model

- **`*DB` is safe for concurrent use by multiple goroutines.** Reads share an
  `RWMutex`; writes, flushes, and compaction take it exclusively.
- **`*Batch` is not.** One batch per goroutine.
- **Reads block during flush and compaction.** Both are stop-the-world. A
  `Compact()` on a large database will stall every reader for its full duration.
- **One process per directory.** There is no file locking. Two processes opening
  the same `dataDir` will corrupt it. If you need that, put StrataKV behind
  your own single-process service.
- **`Open` is not cheap** — it replays the log and validates every segment.
  Open once at startup, keep the handle, close at shutdown. Do not open per
  request.

---

## 10. What does not exist

Do not write code against any of these. They are not implemented.

| Not supported                          | Note                                             |
| -------------------------------------- | ------------------------------------------------ |
| Range scans, prefix scans, iteration   | MemTable is a hash map; keys have no order       |
| TTL / expiry                           | Delete keys yourself                             |
| Eviction / size bounds                 | The database grows until you delete              |
| Automatic compaction                   | §7 — you must drive it                           |
| Transactions beyond one batch          | No multi-batch atomicity, no rollback            |
| MVCC / snapshots / point-in-time reads | Reads see the latest state only                  |
| Multi-process access                   | Single process, no locking                       |
| Replication, clustering, failover      | Single node                                      |
| Compression                            | Values are stored as given                       |
| Backup / restore API                   | Copy the directory while the process is stopped  |
| Encryption at rest                     | Values are plaintext on disk                     |
| `context.Context` support              | No cancellation; `Compact` cannot be interrupted |
| Metrics/tracing hooks                  | Only `Stats()`, polled                           |
| Group commit                           | Use `Batch` or `SyncNever` to amortise fsync     |
| Sparse index within a segment          | Reads scan a candidate segment linearly          |

---

## 11. On-disk layout (for debugging)

A database is a directory:

```
data/
├── 0001.wal                      write-ahead log
├── 00001788712633719543500.seg   segment (zero-padded sequence)
└── 00001788712633719543501.seg
```

Formats, all little-endian:

```
Record (shared by WAL and segments)
  [ crc32c 4B | flags 1B | keyLen 4B | valLen 4B | key | value ]
  crc32c (Castagnoli) covers every byte after itself
  flags: bit0 = tombstone, bit1 = pending (unfinished batch member)

WAL
  [ "STRATAWL" 8B | version u16 ] [ record ]...

Segment
  [ "STRATASG" 8B | version u16 ]
  [ record ]...
  [ encoded Bloom filter ]
  [ trailer 28B: bloomOffset u64 | bloomLen u32 | entryCount u32
               | crc32c u32 | "STRATAFT" 8B ]
```

Useful facts when something looks wrong:

- **A segment with no valid trailer was never finished being written.** The
  trailer is written last, so its absence proves incompleteness. `Open` rejects
  such a file.
- **Segments are written to `*.seg.tmp` then renamed.** A stray `.tmp` file means
  a write failed partway; it is safe to delete.
- **Segment filenames are sequence numbers, parsed as integers.** Higher is
  newer. Never sort them as strings.
- **Reads go newest-segment-first** and stop at the first hit, so an older
  segment holding the same key is a superseded version, not a conflict.
- **Deleting the WAL** loses only writes not yet flushed to a segment. As a last
  resort on an unopenable database, that may recover the rest.

---

## 12. Known warts and gotchas

Ranked by how likely they are to bite you.

1. **You must call `Compact()` yourself** (§7). The single biggest trap.
2. **Non-nil `err` from `Get` is not a cache miss** (§4).
3. **Corruption errors are not matchable** (§8) — internal sentinels.
4. **`Stats.Recovery`'s type is not nameable** (§3) — read fields only.
5. **`Close` does not flush the MemTable.** Data is safe (it's in the log) but
   the next `Open` pays replay cost. Call `Flush()` first if that matters.
6. **`go.mod` declares `go 1.26.4`**, which is stricter than the code needs
   (roughly 1.24). Anyone on 1.24/1.25 is excluded for no reason. One-line fix
   in the library if that matters to you.
7. **Compaction peak memory ≈ total database size.** Not segment count. Plan for
   it on large datasets.
8. **No empty keys** (§8).
9. **Values returned by `Get` must not be mutated.**
10. **Windows/NTFS is slow in `SyncAlways`.** fsync latency there can be
    ~10–20 ms per write, so ~50 writes/sec. Use `Batch` or `SyncNever` if you
    are write-heavy on Windows.
11. **Compaction makes point reads slower.** Segments have no index, so a hit
    scans its segment, and compaction merges many small segments into one
    large one. Measured: a hit went from 585 µs to 5.6 ms after merging 10
    segments (1 KB values). Compact when disk or the segment count is the
    problem, not to speed up reads.

### Known issues (open)

- **Keep `len(key)+len(value)` under 64 MiB.** The limit is only checked when
  a record is read back. A larger value is accepted and acknowledged by `Put`,
  then fails with a corrupt-record error on `Get`, and `Compact` aborts on that
  segment from then on. Not fixed yet.
- **A crash while `Compact` is deleting the old segments can bring back a key
  that was deleted.** Old files are removed newest-first, so the segment holding
  the tombstone can go before the one holding the old value. Removing oldest
  first would avoid it. Not fixed yet.
- **Windows power loss.** The segment rename is followed by a directory fsync on
  POSIX only. I have not tested rename durability on Windows under power loss.
  A process crash is not affected.

---

## 14. Migrating from v1

| v1.0.x                                   | v2.0.0                                      |
| ---------------------------------------- | ------------------------------------------- |
| `github.com/Adityarya11/StrataKV/engine` | `github.com/Adityarya11/StrataKV/v2/engine` |
| `val, found := db.Get(k)`                | `val, found, err := db.Get(k)`              |
| `err := db.Compact()`                    | `result, err := db.Compact()`               |
| `engine.Open(dir)`                       | unchanged, or `OpenWith(dir, opts)`         |
| Empty keys silently accepted             | rejected with `ErrEmptyKey`                 |
| `Open` tolerated corrupt segments        | `Open` fails on them                        |
| Engine printed to stdout                 | silent; use `Stats()`                       |

**Existing data directories are upgraded in place and transparently.** Pre-v2
write-ahead logs are replayed and rewritten in the new checksummed format via a
temp file and rename. Pre-v2 segments stay readable and are rewritten by the
first compaction; `Stats().LegacySegments` reports how many remain. No manual
migration step.

v1 consumers are unaffected — they keep resolving to v1.0.4.

---

## 15. Version history

| Version       | Change                                                                                                                                           |
| ------------- | ------------------------------------------------------------------------------------------------------------------------------------------------ |
| **v2.0.0**    | `Options`/`OpenWith`; atomic `Batch`; `Get` returns an error; `Compact` returns `CompactionResult`; `Stats`, `Flush`, `Has`; empty keys rejected |
| v1.0.4        | Bloom filter: packed bitset, probe-cycle fix. HTTP server layer removed — StrataKV became library-only                                           |
| v1.0.0–v1.0.3 | Initial WAL, MemTable, segments, compaction, Bloom filters, and an HTTP API (since removed)                                                      |

### What v2.0.0 hardened

Three defects in v1 shared one root cause: the engine could not distinguish
_"nothing is there"_ from _"something went wrong."_

1. **A torn WAL write made the database unopenable.** A partially written
   trailing record — the ordinary artifact of a crash mid-write — was returned as
   a fatal error. Recovery now truncates the unreadable tail, keeps every
   checksum-verified record before it, and reports what it dropped.
2. **Compaction silently destroyed data and reported success.** A short read was
   returned as a successful end-of-file, so a truncated segment merged as a
   shorter one and the originals were deleted. Records now carry CRC-32C
   checksums, segments carry a completion-proving trailer, and compaction aborts
   without removing anything if a source fails to read.
3. **Bloom filters were saturated at realistic key densities.** Every segment got
   a fixed 10,000-bit / 3-hash filter regardless of key count, measuring 31% false
   positives at 4,000 keys per segment and 86% at 10,000 — at which point the
   filter prunes nothing while still costing a probe. Filters are now sized from
   the actual key count and hold ~1% across three orders of magnitude.

Test suite: unit tests beside the code (`internal/filter`, `internal/storage`)
and black-box integration tests in `test/integration` that exercise crash
recovery, corruption handling, and batch atomicity through the public API only.

```bash
go test ./...                 # everything
go test ./test/... -v         # the end-to-end proofs
```
