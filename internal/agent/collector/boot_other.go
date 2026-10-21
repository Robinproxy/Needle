//go:build !linux

package collector

func BootID() (string, error) { return "", nil }
