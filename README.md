<!-- # StrataKV

> Embedded LSM-based Key-Value Store in Go with Background Compaction

### Overview

This project is an implementation of the embedded key-value pair database that leverages the old-school append only transactions, leading to grow infinitely. Instead this is a background "Compaction" system that runs on a separate thread, silently taking multiple messy data files, merging them, throwing out deleted or outdated keys, and writing clean, highly compressed files back to disk without interrupting the user's reads and writes.

### Architecture

- if crash happens Wal has the data and memtable rebuilds this. [line49]("/internal/engine/db.go")
- tradeoffs: "Currently uses stop-the-world flushing; future optimization involves active/immutable MemTable separation."
  < Place Holder >

### Storage Design

#### Write-Ahead Log (WAL)

- Sequential append-only log
- Ensures durability before memory update
- Used for crash recovery via log replay

#### MemTable

- In-memory key-to-offset mapping
- Acts as the primary read layer for recent writes

#### Segment Files (SSTables)

- Immutable on-disk files
- Store sorted key-value entries
- Optimized for sequential reads

### Concurrency Model

The system supports concurrent operations using Go synchronization primitives:

- Read-heavy workloads are optimized using `sync.RWMutex`
- Multiple readers can access data concurrently
- Writes are coordinated to ensure consistency and durability
- Background compaction operates independently without blocking reads

### Tradeoffs

**Write Optimization vs Read Amplification:** Faster writes through append-only design, mitigated by compaction

- **Memory Usage:** In-memory index improves read latency at the cost of additional RAM
- **Compaction Overhead:** Background processing introduces additional complexity
- **Simplicity vs Performance:** Prioritizes clarity of design over low-level optimizations

### Future Enhancement

- Low -level optimisations
- maybe AI vector search integration.

### Motivation

This project was developed to gain a deep understanding of storage engine internals by building a simplified but practical system from first principles. It focuses on fundamental design tradeoffs and implementation details that underpin modern databases. -->

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

# Core Components

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

# Compaction Engine

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

# Performance Observations

During durability testing on Windows:

- synchronous `fsync()` operations introduced significant latency
- NTFS synchronous write overhead was observable under heavy write loads
- repeated WAL flushes amplified disk synchronization costs

Potential future optimizations:

- batched WAL writes
- group commit strategies
- asynchronous durability pipelines

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

# Benchmarking

Benchmark metrics are intentionally left modular for future evaluation.

Planned measurements include:

- write throughput
- MemTable flush duration
- compaction duration
- recovery startup latency
- segment scan latency
- read amplification analysis

---

# Project Structure

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
```

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
