package server

// These helpers deliberately bypass report accounting when constructing old or
// incomplete database fixtures. Production writes must use SaveReport instead.
func (s *Store) UpsertAgent(hostname, region string, expiresAt *int64, billingPeriod string) (int64, error) {
	return upsertAgent(s.db, hostname, region, expiresAt, billingPeriod, s.now().Unix())
}

func (s *Store) InsertMetric(m *MetricRow, createdAt int64) error {
	return insertMetric(s.db, m, createdAt)
}

func (s *Store) InsertTCPing(t *TCPingRow, createdAt int64) error {
	return insertTCPing(s.db, t, createdAt)
}
