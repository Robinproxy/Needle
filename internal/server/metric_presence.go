package server

import (
	"fmt"
	"strings"
)

const (
	missingCPU = 1 << iota
	missingMemory
	missingDisk
	missingNetwork
	missingLoad
)

// NULL resource values distinguish an unavailable collector from a real zero.
// The mask is also persisted: legacy zero placeholders cannot prove that the
// network collector ran, whereas new zero counters are a valid baseline.
func metricValue(m *MetricRow, bit int, value any) any {
	if m.MissingMetrics&bit != 0 {
		return nil
	}
	return value
}

const networkCounterPresent = `(COALESCE(missing_metrics,0)&8)=0 AND
 (missing_metrics IS NOT NULL OR total_sent>0 OR total_recv>0)`

var metricResourceColumns = []string{"cpu_usage", "memory_total", "disk_total", "network_up", "load1"}

func metricMaskSQL(aggregate bool) string {
	var terms []string
	for i, col := range metricResourceColumns {
		condition := col + " IS NULL"
		if aggregate {
			condition = "COUNT(" + col + ")=0"
		}
		terms = append(terms, fmt.Sprintf("CASE WHEN %s THEN %d ELSE 0 END", condition, 1<<i))
	}
	return "(" + strings.Join(terms, " + ") + ")"
}

const rawMetricValues = `COALESCE(cpu_usage,0), COALESCE(memory_total,0), COALESCE(memory_used,0),
 COALESCE(disk_total,0), COALESCE(disk_used,0), COALESCE(network_up,0), COALESCE(network_down,0),
 COALESCE(total_sent,0), COALESCE(total_recv,0), COALESCE(load1,0), COALESCE(load5,0), COALESCE(load15,0), uptime, created_at`

// Ingestion-time hourly aggregation. AVG skips
// unavailable collectors; totals are counter high watermarks, never summed.
const aggregateMetricValues = `AVG(cpu_usage), MAX(cpu_usage),
 AVG(memory_total), AVG(memory_used), MAX(CASE WHEN memory_total>0 THEN memory_used*100.0/memory_total END),
 AVG(disk_total), AVG(disk_used), AVG(network_up), MAX(network_up), AVG(network_down), MAX(network_down),
 MAX(total_sent), MAX(total_recv), AVG(load1), AVG(load5), AVG(load15), MAX(uptime), COUNT(*),
 COUNT(cpu_usage), COUNT(memory_total), COUNT(disk_total), COUNT(network_up), COUNT(load1)`
