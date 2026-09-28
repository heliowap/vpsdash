package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// UnitOp is one audited restart or drain of a gh-agents runner unit.
type UnitOp struct {
	ID          int64  `json:"id"`
	HostID      string `json:"host_id"`
	Unit        string `json:"unit"`
	Action      string `json:"action"`
	Status      string `json:"status"`
	Detail      string `json:"detail,omitempty"`
	RequestedAt int64  `json:"requested_at"`
	FinishedAt  int64  `json:"finished_at,omitempty"`
	// StopUnconfirmed marks a drain that ended after its stop request may
	// have reached systemd, without a reading of the unit that settles
	// whether it stopped. The next unit reading resolves it.
	StopUnconfirmed bool `json:"stop_unconfirmed,omitempty"`
}

// Drained reports whether the unit was deliberately stopped by a drain and
// has not been restarted from the panel since.
func (op UnitOp) Drained() bool { return op.Action == "drain" && op.Status == "done" }

func (s *Store) StartUnitOp(ctx context.Context, hostID, unit, action string, at time.Time) (UnitOp, error) {
	op := UnitOp{HostID: hostID, Unit: unit, Action: action, Status: "running", RequestedAt: at.Unix()}
	err := s.db.QueryRowContext(ctx, `INSERT INTO runner_unit_ops(host_id,unit,action,status,requested_at) VALUES (?,?,?,'running',?) RETURNING id`,
		hostID, unit, action, op.RequestedAt).Scan(&op.ID)
	return op, err
}

func (s *Store) SetUnitOpDetail(ctx context.Context, id int64, detail string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE runner_unit_ops SET detail=? WHERE id=? AND status='running'`, detail, id)
	return err
}

func (s *Store) FinishUnitOp(ctx context.Context, id int64, status, detail string, at time.Time) error {
	return s.finishUnitOp(ctx, id, status, detail, false, at)
}

// FinishUnitOpStopUnconfirmed closes a drain whose stop request was sent but
// whose effect on the unit could not be read.
func (s *Store) FinishUnitOpStopUnconfirmed(ctx context.Context, id int64, status, detail string, at time.Time) error {
	return s.finishUnitOp(ctx, id, status, detail, true, at)
}

func (s *Store) finishUnitOp(ctx context.Context, id int64, status, detail string, unconfirmed bool, at time.Time) error {
	if status == "running" {
		return errors.New("finished operation needs a final status")
	}
	flag := 0
	if unconfirmed {
		flag = 1
	}
	result, err := s.db.ExecContext(ctx, `UPDATE runner_unit_ops SET status=?,detail=?,finished_at=?,stop_unconfirmed=? WHERE id=? AND status='running'`, status, detail, at.Unix(), flag, id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// AbandonRunningUnitOps closes operations interrupted by a service restart.
// The in-memory drain loop is gone, so the audit must not keep them running.
// A drain may have sent its stop before the interruption, so its effect stays
// unconfirmed until the next unit reading.
func (s *Store) AbandonRunningUnitOps(ctx context.Context, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE runner_unit_ops SET status='failed',finished_at=?,
stop_unconfirmed=CASE WHEN action='drain' THEN 1 ELSE 0 END,
detail=CASE WHEN action='drain' THEN 'Interrompida: o painel foi reiniciado antes do fim. Estado da unit não confirmado.' ELSE 'Interrompida: o painel foi reiniciado antes do fim.' END
WHERE status='running'`, at.Unix())
	return err
}

// ResolveDrainStop settles an unconfirmed drain from a unit reading: a
// stopped unit makes it a completed drain; otherwise it keeps its status.
func (s *Store) ResolveDrainStop(ctx context.Context, id int64, stopped bool, detail string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE runner_unit_ops SET status=CASE WHEN ? THEN 'done' ELSE status END,detail=?,stop_unconfirmed=0
WHERE id=? AND stop_unconfirmed=1`, stopped, detail, id)
	return err
}

// LatestUnitOps returns the newest operation for each host and unit.
func (s *Store) LatestUnitOps(ctx context.Context) ([]UnitOp, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,host_id,unit,action,status,detail,requested_at,finished_at,stop_unconfirmed FROM runner_unit_ops
WHERE id IN (SELECT MAX(id) FROM runner_unit_ops GROUP BY host_id, unit) ORDER BY host_id,unit`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []UnitOp{}
	for rows.Next() {
		var op UnitOp
		if err := rows.Scan(&op.ID, &op.HostID, &op.Unit, &op.Action, &op.Status, &op.Detail, &op.RequestedAt, &op.FinishedAt, &op.StopUnconfirmed); err != nil {
			return nil, err
		}
		result = append(result, op)
	}
	return result, rows.Err()
}
