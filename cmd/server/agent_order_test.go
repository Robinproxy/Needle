package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"needle/internal/server"
)

func TestMoveAgentCLI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "needle.db")
	s, err := server.NewStoreCLI(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Alpha", "Router"} {
		if _, err := s.SaveReport(&server.ReportWrite{Hostname: name}); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	for _, args := range [][]string{nil, {"Router"}, {"Router", "1", "extra"}, {"Router", "x"}, {"Router", "0"}, {"Router", "3"}, {"missing", "1"}} {
		if err := runMoveAgent(path, args); err == nil {
			t.Fatalf("accepted invalid arguments: %v", args)
		}
	}
	if err := runMoveAgent(path, []string{"Router", "1"}); err != nil {
		t.Fatal(err)
	}
	// list-agents distinguishes current position from the immutable node ID.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	err = runListAgents(path)
	w.Close()
	os.Stdout = old
	if err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) != 3 || strings.Fields(lines[0])[0] != "POS" || strings.Join(strings.Fields(lines[1])[:3], " ") != "1 2 Router" {
		t.Fatalf("list-agents did not show the new order and stable IDs:\n%s", output)
	}
	if err := runMoveAgent(path, []string{"2", "2"}); err != nil {
		t.Fatal(err)
	}
	s, err = server.NewStoreCLI(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	agents, err := s.GetAgents()
	if err != nil || len(agents) != 2 || agents[0].Hostname != "Alpha" || agents[1].ID != 2 {
		t.Fatalf("move by ID did not persist: %+v / %v", agents, err)
	}
}
