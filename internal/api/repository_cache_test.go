package api

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/heliowap/vpsdash/internal/config"
	"github.com/heliowap/vpsdash/internal/store"
)

type slowVariableAPI struct {
	started sync.Once
	ready   chan struct{}
	release chan struct{}
}

func (g *slowVariableAPI) Variable(ctx context.Context, _, name string) (string, error) {
	g.started.Do(func() { close(g.ready) })
	select {
	case <-g.release:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if name == "AGENT_RUNNER" {
		return "ubuntu-latest", nil
	}
	return "self-hosted", nil
}
func (g *slowVariableAPI) SetVariable(context.Context, string, string, string) error { return nil }

func TestDashboardDoesNotWaitForGitHubVariables(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "vpsdash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	gh := &slowVariableAPI{ready: make(chan struct{}), release: make(chan struct{})}
	s := &Server{Config: config.Config{Repositories: []config.Repository{{Name: "heliowap/vpsdash"}}}, Store: st, GitHub: gh}
	done := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		s.dashboard(w, httptest.NewRequest("GET", "/api/dashboard", nil))
		done <- w.Code
	}()
	select {
	case status := <-done:
		if status != 200 {
			t.Fatalf("dashboard status = %d", status)
		}
	case <-time.After(time.Second):
		close(gh.release)
		t.Fatal("dashboard waited for GitHub")
	}
	select {
	case <-gh.ready:
	case <-time.After(time.Second):
		close(gh.release)
		t.Fatal("background variable refresh did not start")
	}
	close(gh.release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		agent, ci, problem := s.repositoryVariables("heliowap/vpsdash")
		if problem == "" {
			if agent != "ubuntu-latest" || ci != "self-hosted" {
				t.Fatalf("cached variables = %q, %q", agent, ci)
			}
			s.noteVariableSet("heliowap/vpsdash", "AGENT_RUNNER", "depot-ubuntu-24.04-4")
			agent, _, _ = s.repositoryVariables("heliowap/vpsdash")
			if agent != "depot-ubuntu-24.04-4" {
				t.Fatalf("successful switch was not visible: %q", agent)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("variable cache did not refresh")
}
