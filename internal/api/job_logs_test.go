package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heliowap/vpsdash/internal/auth"
	"github.com/heliowap/vpsdash/internal/config"
	"github.com/heliowap/vpsdash/internal/githubapp"
)

// noJobLogs completes the GitHub interface for fakes that only exercise
// runner variables.
type noJobLogs struct{}

func (noJobLogs) RecentRuns(context.Context, string, int) ([]githubapp.WorkflowRun, error) {
	return nil, errors.New("not used")
}
func (noJobLogs) Jobs(context.Context, string, int64) ([]githubapp.WorkflowJob, error) {
	return nil, errors.New("not used")
}
func (noJobLogs) Job(context.Context, string, int64) (githubapp.WorkflowJob, error) {
	return githubapp.WorkflowJob{}, errors.New("not used")
}
func (noJobLogs) JobLogTail(context.Context, string, int64, int) (githubapp.LogTail, error) {
	return githubapp.LogTail{}, errors.New("not used")
}

// fakeActions is a GitHub REST fake for runs, jobs and job logs. Logs redirect
// to a separate signed-URL host, as GitHub does.
type fakeActions struct {
	mu         sync.Mutex
	calls      []string
	jobStatus  map[int64]string
	logStatus  map[int64]int
	logs       map[int64]string
	blobAuth   []string
	blobServer *httptest.Server
}

func (f *fakeActions) record(path string) {
	f.mu.Lock()
	f.calls = append(f.calls, path)
	f.mu.Unlock()
}

func (f *fakeActions) callCount(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, call := range f.calls {
		if strings.HasPrefix(call, prefix) {
			n++
		}
	}
	return n
}

