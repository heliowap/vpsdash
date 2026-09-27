package api

import (
	"context"
	"time"
)

type repoVariableCache struct {
	agent, ci  string
	problem    string
	updated    time.Time
	refreshing bool
	version    uint64
}

// repositoryVariables never waits for GitHub on the dashboard request path.
// One bounded refresh per repository updates the snapshot in the background.
func (s *Server) repositoryVariables(repo string) (agent, ci, problem string) {
	if s.GitHub == nil {
		return "", "", "GitHub App não provisionado"
	}
	s.repoMu.Lock()
	if s.repoCache == nil {
		s.repoCache = map[string]*repoVariableCache{}
	}
	state := s.repoCache[repo]
	if state == nil {
		state = &repoVariableCache{}
		s.repoCache[repo] = state
	}
	if !state.refreshing && (state.updated.IsZero() || time.Since(state.updated) >= time.Minute) {
		state.refreshing = true
		go s.refreshRepositoryVariables(repo, state.version)
	}
	agent, ci, problem = state.agent, state.ci, state.problem
	if state.updated.IsZero() {
		problem = "GitHub em coleta"
	}
	s.repoMu.Unlock()
	return
}

func (s *Server) refreshRepositoryVariables(repo string, version uint64) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	agent, err := s.GitHub.Variable(ctx, repo, "AGENT_RUNNER")
	ci := ""
	if err == nil {
		ci, err = s.GitHub.Variable(ctx, repo, "CI_RUNNER")
	}
	s.repoMu.Lock()
	defer s.repoMu.Unlock()
	state := s.repoCache[repo]
	if state.version == version {
		if err == nil {
			state.agent, state.ci, state.problem = agent, ci, ""
		} else {
			state.problem = "GitHub indisponível"
		}
		state.updated = time.Now()
	}
	state.refreshing = false
}

func (s *Server) noteVariableSet(repo, variable, value string) {
	s.repoMu.Lock()
	defer s.repoMu.Unlock()
	if s.repoCache == nil {
		s.repoCache = map[string]*repoVariableCache{}
	}
	state := s.repoCache[repo]
	if state == nil {
		state = &repoVariableCache{}
		s.repoCache[repo] = state
	}
	state.version++ // Discard a read that started before the successful write.
	if variable == "AGENT_RUNNER" {
		state.agent = value
	} else {
		state.ci = value
	}
	if state.problem == "" && !state.updated.IsZero() {
		state.updated = time.Now()
	} else {
		state.updated = time.Time{}
	}
}
