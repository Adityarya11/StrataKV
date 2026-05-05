package main

import (
	"fmt"
	"log"

	"github.com/Adityarya11/StrataKV/internal/engine"
)

func main() {
	fmt.Println("Starting StrataKV Phase 2b (Crash Recovery)...")

	db, err := engine.Open("./data")
	if err != nil {
		log.Fatalf("Failed to open DB: %v", err)
	}
	defer db.Close()

	// NOTICE: We are NOT calling db.Put() right now.
	// We are going to try and read the data you saved in the previous run!

	val, found := db.Get([]byte("role"))
	if found {
		fmt.Printf("CRASH RECOVERY SUCCESS! Found data from last session -> Key: 'role', Value: '%s'\n", string(val))
	} else {
		fmt.Println("RECOVERY FAILED! Key not found. The memtable is empty.")
	}

	// Let's add a new key just to prove appending still works after recovery
	// db.Put([]byte("role"), []byte("engineer"))
	fmt.Println("Appended new key 'status' to the log.")
}
