package engine_test

import (
	"fmt"
	"log"
	"os"

	"github.com/Adityarya11/StrataKV/v2/engine"
)

func Example() {
	dir, _ := os.MkdirTemp("", "stratakv")
	defer os.RemoveAll(dir)

	db, err := engine.Open(dir)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if err := db.Put([]byte("user:101"), []byte("aditya")); err != nil {
		log.Fatal(err)
	}

	// Three results, and the distinction matters: found reports whether the key
	// exists, while err reports whether the read could be completed at all. A
	// checksum failure is not a cache miss.
	value, found, err := db.Get([]byte("user:101"))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(value), found)

	if err := db.Delete([]byte("user:101")); err != nil {
		log.Fatal(err)
	}

	_, found, _ = db.Get([]byte("user:101"))
	fmt.Println(found)

	// Output:
	// aditya true
	// false
}

// Batches group writes so they share a single fsync and apply atomically.
func ExampleDB_NewBatch() {
	dir, _ := os.MkdirTemp("", "stratakv")
	defer os.RemoveAll(dir)

	db, err := engine.Open(dir)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	batch := db.NewBatch()
	for i := range 1000 {
		if err := batch.Put(fmt.Appendf(nil, "key:%d", i), []byte("value")); err != nil {
			log.Fatal(err)
		}
	}

	// One fsync for all thousand writes, instead of a thousand. A crash before
	// this returns leaves none of them applied.
	if err := batch.Commit(); err != nil {
		log.Fatal(err)
	}

	_, found, _ := db.Get([]byte("key:999"))
	fmt.Println(found)

	// Output:
	// true
}

// Options configure durability, memory, and filter accuracy.
func ExampleOpenWith() {
	dir, _ := os.MkdirTemp("", "stratakv")
	defer os.RemoveAll(dir)

	opts := engine.DefaultOptions()
	opts.MemTableSize = 4 << 20 // flush every 4 MiB instead of 1

	// Safe here only because this data can be regenerated. Writes since the
	// last flush are lost if the machine loses power; a clean Close loses
	// nothing.
	opts.Sync = engine.SyncNever

	db, err := engine.OpenWith(dir, opts)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if err := db.Put([]byte("cache:sha256"), []byte("compiled output")); err != nil {
		log.Fatal(err)
	}

	value, _, _ := db.Get([]byte("cache:sha256"))
	fmt.Println(string(value))

	// Output:
	// compiled output
}

// Compaction merges every segment into one, dropping superseded versions and
// physically applying deletes. Nothing triggers it for you: drive it from
// Stats, or on a schedule that suits your workload.
func ExampleDB_Compact() {
	dir, _ := os.MkdirTemp("", "stratakv")
	defer os.RemoveAll(dir)

	opts := engine.DefaultOptions()
	opts.MemTableSize = 4096 // tiny, so this example produces several segments

	db, err := engine.OpenWith(dir, opts)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	value := make([]byte, 256)
	for i := range 500 {
		if err := db.Put(fmt.Appendf(nil, "key:%03d", i), value); err != nil {
			log.Fatal(err)
		}
	}

	// Overwriting leaves the older versions stranded in earlier segments.
	// Reclaiming them is what compaction is for.
	for i := range 200 {
		if err := db.Put(fmt.Appendf(nil, "key:%03d", i), value); err != nil {
			log.Fatal(err)
		}
	}

	result, err := db.Compact()
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(result.RecordsWritten < result.RecordsRead)
	fmt.Println(db.Stats().Segments)

	// Output:
	// true
	// 1
}
