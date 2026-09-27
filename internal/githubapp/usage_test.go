package githubapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func writeToken(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"token":"installation-token","expires_at":"2030-01-01T00:00:00Z"}`))
}

func TestCompletedRunsFiltersByStatusAndCreationDay(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/installations/7/access_tokens":
			writeToken(w)
		case "/repos/heliowap/vpsdash/actions/runs":
			q := r.URL.Query()
			if q.Get("status") != "completed" || q.Get("created") != ">=2026-08-28" || q.Get("page") != "2" || q.Get("per_page") != "100" {
				t.Errorf("runs query = %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"workflow_runs":[{"id":11,"run_attempt":2,"created_at":"2026-09-01T10:00:00Z"}]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	runs, err := c.CompletedRuns(context.Background(), "heliowap/vpsdash", time.Date(2026, 8, 28, 13, 0, 0, 0, time.UTC), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != 11 || runs[0].RunAttempt != 2 || runs[0].CreatedAt.IsZero() {
		t.Fatalf("runs = %+v", runs)
	}
}

func TestRunAttemptJobsReadsEveryPage(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/installations/7/access_tokens":
			writeToken(w)
		case "/repos/heliowap/vpsdash/actions/runs/11/attempts/2/jobs":
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			count := 100
			if page == 2 {
				count = 3
			}
			body := `{"total_count":103,"jobs":[`
			for i := 0; i < count; i++ {
				if i > 0 {
					body += ","
				}
				body += fmt.Sprintf(`{"id":%d,"status":"completed","labels":["self-hosted"],"runner_name":"r","started_at":"2026-09-01T10:00:00Z","completed_at":"2026-09-01T10:01:30Z"}`, page*1000+i)
			}
			_, _ = w.Write([]byte(body + "]}"))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	jobs, err := c.RunAttemptJobs(context.Background(), "heliowap/vpsdash", 11, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 103 || jobs[0].StartedAt == nil || jobs[0].CompletedAt.Sub(*jobs[0].StartedAt) != 90*time.Second {
		t.Fatalf("jobs = %d %+v", len(jobs), jobs[0])
	}
}

func TestUsageReadsKeepTheFleetReserve(t *testing.T) {
	calls := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/installations/7/access_tokens" {
			writeToken(w)
			return
		}
		calls++
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(ReservedRequests-1))
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
		_, _ = w.Write([]byte(`{"workflow_runs":[]}`))
	})
	ctx := context.Background()
	if _, err := c.CompletedRuns(ctx, "heliowap/vpsdash", time.Now(), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CompletedRuns(ctx, "heliowap/vpsdash", time.Now(), 1); !errors.Is(err, ErrRateBudget) {
		t.Fatalf("second read error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("GitHub was called %d times after the budget dropped below the reserve", calls)
	}
}

func TestUsageReadReportsRateLimitResponse(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/installations/7/access_tokens" {
			writeToken(w)
			return
		}
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
		http.Error(w, `{"message":"API rate limit exceeded"}`, http.StatusForbidden)
	})
	if _, err := c.RunAttemptJobs(context.Background(), "heliowap/vpsdash", 1, 1); !errors.Is(err, ErrRateBudget) {
		t.Fatalf("error = %v", err)
	}
}
