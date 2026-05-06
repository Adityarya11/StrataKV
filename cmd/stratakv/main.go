package main

import (
	"fmt"
	"log"

	"github.com/Adityarya11/StrataKV/internal/engine"
)

func main() {
	fmt.Println("🚀 Starting StrataKV Phase 3b (Background Compaction)...")

	db, err := engine.Open("./data")
	if err != nil {
		log.Fatalf("Failed to open DB: %v", err)
	}
	defer db.Close()

	fmt.Println("🔨 Writing lots of duplicate data to simulate updates...")

	// We are writing to the SAME 10 keys 5000 times each.
	// Without compaction, disk space blows up. With compaction, it shrinks instantly.
	for i := 0; i < 50000; i++ {
		key := []byte(fmt.Sprintf("user_key_%d", i%10)) // Only 10 unique keys!
		val := []byte(fmt.Sprintf("some_long_user_payload_data_to_take_up_space_iteration_%d", i))

		if err := db.Put(key, val); err != nil {
			log.Fatalf("Put failed at iteration %d: %v", i, err)
		}
	}

	fmt.Println("💾 Load complete. Now triggering forced compaction...")

	// Force the compaction process
	if err := db.Compact(); err != nil {
		log.Fatalf("Compaction failed: %v", err)
	}

	fmt.Println("✅ Run complete. Look at your ./data directory!")
}
