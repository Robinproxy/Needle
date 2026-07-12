package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

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
	certFile := flag.String("cert", "", "TLS certificate file")
	keyFile := flag.String("key", "", "TLS key file")
	yes := flag.Bool("y", false, "skip confirmation for delete-agent / revoke-token")
	flag.Parse()

	args := flag.Args()
	if len(args) > 0 {
		switch args[0] {
		case "list-agents":
			if err := runListAgents(*dbPath); err != nil {
				log.Fatal(err)
			}
			return
		case "delete-agent":
			if len(args) < 2 {
				log.Fatal("usage: needle-server -db <path> [-y] delete-agent <id|hostname>")
			}
			if err := runDeleteAgent(*dbPath, args[1], *yes); err != nil {
				log.Fatal(err)
			}
			return
		case "allow-token":
			if len(args) < 2 {
				log.Fatal("usage: needle-server -db <path> allow-token <token>")
			}
			if err := runAllowToken(*dbPath, args[1]); err != nil {
				log.Fatal(err)
			}
			return
		case "list-tokens":
			if err := runListTokens(*dbPath); err != nil {
				log.Fatal(err)
			}
			return
		case "revoke-token":
			if len(args) < 2 {
				log.Fatal("usage: needle-server -db <path> [-y] revoke-token <token>")
			}
			if err := runRevokeToken(*dbPath, args[1], *yes); err != nil {
				log.Fatal(err)
			}
			return
		default:
			log.Fatalf("unknown command %q (supported: list-agents, delete-agent, allow-token, list-tokens, revoke-token)", args[0])
		}
	}

	if dir := filepath.Dir(*dbPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0700); err != nil {
			log.Fatalf("create data dir: %v", err)
		}
	}

	store, err := server.NewStore(*dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer store.Close()

	mux := http.NewServeMux()
	handler := server.NewHandler(store)
	handler.Register(mux)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           server.SecurityHeaders(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		if *certFile != "" && *keyFile != "" {
			log.Printf("Needle Server listening on %s (TLS)", *addr)
			if err := srv.ListenAndServeTLS(*certFile, *keyFile); err != nil && err != http.ErrServerClosed {
				log.Fatalf("server: %v", err)
			}
		} else {
			log.Printf("Needle Server listening on %s", *addr)
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("server: %v", err)
			}
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Println("shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}

func openStore(dbPath string) (*server.Store, error) {
	return server.NewStoreCLI(dbPath)
}

func runListAgents(dbPath string) error {
	store, err := openStore(dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer store.Close()

	agents, err := store.GetAgents()
	if err != nil {
		return err
	}
	if len(agents) == 0 {
		fmt.Println("no agents")
		return nil
	}

	fmt.Printf("%-6s %-24s %-8s %s\n", "ID", "HOSTNAME", "REGION", "LAST_SEEN")
	for _, a := range agents {
		lastSeen := "-"
		if m, err := store.GetLatestMetric(a.ID); err == nil && m != nil {
			lastSeen = time.Unix(m.CreatedAt, 0).UTC().Format(time.RFC3339)
		}
		fmt.Printf("%-6d %-24s %-8s %s\n", a.ID, a.Hostname, a.Region, lastSeen)
	}
	return nil
}

func runDeleteAgent(dbPath, target string, yes bool) error {
	store, err := openStore(dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer store.Close()

	agents, err := store.GetAgents()
	if err != nil {
		return err
	}

	var match *server.AgentRow
	if id, err := strconv.ParseInt(target, 10, 64); err == nil {
		for i := range agents {
			if agents[i].ID == id {
				match = &agents[i]
				break
			}
		}
	}
	if match == nil {
		for i := range agents {
			if agents[i].Hostname == target {
				match = &agents[i]
				break
			}
		}
	}
	if match == nil {
		return fmt.Errorf("agent not found: %s", target)
	}

	if !yes {
		fmt.Printf("Delete agent %q (id=%d) and all its metrics/tcpping data? [y/N] ", match.Hostname, match.ID)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		line = strings.TrimSpace(strings.ToLower(line))
		if line != "y" && line != "yes" {
			fmt.Println("aborted")
			return nil
		}
	}

	if err := store.DeleteAgent(match.ID); err != nil {
		return err
	}
	fmt.Printf("deleted agent %q (id=%d)\n", match.Hostname, match.ID)
	return nil
}

func runAllowToken(dbPath, token string) error {
	store, err := openStore(dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer store.Close()

	if err := store.AllowToken(token); err != nil {
		return err
	}
	fmt.Println("token allowed (hostname will bind on first report)")
	return nil
}

func maskToken(t string) string {
	if len(t) <= 12 {
		return t[:min(4, len(t))] + "..."
	}
	return t[:8] + "..." + t[len(t)-4:]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func runListTokens(dbPath string) error {
	store, err := openStore(dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer store.Close()

	tokens, err := store.ListTokens()
	if err != nil {
		return err
	}
	if len(tokens) == 0 {
		fmt.Println("no tokens")
		return nil
	}

	fmt.Printf("%-6s %-20s %-24s %s\n", "ID", "TOKEN", "HOSTNAME", "BOUND_AT")
	for _, t := range tokens {
		host := t.Hostname
		if host == "" {
			host = "-"
		}
		bound := "-"
		if t.BoundAt != nil {
			bound = time.Unix(*t.BoundAt, 0).UTC().Format(time.RFC3339)
		}
		fmt.Printf("%-6d %-20s %-24s %s\n", t.ID, maskToken(t.Token), host, bound)
	}
	return nil
}

func runRevokeToken(dbPath, token string, yes bool) error {
	store, err := openStore(dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer store.Close()

	row, err := store.LookupToken(token)
	if err != nil {
		return err
	}

	if !yes {
		host := row.Hostname
		if host == "" {
			host = "(unbound)"
		}
		fmt.Printf("Revoke token %s bound to %s? [y/N] ", maskToken(token), host)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		line = strings.TrimSpace(strings.ToLower(line))
		if line != "y" && line != "yes" {
			fmt.Println("aborted")
			return nil
		}
	}

	if err := store.RevokeToken(token); err != nil {
		return err
	}
	fmt.Println("token revoked")
	return nil
}
