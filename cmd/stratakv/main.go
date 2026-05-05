package main

import (
	"fmt"
	"log"

	"github.com/Adityarya11/StrataKV/internal/engine"
)

func main() {
	fmt.Println("Starting StrataKV Phase 3a (Load Testing & Flushing)...")

	db, err := engine.Open("./data")
	if err != nil {
		log.Fatalf("Failed to open DB: %v", err)
	}
	defer db.Close()

	fmt.Println("Pumping 50,000 records to trigger a flush...")

	// Write enough data to exceed the 1MB threshold
	for i := 0; i < 50000; i++ {
		key := []byte(fmt.Sprintf("user_key_%d", i))
		val := []byte(fmt.Sprintf("some_long_user_payload_data_to_take_up_space_%d", i))

		if err := db.Put(key, val); err != nil {
			log.Fatalf("Put failed at iteration %d: %v", i, err)
		}
	}

	fmt.Println("Load test complete. Check your ./data directory!")
}
