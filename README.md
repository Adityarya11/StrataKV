# StrataKV

An embedded LSM-inspired key-value storage engine written in Go, featuring Write-Ahead Logging (WAL), crash recovery, immutable segment files, tombstone-based deletion semantics, background compaction, and an HTTP interface for external access.

---

# Overview

StrataKV is a lightweight storage engine designed to explore the core architectural principles behind modern log-structured databases such as LevelDB and RocksDB.

The project focuses on understanding how durable high-throughput storage systems manage:

- append-only writes
- crash recovery
- memory-to-disk flushing
- immutable on-disk storage
- tombstone handling
- compaction
- read amplification tradeoffs

Rather than implementing a full relational database with SQL parsing and query planning, StrataKV intentionally focuses on the storage engine layer itself.

The system exposes a lightweight HTTP API using Go’s standard `net/http` package and operates as a standalone embedded database server.

---

# Motivation

This project was built to explore storage-engine internals from first principles rather than relying solely on external databases as black-box abstractions.

The implementation focuses on understanding:

- durability guarantees
- append-only storage models
- memory-to-disk data flow
- compaction tradeoffs
- crash recovery mechanics
- storage-engine concurrency

StrataKV is intentionally educational in scope while remaining architecturally aligned with modern log-structured storage systems.

---

# Problem Statement

Traditional append-only storage models optimize write throughput by sequentially appending updates to disk. However, this design introduces several long-term bottlenecks:

- unbounded log growth
- increasing disk fragmentation
- stale and duplicated key versions
- degraded read performance due to multiple file scans
- storage inefficiency caused by overwritten values

StrataKV addresses these challenges by implementing:

- in-memory indexing through a MemTable
- immutable on-disk segment files
- tombstone-based deletion semantics
- background compaction to reclaim storage and reduce read amplification

---

# Design Goals

The system was designed around the following principles:

- prioritize write simplicity and durability
- preserve crash consistency through WAL replay
- separate memory and disk responsibilities cleanly
- keep the architecture understandable and modular
- expose real storage-engine tradeoffs explicitly
- avoid framework-heavy abstractions

This project intentionally favors architectural clarity over production-scale optimization.

---

# High-Level Architecture

```text
Client
   │
   ▼
HTTP Server Layer
   │
   ▼
Storage Engine
   │
   ├── Write-Ahead Log (WAL)
   ├── MemTable
   ├── Segment Files (.seg)
   └── Compaction Engine
```

---

# Core Storage Engine Components

## Write-Ahead Log (WAL)

The WAL is the durability backbone of the database.

Every write operation is first appended sequentially to a `.wal` file before the MemTable is updated. This guarantees crash recovery even if the process terminates unexpectedly.

### WAL Record Format

### Put Operation

```text
[0] [Key Length] [Value Length] [Key Bytes] [Value Bytes]
```

### Delete Operation

```text
[1] [Key Length] [0] [Key Bytes]
```

Where:

- `0` indicates insertion/update
- `1` represents a tombstone (deletion marker)

The WAL is replayed during startup to reconstruct the MemTable state.

---

## MemTable

The MemTable is the active in-memory write layer.

Internally, it stores:

```go
map[string]Entry
```

Each entry tracks:

- value bytes
- deletion state (tombstone)

The MemTable serves as:

- the fastest read path
- the temporary write buffer before disk flushing

Once the configured threshold is reached, the MemTable is flushed to disk as an immutable segment file.

---

## Segment Files (SSTable-like Storage)

Flushed MemTables are serialized into immutable `.seg` files.

Each segment stores compact binary records containing:

- tombstone flag
- key length
- value length
- key bytes
- value bytes

Segments are immutable after creation.

This design:

- simplifies concurrency
- avoids random disk writes
- enables sequential disk access

---

## Tombstones

Deletes are implemented using tombstones rather than immediate physical deletion.

When a key is deleted:

- the key remains stored
- its entry is marked as deleted

This ensures that:

- older segment versions remain logically invalidated
- future compaction cycles can safely purge obsolete data

Tombstones are persisted across:

- WAL replay
- MemTable flushing
- segment merging

---

## Read Path

The read path follows a newest-first lookup strategy.

### Lookup Order

```text
1. MemTable
2. Newest Segment
3. Older Segments
```

The engine scans segment files from newest to oldest because newer segments contain the latest version of overwritten keys.

This prevents stale reads while preserving append-only semantics.

---

## Flush Mechanism

When the MemTable crosses the configured memory threshold:

```text
MemTable → Segment File
```

The flush process:

- serializes MemTable entries
- writes a new immutable segment
- clears the active MemTable
- rotates the WAL

