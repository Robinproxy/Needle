package server

import (
	"context"
	"time"
)

// GetTCPingSummary reads at most one hour of raw history through the existing
// (agent_id, created_at) index. Do not query history for disabled probes, mix
// old targets into renamed routes, or let failed probes dilute mean latency.
func (s *Store) GetTCPingSummary(agentID int64, current []TCPingRow, now time.Time) ([]TCPingSummary, error) {
	if len(current) == 0 {
		return nil, nil
	}
	type route struct{ name, target string }
	active := make(map[route]int, len(current))
	summary := make([]TCPingSummary, len(current))
	for i, probe := range current {
		summary[i] = TCPingSummary{Name: probe.Name, Target: probe.Target}
		active[route{probe.Name, probe.Target}] = i
	}
	ctx, cancel := context.WithTimeout(context.Background(), historyQueryTimeout)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT name,target,COUNT(*),SUM(CASE WHEN success=1 THEN 1 ELSE 0 END),
 COALESCE(AVG(CASE WHEN success=1 THEN latency_ms END),0)
 FROM tcpping_results WHERE agent_id=? AND created_at>=? AND created_at<? GROUP BY name,target`,
		agentID, now.Add(-time.Hour).Unix(), now.Unix()+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item TCPingSummary
		if err := rows.Scan(&item.Name, &item.Target, &item.SampleCount, &item.SuccessCount, &item.LatencyMs); err != nil {
			return nil, err
		}
		if i, ok := active[route{item.Name, item.Target}]; ok {
			summary[i] = item
		}
	}
	return summary, rows.Err()
}
