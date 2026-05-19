package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Adityarya11/StrataKV/engine"
	"github.com/Adityarya11/StrataKV/internal/server"
)

func main() {
	fmt.Printf("StrataKV DB has started....\n")

	db, err := engine.Open("./data")
	if err != nil {
		log.Fatalf("Failed to open Database : %v", err)
	}

	// This defer ensures the DB is closed safely when main exits
	defer func() {
		if err := db.Close(); err != nil {
			log.Printf("Error closing DB: %v\n", err)
		} else {
			fmt.Println("Database connection closed successfully.")
		}
	}()

	fmt.Println("Storage Engine mounted, Crash recovery Complete..")

	ser := server.NewServer(db)

	port := ":8080"
	fmt.Printf("StrataKV Server started on port http://localhost%s\n", port)

	// Run the server in a goroutine so it doesn't block
	go func() {
		if err := ser.Start(port); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server crashed: %v", err)
		}
	}()

	// Wait for interrupt signal to gracefully shut down the server
	quit := make(chan os.Signal, 1)
	// kill (no param) default send syscall.SIGTERM
	// kill -2 is syscall.SIGINT (Ctrl+C)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit // Block until a signal is received

	fmt.Println("\nGracefully shutting down server...")

	// 5-second timeout for the shutdown process
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := ser.Stop(ctx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}

	fmt.Println("HTTP server stopped.")
}