Current implementation uses a stop-the-world flush strategy:

- writes pause temporarily during flushing
- prioritizes correctness and simplicity over concurrent throughput

Future optimizations may adopt:

- immutable MemTables
- asynchronous background flushing
- zero-pause flush pipelines

---

## Compaction Engine

Compaction addresses the primary weakness of append-only systems:

- read amplification
- stale data accumulation
- storage bloat

The compaction engine:

- scans multiple segment files
- merges entries in memory
- keeps only the latest key versions
- purges obsolete tombstones
- rewrites a compacted segment

After compaction:

- old segments are deleted
- disk usage decreases
- read latency improves

---

# Concurrency Model

StrataKV uses Go synchronization primitives to coordinate access:

- `sync.RWMutex` for database-level read/write coordination
- `sync.Mutex` for WAL serialization

Read-heavy workloads benefit from:

- concurrent readers
- isolated WAL writes

Current compaction implementation acquires a global lock during merging, meaning:

- reads and writes pause during compaction
- correctness is prioritized over concurrent availability

---

# HTTP API Layer

The database exposes a lightweight REST-style interface using Go’s built-in `net/http` package.

No external frameworks are used.

---

# API Endpoints

## Insert / Update

```http
PUT /put
```

### Request Body

```json
{
  "key": "user:101",
  "value": "aditya"
}
```

---

## Retrieve

```http
GET /get?key=user:101
```

### Response

```json
{
  "key": "user:101",
  "value": "aditya",
  "found": true
}
```

---

## Delete

```http
DELETE /delete?key=user:101
```

---

## Trigger Manual Compaction

```http
POST /compact
```

---

# Graceful Shutdown

The server implements graceful shutdown handling using:

- OS signal interception
- context-based shutdown timeout

This ensures:

- WAL handles close correctly
- pending operations terminate cleanly
- abrupt process termination is minimized

---

# Performance Evaluation

StrataKV was evaluated using a synthetic append-heavy workload designed to stress the core architectural characteristics of an LSM-inspired storage engine. ![performance](/imgs/image.png)

The benchmark focused on validating:

- append-only storage behavior
- stale data accumulation
- tombstone handling
- segment growth
- compaction effectiveness
- read amplification
- WAL-based crash recovery

The workload intentionally generated high overwrite density and fragmented segment layouts in order to observe how the storage engine behaves under sustained append-oriented pressure.

---

## Benchmark Environment

| Component        | Details                              |
| ---------------- | ------------------------------------ |
| Operating System | Windows 11                           |
| Filesystem       | NTFS                                 |
| Language         | Go                                   |
| Storage Model    | Append-Only WAL + Immutable Segments |
| Benchmark Type   | Local Synthetic Workload             |

---

## Workload Characteristics

The benchmark suite generated:

- 50,000 initial key insertions
- 200,000 repeated overwrites
- 80,000 tombstone-based deletes
- 8KB average payload size

The workload intentionally concentrated repeated writes on overlapping key ranges to simulate:

- stale historical entries
- append-only storage growth
- fragmented segment accumulation
- tombstone pressure

This produced a large number of immutable segment files and significant read amplification prior to compaction.

---

## Benchmark Results

| Metric                                  | Result     |
| --------------------------------------- | ---------- |
| Total Workload Execution Time           | 6m 10s     |
| Pre-Compaction Disk Usage               | 1954.08 MB |
| Post-Compaction Disk Usage              | 393.71 MB  |
| Storage Reclaimed                       | 79.85%     |
| Segment Reduction                       | 1945 → 2   |
| Average GET Latency (Before Compaction) | 1.35s      |
| Average GET Latency (After Compaction)  | 152ms      |
| WAL Recovery Startup Time               | 632ms      |
| Compaction Duration                     | 8.33s      |

---

## Architectural Observations

### Read Amplification Under Append-Only Growth

The append-heavy workload generated nearly 2,000 immutable segment files prior to compaction.

Because the current read path performs:

- newest-to-oldest segment traversal
- sequential binary scanning
- no sparse indexing
- no Bloom filter optimization

read latency degraded significantly as segment count increased.

Observed average GET latency before compaction:

```text id="jlwm88"
~1.35 seconds
```

This demonstrates a classic LSM-tree tradeoff:

> append-only writes improve write simplicity while increasing long-term read amplification.

---

### Impact of Compaction

Compaction merged:

```text id="jlwm89"
1945 segment files → 2 segment files
```

while reclaiming:

```text id="jlwm90"
~80% of disk usage
```

The reduction in stale historical entries and fragmented segments improved average GET latency from:

```text id="jlwm91"
1.35s → 152ms
```

This validates the role of compaction in:

