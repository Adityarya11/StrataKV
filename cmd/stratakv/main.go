package main

import (
	"fmt"
	"log"

	"github.com/Adityarya11/StrataKV/internal/engine"
	"github.com/Adityarya11/StrataKV/internal/server"
)

func main() {
	fmt.Printf("StrataKV DB has started....\n")

	db, err := engine.Open("./data")
	if err != nil {
		log.Fatalf("Failed to open Database : %v", err)
	}

	defer db.Close()

	fmt.Println("storage Engine mounted, Crash recovery Complete..")

	ser := server.NewServer(db)

	port := ":8080"
	fmt.Printf("StrataKV Server started to the port http://localhost%s\n", port)

	if err := ser.Start(port); err != nil {
		log.Fatalf("Server crashed: %v", err)
	}
}
