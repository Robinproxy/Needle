package collector

import (
	"github.com/shirou/gopsutil/v4/net"
	"testing"
	"time"
)

func TestNetworkScopeTracksOnlySuccessfulInterfaceIdentities(t *testing.T) {
	now := time.Unix(100, 0)
	c := NewNetworkCollector([]string{"fake-a", "fake-b"})
	a, err := c.collect([]net.IOCountersStat{{Name: "fake-a", BytesSent: 100}, {Name: "fake-b", BytesSent: 200}}, now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.collect([]net.IOCountersStat{{Name: "fake-b", BytesSent: 300}, {Name: "fake-a", BytesSent: 200}}, now.Add(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if a.ScopeID == "" || a.ScopeID != b.ScopeID {
		t.Fatal("order or counter growth changed scope")
	}
	if _, err = c.collect(nil, now.Add(20*time.Second)); err == nil {
		t.Fatal("empty interface snapshot accepted")
	}
	d, err := c.collect([]net.IOCountersStat{{Name: "fake-a", BytesSent: 250}}, now.Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if d.ScopeID == a.ScopeID {
		t.Fatal("interface removal did not change scope")
	}
}

func TestIfindexChangeRebaselinesScopeAndRates(t *testing.T) {
	c := NewNetworkCollector([]string{"fake"})
	index := 1
	c.indexOf = func(string) int { return index }
	now := time.Unix(100, 0)
	a, err := c.collect([]net.IOCountersStat{{Name: "fake", BytesSent: 100}}, now)
	if err != nil {
		t.Fatal(err)
	}
	index = 2
	b, err := c.collect([]net.IOCountersStat{{Name: "fake", BytesSent: 100000000}}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if a.ScopeID == b.ScopeID || b.Up != 0 {
		t.Fatal("recreated interface produced an artificial rate spike", a, b)
	}
}