- reducing read amplification
- reclaiming obsolete storage
- consolidating fragmented append-only state

---

### WAL Durability Tradeoff

Each write operation is synchronously persisted through the WAL before memory mutation.

The durability flow follows:

```text id="jlwm92"
append → fsync() → MemTable update
```

This guarantees replayable crash consistency but introduces substantial synchronous I/O overhead.

The workload generation phase exhibited high write latency, particularly under Windows NTFS, where repeated synchronous disk flushes significantly reduced throughput.

Potential contributing factors include:

- NTFS metadata journaling
- synchronous flush barriers
- filesystem synchronization overhead
- real-time file interception by Windows Defender

This reflects a fundamental durability-throughput tradeoff commonly encountered in log-structured storage systems.

---

### Recovery Performance

Despite the large append-heavy workload, WAL replay successfully reconstructed MemTable state in:

```text id="jlwm93"
~632ms
```

This validates:

- replay-based crash recovery
- append-only durability correctness
- startup reconstruction flow

---

## Benchmark Limitations

The benchmark intentionally prioritizes architectural observability rather than production-scale realism.

Current limitations include:

- synthetic workload generation
- uniform access distribution
- lack of Zipfian hot-key modeling
- no sparse segment indexes
- no Bloom filters
- stop-the-world compaction
- linear segment scans
- single-node execution only

The benchmark should therefore be interpreted as:

```text id="jlwm94"
storage-engine architectural validation
```

rather than competitive database performance analysis.

---

## Benchmarking Conclusion

The benchmark successfully demonstrated the fundamental tradeoffs of append-only LSM-inspired storage systems:

- append-only growth simplifies writes but increases read amplification
- immutable segment accumulation degrades read latency over time
- tombstones and stale historical entries inflate storage usage
- compaction reclaims obsolete state and reduces segment fragmentation
- synchronous WAL durability introduces measurable write overhead

The resulting behavior aligns closely with the core architectural challenges addressed by modern log-structured storage engines such as LevelDB and RocksDB.

---

# Storage Tradeoffs

## Write Optimization vs Read Amplification

Append-only systems optimize writes by avoiding random disk updates.

Tradeoff:

- writes become fast
- reads may scan multiple segments

Compaction mitigates this issue by reducing segment fragmentation.

---

## Memory Usage vs Read Latency

The MemTable accelerates reads significantly by keeping hot data in memory.

Tradeoff:

- faster lookups
- higher RAM consumption

---

## Durability vs Throughput

The WAL performs synchronous disk flushes using `fsync()` after writes.

Benefits:

- strong crash consistency
- guaranteed persistence

Tradeoff:

- reduced throughput due to synchronous I/O overhead

This overhead is particularly noticeable on Windows NTFS systems.

---

# Known Limitations

The current implementation intentionally prioritizes architectural clarity over production-scale optimization.

Current limitations include:

- linear segment scans during reads
- no sparse indexes
- no Bloom filters
- stop-the-world compaction
- full in-memory segment merges during compaction
- no checksums or corruption detection
- no replication or distributed coordination
- no transactional isolation guarantees
- synchronous WAL flush on every write

These tradeoffs are documented intentionally as part of the learning-oriented architecture.

---

# Future Enhancements

Potential future improvements include:

- Bloom filter integration
- sparse segment indexing
- batched WAL synchronization
- immutable MemTable flushing
- asynchronous background compaction
- compression support
- checksum-based corruption detection
- vector storage extensions
- MVCC-based concurrency control
- gRPC or raw TCP protocol layer

---

<!-- # Project Structure

```text
StrataKV/
├── cmd/
│   └── stratakv/
│       └── main.go

├── internal/
│   ├── engine/
│   │   ├── db.go
│   │   └── compaction.go
│   │
│   ├── memtable/
│   │   └── memtable.go
│   │
│   ├── server/
│   │   └── http.go
│   │
│   └── storage/
│       ├── wal.go
│       └── segment.go

├── data/
├── docs/
├── README.md
└── go.mod
```

---

# Running the Server

## Start the Database

```bash
go run cmd/stratakv/main.go
```

The server will start on:

```text
http://localhost:8080
```

---

# Example Usage

## Insert Data

```bash
curl -X PUT http://localhost:8080/put \
-H "Content-Type: application/json" \
-d "{\"key\":\"user:101\", \"value\":\"aditya\"}"
```

---

## Retrieve Data

```bash
curl -X GET "http://localhost:8080/get?key=user:101"
```

---

## Delete Data

```bash
curl -X DELETE "http://localhost:8080/delete?key=user:101"
```

---

## Trigger Compaction

```bash
curl -X POST http://localhost:8080/compact
``` -->
