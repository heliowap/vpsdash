package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/heliowap/vpsdash/internal/githubapp"
)

// Job listing and log tails are read on demand while the fleet page or a log
// view is open. Nothing is persisted: logs stay in memory for one response.
const (
	jobListTTL        = 15 * time.Second
	runsPerRepository = 5
	completedRunsKept = 2
	maxListedJobs     = 60
	logPollInterval   = 5 * time.Second
	concurrentLogs    = 4
)

type jobView struct {
	Repo        string `json:"repo"`
	ID          int64  `json:"id"`
	RunID       int64  `json:"run_id"`
	Name        string `json:"name"`
	Workflow    string `json:"workflow,omitempty"`
	RunTitle    string `json:"run_title,omitempty"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion,omitempty"`
	RunnerName  string `json:"runner_name,omitempty"`
	StartedAt   int64  `json:"started_at,omitempty"`
	CompletedAt int64  `json:"completed_at,omitempty"`
	HTMLURL     string `json:"html_url,omitempty"`
}

type jobList struct {
	Jobs       []jobView         `json:"jobs"`
	Errors     map[string]string `json:"errors"`
	ObservedAt int64             `json:"observed_at"`
}

type logView struct {
	State     string `json:"state"` // ok | pending | unavailable | expired | too_large
	Text      string `json:"text"`
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated"`
	Message   string `json:"message,omitempty"`
}

func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func newJobView(repo string, job githubapp.WorkflowJob, run githubapp.WorkflowRun) jobView {
	return jobView{Repo: repo, ID: job.ID, RunID: job.RunID, Name: job.Name, Workflow: run.Name, RunTitle: run.DisplayTitle, Status: job.Status, Conclusion: job.Conclusion, RunnerName: job.RunnerName, StartedAt: unix(job.StartedAt), CompletedAt: unix(job.CompletedAt), HTMLURL: job.HTMLURL}
}

func (s *Server) inventoryRepo(name string) bool {
	for _, repo := range s.Config.Repositories {
		if repo.Name == name {
			return true
		}
	}
	return false
}

func jobRank(status string) int {
	switch status {
	case "in_progress":
		return 0
	case "completed":
		return 2
	default:
		return 1
	}
}

func (s *Server) jobs(w http.ResponseWriter, r *http.Request) {
	if s.GitHub == nil {
		errorResponse(w, 503, "GitHub App não provisionado.")
		return
	}
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	if s.jobsCache != nil && time.Since(time.Unix(s.jobsCache.ObservedAt, 0)) < jobListTTL {
		jsonResponse(w, 200, s.jobsCache)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	result := &jobList{Jobs: []jobView{}, Errors: map[string]string{}}
	for _, repo := range s.Config.Repositories {
		runs, err := s.GitHub.RecentRuns(ctx, repo.Name, runsPerRepository)
		if err != nil {
			log.Printf("recent runs %s: %v", repo.Name, err)
			result.Errors[repo.Name] = "Execuções não lidas no GitHub."
			continue
		}
		completed := 0
		for _, run := range runs {
			if run.Status == "completed" {
				if completed >= completedRunsKept {
					continue
				}
				completed++
			}
			jobs, err := s.GitHub.Jobs(ctx, repo.Name, run.ID)
			if err != nil {
				log.Printf("jobs %s run %d: %v", repo.Name, run.ID, err)
				result.Errors[repo.Name] = "Alguns jobs não foram lidos no GitHub."
				continue
			}
			for _, job := range jobs {
				if job.ID > 0 {
					result.Jobs = append(result.Jobs, newJobView(repo.Name, job, run))
				}
			}
		}
	}
	sort.SliceStable(result.Jobs, func(i, j int) bool {
		a, b := result.Jobs[i], result.Jobs[j]
		if jobRank(a.Status) != jobRank(b.Status) {
			return jobRank(a.Status) < jobRank(b.Status)
		}
		return a.StartedAt > b.StartedAt
	})
	if len(result.Jobs) > maxListedJobs {
		result.Jobs = result.Jobs[:maxListedJobs]
	}
	result.ObservedAt = time.Now().Unix()
	if len(result.Errors) == 0 {
		s.jobsCache = result
	} else {
		s.jobsCache = nil
	}
	jsonResponse(w, 200, result)
}

func (s *Server) jobLog(w http.ResponseWriter, r *http.Request) {
	repo := r.PathValue("owner") + "/" + r.PathValue("repo")
	if !s.inventoryRepo(repo) {
		errorResponse(w, 404, "Repositório fora do inventário.")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("job"), 10, 64)
	if err != nil || id <= 0 {
		errorResponse(w, 400, "Job inválido.")
		return
	}
	if s.GitHub == nil {
		errorResponse(w, 503, "GitHub App não provisionado.")
		return
	}
	select {
	case s.logSlots <- struct{}{}:
		defer func() { <-s.logSlots }()
	default:
		errorResponse(w, 429, "Muitas leituras de log ao mesmo tempo. Nova tentativa em instantes.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	job, err := s.GitHub.Job(ctx, repo, id)
	if errors.Is(err, githubapp.ErrNotFound) {
		errorResponse(w, 404, "Job não encontrado neste repositório.")
		return
	}
	if err != nil {
		log.Printf("job %s/%d: %v", repo, id, err)
		errorResponse(w, 502, "Não foi possível ler o job no GitHub.")
		return
	}
	complete := job.Status == "completed"
	view := logView{State: "ok"}
	if job.Status != "in_progress" && !complete {
		view = logView{State: "pending", Message: "Job na fila. O log aparece quando um runner assumir."}
	} else {
		tail, err := s.GitHub.JobLogTail(ctx, repo, id, githubapp.MaxLogTail)
		switch {
		case err == nil:
			view.Text, view.Size, view.Truncated = tail.Text, tail.Size, tail.Truncated
		case errors.Is(err, githubapp.ErrLogUnavailable) && !complete:
			view = logView{State: "pending", Message: "Log ainda indisponível no GitHub. Nova tentativa em instantes."}
		case errors.Is(err, githubapp.ErrLogUnavailable):
			view = logView{State: "unavailable", Message: "O GitHub não disponibilizou o log deste job."}
		case errors.Is(err, githubapp.ErrLogExpired):
			view = logView{State: "expired", Message: "Log expirado ou removido pela retenção do GitHub."}
		case errors.Is(err, githubapp.ErrLogTooLarge):
			view = logView{State: "too_large", Message: "Log grande demais para ler aqui (mais de 32 MB). Abra o job no GitHub."}
		default:
			log.Printf("job log %s/%d: %v", repo, id, err)
			errorResponse(w, 502, "Não foi possível ler o log no GitHub.")
			return
		}
	}
	// poll_after_ms 0 tells the client to stop. A log that is too large only
	// grows, so it ends polling even while the job runs.
	pollAfter := int64(0)
	if !complete && view.State != "too_large" {
		pollAfter = logPollInterval.Milliseconds()
	}
	jsonResponse(w, 200, map[string]any{"job": newJobView(repo, job, githubapp.WorkflowRun{}), "log": view, "complete": complete, "poll_after_ms": pollAfter, "observed_at": time.Now().Unix()})
}
