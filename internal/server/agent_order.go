package server

import (
	"fmt"
	"strconv"
	"strings"
)

func (s *Store) migrateAgentOrder() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`ALTER TABLE agents ADD COLUMN display_order INTEGER NOT NULL DEFAULT 0`); err != nil {
		if strings.Contains(err.Error(), "duplicate column name") {
			return nil
		}
		return err
	}
	// Preserve ID order on upgrade. Gaps are harmless: positions are the
	// one-based indexes of the sorted nodes, not these internal sort keys.
	if _, err := tx.Exec(`UPDATE agents SET display_order=id`); err != nil {
		return err
	}
	return tx.Commit()
}

// MoveAgent moves one node to a one-based position without changing its ID or
// associated data. The write lock is acquired before reading, so reports and
// other CLI commands cannot change the ordering halfway through this move.
func (s *Store) MoveAgent(target string, position int) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE agents SET display_order=display_order WHERE 0`); err != nil {
		return err
	}
	rows, err := tx.Query(`SELECT id, hostname FROM agents ORDER BY display_order, id`)
	if err != nil {
		return err
	}
	var agents []AgentRow
	for rows.Next() {
		var a AgentRow
		if err := rows.Scan(&a.ID, &a.Hostname); err != nil {
			rows.Close()
			return err
		}
		agents = append(agents, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	index := -1
	if id, err := strconv.ParseInt(target, 10, 64); err == nil {
		for i, a := range agents {
			if a.ID == id {
				index = i
				break
			}
		}
	}
	if index < 0 {
		for i, a := range agents {
			if a.Hostname == target {
				index = i
				break
			}
		}
	}
	if index < 0 {
		return fmt.Errorf("agent not found: %s", target)
	}
	if position < 1 || position > len(agents) {
		return fmt.Errorf("position must be between 1 and %d", len(agents))
	}
	moved := agents[index]
	agents = append(agents[:index], agents[index+1:]...)
	agents = append(agents, AgentRow{})
	copy(agents[position:], agents[position-1:])
	agents[position-1] = moved
	for i, a := range agents {
		if _, err := tx.Exec(`UPDATE agents SET display_order=? WHERE id=?`, i+1, a.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
