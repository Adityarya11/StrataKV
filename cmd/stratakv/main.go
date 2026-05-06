package main

import (
	"fmt"
	"log"
	"time"

	"github.com/Adityarya11/StrataKV/internal/engine"
)

func main() {
	start := time.Now()
	fmt.Println("🚀 Starting StrataKV Phase 3c (The Complete Read Path)...")

	db, err := engine.Open("./data")
	if err != nil {
		log.Fatalf("Failed to open DB: %v", err)
	}
	defer db.Close()

	// 1. Write a specific key we want to track
	targetKey := []byte("secret_agent")
	db.Put(targetKey, []byte("007"))
	fmt.Println("💾 Wrote 'secret_agent' to MemTable.")

	// 2. Flood the database to force a flush to disk
	fmt.Println("🌊 Flooding database to trigger a flush...")
	for i := 0; i < 30000; i++ {
		key := []byte(fmt.Sprintf("junk_key_%d", i))
		db.Put(key, []byte("junk_data_to_fill_memory"))
	}

	// 3. At this point, the MemTable was cleared, and 'secret_agent' is trapped in a .seg file.
	// Let's see if Get() can find it!
	fmt.Println("🔍 Searching for 'secret_agent'...")
	val, found := db.Get(targetKey)

	if found {
		fmt.Printf("✅ SUCCESS! Found key on disk -> Value: '%s'\n", string(val))
	} else {
		fmt.Println("❌ FAILURE! Key was lost after the flush.")
	}

	fmt.Printf("⏱️ Total time taken: %v\n", time.Since(start))
}
