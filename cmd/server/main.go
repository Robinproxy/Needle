package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"needle/internal/logging"
	"needle/internal/server"
)

var BuildCommit = "unknown"

func main() {
	privateDatabaseUmask()
	addr := flag.String("l", func() string {
		if env := os.Getenv("NEEDLE_LISTEN"); env != "" {
			return env
		}
		return "127.0.0.1:8008"
	}(), "listen address")
	dbPath := flag.String("db", "./data/needle.db", "database path")
	certFile := flag.String("cert", "", "TLS certificate file")
	keyFile := flag.String("key", "", "TLS key file")
	yes := flag.Bool("y", false, "skip confirmation for delete-agent / revoke-token")
	showVersion := flag.Bool("version", false, "show binary version and source commit")
	installerProtocol := flag.Bool("installer-protocol", false, "show supported installer protocol")
	flag.Parse()
	if *showVersion {
		fmt.Printf("needle-server %s commit=%s\n", server.Version, BuildCommit)
		return
	}
	if *installerProtocol {
		fmt.Println("needle-install-v2 persistent-report-v1 realtime-v1")
		return
	}

	if (*certFile == "") != (*keyFile == "") {
		logging.Fatal("server", "tls_config_invalid", fmt.Errorf("both -cert and -key are required for TLS"))
	}
	args := flag.Args()
	if len(args) > 0 {
		unlock, err := lockDatabaseAdministration(*dbPath)
		if err != nil {
			logging.Fatal("server", "command_failed", err)
		}
		defer unlock()
		handled, err := databaseOwnerCommand(*dbPath)
		if err != nil {
			logging.Fatal("server", "command_failed", err)
		}
		if handled {
			return
		}
		switch args[0] {
		case "repair-traffic":
			if err := runRepairTraffic(*dbPath, args[1:]); err != nil {
				logging.Fatal("server", "command_failed", err)
			}
			return
		case "list-agents":
			if err := runListAgents(*dbPath); err != nil {
				logging.Fatal("server", "command_failed", err)
			}
			return
		case "move-agent":
			if err := runMoveAgent(*dbPath, args[1:]); err != nil {
				logging.Fatal("server", "command_failed", err)
			}
			return
		case "delete-agent":
			if len(args) < 2 {
				logging.Fatal("server", "arguments_invalid", fmt.Errorf("usage: needle-server -db <path> [-y] delete-agent <id|hostname>"))
			}
			if err := runDeleteAgent(*dbPath, args[1], *yes); err != nil {
				logging.Fatal("server", "command_failed", err)
			}
			return
		case "allow-token":
			if len(args) < 2 {
				logging.Fatal("server", "arguments_invalid", fmt.Errorf("usage: needle-server -db <path> allow-token <token>"))
			}
			if err := runAllowToken(*dbPath, args[1]); err != nil {
				logging.Fatal("server", "command_failed", err)
			}
			return
		case "list-tokens":
			if err := runListTokens(*dbPath); err != nil {
				logging.Fatal("server", "command_failed", err)
			}
			return
		case "revoke-token":
			if len(args) < 2 {
				logging.Fatal("server", "arguments_invalid", fmt.Errorf("usage: needle-server -db <path> [-y] revoke-token <token>"))
			}
			if err := runRevokeToken(*dbPath, args[1], *yes); err != nil {
				logging.Fatal("server", "command_failed", err)
			}
			return
		default:
			logging.Fatal("server", "command_invalid", fmt.Errorf("unknown command %q (supported: list-agents, move-agent, delete-agent, allow-token, list-tokens, revoke-token, repair-traffic)", args[0]))
		}
	}

	if dir := filepath.Dir(*dbPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0700); err != nil {
			logging.Fatal("server", "data_directory_failed", err)
		}
	}

	host, _, _ := net.SplitHostPort(*addr)
	if host == "" || host == "0.0.0.0" || host == "::" {
		logging.Print("WARN", "server", "public_listener", "listen", *addr, "action", "use a reverse proxy with access controls as needed")
	}

	store, err := server.NewStore(*dbPath)
	if err != nil {
		logging.Fatal("server", "database_open_failed", err)
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
		logging.Print("INFO", "server", "started", "version", server.Version, "commit", BuildCommit, "listen", *addr, "tls", *certFile != "")
		if *certFile != "" && *keyFile != "" {
			if err := srv.ListenAndServeTLS(*certFile, *keyFile); err != nil && err != http.ErrServerClosed {
				logging.Fatal("server", "listen_failed", err)
			}
		} else {
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logging.Fatal("server", "listen_failed", err)
			}
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	logging.Print("INFO", "server", "stopping")
	handler.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logging.Print("ERROR", "server", "shutdown_failed", "error", err)
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

	fmt.Printf("%-6s %-6s %-24s %-8s %s\n", "POS", "ID", "HOSTNAME", "REGION", "LAST_SEEN")
	for i, a := range agents {
		lastSeen := "-"
		if a.LastSeen != nil {
			lastSeen = time.Unix(*a.LastSeen, 0).UTC().Format(time.RFC3339)
		} else if m, err := store.GetLatestMetric(a.ID); err == nil && m != nil {
			lastSeen = time.Unix(m.CreatedAt, 0).UTC().Format(time.RFC3339)
		}
		fmt.Printf("%-6d %-6d %-24s %-8s %s\n", i+1, a.ID, a.Hostname, a.Region, lastSeen)
	}
	return nil
}

func runMoveAgent(dbPath string, args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: needle-server -db <path> move-agent <id|hostname> <position>")
	}
	position, err := strconv.Atoi(args[1])
	if err != nil || position < 1 {
		return fmt.Errorf("position must be a positive integer")
	}
	store, err := openStore(dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer store.Close()
	if err := store.MoveAgent(args[0], position); err != nil {
		return err
	}
	fmt.Printf("moved agent %q to position %d; dashboard updates on its next refresh\n", args[0], position)
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
