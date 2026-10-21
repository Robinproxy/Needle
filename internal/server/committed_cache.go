package server

import (
	"database/sql"
	"errors"
	"reflect"
	"time"
)

type committedEntry struct {
	agent  AgentRow
	node   agentWithMetric
	loaded time.Time
}

func (s *Store) migrateAdminRevision() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS admin_revision(id INTEGER PRIMARY KEY CHECK(id=1),revision INTEGER NOT NULL);INSERT OR IGNORE INTO admin_revision VALUES(1,0)`)
	return err
}
func bumpAdminRevision(x execer) error {
	_, err := x.Exec(`UPDATE admin_revision SET revision=revision+1 WHERE id=1`)
	return err
}
func (h *Handler) syncAdminRevision() error {
	var revision, schema int64
	if err := h.store.db.QueryRow(`SELECT revision,(SELECT schema_version FROM pragma_schema_version) FROM admin_revision WHERE id=1`).Scan(&revision, &schema); err != nil {
		return err
	}
	h.committedMu.Lock()
	defer h.committedMu.Unlock()
	if h.adminRevision != revision || h.schemaRevision != schema {
		h.committed = make(map[int64]committedEntry)
		h.adminRevision = revision
		h.schemaRevision = schema
	}
	return nil
}
func (h *Handler) invalidateCommitted(id int64) {
	h.committedMu.Lock()
	delete(h.committed, id)
	h.committedMu.Unlock()
}
func (h *Handler) committedNode(a AgentRow) (agentWithMetric, error) {
	// Callers serialize full commits with fullMu. Cache only committed DB data;
	// realtime overlays/freshness are always applied separately. A short TTL also
	// reconciles unsupported direct SQLite edits without indefinite stale values.
	h.committedMu.Lock()
	defer h.committedMu.Unlock()
	if entry, ok := h.committed[a.ID]; ok && cacheCommittedSnapshots && reflect.DeepEqual(entry.agent, a) && h.now().Sub(entry.loaded) >= 0 && h.now().Sub(entry.loaded) < 15*time.Second && entry.loaded.In(h.now().Location()).Format("2006-01-02") == h.now().Format("2006-01-02") {
		return entry.node, nil
	}
	m, err := h.store.GetLatestMetric(a.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return agentWithMetric{}, err
	}
	probes, err := h.store.GetLatestTCPing(a.ID)
	if err != nil {
		return agentWithMetric{}, err
	}
	traffic, err := h.store.GetTrafficUsage(a.ID)
	if err != nil {
		return agentWithMetric{}, err
	}
	node := agentWithMetric{Agent: a, Metric: m, LatestTCPing: probes, TCPingEnabled: len(probes) > 0, Traffic: traffic}
	if len(h.committed) >= maxLiveNodes {
		clear(h.committed)
	}
	if cacheCommittedSnapshots {
		h.committed[a.ID] = committedEntry{agent: a, node: node, loaded: h.now()}
	}
	return node, nil
}
