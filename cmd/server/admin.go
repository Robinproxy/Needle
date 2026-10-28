package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"needle/internal/server"

	"golang.org/x/term"
)

func runAdminSetup(dbPath string, reset bool) error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return fmt.Errorf("admin setup requires an interactive terminal (Docker: docker compose exec needle-server ...); passwords are never accepted as command arguments")
	}
	store, err := openStore(dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	username, origin, err := store.AdminDefaults()
	if err != nil {
		return err
	}
	if reset && username == "" {
		return fmt.Errorf("administrator is not initialized; use admin-init")
	}
	if !reset && username != "" {
		return fmt.Errorf("administrator already exists; use admin-reset")
	}
	if username == "" {
		username = "admin"
	}
	reader := bufio.NewReader(os.Stdin)
	prompt := func(label, fallback string) (string, error) {
		fmt.Printf("%s [%s]: ", label, fallback)
		value, err := reader.ReadString('\n')
		if err != nil {
			return "", fmt.Errorf("read setup input: %w", err)
		}
		if value = strings.TrimSpace(value); value != "" {
			return value, nil
		}
		return fallback, nil
	}
	if username, err = prompt("Administrator username", username); err != nil {
		return err
	}
	if origin, err = prompt("Public origin (HTTPS; no path)", origin); err != nil {
		return err
	}
	if origin, err = server.AdminOrigin(origin); err != nil {
		return err
	}
	password, err := readAdminPassword(fd, reader)
	if err != nil {
		return err
	}
	if err := store.SetAdmin(username, password, origin, reset); err != nil {
		return err
	}
	_, err = io.WriteString(os.Stdout, "Administrator saved. Open /admin on the configured origin. Existing sessions are invalidated; no Server restart is needed.\n")
	return err
}

func readAdminPassword(fd int, reader io.Reader) (string, error) {
	// Disable kernel echo before printing either password prompt. Terminal's
	// reader handles Ctrl+C itself, so deferred restoration also runs on cancel.
	state, err := term.MakeRaw(fd)
	if err != nil {
		return "", err
	}
	defer term.Restore(fd, state)
	terminal := term.NewTerminal(struct {
		io.Reader
		io.Writer
	}{reader, os.Stdout}, "")
	password, err := terminal.ReadPassword("Password (at least 15 characters; hidden): ")
	if err != nil {
		return "", err
	}
	confirm, err := terminal.ReadPassword("Confirm password (hidden): ")
	if err != nil {
		return "", err
	}
	if password != confirm {
		return "", fmt.Errorf("passwords do not match; credentials unchanged")
	}
	return password, nil
}