func (f *fakeActions) serveAPI(w http.ResponseWriter, r *http.Request) {
	f.record(r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	var id, run int64
	switch {
	case r.URL.Path == "/app/installations/7/access_tokens":
		_, _ = w.Write([]byte(`{"token":"installation-token","expires_at":"2030-01-01T00:00:00Z"}`))
	case r.URL.Path == "/repos/heliowap/vpsdash/actions/runs":
		_, _ = w.Write([]byte(`{"workflow_runs":[
			{"id":1,"name":"CI","display_title":"feat: logs","status":"completed","conclusion":"success"},
			{"id":2,"name":"CI","display_title":"fix: fila","status":"in_progress"},
			{"id":3,"name":"CI","display_title":"antigo","status":"completed","conclusion":"failure"},
			{"id":4,"name":"CI","display_title":"mais antigo","status":"completed","conclusion":"success"}]}`))
	case sscanf(r.URL.Path, "/repos/heliowap/vpsdash/actions/runs/%d/jobs", &run):
		status, started := "completed", "2026-09-27T09:00:00Z"
		if run == 2 {
			status, started = "in_progress", "2026-09-27T08:00:00Z"
		}
		fmt.Fprintf(w, `{"jobs":[{"id":%d,"run_id":%d,"name":"build","status":%q,"started_at":%q}]}`, run*10, run, status, started)
	case sscanf(r.URL.Path, "/repos/heliowap/vpsdash/actions/jobs/%d/logs", &id):
		if status := f.logStatus[id]; status != 0 {
			w.WriteHeader(status)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("%s/blob/%d?sig=signed", f.blobServer.URL, id), http.StatusFound)
	case sscanf(r.URL.Path, "/repos/heliowap/vpsdash/actions/jobs/%d", &id):
		status, ok := f.jobStatus[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		fmt.Fprintf(w, `{"id":%d,"run_id":2,"name":"build","status":%q,"runner_name":"vps-1","html_url":"https://github.com/heliowap/vpsdash/actions/runs/2/job/%d"}`, id, status, id)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeActions) serveBlob(w http.ResponseWriter, r *http.Request) {
	var id int64
	sscanf(r.URL.Path, "/blob/%d", &id)
	f.mu.Lock()
	f.blobAuth = append(f.blobAuth, r.Header.Get("Authorization"))
	f.mu.Unlock()
	_, _ = w.Write([]byte(f.logs[id]))
}

// sscanf matches a path against a format with one %d segment.
func sscanf(path, format string, id *int64) bool {
	prefix, suffix, _ := strings.Cut(format, "%d")
	middle, ok := strings.CutPrefix(path, prefix)
	if !ok {
		return false
	}
	middle, ok = strings.CutSuffix(middle, suffix)
	if !ok {
		return false
	}
	value, err := strconv.ParseInt(middle, 10, 64)
	*id = value
	return err == nil
}

func jobLogFixture(t *testing.T) (http.Handler, *fakeActions, *http.Cookie) {
	t.Helper()
	fake := &fakeActions{jobStatus: map[int64]string{}, logStatus: map[int64]int{}, logs: map[int64]string{}}
	api := httptest.NewServer(http.HandlerFunc(fake.serveAPI))
	t.Cleanup(api.Close)
	fake.blobServer = httptest.NewServer(http.HandlerFunc(fake.serveBlob))
	t.Cleanup(fake.blobServer.Close)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	client, err := githubapp.New(42, pemKey, map[string]int64{"heliowap": 7, "other": 8}, api.URL, api.Client())
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.New("$argon2id$unused", []byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	issued := httptest.NewRecorder()
	a.Issue(issued, time.Now())
	cookies := issued.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("no session cookie issued")
	}
	cfg := config.Config{Repositories: []config.Repository{{Name: "heliowap/vpsdash"}}}
	return New(cfg, nil, nil, client, a).Handler(), fake, cookies[0]
}

func getJSON(t *testing.T, h http.Handler, path string, cookie *http.Cookie, out any) int {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if out != nil && w.Code == 200 {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
	return w.Code
}

type logResponse struct {
	Job struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
	} `json:"job"`
	Log struct {
		State     string `json:"state"`
		Text      string `json:"text"`
		Size      int64  `json:"size"`
		Truncated bool   `json:"truncated"`
		Message   string `json:"message"`
	} `json:"log"`
	Complete    bool  `json:"complete"`
	PollAfterMS int64 `json:"poll_after_ms"`
}

func TestJobLogTailsInProgressJobAsPlainText(t *testing.T) {
	h, fake, cookie := jobLogFixture(t)
	fake.jobStatus[20] = "in_progress"
	fake.logs[20] = "\uFEFF2026-09-27T10:00:00.0000000Z \x1b[1;32mcompilando\x1b[0m <img src=x onerror=alert(1)>\n"
	if got := getJSON(t, h, "/api/repos/heliowap/vpsdash/jobs/20/log", nil, nil); got != 401 {
		t.Fatalf("unauthenticated log = %d", got)
	}
	var body logResponse
	if got := getJSON(t, h, "/api/repos/heliowap/vpsdash/jobs/20/log", cookie, &body); got != 200 {
		t.Fatalf("log = %d", got)
	}
	if body.Log.State != "ok" || body.Complete || body.PollAfterMS != 5000 {
		t.Fatalf("response = %+v", body)
	}
	if body.Log.Text != "2026-09-27T10:00:00.0000000Z compilando <img src=x onerror=alert(1)>\n" {
		t.Fatalf("text = %q", body.Log.Text)
	}
	if len(fake.blobAuth) != 1 || fake.blobAuth[0] != "" {
		t.Fatalf("signed URL host received Authorization: %q", fake.blobAuth)
	}
}

func TestJobLogStatesAreExplicit(t *testing.T) {
	h, fake, cookie := jobLogFixture(t)
	fake.jobStatus[30] = "in_progress"
	fake.logStatus[30] = http.StatusNotFound
	fake.jobStatus[31] = "completed"
	fake.logStatus[31] = http.StatusGone
	fake.jobStatus[32] = "queued"
	fake.jobStatus[33] = "completed"
	fake.logs[33] = "fim\n"
	cases := []struct {
		id       int64
		state    string
		complete bool
	}{{30, "pending", false}, {31, "expired", true}, {32, "pending", false}, {33, "ok", true}}
	for _, tc := range cases {
		var body logResponse
		if got := getJSON(t, h, fmt.Sprintf("/api/repos/heliowap/vpsdash/jobs/%d/log", tc.id), cookie, &body); got != 200 {
			t.Fatalf("job %d = %d", tc.id, got)
		}
		if body.Log.State != tc.state || body.Complete != tc.complete || (tc.state != "ok" && body.Log.Message == "") {
			t.Fatalf("job %d = %+v", tc.id, body)
		}
		if tc.complete != (body.PollAfterMS == 0) {
			t.Fatalf("job %d poll = %d", tc.id, body.PollAfterMS)
		}
	}
	if n := fake.callCount("/repos/heliowap/vpsdash/actions/jobs/32/logs"); n != 0 {
		t.Fatalf("queued job log requested %d times", n)
	}
}

func TestJobLogRejectsJobsOutsideInventory(t *testing.T) {
	h, fake, cookie := jobLogFixture(t)
	for path, want := range map[string]int{
		"/api/repos/other/private/jobs/20/log":      404,
		"/api/repos/heliowap/vpsdash/jobs/abc/log":  400,
		"/api/repos/heliowap/vpsdash/jobs/-1/log":   400,
		"/api/repos/heliowap/vpsdash/jobs/0/log":    400,
		"/api/repos/heliowap/vpsdash/jobs/9999/log": 404, // not a job of this repository
	} {
		if got := getJSON(t, h, path, cookie, nil); got != want {
			t.Fatalf("%s = %d, want %d", path, got, want)
		}
	}
	if n := fake.callCount("/repos/other/"); n != 0 {
		t.Fatalf("repository outside inventory reached GitHub %d times", n)
	}
	if n := fake.callCount("/repos/heliowap/vpsdash/actions/jobs/9999/logs"); n != 0 {
		t.Fatal("log requested for unknown job")
	}
}

func TestJobListOrdersActiveFirstAndBoundsCompletedRuns(t *testing.T) {
	h, fake, cookie := jobLogFixture(t)
	var list struct {
		Jobs []struct {
			ID       int64  `json:"id"`
			Status   string `json:"status"`
			RunTitle string `json:"run_title"`
		} `json:"jobs"`
		Errors map[string]string `json:"errors"`
	}
	if got := getJSON(t, h, "/api/jobs", cookie, &list); got != 200 {
		t.Fatalf("jobs = %d", got)
	}
	if len(list.Errors) != 0 || len(list.Jobs) != 3 {
		t.Fatalf("list = %+v", list)
	}
	if list.Jobs[0].ID != 20 || list.Jobs[0].Status != "in_progress" || list.Jobs[0].RunTitle != "fix: fila" {
		t.Fatalf("active job not first: %+v", list.Jobs)
	}
	if n := fake.callCount("/repos/heliowap/vpsdash/actions/runs/4/jobs"); n != 0 {
		t.Fatal("third completed run was read")
	}
	before := fake.callCount("/repos/")
	if got := getJSON(t, h, "/api/jobs", cookie, &list); got != 200 {
		t.Fatalf("cached jobs = %d", got)
	}
	if after := fake.callCount("/repos/"); after != before {
		t.Fatalf("job list not cached: %d GitHub calls", after-before)
	}
}
