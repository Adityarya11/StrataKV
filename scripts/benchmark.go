/*

AI GENERATED FILE FOR TESTING THE SYSTEM AND GETTING THE METRICS
THESE TESTS WERE APPLIED ON THE WINDOWS MACHINE WITH NTFS FILE SYSTEM, HENCE SLOW AND SYNC. WRITE HEAVY.

*/

// ------------------------------------------------------------------------------------------------------------------------

package main

import (
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/Adityarya11/StrataKV/engine"
)

const (
	dataDir = "benchmark_data"

	// Initial unique dataset
	initialWrites = 50000

	// Heavy overwrite pressure
	overwriteOps = 200000

	// Tombstone pressure
	deleteOps = 80000

	// Payload size (8KB)
	payloadSize = 8 * 1024

	// Read test count
	readTests = 1000
)

func main() {
	suiteStart := time.Now()

	fmt.Println("======================================")
	fmt.Println("StrataKV Benchmark Suite")
	fmt.Println("======================================")

	// Clean old benchmark data
	os.RemoveAll(dataDir)

	startTotal := time.Now()

	db, err := engine.Open(dataDir)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	fmt.Println("\n[1] Generating Initial Dataset...")

	for i := 0; i < initialWrites; i++ {
		key := fmt.Sprintf("user:%d", i)
		value := randomPayload(payloadSize)

		if err := db.Put([]byte(key), value); err != nil {
			log.Fatal(err)
		}

		if i%5000 == 0 {
			fmt.Printf("Inserted %d records...\n", i)
		}
	}

	fmt.Println("\n[2] Generating Heavy Overwrite Pressure...")

	// Repeatedly overwrite existing keys
	for i := 0; i < overwriteOps; i++ {
		// Force repeated updates on smaller key range
		keyID := rand.Intn(initialWrites / 4)

		key := fmt.Sprintf("user:%d", keyID)

		// Generate different payload each overwrite
		value := []byte(
			fmt.Sprintf(
				"version:%d|%s",
				i,
				randomPayload(payloadSize),
			),
		)

		if err := db.Put([]byte(key), value); err != nil {
			log.Fatal(err)
		}

		if i%10000 == 0 {
			fmt.Printf("Overwrite operations: %d\n", i)
		}
	}

	fmt.Println("\n[3] Generating Tombstone Pressure...")

	for i := 0; i < deleteOps; i++ {
		keyID := rand.Intn(initialWrites / 2)

		key := fmt.Sprintf("user:%d", keyID)

		if err := db.Delete([]byte(key)); err != nil {
			log.Fatal(err)
		}

		if i%5000 == 0 {
			fmt.Printf("Delete operations: %d\n", i)
		}
	}

	totalDuration := time.Since(startTotal)

	fmt.Println("\n======================================")
	fmt.Println("[4] Workload Generation Complete")
	fmt.Println("======================================")

	fmt.Printf("Total workload time: %v\n", totalDuration)

	preCompactionSize := dirSize(dataDir)

	filesBefore := countFiles(dataDir)

	fmt.Printf("\nPre-Compaction Disk Usage: %.2f MB\n",
		float64(preCompactionSize)/(1024*1024),
	)

	fmt.Printf("Segment/WAL File Count: %d\n", filesBefore)

	// --- 🚀 NEW BLOOM FILTER BENCHMARKS HERE ---
	fmt.Println("\n[Read Benchmarks Before Compaction]")
	avgReadBefore := benchmarkReads(db)
	fmt.Printf("Average GET Latency (Existing Keys): %v\n", avgReadBefore)

	avgMissingReadBefore := benchmarkMissingReads(db)
	fmt.Printf("Average GET Latency (Missing Keys):  %v  <-- Watch this drop with Bloom Filters!\n", avgMissingReadBefore)
	// -------------------------------------------

	fmt.Println("\n======================================")
	fmt.Println("[5] Running Compaction")
	fmt.Println("======================================")

	compactionStart := time.Now()

	compaction, err := db.Compact()
	if err != nil {
		log.Fatal(err)
	}

	compactionDuration := time.Since(compactionStart)

	fmt.Printf("Compaction completed in %v\n", compactionDuration)
	fmt.Printf("Merged %d segments: %d records in, %d out, %d tombstones purged\n",
		compaction.SegmentsMerged, compaction.RecordsRead,
		compaction.RecordsWritten, compaction.TombstonesPurged)

	postCompactionSize := dirSize(dataDir)

	filesAfter := countFiles(dataDir)

	fmt.Printf("\nPost-Compaction Disk Usage: %.2f MB\n",
		float64(postCompactionSize)/(1024*1024),
	)

	fmt.Printf("Segment/WAL File Count: %d\n", filesAfter)

	reclaimed := float64(preCompactionSize-postCompactionSize) /
		float64(preCompactionSize) * 100

	fmt.Printf("Storage Reclaimed: %.2f%%\n", reclaimed)

	// --- 🚀 NEW BLOOM FILTER BENCHMARKS POST-COMPACTION ---
	fmt.Println("\n[Read Benchmarks After Compaction]")
	avgReadAfter := benchmarkReads(db)
	fmt.Printf("Average GET Latency (Existing Keys): %v\n", avgReadAfter)

	avgMissingReadAfter := benchmarkMissingReads(db)
	fmt.Printf("Average GET Latency (Missing Keys):  %v\n", avgMissingReadAfter)
	// ------------------------------------------------------

	fmt.Println("\n======================================")
	fmt.Println("[6] Testing Crash Recovery")
	fmt.Println("======================================")

	db.Close()

	recoveryStart := time.Now()

	recoveredDB, err := engine.Open(dataDir)
	if err != nil {
		log.Fatal(err)
	}
	defer recoveredDB.Close()

	recoveryDuration := time.Since(recoveryStart)

	fmt.Printf("Recovery completed in %v\n", recoveryDuration)

	fmt.Println("\n======================================")
	fmt.Println("Benchmark Complete")
	fmt.Printf("Total suite execution time: %v\n", time.Since(suiteStart))
	fmt.Println("======================================")
}

func randomPayload(size int) []byte {
	charset := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

	payload := make([]byte, size)

	for i := range payload {
		payload[i] = charset[rand.Intn(len(charset))]
	}

	return payload
}

// benchmarkReads tests keys that we KNOW exist in the database.
func benchmarkReads(db *engine.DB) time.Duration {
	start := time.Now()

	for i := 0; i < readTests; i++ {
		keyID := rand.Intn(initialWrites)

		key := "user:" + strconv.Itoa(keyID)

		_, _, _ = db.Get([]byte(key))
	}

	return time.Since(start) / readTests
}

// 🚀 benchmarkMissingReads tests keys that we KNOW DO NOT exist.
// Without Bloom filters, this causes worst-case linear scanning across all segments.
func benchmarkMissingReads(db *engine.DB) time.Duration {
	start := time.Now()

	for i := 0; i < readTests; i++ {
		// Keys formulated like "missing:99999" are guaranteed not to be in the initial dataset
		key := "missing_user:" + strconv.Itoa(i)

		_, _, _ = db.Get([]byte(key))
	}

	return time.Since(start) / readTests
}

func dirSize(path string) int64 {
	var size int64

	filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() {
			size += info.Size()
		}

		return nil
	})

	return size
}

func countFiles(path string) int {
	count := 0

	filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() {
			count++
		}

		return nil
	})

	return count
}
