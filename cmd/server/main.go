package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"needle/internal/server"
)

func main() {
	addr := flag.String("l", func() string {
		if env := os.Getenv("NEEDLE_LISTEN"); env != "" {
			return env
		}
		return ":8008"
	}(), "listen address")
	dbPath := flag.String("db", "./data/needle.db", "database path")
	token := flag.String("token", "", "server token for agent authentication")
	flag.Parse()

	if *token == "" {
		*token = os.Getenv("NEEDLE_TOKEN")
	}
	if *token == "" {
		log.Fatal("token is required, set via -token flag or NEEDLE_TOKEN env")
	}

	if err := os.MkdirAll("./data", 0755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}

	store, err := server.NewStore(*dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer store.Close()

	mux := http.NewServeMux()
	handler := server.NewHandler(store, *token)
	handler.Register(mux)

	srv := &http.Server{
		Addr:    *addr,
		Handler: mux,
	}

	go func() {
		log.Printf("Needle Server listening on %s", *addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Println("shutting down...")
	srv.Close()
}
