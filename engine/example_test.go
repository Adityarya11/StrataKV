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

	// found says whether the key exists, err says whether the read worked at
	// all. A checksum failure is not a cache miss.
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

// A batch is one fsync and applies atomically.
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

	// one fsync covers all thousand writes, and a crash before this returns
	// applies none of them
	if err := batch.Commit(); err != nil {
		log.Fatal(err)
	}

	_, found, _ := db.Get([]byte("key:999"))
	fmt.Println(found)

	// Output:
	// true
}

// Options control durability, memory use and filter accuracy.
func ExampleOpenWith() {
	dir, _ := os.MkdirTemp("", "stratakv")
	defer os.RemoveAll(dir)

	opts := engine.DefaultOptions()
	opts.MemTableSize = 4 << 20 // flush every 4 MiB instead of 1

	// fine here only because the data can be regenerated: writes since the
	// last flush are lost on power loss, though a clean Close loses nothing
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

// Compact merges every segment into one, dropping overwritten versions and
// applying deletes. Nothing calls it for you, so drive it from Stats or on a
// schedule.
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

	// overwriting leaves the old versions behind in earlier segments, which
	// compaction removes
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
