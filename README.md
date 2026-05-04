# StrataKV

> Embedded LSM-based Key-Value Store in Go with Background Compaction

### Overview

This project is an implementation of the embedded key-value pair database that leverages the old-school append only transactions, leading to grow infinitely. Instead this is a background "Compaction" system that runs on a separate thread, silently taking multiple messy data files, merging them, throwing out deleted or outdated keys, and writing clean, highly compressed files back to disk without interrupting the user's reads and writes.

### Architecture

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

-**Write Optimization vs Read Amplification:** Faster writes through append-only design, mitigated by compaction

- **Memory Usage:** In-memory index improves read latency at the cost of additional RAM
- **Compaction Overhead:** Background processing introduces additional complexity
- **Simplicity vs Performance:** Prioritizes clarity of design over low-level optimizations

### Future Enhancement

- Low -level optimisations
- maybe AI vector search integration.

### Motivation

This project was developed to gain a deep understanding of storage engine internals by building a simplified but practical system from first principles. It focuses on fundamental design tradeoffs and implementation details that underpin modern databases.
