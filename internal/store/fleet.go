package store

import (
	"context"
	"errors"
	"time"
)

type Runner struct {
	Repo   string `json:"repo"`
	ID     int64  `json:"runner_id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Busy   bool   `json:"busy"`
	Job    string `json:"job,omitempty"`
	SeenAt int64  `json:"seen_at"`
}

type Session struct {
	HostID  string `json:"host_id"`
	Name    string `json:"name"`
	PanePID int    `json:"pane_pid"`
	CWD     string `json:"cwd"`
	Agent   string `json:"agent,omitempty"`
	State   string `json:"state,omitempty"`
	SeenAt  int64  `json:"seen_at"`
}

type Alert struct {
	ID      int64  `json:"id"`
	Kind    string `json:"kind"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
	SentAt  int64  `json:"sent_at"`
	Channel string `json:"channel"`
}

func (s *Store) UpsertRunner(ctx context.Context, r Runner, at time.Time) error {
	var busy int
	if r.Busy {
		busy = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO runners(repo,runner_id,name,status,busy,job,seen_at) VALUES (?,?,?,?,?,?,?)
ON CONFLICT(repo,runner_id) DO UPDATE SET name=excluded.name,status=excluded.status,busy=excluded.busy,job=excluded.job,seen_at=excluded.seen_at`, r.Repo, r.ID, r.Name, r.Status, busy, r.Job, at.Unix())
	return err
}

func (s *Store) Runners(ctx context.Context) ([]Runner, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repo,runner_id,name,status,busy,COALESCE(job,''),seen_at FROM runners ORDER BY repo,name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Runner{}
	for rows.Next() {
		var r Runner
		var busy int
		if err := rows.Scan(&r.Repo, &r.ID, &r.Name, &r.Status, &busy, &r.Job, &r.SeenAt); err != nil {
			return nil, err
		}
		r.Busy = busy != 0
		result = append(result, r)
	}
	return result, rows.Err()
}

func (s *Store) UpsertSession(ctx context.Context, x Session, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO tmux_sessions(host_id,name,pane_pid,cwd,agent,state,seen_at) VALUES (?,?,?,?,?,?,?)
ON CONFLICT(host_id,name) DO UPDATE SET pane_pid=excluded.pane_pid,cwd=excluded.cwd,agent=excluded.agent,state=excluded.state,seen_at=excluded.seen_at`, x.HostID, x.Name, x.PanePID, x.CWD, x.Agent, x.State, at.Unix())
	return err
}

// ReplaceSessions records a complete successful snapshot for one host. A
// failed collector poll never calls this method, so its last snapshot remains.
// An agent session that enters the "waiting" state queues an agent_waiting
// alert, which is routed to Web Push only.
func (s *Store) ReplaceSessions(ctx context.Context, hostID string, sessions []Session, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	previous := map[string]string{}
	rows, err := tx.QueryContext(ctx, `SELECT name,COALESCE(state,'') FROM tmux_sessions WHERE host_id=?`, hostID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var name, state string
		if err := rows.Scan(&name, &state); err != nil {
			_ = rows.Close()
			return err
		}
		previous[name] = state
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tmux_sessions WHERE host_id=?`, hostID); err != nil {
		return err
	}
	for _, session := range sessions {
		if _, err := tx.ExecContext(ctx, `INSERT INTO tmux_sessions(host_id,name,pane_pid,cwd,agent,state,seen_at) VALUES (?,?,?,?,?,?,?)`,
			hostID, session.Name, session.PanePID, session.CWD, session.Agent, session.State, at.Unix()); err != nil {
			return err
		}
		if session.State == "waiting" && previous[session.Name] != "waiting" {
			if err := queueAlert(ctx, tx, nil, AlertAgentWaiting, hostID+" / "+session.Name, agentWaitingBody(session), at); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *Store) Sessions(ctx context.Context) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT host_id,name,COALESCE(pane_pid,0),COALESCE(cwd,''),COALESCE(agent,''),COALESCE(state,''),seen_at FROM tmux_sessions ORDER BY host_id,name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Session{}
	for rows.Next() {
		var x Session
		if err := rows.Scan(&x.HostID, &x.Name, &x.PanePID, &x.CWD, &x.Agent, &x.State, &x.SeenAt); err != nil {
			return nil, err
		}
		result = append(result, x)
	}
	return result, rows.Err()
}

func (s *Store) PendingAlerts(ctx context.Context) ([]Alert, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,kind,subject,body,sent_at,channel FROM alerts WHERE sent_at=0 AND channel='smtp' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Alert{}
	for rows.Next() {
		var a Alert
		if err := rows.Scan(&a.ID, &a.Kind, &a.Subject, &a.Body, &a.SentAt, &a.Channel); err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}

func (s *Store) MarkAlertSent(ctx context.Context, id int64, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE alerts SET sent_at=? WHERE id=? AND sent_at=0`, at.Unix(), id)
	return err
}

func agentWaitingBody(session Session) string {
	agent := session.Agent
	if agent == "" {
		agent = "O agente"
	}
	body := agent + " aguarda sua resposta na sessão " + session.Name + "."
	if session.CWD != "" {
		body += " Diretório: " + session.CWD + "."
	}
	return body
}
