package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db        *sql.DB
	history   *sql.DB
	historyMu sync.RWMutex
}

type Host struct {
	ID          string  `json:"id"`
	TailnetName string  `json:"tailnet_name"`
	Kind        string  `json:"kind"`
	SSHUser     string  `json:"ssh_user,omitempty"`
	Online      bool    `json:"online"`
	SeenAt      int64   `json:"seen_at,omitempty"`
	Latest      *Metric `json:"latest,omitempty"`
}

type Metric struct {
	TS     int64    `json:"ts"`
	CPU    *float64 `json:"cpu_pct,omitempty"`
	Memory float64  `json:"mem_pct"`
	Disk   float64  `json:"disk_pct"`
	Uptime int64    `json:"uptime_s"`
}

type Project struct {
	ID        int64  `json:"id"`
	HostID    string `json:"host_id"`
	Name      string `json:"name"`
	Source    string `json:"source"`
	Monitored bool   `json:"monitored"`
	Native    bool   `json:"native,omitempty"`
	HealthURL string `json:"health_url,omitempty"`
	Expected  string `json:"expected,omitempty"`
	CheckOK   *bool  `json:"check_ok,omitempty"`
	CheckedAt int64  `json:"checked_at,omitempty"`
}

func Open(path string) (*Store, error) {
	var err error
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	uri := url.URL{Scheme: "file", Path: path}
	uri.RawQuery = url.Values{"mode": {"rwc"}, "_foreign_keys": {"1"}, "_busy_timeout": {"5000"}}.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		_ = db.Close()
		return nil, err
	}
	uri.RawQuery = url.Values{"mode": {"ro"}, "_query_only": {"1"}, "_busy_timeout": {"5000"}}.Encode()
	history, err := sql.Open("sqlite", uri.String())
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	history.SetMaxOpenConns(1)
	if err := history.Ping(); err != nil {
		_ = history.Close()
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, history: history}, nil
}

func (s *Store) Close() error { return errors.Join(s.history.Close(), s.db.Close()) }

var migrations = []string{
	`CREATE TABLE hosts (
  id TEXT PRIMARY KEY,
  tailnet_name TEXT NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('vps','presence')),
  ssh_user TEXT,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE metrics (host_id TEXT NOT NULL REFERENCES hosts(id), ts INTEGER NOT NULL, cpu_pct REAL, mem_pct REAL, disk_pct REAL, uptime_s INTEGER);
CREATE INDEX idx_metrics_host_ts ON metrics(host_id, ts);
CREATE TABLE projects (
  id INTEGER PRIMARY KEY, host_id TEXT NOT NULL REFERENCES hosts(id), name TEXT NOT NULL,
  source TEXT NOT NULL CHECK(source IN ('docker','systemd','tmux')),
  monitored INTEGER NOT NULL DEFAULT 0, health_url TEXT, expected TEXT,
  UNIQUE(host_id, name, source)
);
CREATE TABLE checks (project_id INTEGER NOT NULL REFERENCES projects(id), ts INTEGER NOT NULL, ok INTEGER NOT NULL, detail TEXT);
CREATE INDEX idx_checks_proj_ts ON checks(project_id, ts);
CREATE TABLE tmux_sessions (
  host_id TEXT NOT NULL, name TEXT NOT NULL, pane_pid INTEGER, cwd TEXT, agent TEXT,
  state TEXT, seen_at INTEGER NOT NULL, PRIMARY KEY(host_id,name)
);
CREATE TABLE runners (
  repo TEXT NOT NULL, runner_id INTEGER NOT NULL, name TEXT NOT NULL, status TEXT NOT NULL,
  busy INTEGER NOT NULL, job TEXT, seen_at INTEGER NOT NULL, PRIMARY KEY(repo,runner_id)
);
CREATE TABLE alerts (
  id INTEGER PRIMARY KEY, kind TEXT NOT NULL, subject TEXT NOT NULL, body TEXT NOT NULL,
  sent_at INTEGER NOT NULL, channel TEXT NOT NULL CHECK(channel IN ('smtp','webpush'))
);`,
	`CREATE TABLE host_status (
  host_id TEXT PRIMARY KEY REFERENCES hosts(id), online INTEGER NOT NULL, seen_at INTEGER NOT NULL
);
CREATE INDEX idx_alerts_pending ON alerts(channel, sent_at);`,
	`ALTER TABLE projects ADD COLUMN native INTEGER NOT NULL DEFAULT 0 CHECK(native IN (0,1));`,
	`CREATE INDEX idx_runners_seen_at ON runners(seen_at);`,
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT (datetime('now')))`); err != nil {
		return err
	}
	var current int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&current); err != nil {
		return err
	}
	for i := current; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err = tx.Exec(migrations[i]); err == nil {
			_, err = tx.Exec(`INSERT INTO schema_migrations(version) VALUES (?)`, i+1)
		}
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) UpsertHost(ctx context.Context, h Host) error {
	if h.Kind != "vps" && h.Kind != "presence" {
		return errors.New("invalid host kind")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO hosts(id,tailnet_name,kind,ssh_user) VALUES (?,?,?,NULLIF(?,''))
ON CONFLICT(id) DO UPDATE SET tailnet_name=excluded.tailnet_name,kind=excluded.kind,ssh_user=excluded.ssh_user`, h.ID, h.TailnetName, h.Kind, h.SSHUser)
	return err
}

