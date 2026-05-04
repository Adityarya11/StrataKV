package main

import (
	"fmt"
	"log"

	"github.com/Adityarya11/StrataKV/internal/engine"
)

func main() {
	fmt.Println(" Starting StrataKV...")

	db, err := engine.Open("./data")
	if err != nil {
		log.Fatalf("Failed to open DB: %v", err)
	}
	defer db.Close()

	db.Put([]byte("engineer"), []byte("Aditya"))
	db.Put([]byte("role"), []byte("Backend Systems"))

	fmt.Println("✅ Data written to Memtable AND appended to WAL.")

	val, found := db.Get([]byte("role"))
	if found {
		fmt.Printf("🔍 Read Success -> Key: 'role', Value: '%s'\n", string(val))
	}
}
