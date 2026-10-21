//go:build linux

package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
)

// Re-exec CLI commands as the database owner before SQLite can create WAL/SHM.
// Do not infer an identity from environment variables or change existing owners.
func databaseOwnerCommand(path string) (bool, error) {
	if os.Geteuid() != 0 {
		return false, nil
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	dir, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || !dir.IsDir() || dir.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("database and parent must be regular, non-symlink paths")
	}
	st := info.Sys().(*syscall.Stat_t)
	ds := dir.Sys().(*syscall.Stat_t)
	if st.Uid != ds.Uid {
		return false, fmt.Errorf("database and data directory owners differ; repair ownership before running CLI")
	}
	if st.Uid == 0 {
		return false, nil
	} // Legacy root deployment, before migration.
	account, err := user.LookupId(strconv.FormatUint(uint64(st.Uid), 10))
	if err != nil {
		return false, fmt.Errorf("database owner has no account; repair ownership before running CLI: %w", err)
	}
	primaryGID, err := strconv.ParseUint(account.Gid, 10, 32)
	if err != nil || primaryGID == 0 {
		return false, fmt.Errorf("database owner must have a non-root primary group")
	}
	if st.Gid != uint32(primaryGID) || ds.Gid != uint32(primaryGID) {
		return false, fmt.Errorf("database/directory group must match the owner's dedicated primary group; repair ownership first")
	}
	exe, err := os.Executable()
	if err != nil {
		return false, err
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: st.Uid, Gid: uint32(primaryGID), Groups: []uint32{uint32(primaryGID)}}}
	if err := cmd.Run(); err != nil {
		return true, fmt.Errorf("CLI as database owner: %w", err)
	}
	return true, nil
}

func privateDatabaseUmask() { syscall.Umask(0077) }

// Root administrative commands and the installer share a per-DB lock so a
// permission migration cannot race a root SQLite connection creating sidecars.
func lockDatabaseAdministration(path string) (func(), error) {
	if os.Geteuid() != 0 {
		return func() {}, nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(absolute))
	lockPath := fmt.Sprintf("/run/needle-server-db-%x.lock", digest)
	fd, err := syscall.Open(lockPath, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, fmt.Errorf("open administrative lock: %w", err)
	}
	f := os.NewFile(uintptr(fd), lockPath)
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Sys().(*syscall.Stat_t).Uid != 0 {
		f.Close()
		return nil, fmt.Errorf("unsafe administrative lock")
	}
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return nil, err
	}
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another Server installer/administrator operation is using this database; retry when it finishes")
	}
	return func() { f.Close() }, nil
}
