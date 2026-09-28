package collect

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heliowap/vpsdash/internal/config"
	"github.com/heliowap/vpsdash/internal/githubapp"
	"github.com/heliowap/vpsdash/internal/store"
)

type fakeActions struct {
	mu        sync.Mutex
	runs      string
	jobs      map[string]string
	jobReads  map[string]int
	listReads int
	created   []string
	remaining int
}

func (f *fakeActions) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/app/installations/7/access_tokens" {
		_, _ = w.Write([]byte(`{"token":"installation-token","expires_at":"2030-01-01T00:00:00Z"}`))
		return
	}
	if f.remaining > 0 {
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(f.remaining))
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
	}
	if r.URL.Path == "/repos/heliowap/vpsdash/actions/runs" {
		f.listReads++
		f.created = append(f.created, r.URL.Query().Get("created"))
		_, _ = w.Write([]byte(f.runs))
		return
	}
	if strings.HasPrefix(r.URL.Path, "/repos/heliowap/vpsdash/actions/runs/") {
		key := strings.TrimPrefix(r.URL.Path, "/repos/heliowap/vpsdash/actions/runs/")
		if body, ok := f.jobs[key]; ok {
			f.jobReads[key]++
			_, _ = w.Write([]byte(body))
			return
		}
	}
	http.NotFound(w, r)
}

func minutesCollector(t *testing.T, fake *fakeActions) (*Collector, *store.Store) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	client, err := githubapp.New(42, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), map[string]int64{"heliowap": 7}, server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(filepath.Join(t.TempDir(), "vpsdash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	c := New(config.Config{Repositories: []config.Repository{{Name: "heliowap/vpsdash"}}}, s, client)
	t.Cleanup(c.Close)
	if c.Minutes == nil {
		t.Fatal("GitHub App client was not used for minutes")
	}
	return c, s
}

func job(id int, labels, runner, started, completed, status string) string {
	return `{"id":` + strconv.Itoa(id) + `,"status":"` + status + `","labels":[` + labels + `],"runner_name":"` + runner + `","started_at":"` + started + `","completed_at":"` + completed + `"}`
}

func TestMinutesCollectorReadsEachRunAttemptOnce(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	fake := &fakeActions{
		runs: `{"workflow_runs":[{"id":2,"run_attempt":1,"created_at":"2026-09-26T10:00:00Z"},{"id":1,"run_attempt":1,"created_at":"2026-09-10T10:00:00Z"}]}`,
		jobs: map[string]string{
			"2/attempts/1/jobs": `{"total_count":3,"jobs":[` +
				job(21, `"self-hosted","linux"`, "vps-1", "2026-09-26T10:00:00Z", "2026-09-26T10:04:01Z", "completed") + `,` +
				job(22, `"depot-ubuntu-24.04-4"`, "depot-abc", "2026-09-26T10:00:00Z", "2026-09-26T10:00:30Z", "completed") + `,` +
				`{"id":23,"status":"completed","conclusion":"skipped","labels":["ubuntu-latest"],"runner_name":null,"started_at":"2026-09-26T10:00:00Z","completed_at":"2026-09-26T10:00:00Z"}]}`,
			"1/attempts/1/jobs": `{"total_count":1,"jobs":[` + job(11, `"ubuntu-latest"`, "GitHub Actions 3", "2026-09-10T10:00:00Z", "2026-09-10T10:10:00Z", "completed") + `]}`,
			"2/attempts/2/jobs": `{"total_count":1,"jobs":[` + job(24, `"ubicloud-standard-2"`, "ubicloud-x", "2026-09-26T11:00:00Z", "2026-09-26T11:02:00Z", "completed") + `]}`,
		},
		jobReads: map[string]int{},
	}
	c, s := minutesCollector(t, fake)
	ctx := context.Background()
	if err := c.pollMinutes(ctx, "heliowap/vpsdash", now); err != nil {
		t.Fatal(err)
	}
	if fake.created[0] != ">=2026-08-28" {
		t.Fatalf("first pass listed from %s, want the 30-day window", fake.created[0])
	}
	// The second pass lists again but must not read jobs of stored attempts;
	// a re-run adds a new attempt, which is read once.
	fake.runs = `{"workflow_runs":[{"id":2,"run_attempt":2,"created_at":"2026-09-26T10:00:00Z"},{"id":1,"run_attempt":1,"created_at":"2026-09-10T10:00:00Z"}]}`
	later := now.Add(10 * time.Minute)
	if err := c.pollMinutes(ctx, "heliowap/vpsdash", later); err != nil {
		t.Fatal(err)
	}
	if fake.created[1] != ">=2026-09-24" {
		t.Fatalf("incremental pass listed from %s", fake.created[1])
	}
	for key, count := range fake.jobReads {
		if count != 1 {
			t.Fatalf("jobs of %s read %d times", key, count)
		}
	}
	usage, err := s.BackendMinutes(ctx, later)
	if err != nil {
		t.Fatal(err)
	}
	repo := usage["heliowap/vpsdash"]
	if repo.CollectedAt != later.Unix() || repo.CoveredSince != time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC).Unix() || repo.Error != "" {
		t.Fatalf("cursor = %+v", repo)
	}
	got := map[string]store.BackendUsage{}
	for _, b := range repo.Backends {
		got[b.Backend] = b
	}
	want := map[string][4]int64{
		"self-hosted":   {1, 5, 1, 5}, // 4m01s rounds up to 5
		"depot-*":       {1, 1, 1, 1},
		"ubicloud-*":    {1, 2, 1, 2},
		"ubuntu-latest": {0, 0, 1, 10}, // older than 7 days
	}
	if len(got) != len(want) {
		t.Fatalf("backends = %+v", repo.Backends)
	}
	for backend, w := range want {
		b := got[backend]
		if b.Jobs7d != w[0] || b.Minutes7d != w[1] || b.Jobs30d != w[2] || b.Minutes30d != w[3] {
			t.Fatalf("%s = %+v, want %v", backend, b, w)
		}
	}
}