func (s *Store) SetHostPresence(ctx context.Context, id string, online bool, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO host_status(host_id,online,seen_at) VALUES (?,?,?)
ON CONFLICT(host_id) DO UPDATE SET online=excluded.online,seen_at=excluded.seen_at`, id, online, at.Unix())
	return err
}

func (s *Store) RecordMetric(ctx context.Context, hostID string, m Metric, at time.Time) error {
	var cpu any
	if m.CPU != nil {
		cpu = *m.CPU
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO metrics(host_id,ts,cpu_pct,mem_pct,disk_pct,uptime_s) VALUES (?,?,?,?,?,?)`, hostID, at.Unix(), cpu, m.Memory, m.Disk, m.Uptime)
	return err
}

func (s *Store) Hosts(ctx context.Context) ([]Host, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT h.id,h.tailnet_name,h.kind,COALESCE(h.ssh_user,''),COALESCE(hs.online,0),COALESCE(hs.seen_at,0),
COALESCE(m.ts,0),m.cpu_pct,COALESCE(m.mem_pct,0),COALESCE(m.disk_pct,0),COALESCE(m.uptime_s,0)
FROM hosts h LEFT JOIN host_status hs ON hs.host_id=h.id
LEFT JOIN metrics m ON m.rowid=(SELECT rowid FROM metrics WHERE host_id=h.id ORDER BY ts DESC,rowid DESC LIMIT 1)
ORDER BY h.kind,h.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Host{}
	for rows.Next() {
		var h Host
		var online int
		var m Metric
		var cpu sql.NullFloat64
		if err := rows.Scan(&h.ID, &h.TailnetName, &h.Kind, &h.SSHUser, &online, &h.SeenAt, &m.TS, &cpu, &m.Memory, &m.Disk, &m.Uptime); err != nil {
			return nil, err
		}
		if cpu.Valid {
			m.CPU = &cpu.Float64
		}
		h.Online = online != 0
		if m.TS != 0 {
			h.Latest = &m
		}
		result = append(result, h)
	}
	return result, rows.Err()
}

func (s *Store) MetricHistory(ctx context.Context, hostID string, since, until int64) ([]Metric, error) {
	s.historyMu.RLock()
	defer s.historyMu.RUnlock()
	step := (until - since + 719) / 720
	if step < 60 {
		step = 60
	}
	rows, err := s.history.QueryContext(ctx, `SELECT MAX(ts),AVG(cpu_pct),COALESCE(AVG(mem_pct),0),COALESCE(AVG(disk_pct),0),COALESCE(MAX(uptime_s),0)
FROM metrics WHERE host_id=? AND ts>=? AND ts<? GROUP BY (ts-?)/? ORDER BY MAX(ts)`, hostID, since, until, since, step)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Metric{}
	for rows.Next() {
		var m Metric
		var cpu sql.NullFloat64
		if err := rows.Scan(&m.TS, &cpu, &m.Memory, &m.Disk, &m.Uptime); err != nil {
			return nil, err
		}
		if cpu.Valid {
			m.CPU = &cpu.Float64
		}
		result = append(result, m)
	}
	return result, rows.Err()
}

