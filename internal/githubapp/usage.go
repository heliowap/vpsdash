package githubapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ReservedRequests is the per-installation budget the minutes collector leaves
// for fleet polling and the runner switch. Usage reads stop below it.
const ReservedRequests = 500

// ErrRateBudget reports that the installation is close to its GitHub API
// limit. Callers should stop and try again after the reset time.
var ErrRateBudget = errors.New("GitHub API rate budget reserved for fleet operations")

type rateBudget struct {
	remaining int
	reset     time.Time
}

// CompletedRun is a finished workflow run attempt listed for usage accounting.
type CompletedRun struct {
	ID         int64     `json:"id"`
	RunAttempt int       `json:"run_attempt"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// CompletedJob carries the fields the Jobs API already exposes under
// actions:read; they are enough to measure time per runner backend.
type CompletedJob struct {
	ID          int64      `json:"id"`
	RunID       int64      `json:"run_id"`
	RunAttempt  int        `json:"run_attempt"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion"`
	Labels      []string   `json:"labels"`
	RunnerName  string     `json:"runner_name"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
}

func (c *Client) noteBudget(owner string, header http.Header) {
	if header == nil {
		return
	}
	remaining, err := strconv.Atoi(header.Get("X-RateLimit-Remaining"))
	if err != nil {
		return
	}
	reset, err := strconv.ParseInt(header.Get("X-RateLimit-Reset"), 10, 64)
	if err != nil {
		return
	}
	c.mu.Lock()
	c.budgets[owner] = rateBudget{remaining: remaining, reset: time.Unix(reset, 0)}
	c.mu.Unlock()
}

func (c *Client) budgetExhausted(owner string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	budget, ok := c.budgets[owner]
	return ok && budget.remaining < ReservedRequests && now.Before(budget.reset)
}

// usageGet issues a read for usage accounting, refusing to spend the reserve
// that the fleet poll and the runner switch depend on.
func (c *Client) usageGet(ctx context.Context, repo, path string, out any) error {
	token, err := c.repoToken(ctx, repo)
	if err != nil {
		return err
	}
	owner, _, _ := strings.Cut(repo, "/")
	if c.budgetExhausted(owner, time.Now()) {
		return ErrRateBudget
	}
	status, header, err := c.requestHeaders(ctx, "GET", path, token, nil, out)
	c.noteBudget(owner, header)
	if err != nil && (status == http.StatusTooManyRequests || (status == http.StatusForbidden && header.Get("X-RateLimit-Remaining") == "0")) {
		return fmt.Errorf("%w: %w", ErrRateBudget, err)
	}
	return err
}

// CompletedRuns lists one page (100 runs, newest first) of completed runs
// created on or after the given day.
func (c *Client) CompletedRuns(ctx context.Context, repo string, createdSince time.Time, page int) ([]CompletedRun, error) {
	if page < 1 {
		return nil, errors.New("invalid page")
	}
	var result struct {
		WorkflowRuns []CompletedRun `json:"workflow_runs"`
	}
	path := fmt.Sprintf("/repos/%s/actions/runs?status=completed&created=%%3E%%3D%s&per_page=100&page=%d", repo, createdSince.UTC().Format("2006-01-02"), page)
	err := c.usageGet(ctx, repo, path, &result)
	return result.WorkflowRuns, err
}

// RunAttemptJobs lists the jobs of one run attempt. Earlier attempts keep
// their own job IDs, so each attempt is read once.
func (c *Client) RunAttemptJobs(ctx context.Context, repo string, runID int64, attempt int) ([]CompletedJob, error) {
	if runID <= 0 || attempt <= 0 {
		return nil, errors.New("invalid run attempt")
	}
	var jobs []CompletedJob
	for page := 1; page <= 10; page++ {
		var result struct {
			TotalCount int            `json:"total_count"`
			Jobs       []CompletedJob `json:"jobs"`
		}
		path := fmt.Sprintf("/repos/%s/actions/runs/%d/attempts/%d/jobs?per_page=100&page=%d", repo, runID, attempt, page)
		if err := c.usageGet(ctx, repo, path, &result); err != nil {
			return nil, err
		}
		jobs = append(jobs, result.Jobs...)
		if len(result.Jobs) < 100 || len(jobs) >= result.TotalCount {
			return jobs, nil
		}
	}
	return nil, fmt.Errorf("run %d attempt %d has more than 1000 jobs", runID, attempt)
}
