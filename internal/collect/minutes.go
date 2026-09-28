package collect

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/heliowap/vpsdash/internal/githubapp"
	"github.com/heliowap/vpsdash/internal/store"
)

// MinutesAPI reads completed runs and their jobs for the backend minutes
// proxy. It uses the same actions:read permission as the fleet poll.
type MinutesAPI interface {
	CompletedRuns(context.Context, string, time.Time, int) ([]githubapp.CompletedRun, error)
	RunAttemptJobs(context.Context, string, int64, int) ([]githubapp.CompletedJob, error)
}

const (
	// MinutesInterval is the pause between collection passes.
	MinutesInterval = 10 * time.Minute
	// minutesJobReads bounds job reads per repository in one pass; a
	// backfill larger than this continues in the next pass.
	minutesJobReads = 100
	// minutesListPages bounds the runs listing (GitHub caps it at 1000 runs).
	minutesListPages = 10
	// minutesOverlap re-lists runs created shortly before the last complete
	// pass, because a run is listed only after it finishes.
	minutesOverlap = 72 * time.Hour
)

// RunnerBackend groups a job's runs-on labels into the backends the switch
// chooses between.
func RunnerBackend(labels []string) string {
	lower := make([]string, 0, len(labels))
	for _, label := range labels {
		lower = append(lower, strings.ToLower(strings.TrimSpace(label)))
	}
	for _, label := range lower {
		if label == "self-hosted" {
			return "self-hosted"
		}
	}
	for _, label := range lower {
		switch {
		case strings.HasPrefix(label, "depot-"):
			return "depot-*"
		case strings.HasPrefix(label, "ubicloud"):
			return "ubicloud-*"
		}
	}
	for _, label := range lower {
		if label == "ubuntu-latest" {
			return "ubuntu-latest"
		}
	}
	return "outros"
}

func (c *Collector) minutesLoop(ctx context.Context) {
	if c.Minutes == nil {
		return
	}
	for {
		for _, repo := range c.Config.Repositories {
			if ctx.Err() != nil {
				return
			}
			if err := c.pollMinutes(ctx, repo.Name, time.Now()); err != nil {
				log.Printf("minutes %s: %v", repo.Name, err)
			}
		}
		timer := time.NewTimer(MinutesInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// pollMinutes reads completed run attempts not yet stored. Each attempt's
// jobs are read once; listing pages are the only repeated reads.
func (c *Collector) pollMinutes(ctx context.Context, repo string, now time.Time) error {
	cursor, err := c.Store.MinutesCursor(ctx, repo)
	if err != nil {
		return err
	}
	windowStart := now.Add(-store.MinutesRetention)
	since := windowStart
	if cursor.CoveredSince > 0 && cursor.CollectedAt > 0 {
		if incremental := time.Unix(cursor.CollectedAt, 0).Add(-minutesOverlap); incremental.After(since) {
			since = incremental
		}
	}
	since = time.Date(since.UTC().Year(), since.UTC().Month(), since.UTC().Day(), 0, 0, 0, 0, time.UTC)
	fail := func(err error) error {
		problem := compactProblem(err)
		if errors.Is(err, githubapp.ErrRateBudget) {
			problem = "Coleta pausada para preservar o limite da API do GitHub."
		}
		if markErr := c.Store.MarkMinutesAttempt(ctx, repo, now, problem); markErr != nil {
			return errors.Join(err, markErr)
		}
		return err
	}
	reads := 0
	complete := true
listing:
	for page := 1; page <= minutesListPages; page++ {
		runs, err := c.Minutes.CompletedRuns(ctx, repo, since, page)
		if err != nil {
			return fail(err)
		}
		for _, run := range runs {
			attempt := run.RunAttempt
			if attempt < 1 {
				attempt = 1
			}
			scanned, err := c.Store.RunScanned(ctx, repo, run.ID, attempt)
			if err != nil {
				return fail(err)
			}
			if scanned {
				continue
			}
			if reads >= minutesJobReads {
				complete = false
				break listing
			}
			reads++
			jobs, err := c.Minutes.RunAttemptJobs(ctx, repo, run.ID, attempt)
			if err != nil {
				return fail(err)
			}
			usage := make([]store.JobUsage, 0, len(jobs))
			for _, job := range jobs {
				if job.Status != "completed" || job.StartedAt == nil || job.CompletedAt == nil || job.RunnerName == "" {
					continue // skipped or never assigned to a runner
				}
				started, completed := job.StartedAt.Unix(), job.CompletedAt.Unix()
				if completed < started || completed < windowStart.Unix() {
					continue
				}
				usage = append(usage, store.JobUsage{JobID: job.ID, Backend: RunnerBackend(job.Labels), Labels: strings.Join(job.Labels, ","), RunnerName: job.RunnerName, StartedAt: started, CompletedAt: completed})
			}
			if err := c.Store.RecordRunUsage(ctx, repo, store.RunScan{RunID: run.ID, RunAttempt: attempt, CreatedAt: run.CreatedAt.Unix()}, usage, now); err != nil {
				return fail(err)
			}
		}
		if len(runs) < 100 {
			break
		}
		if page == minutesListPages {
			complete = false // GitHub stops listing at 1000 runs
		}
	}
	if !complete {
		return c.Store.MarkMinutesAttempt(ctx, repo, now, "")
	}
	covered := cursor.CoveredSince
	if covered == 0 || since.Unix() < covered {
		covered = since.Unix()
	}
	return c.Store.MarkMinutesCollected(ctx, repo, now, covered)
}

func compactProblem(err error) string {
	line := strings.TrimSpace(strings.SplitN(err.Error(), "\n", 2)[0])
	if line == "" {
		return "GitHub indisponível"
	}
	runes := []rune(line)
	if len(runes) > 180 {
		return string(runes[:179]) + "…"
	}
	return line
}