func TestMinutesCollectorKeepsLastCollectionWhenRateBudgetIsLow(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	fake := &fakeActions{runs: `{"workflow_runs":[]}`, jobs: map[string]string{}, jobReads: map[string]int{}}
	c, s := minutesCollector(t, fake)
	ctx := context.Background()
	if err := c.pollMinutes(ctx, "heliowap/vpsdash", now); err != nil {
		t.Fatal(err)
	}
	fake.remaining = githubapp.ReservedRequests - 1
	if err := c.pollMinutes(ctx, "heliowap/vpsdash", now.Add(10*time.Minute)); err != nil {
		t.Fatal(err) // the listing itself succeeds and records the low budget
	}
	later := now.Add(20 * time.Minute)
	if err := c.pollMinutes(ctx, "heliowap/vpsdash", later); err == nil {
		t.Fatal("collection continued below the reserved budget")
	}
	if fake.listReads != 2 {
		t.Fatalf("list reads = %d", fake.listReads)
	}
	cursor, err := s.MinutesCursor(ctx, "heliowap/vpsdash")
	if err != nil {
		t.Fatal(err)
	}
	if cursor.CollectedAt != now.Add(10*time.Minute).Unix() || cursor.AttemptedAt != later.Unix() || !strings.Contains(cursor.Error, "limite da API") {
		t.Fatalf("cursor = %+v", cursor)
	}
}

func TestRunnerBackendGroupsLabels(t *testing.T) {
	cases := map[string][]string{
		"self-hosted":   {"self-hosted", "linux", "x64"},
		"ubuntu-latest": {"ubuntu-latest"},
		"depot-*":       {"depot-ubuntu-24.04-8"},
		"ubicloud-*":    {"ubicloud-standard-2"},
		"outros":        {"macos-latest"},
	}
	for want, labels := range cases {
		if got := RunnerBackend(labels); got != want {
			t.Fatalf("RunnerBackend(%v) = %s, want %s", labels, got, want)
		}
	}
}
