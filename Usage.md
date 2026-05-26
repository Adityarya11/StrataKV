# Real-World Integration: Compiler Cache Engine

StrataKV was designed to be imported as a reusable storage engine library. It is currently deployed as the high-speed caching layer for the [**Blan Compiler Backend**](https://github.com/Adityarya11/blan-backend), acting as an embedded, low-latency alternative to Redis.

By hashing incoming C++ source code via SHA-256, the backend queries StrataKV to retrieve previous compilation results instantly. If a cache miss occurs, the code is executed asynchronously, and the result is written back to StrataKV's Write-Ahead Log and MemTable.

### 1. Installation

To use StrataKV as an embedded engine in your own Go projects:

```bash
go get github.com/Adityarya11/StrataKV@latest
```

### 2. Example: Integrating as a Cache Wrapper

Below is a production example of how StrataKV is wrapped to provide a self-contained caching service, complete with a health-check system and graceful teardown:

```go
package cache

import (
	"log"

	stratakv "github.com/Adityarya11/StrataKV/engine"
)

var DB *stratakv.DB
const strataHealthKey = "__stratakv_health__"

// InitStrataKV mounts the storage engine.
func InitStrataKV(dataDir string) {
	var err error
	DB, err = stratakv.Open(dataDir)
	if err != nil {
		log.Fatalf("StrataKV failed to open: %v", err)
	}
	log.Println("⚡ StrataKV LSM-Tree Engine Initialized")
}

// CheckStrataKV validates the engine's read/write path.
func CheckStrataKV() (bool, string) {
	if DB == nil {
		return false, "not initialized"
	}

	if err := DB.Put([]byte(strataHealthKey), []byte("ok")); err != nil {
		return false, "put failed: " + err.Error()
	}

	val, found := DB.Get([]byte(strataHealthKey))
	if !found || string(val) != "ok" {
		return false, "health check retrieval failed"
	}

	return true, "ok"
}

// GetCachedOutput checks the LSM tree for previous executions.
func GetCachedOutput(hashKey string) (string, bool) {
	if DB == nil {
		return "", false
	}

	val, found := DB.Get([]byte(hashKey))
	if !found {
		return "", false
	}

	return string(val), true
}

// SaveCacheOutput writes the execution result to the WAL and MemTable.
func SaveCacheOutput(hashkey, output string) {
	if DB == nil {
		return
	}

	err := DB.Put([]byte(hashkey), []byte(output))
	if err != nil {
		log.Printf("StrataKV Write Error for %s: %v\n", hashkey[:8], err)
		return
	}

	log.Printf("StrataKV: Cached execution result for %s\n", hashkey[:8])
}

// CloseStrata safely shuts down the engine and flushes the WAL.
func CloseStrata() {
	if DB != nil {
		if err := DB.Close(); err != nil {
			log.Printf("Error closing StrataKV: %v", err)
		} else {
			log.Println("StrataKV cleanly shut down.")
		}
	}
}
```

If you find this repo helpful, please hit a Star. Thanks for using StrataKV =).
