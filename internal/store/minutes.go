package store

import (
	"context"
	"time"
)

// MinutesRetention matches the 30-day retention of metrics and checks.
const MinutesRetention = 30 * 24 * time.Hour

// JobUsage is the measured duration of one completed workflow job.
type JobUsage struct {
	JobID       int64
	Backend     string
	Labels      string
	RunnerName  string
	StartedAt   int64
	CompletedAt int64
}

// RunScan marks one completed run attempt whose jobs were read.
type RunScan struct {
	RunID      int64
	RunAttempt int
	CreatedAt  int64
}

type MinutesCursor struct {
	CollectedAt  int64
	CoveredSince int64
	AttemptedAt  int64
	Error        string
}

type BackendUsage struct {
	Backend    string `json:"backend"`
	Jobs7d     int64  `json:"jobs_7d"`
	Minutes7d  int64  `json:"minutes_7d"`
	Jobs30d    int64  `json:"jobs_30d"`
	Minutes30d int64  `json:"minutes_30d"`
}

type RepoMinutes struct {
	Repo         string         `json:"repo"`
	CollectedAt  int64          `json:"collected_at"`
	CoveredSince int64          `json:"covered_since"`
	AttemptedAt  int64          `json:"attempted_at"`
	Error        string         `json:"error,omitempty"`
	Backends     []BackendUsage `json:"backends"`
}

func (s *Store) RunScanned(ctx context.Context, repo string, runID int64, attempt int) (bool, error) {
	var found int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_scans WHERE repo=? AND run_id=? AND run_attempt=?`, repo, runID, attempt).Scan(&found)
	return found > 0, err
}

// RecordRunUsage stores the jobs of one run attempt and marks the attempt as
// read in the same transaction, so a failed write is retried as a whole.
func (s *Store) RecordRunUsage(ctx context.Context, repo string, run RunScan, jobs []JobUsage, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, job := range jobs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO job_minutes(repo,job_id,run_id,run_attempt,backend,labels,runner_name,started_at,completed_at,seconds)
VALUES (?,?,?,?,?,?,NULLIF(?,''),?,?,?) ON CONFLICT(repo,job_id) DO NOTHING`,
			repo, job.JobID, run.RunID, run.RunAttempt, job.Backend, job.Labels, job.RunnerName, job.StartedAt, job.CompletedAt, job.CompletedAt-job.StartedAt); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_scans(repo,run_id,run_attempt,created_at,scanned_at) VALUES (?,?,?,?,?)
ON CONFLICT(repo,run_id,run_attempt) DO NOTHING`, repo, run.RunID, run.RunAttempt, run.CreatedAt, now.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) MinutesCursor(ctx context.Context, repo string) (MinutesCursor, error) {
	var cursor MinutesCursor
	rows, err := s.db.QueryContext(ctx, `SELECT collected_at,covered_since,attempted_at,error FROM minutes_cursor WHERE repo=?`, repo)
	if err != nil {
		return cursor, err
	}
	defer rows.Close()
	if rows.Next() {
		if err := rows.Scan(&cursor.CollectedAt, &cursor.CoveredSince, &cursor.AttemptedAt, &cursor.Error); err != nil {
			return cursor, err
		}
	}
	return cursor, rows.Err()
}

// MarkMinutesCollected records a pass that read every listed run. coveredSince
// is the oldest creation day that has been fully read for this repository.
func (s *Store) MarkMinutesCollected(ctx context.Context, repo string, at time.Time, coveredSince int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO minutes_cursor(repo,collected_at,covered_since,attempted_at,error) VALUES (?,?,?,?, '')
ON CONFLICT(repo) DO UPDATE SET collected_at=excluded.collected_at,covered_since=excluded.covered_since,attempted_at=excluded.attempted_at,error=''`, repo, at.Unix(), coveredSince, at.Unix())
	return err
}

// MarkMinutesAttempt records a pass that stopped early. The last complete
// collection keeps its time, so the UI can age it honestly.
func (s *Store) MarkMinutesAttempt(ctx context.Context, repo string, at time.Time, problem string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO minutes_cursor(repo,attempted_at,error) VALUES (?,?,?)
ON CONFLICT(repo) DO UPDATE SET attempted_at=excluded.attempted_at,error=excluded.error`, repo, at.Unix(), problem)
	return err
}

// BackendMinutes aggregates job time per repository and backend for the last
// 7 and 30 days. Each job is rounded up to a whole minute, as GitHub does.
func (s *Store) BackendMinutes(ctx context.Context, now time.Time) (map[string]RepoMinutes, error) {
	result := map[string]RepoMinutes{}
	cursors, err := s.db.QueryContext(ctx, `SELECT repo,collected_at,covered_since,attempted_at,error FROM minutes_cursor`)
	if err != nil {
		return nil, err
	}
	for cursors.Next() {
		r := RepoMinutes{Backends: []BackendUsage{}}
		if err := cursors.Scan(&r.Repo, &r.CollectedAt, &r.CoveredSince, &r.AttemptedAt, &r.Error); err != nil {
			cursors.Close()
			return nil, err
		}
		result[r.Repo] = r
	}
	if err := cursors.Close(); err != nil {
		return nil, err
	}
	week := now.Add(-7 * 24 * time.Hour).Unix()
	month := now.Add(-MinutesRetention).Unix()
	rows, err := s.db.QueryContext(ctx, `SELECT repo,backend,
SUM(CASE WHEN completed_at>=? THEN 1 ELSE 0 END),
SUM(CASE WHEN completed_at>=? THEN (seconds+59)/60 ELSE 0 END),
COUNT(*), SUM((seconds+59)/60)
FROM job_minutes WHERE completed_at>=? GROUP BY repo,backend ORDER BY repo,SUM(seconds) DESC,backend`, week, week, month)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var repo string
		var usage BackendUsage
		if err := rows.Scan(&repo, &usage.Backend, &usage.Jobs7d, &usage.Minutes7d, &usage.Jobs30d, &usage.Minutes30d); err != nil {
			return nil, err
		}
		r, ok := result[repo]
		if !ok {
			r = RepoMinutes{Repo: repo, Backends: []BackendUsage{}}
		}
		r.Backends = append(r.Backends, usage)
		result[repo] = r
	}
	return result, rows.Err()
}
