package store

import (
	"context"
	"time"
)

// AuditEntry records an interactive action. It never holds keystrokes,
// terminal output, or snippet output.
type AuditEntry struct {
	ID       int64  `json:"id"`
	TS       int64  `json:"ts"`
	Action   string `json:"action"`
	HostID   string `json:"host_id"`
	Target   string `json:"target"`
	ClientIP string `json:"client_ip"`
	Outcome  string `json:"outcome"`
}

func (s *Store) RecordAudit(ctx context.Context, entry AuditEntry, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit_log(ts,action,host_id,target,client_ip,outcome) VALUES (?,?,?,?,?,?)`,
		at.Unix(), entry.Action, entry.HostID, entry.Target, entry.ClientIP, entry.Outcome)
	return err
}

func (s *Store) RecentAudit(ctx context.Context, limit int) ([]AuditEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,ts,action,host_id,target,client_ip,outcome FROM audit_log ORDER BY ts DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.TS, &e.Action, &e.HostID, &e.Target, &e.ClientIP, &e.Outcome); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}
