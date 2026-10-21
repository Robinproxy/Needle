//go:build linux

package collector

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Also runnable as the dedicated service user: no root-only device, raw socket
// or privileged process API should be needed by a basic collector.
func TestUnprivilegedLinuxCollectors(t *testing.T) {
	if os.Getenv("NEEDLE_REQUIRE_NONROOT") == "1" && os.Geteuid() == 0 {
		t.Fatal("this test must run as an unprivileged user")
	}
	cpuWindow := &CPUCollector{}
	if _, err := cpuWindow.Collect(); !errors.Is(err, ErrBaseline) {
		t.Fatalf("CPU baseline: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	cpu, err := cpuWindow.Collect()
	if err != nil || cpu == nil || cpu.Cores < 1 {
		t.Fatalf("CPU: %+v %v", cpu, err)
	}
	memory, err := CollectMemory()
	if err != nil || memory == nil || memory.Total == 0 || memory.SwapUsed > memory.SwapTotal {
		t.Fatalf("memory/Swap: %+v %v", memory, err)
	}
	disk, err := CollectDisk()
	if err != nil || disk == nil || disk.Total == 0 {
		t.Fatalf("disk: %+v %v", disk, err)
	}
	load, err := CollectLoad()
	if err != nil || load == nil {
		t.Fatalf("load: %+v %v", load, err)
	}
	uptime, err := CollectUptime()
	if err != nil || uptime == 0 {
		t.Fatalf("uptime: %d %v", uptime, err)
	}
	network := NewNetworkCollector([]string{"lo"})
	first, err := network.Collect()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			conn.Close()
		}
	}()
	probes := TCPing([]TCPingTarget{{Name: "local", Target: listener.Addr().String()}})
	if len(probes) != 1 || !probes[0].Success || probes[0].LatencyMs < 0 {
		t.Fatalf("TCP Ping: %+v", probes)
	}
	time.Sleep(10 * time.Millisecond)
	second, err := network.Collect()
	if err != nil || second.TotalSent <= first.TotalSent || second.TotalRecv <= first.TotalRecv || second.Up <= 0 || second.Down <= 0 {
		t.Fatalf("network counters/rates: first=%+v second=%+v err=%v", first, second, err)
	}
}

func TestProcPermissionErrorsAreNotHealthyZeros(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions; run this test as the service user")
	}
	proc := t.TempDir()
	for _, name := range []string{"stat", "meminfo", "loadavg", "uptime"} {
		if err := os.WriteFile(filepath.Join(proc, name), []byte("unreadable"), 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(proc, "net"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proc, "net", "dev"), []byte("unreadable"), 0); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOST_PROC", proc)
	checks := map[string]func() error{
		"cpu":     func() error { _, err := (&CPUCollector{}).Collect(); return err },
		"memory":  func() error { _, err := CollectMemory(); return err },
		"network": func() error { _, err := NewNetworkCollector(nil).Collect(); return err },
	}
	for name, collect := range checks {
		if err := collect(); err == nil {
			t.Errorf("%s hid an unreadable /proc file as a normal sample", name)
		}
	}
}
