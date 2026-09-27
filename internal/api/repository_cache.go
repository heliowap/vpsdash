package api

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
)

type repoVariableCache struct {
	agent, ci           string
	agentKnown, ciKnown bool
	agentError, ciError string
	problem             string
	updated             time.Time
	refreshing          bool
	version             uint64
}

// repositoryVariables never waits for GitHub on the dashboard request path.
// One bounded refresh per repository updates the snapshot in the background.
func (s *Server) repositoryVariables(repo string) (agent, ci string, agentKnown, ciKnown bool, agentError, ciError, problem string) {
	if s.GitHub == nil {
		return "", "", false, false, "GitHub App não provisionado", "GitHub App não provisionado", "GitHub App não provisionado"
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
	needsObservation := state.problem == "" && (!state.agentKnown || !state.ciKnown)
	if !state.refreshing && (state.updated.IsZero() || time.Since(state.updated) >= time.Minute || needsObservation) {
		state.refreshing = true
		go s.refreshRepositoryVariables(repo, state.version)
	}
	agent, ci, agentKnown, ciKnown = state.agent, state.ci, state.agentKnown, state.ciKnown
	agentError, ciError, problem = state.agentError, state.ciError, state.problem
	if state.updated.IsZero() || needsObservation {
		problem = "GitHub em coleta"
	}
	s.repoMu.Unlock()
	return
}

func (s *Server) refreshRepositoryVariables(repo string, version uint64) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	var agent, ci string
	var agentErr, ciErr error
	var reads sync.WaitGroup
	reads.Add(2)
	go func() {
		defer reads.Done()
		agent, agentErr = s.GitHub.Variable(ctx, repo, "AGENT_RUNNER")
	}()
	go func() {
		defer reads.Done()
		ci, ciErr = s.GitHub.Variable(ctx, repo, "CI_RUNNER")
	}()
	reads.Wait()
	if agentErr != nil {
		log.Printf("repository variable %s AGENT_RUNNER: %v", repo, agentErr)
	}
	if ciErr != nil {
		log.Printf("repository variable %s CI_RUNNER: %v", repo, ciErr)
	}
	s.repoMu.Lock()
	defer s.repoMu.Unlock()
	state := s.repoCache[repo]
	if state.version == version {
		if agentErr == nil {
			state.agent, state.agentKnown, state.agentError = agent, true, ""
		} else {
			state.agentError = compactRepositoryProblem(agentErr)
		}
		if ciErr == nil {
			state.ci, state.ciKnown, state.ciError = ci, true, ""
		} else {
			state.ciError = compactRepositoryProblem(ciErr)
		}
		state.problem = firstRepositoryProblem(state.agentError, state.ciError)
		state.updated = time.Now()
	}
	state.refreshing = false
}

func firstRepositoryProblem(agentError, ciError string) string {
	if agentError != "" {
		return agentError
	}
	return ciError
}

func compactRepositoryProblem(err error) string {
	line := strings.TrimSpace(strings.SplitN(err.Error(), "\n", 2)[0])
	if line == "" {
		return "GitHub indisponível"
	}
	const limit = 180
	runes := []rune(line)
	if len(runes) > limit {
		return string(runes[:limit-1]) + "…"
	}
	return line
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
		state.agentKnown = true
		state.agentError = ""
	} else {
		state.ci = value
		state.ciKnown = true
		state.ciError = ""
	}
	state.problem = firstRepositoryProblem(state.agentError, state.ciError)
	state.updated = time.Now()
}