func (s *Store) UpsertCandidate(ctx context.Context, hostID, name, source string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO projects(host_id,name,source) VALUES (?,?,?) ON CONFLICT(host_id,name,source) DO NOTHING`, hostID, name, source)
	return err
}

func (s *Store) UpsertNativeRunnerUnit(ctx context.Context, hostID, name string) (Project, error) {
	expected, err := json.Marshal([]string{"user:" + name})
	if err != nil {
		return Project{}, err
	}
	p := Project{HostID: hostID, Name: name, Source: "systemd", Monitored: true, Native: true, Expected: string(expected)}
	err = s.db.QueryRowContext(ctx, `INSERT INTO projects(host_id,name,source,monitored,native,expected) VALUES (?,?,'systemd',1,1,?)
ON CONFLICT(host_id,name,source) DO UPDATE SET monitored=1,native=1,health_url=NULL,expected=excluded.expected RETURNING id`, hostID, name, p.Expected).Scan(&p.ID)
	return p, err
}

func (s *Store) Projects(ctx context.Context) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.id,p.host_id,p.name,p.source,p.monitored,p.native,COALESCE(p.health_url,''),COALESCE(p.expected,''),
c.ok,COALESCE(c.ts,0) FROM projects p LEFT JOIN checks c ON c.rowid=(SELECT rowid FROM checks WHERE project_id=p.id ORDER BY ts DESC,rowid DESC LIMIT 1)
ORDER BY p.monitored DESC,p.host_id,p.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Project{}
	for rows.Next() {
		var p Project
		var monitored, native int
		var ok sql.NullInt64
		if err := rows.Scan(&p.ID, &p.HostID, &p.Name, &p.Source, &monitored, &native, &p.HealthURL, &p.Expected, &ok, &p.CheckedAt); err != nil {
			return nil, err
		}
		p.Monitored = monitored != 0
		p.Native = native != 0
		if ok.Valid {
			value := ok.Int64 != 0
			p.CheckOK = &value
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

func (s *Store) SetMonitored(ctx context.Context, id int64, monitored bool, healthURL, expected string) error {
	var n int
	if monitored {
		n = 1
	}
	result, err := s.db.ExecContext(ctx, `UPDATE projects SET monitored=?,health_url=NULLIF(?,''),expected=NULLIF(?, '') WHERE id=? AND native=0`, n, healthURL, expected, id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) RecordCheck(ctx context.Context, id int64, ok bool, detail string, at time.Time) error {
	var n int
	if ok {
		n = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO checks(project_id,ts,ok,detail) VALUES (?,?,?,?)`, id, at.Unix(), n, detail)
	return err
}

func (s *Store) ConsecutiveFailures(ctx context.Context, id int64) (int, error) {
	// idx_checks_proj_ts lets SQLite read only the three newest rows for this project.
	rows, err := s.db.QueryContext(ctx, `SELECT ok FROM checks WHERE project_id=? ORDER BY ts DESC,rowid DESC LIMIT 3`, id)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var ok int
		if err := rows.Scan(&ok); err != nil {
			return 0, err
		}
		if ok != 0 {
			break
		}
		count++
	}
	return count, rows.Err()
}

type pruneQuery struct {
	sql    string
	cutoff int64
}

func (s *Store) PruneRunners(ctx context.Context, now time.Time) error {
	cutoff := now.Add(-10 * time.Minute).Unix()
	return s.prune(ctx, []pruneQuery{
		{`DELETE FROM runners WHERE seen_at < ?`, cutoff},
	})
}

func (s *Store) PruneHistory(ctx context.Context, now time.Time) error {
	return s.prune(ctx, []pruneQuery{
		{`DELETE FROM metrics WHERE ts < ?`, now.Add(-30 * 24 * time.Hour).Unix()},
		{`DELETE FROM checks WHERE ts < ?`, now.Add(-30 * 24 * time.Hour).Unix()},
		{`DELETE FROM alerts WHERE sent_at > 0 AND sent_at < ?`, now.Add(-90 * 24 * time.Hour).Unix()},
	})
}

func (s *Store) prune(ctx context.Context, queries []pruneQuery) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, query := range queries {
		if _, err := tx.ExecContext(ctx, query.sql, query.cutoff); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Vacuum(ctx context.Context) error {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	_, err := s.db.ExecContext(ctx, `VACUUM`)
	return err
}
