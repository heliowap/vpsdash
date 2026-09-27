package store

import (
	"context"
	"sort"
	"time"
)

// Retention windows fixed by the phase B spec. History reads never look past
// them, even before the nightly prune has removed older rows.
const (
	CheckRetention = 30 * 24 * time.Hour
	AlertRetention = 90 * 24 * time.Hour
	// AlertThreshold mirrors the collector rule: three consecutive failed
	// checks queue a project_down alert.
	AlertThreshold = 3
	maxIncidents   = 100
)

// Incident is a run of consecutive failed checks, or an alert whose checks
// have already left the retention window.
type Incident struct {
	Source        string `json:"source"`      // "checks" | "alert"
	State         string `json:"state"`       // "open" | "recovered" | "unresolved"
	StartedAt     int64  `json:"started_at"`  // first failed check, or alert time
	StartKnown    bool   `json:"start_known"` // false when the run began before the retained checks
	LastFailureAt int64  `json:"last_failure_at,omitempty"`
	RecoveredAt   int64  `json:"recovered_at,omitempty"` // first passing check after the run
	FailedChecks  int    `json:"failed_checks"`
	Detail        string `json:"detail"`
	AlertAt       int64  `json:"alert_at,omitempty"`
}

type IncidentHistory struct {
	ProjectID      int64      `json:"project_id"`
	Monitored      bool       `json:"monitored"`
	ObservedAt     int64      `json:"observed_at"`
	ChecksSince    int64      `json:"checks_since"`
	AlertsSince    int64      `json:"alerts_since"`
	AlertThreshold int        `json:"alert_threshold"`
	CheckCount     int        `json:"check_count"`
	FirstCheckAt   int64      `json:"first_check_at,omitempty"`
	LastCheckAt    int64      `json:"last_check_at,omitempty"`
	Truncated      bool       `json:"truncated"`
	Incidents      []Incident `json:"incidents"`
}

// ProjectIncidents derives the failure/recovery timeline of one project from
// the retained checks (30 days) and project alerts (90 days). It returns
// sql.ErrNoRows when the project does not exist.
func (s *Store) ProjectIncidents(ctx context.Context, projectID int64, now time.Time) (IncidentHistory, error) {
	s.historyMu.RLock()
	defer s.historyMu.RUnlock()
	history := IncidentHistory{
		ProjectID:      projectID,
		ObservedAt:     now.Unix(),
		ChecksSince:    now.Add(-CheckRetention).Unix(),
		AlertsSince:    now.Add(-AlertRetention).Unix(),
		AlertThreshold: AlertThreshold,
		Incidents:      []Incident{},
	}
	var monitored int
	if err := s.history.QueryRowContext(ctx, `SELECT monitored FROM projects WHERE id=?`, projectID).Scan(&monitored); err != nil {
		return IncidentHistory{}, err
	}
	history.Monitored = monitored != 0
	var olderChecks bool
	if err := s.history.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM checks WHERE project_id=? AND ts<?)`, projectID, history.ChecksSince).Scan(&olderChecks); err != nil {
		return IncidentHistory{}, err
	}
	runs, err := s.checkRuns(ctx, &history, olderChecks)
	if err != nil {
		return IncidentHistory{}, err
	}
	alerts, err := s.projectAlerts(ctx, projectID, history.AlertsSince, now.Unix())
	if err != nil {
		return IncidentHistory{}, err
	}
	for _, alert := range alerts {
		at := alert.at
		matched := false
		for i := range runs {
			end := runs[i].RecoveredAt
			if at >= runs[i].StartedAt && (end == 0 || at < end) {
				if runs[i].AlertAt == 0 {
					runs[i].AlertAt = at
				}
				matched = true
				break
			}
		}
		if !matched {
			runs = append(runs, Incident{Source: "alert", State: "unresolved", StartedAt: at, StartKnown: true, AlertAt: at, Detail: alert.body})
		}
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].StartedAt > runs[j].StartedAt })
	if len(runs) > maxIncidents {
		runs = runs[:maxIncidents]
		history.Truncated = true
	}
	history.Incidents = runs
	return history, nil
}

func (s *Store) checkRuns(ctx context.Context, history *IncidentHistory, olderChecks bool) ([]Incident, error) {
	rows, err := s.history.QueryContext(ctx, `SELECT ts,ok,CASE WHEN ok=0 THEN COALESCE(detail,'') ELSE '' END
FROM checks WHERE project_id=? AND ts>=? AND ts<=? ORDER BY ts,rowid`, history.ProjectID, history.ChecksSince, history.ObservedAt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []Incident{}
	var current *Incident
	for rows.Next() {
		var ts int64
		var ok int
		var detail string
		if err := rows.Scan(&ts, &ok, &detail); err != nil {
			return nil, err
		}
		history.CheckCount++
		first := history.FirstCheckAt == 0
		if first {
			history.FirstCheckAt = ts
		}
		history.LastCheckAt = ts
		if ok != 0 {
			if current != nil {
				current.RecoveredAt = ts
				current.State = "recovered"
				runs = append(runs, *current)
				current = nil
			}
			continue
		}
		if current == nil {
			// A run that starts at the retention horizon may have begun earlier.
			startKnown := !(first && (olderChecks || ts-history.ChecksSince < 3600))
			current = &Incident{Source: "checks", State: "open", StartedAt: ts, StartKnown: startKnown}
		}
		current.FailedChecks++
		current.LastFailureAt = ts
		if detail != "" {
			current.Detail = detail
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if current != nil {
		runs = append(runs, *current)
	}
	return runs, nil
}

type projectAlert struct {
	at   int64
	body string
}

func (s *Store) projectAlerts(ctx context.Context, projectID, since, until int64) ([]projectAlert, error) {
	// One event is stored once per delivery channel; history shows it once.
	rows, err := s.history.QueryContext(ctx, `SELECT created_at,body FROM alerts WHERE project_id=? AND created_at>=? AND created_at<=? GROUP BY created_at,body ORDER BY created_at,MIN(id)`, projectID, since, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []projectAlert{}
	for rows.Next() {
		var a projectAlert
		if err := rows.Scan(&a.at, &a.body); err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}
