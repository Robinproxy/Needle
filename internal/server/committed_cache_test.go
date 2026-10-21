package server

import (
	"testing"
	"time"
)

func TestCommittedCacheInvalidatesForAdministratorAndHistory(t *testing.T) {
	h, s, now, id := realtimeFixture(t)
	nodes, err := h.snapshots()
	if err != nil || len(nodes) != 1 {
		t.Fatal(err)
	}
	before := nodes[0].Traffic.Sent
	// External administrator CLI writes bump the persistent revision in the same tx.
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE traffic_baselines SET usage_sent=usage_sent+42 WHERE agent_id=?`, id); err != nil {
		t.Fatal(err)
	}
	if err = bumpAdminRevision(tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	nodes, err = h.snapshots()
	if err != nil || nodes[0].Traffic.Sent != before+42 {
		t.Fatal(nodes, err)
	}
	*now = now.Add(time.Second)
	if err := s.RevokeToken("live-secret"); err != nil {
		t.Fatal(err)
	}
	nodes, err = h.snapshots()
	if err != nil || len(nodes) != 1 {
		t.Fatal("revocation removed node or broke cache", nodes, err)
	}
	if err = s.DeleteAgent(id); err != nil {
		t.Fatal(err)
	}
	nodes, err = h.snapshots()
	if err != nil || len(nodes) != 0 {
		t.Fatal("deleted node resurrected", nodes, err)
	}
}
