package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdminSetupRequiresTerminalBeforeOpeningDatabase(t *testing.T) {
	input, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer output.Close()
	old := os.Stdin
	os.Stdin = input
	defer func() { os.Stdin = old }()
	path := filepath.Join(t.TempDir(), "needle.db")
	if err := runAdminSetup(path, false); err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatal("non-interactive credential input accepted", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("credential setup touched the database before terminal validation")
	}
}
