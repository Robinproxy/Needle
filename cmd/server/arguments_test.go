package main

import (
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandArgumentValidation(t *testing.T) {
	for _, args := range [][]string{
		nil, {"admin-init"}, {"admin-reset"}, {"list-agents"}, {"list-tokens"},
		{"allow-token", "unique-token"}, {"delete-agent", "Router"}, {"revoke-token", "unique-token"},
		{"move-agent", "Router", "1"}, {"repair-traffic", "all"},
		{"repair-traffic", "--since", "2026-10-18T03:17:38Z", "--apply", "Router"},
	} {
		if err := validateCommandArgs(args, false); err != nil {
			t.Fatalf("valid command %v: %v", args, err)
		}
	}
	for _, command := range []string{"delete-agent", "revoke-token"} {
		if err := validateCommandArgs([]string{command, "target"}, true); err != nil {
			t.Fatal("-y before destructive command rejected", err)
		}
	}
	for _, args := range [][]string{nil, {"list-agents"}, {"list-tokens"}, {"allow-token", "token"}, {"repair-traffic", "all"}} {
		if err := validateCommandArgs(args, true); err == nil {
			t.Fatalf("irrelevant -y accepted for %v", args)
		}
	}
}

// Use a real process to cover global flag.Parse and fatal exits. A validation
// helper test alone would miss commands that opened the database first.
func TestCLIArgumentBoundaryProcess(t *testing.T) {
	if os.Getenv("NEEDLE_TEST_SERVER_CLI") != "1" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("NEEDLE_TEST_SERVER_CLI_ARGS")), &args); err != nil {
		t.Fatal(err)
	}
	os.Args = append([]string{"needle-server"}, args...)
	flag.CommandLine = flag.NewFlagSet("needle-server", flag.ExitOnError)
	main()
}

func TestMalformedCommandsDoNotTouchDatabase(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"unknown-command"}, {"list-agents", "extra"}, {"list-tokens", "-y"},
		{"admin-init", "unexpected"}, {"admin-reset", "-y"},
		{"allow-token", "token", "extra"}, {"allow-token", "-y"},
		{"delete-agent", "Router", "-y"}, {"delete-agent", "-y", "Router"},
		{"revoke-token", "token", "-y"}, {"move-agent", "Router", "0"},
		{"repair-traffic", "all", "--apply"}, {"repair-traffic", "--apply", "Router"},
		{"-version", "list-agents"}, {"-installer-protocol", "list-tokens"}, {"-y", "list-agents"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "needle.db")
			// SQLite must never open this sentinel, attempt a migration, or create
			// WAL/SHM sidecars when the command syntax itself is invalid.
			original := []byte("database must stay unopened")
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(append([]string{"-db", path}, args...))
			cmd := exec.Command(exe, "-test.run=^TestCLIArgumentBoundaryProcess$")
			cmd.Env = append(os.Environ(), "NEEDLE_TEST_SERVER_CLI=1", "NEEDLE_TEST_SERVER_CLI_ARGS="+string(encoded))
			output, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "event=arguments_invalid") {
				t.Fatalf("malformed command did not fail at argument validation: %v\n%s", err, output)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != string(original) {
				t.Fatalf("malformed command changed its database: %q / %v", data, err)
			}
			files, err := os.ReadDir(dir)
			if err != nil || len(files) != 1 {
				t.Fatalf("malformed command created database sidecars: %v / %v", files, err)
			}
		})
	}
}
