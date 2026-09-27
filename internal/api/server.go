package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/heliowap/vpsdash/internal/auth"
	"github.com/heliowap/vpsdash/internal/collect"
	"github.com/heliowap/vpsdash/internal/config"
	"github.com/heliowap/vpsdash/internal/githubapp"
	"github.com/heliowap/vpsdash/internal/interactive"
	"github.com/heliowap/vpsdash/internal/store"
	"github.com/heliowap/vpsdash/internal/webpush"
)

type GitHub interface {
	Variable(context.Context, string, string) (string, error)
	SetVariable(context.Context, string, string, string) error
	RecentRuns(context.Context, string, int) ([]githubapp.WorkflowRun, error)
	Jobs(context.Context, string, int64) ([]githubapp.WorkflowJob, error)
	Job(context.Context, string, int64) (githubapp.WorkflowJob, error)
	JobLogTail(context.Context, string, int64, int) (githubapp.LogTail, error)
}

type Server struct {
	Config          config.Config
	Store           *store.Store
	Collector       *collect.Collector
	GitHub          GitHub
	Auth            *auth.Authenticator
	Static          http.FileSystem
	SMTPProvisioned bool
	Files           FileBrowser
	// Push is nil until webpush.env holds a VAPID key pair.
	Push           *webpush.Service
	loginMu        sync.Mutex
	loginAttempts  map[string]*loginAttempts
	globalFailures []time.Time
	globalPending  int
	lastLoginPrune time.Time
	loginSlots     chan struct{}
	verifyPassword func(string) bool
	repoMu         sync.Mutex
	repoCache      map[string]*repoVariableCache
	staticMu       sync.RWMutex
	staticCache    map[string]staticAsset
	jobsMu         sync.Mutex
	jobsCache      *jobList
	logSlots       chan struct{}
	// Interactive opens terminal, attach, and snippet SSH sessions with the
	// per-host interactive key. Nil disables those features.
	Interactive *interactive.Dialer
	interactiveState
}

type staticAsset struct {
	data []byte
	etag string
}

type loginAttempts struct {
	failures []time.Time
	pending  int
}

func New(cfg config.Config, s *store.Store, c *collect.Collector, gh GitHub, a *auth.Authenticator) *Server {
	return &Server{Config: cfg, Store: s, Collector: c, GitHub: gh, Auth: a, loginAttempts: map[string]*loginAttempts{}, loginSlots: make(chan struct{}, 2), logSlots: make(chan struct{}, concurrentLogs), verifyPassword: a.VerifyPassword, repoCache: map[string]*repoVariableCache{}}
}

// Handler serves the public listener: login, dashboard, and read-only
// operations. Terminal, attach, and snippet routes are never registered here,
// so they answer 404 even with a valid session.
func (s *Server) Handler() http.Handler { return s.handler(false) }

// PrivateHandler serves the private listener that only Tailscale Serve
// reaches. It adds the interactive routes to everything Handler serves.
func (s *Server) PrivateHandler() http.Handler { return s.handler(true) }

func (s *Server) handler(privateRoute bool) http.Handler {
	root := http.NewServeMux()
	root.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	root.HandleFunc("GET /api/session", func(w http.ResponseWriter, r *http.Request) { s.session(w, r, privateRoute) })
	root.HandleFunc("POST /api/login", s.login)
	authenticated := http.NewServeMux()
	authenticated.HandleFunc("GET /api/dashboard", func(w http.ResponseWriter, r *http.Request) { s.dashboard(w, r, privateRoute) })
	authenticated.HandleFunc("GET /api/hosts/{id}/metrics", s.metrics)
	authenticated.HandleFunc("GET /api/audit", s.audit)
	authenticated.HandleFunc("PATCH /api/projects/{id}", s.updateProject)
	authenticated.HandleFunc("POST /api/repos/{owner}/{repo}/switch", s.switchRunner)
	authenticated.HandleFunc("POST /api/switches/bulk", s.bulkSwitch)
	authenticated.HandleFunc("POST /api/presets/{name}", s.preset)
	authenticated.HandleFunc("GET /api/minutes", s.minutes)
	authenticated.HandleFunc("GET /api/hosts/{id}/files", s.listFiles)
	authenticated.HandleFunc("GET /api/hosts/{id}/file", s.readFile)
	authenticated.HandleFunc("GET /api/projects/{id}/incidents", s.projectIncidents)
	authenticated.HandleFunc("GET /api/jobs", s.jobs)
	authenticated.HandleFunc("GET /api/repos/{owner}/{repo}/jobs/{job}/log", s.jobLog)
	authenticated.HandleFunc("POST /api/runner-units/{host}/{unit}/restart", s.runnerUnitOp("restart"))
	authenticated.HandleFunc("POST /api/runner-units/{host}/{unit}/drain", s.runnerUnitOp("drain"))
	authenticated.HandleFunc("POST /api/runner-units/{host}/{unit}/drain/cancel", s.cancelDrain)
	authenticated.HandleFunc("GET /api/push", s.pushStatus)
	authenticated.HandleFunc("POST /api/push/subscriptions", s.pushSubscribe)
	authenticated.HandleFunc("DELETE /api/push/subscriptions", s.pushUnsubscribe)
	authenticated.HandleFunc("POST /api/push/test", s.pushTest)
	authenticated.HandleFunc("POST /api/logout", s.logout)
	if privateRoute {
		s.registerInteractive(authenticated)
	}
	root.Handle("/api", s.authorize(authenticated))
	root.Handle("/api/", s.authorize(authenticated))
	root.HandleFunc("/", s.static)
	return securityHeaders(root, privateRoute)
}

func securityHeaders(next http.Handler, privateRoute bool) http.Handler {
	// xterm.js writes its measured cell sizes and theme into <style>
	// elements. Only the private listener, which serves the terminal, allows
	// them; the public listener keeps style-src 'self'.
	styleSource := "'self'"
	if privateRoute {
		styleSource = "'self' 'unsafe-inline'"
	}
	csp := "default-src 'self'; script-src 'self'; style-src " + styleSource + "; connect-src 'self'; img-src 'self' data:; font-src 'self'; frame-ancestors 'none'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", csp)
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func jsonResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func errorResponse(w http.ResponseWriter, status int, message string) {
	jsonResponse(w, status, map[string]string{"error": message})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, value any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return err
		}
		return errors.New("multiple JSON values")
	}
	return nil
}

func (s *Server) session(w http.ResponseWriter, r *http.Request, privateRoute bool) {
	csrf, ok := s.Auth.Validate(r, time.Now())
	// private reports which listener received the request. It comes from the
	// handler that was mounted, never from request headers.
	jsonResponse(w, http.StatusOK, map[string]any{"authenticated": ok, "csrf": csrf, "private": privateRoute})
}

func (s *Server) reserveLogin(ip string, now time.Time) bool {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	cutoff := now.Add(-5 * time.Minute)
	recentGlobal := s.globalFailures[:0]
	for _, t := range s.globalFailures {
		if t.After(cutoff) {
			recentGlobal = append(recentGlobal, t)
		}
	}
	s.globalFailures = recentGlobal
	if len(recentGlobal)+s.globalPending >= 20 {
		return false
	}
	if now.Sub(s.lastLoginPrune) >= time.Minute {
		for key, attempts := range s.loginAttempts {
			recent := attempts.failures[:0]
			for _, t := range attempts.failures {
				if t.After(cutoff) {
					recent = append(recent, t)
				}
			}
			attempts.failures = recent
			if attempts.pending == 0 && len(recent) == 0 {
				delete(s.loginAttempts, key)
			}
		}
		s.lastLoginPrune = now
	}
	attempts := s.loginAttempts[ip]
	if attempts == nil {
		attempts = &loginAttempts{}
		s.loginAttempts[ip] = attempts
	}
	recent := attempts.failures[:0]
	for _, t := range attempts.failures {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	attempts.failures = recent
	if len(recent)+attempts.pending >= 5 {
		return false
	}
	attempts.pending++
	s.globalPending++
	return true
}

func (s *Server) finishLogin(ip string, success bool, now time.Time) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	attempts := s.loginAttempts[ip]
	if attempts == nil {
		// Release the global reservation if the client entry was lost.
		if s.globalPending > 0 {
			s.globalPending--
		}
		if !success {
			s.globalFailures = append(s.globalFailures, now)
		}
		return
	}
	attempts.pending--
	s.globalPending--
	if success {
		attempts.failures = nil
	} else {
		attempts.failures = append(attempts.failures, now)
		s.globalFailures = append(s.globalFailures, now)
	}
	if attempts.pending == 0 && len(attempts.failures) == 0 {
		delete(s.loginAttempts, ip)
	}
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		errorResponse(w, http.StatusBadRequest, "Senha inválida.")
		return
	}
	select {
	case s.loginSlots <- struct{}{}:
		defer func() { <-s.loginSlots }()
	default:
		errorResponse(w, http.StatusTooManyRequests, "Muitas tentativas. Tente novamente em instantes.")
		return
	}
	ip := s.loginClientIP(r)
	if !s.reserveLogin(ip, time.Now()) {
		errorResponse(w, http.StatusTooManyRequests, "Muitas tentativas. Tente novamente em cinco minutos.")
		return
	}
	verified := s.verifyPassword(body.Password)
	s.finishLogin(ip, verified, time.Now())
	if !verified {
		errorResponse(w, http.StatusUnauthorized, "Senha incorreta.")
		return
	}
	csrf := s.Auth.Issue(w, time.Now())
	if csrf == "" {
		errorResponse(w, http.StatusInternalServerError, "Não foi possível iniciar a sessão.")
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"authenticated": true, "csrf": csrf})
}

var tailscaleIPv4 = netip.MustParsePrefix("100.64.0.0/10")
var tailscaleIPv6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")

func (s *Server) loginClientIP(r *http.Request) string {
	ip := r.RemoteAddr
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	remote := net.ParseIP(ip)
	if remote == nil || !remote.IsLoopback() {
		return ip
	}
	// Serve supplies the tailnet source in X-Forwarded-For. Caddy must replace
	// both forwarding headers with the observed public source (see README).
	// Require the two headers to agree before trusting X-Real-IP, so a Serve
	// request cannot choose its own rate-limit key even without a serve host.
	values := r.Header.Values("X-Forwarded-For")
	forwardedValue := ""
	if len(values) > 0 {
		forwardedValue = values[len(values)-1]
	}
	if last := strings.LastIndexByte(forwardedValue, ','); last >= 0 {
		forwardedValue = forwardedValue[last+1:]
	}
	forwarded, err := netip.ParseAddr(strings.TrimSpace(forwardedValue))
	if err == nil {
		forwarded = forwarded.Unmap()
		if tailscaleIPv4.Contains(forwarded) || tailscaleIPv6.Contains(forwarded) {
			return forwarded.String()
		}
	}
	if s.Config.TrustProxyHeader {
		if realIP, realErr := netip.ParseAddr(strings.TrimSpace(r.Header.Get("X-Real-IP"))); realErr == nil && err == nil {
			realIP = realIP.Unmap()
			if realIP == forwarded {
				return realIP.String()
			}
		}
	}
	return ip
}

func (s *Server) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		csrf, ok := s.Auth.Validate(r, time.Now())
		if !ok {
			errorResponse(w, http.StatusUnauthorized, "Sessão expirada. Entre novamente.")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && !auth.ValidCSRF(csrf, r.Header.Get("X-CSRF-Token")) {
			errorResponse(w, http.StatusForbidden, "Recarregue a página e tente novamente.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if sid, ok := s.Auth.SessionID(r, time.Now()); ok {
		s.clearStepUp(sid)
	}
	auth.Clear(w)
	jsonResponse(w, http.StatusOK, map[string]bool{"authenticated": false})
}

type repoView struct {
	Name            string `json:"name"`
	AgentSwitchable bool   `json:"agent_switchable"`
	CISwitchable    bool   `json:"ci_switchable"`
	AgentRunner     string `json:"agent_runner"`
	CIRunner        string `json:"ci_runner"`
	AgentKnown      bool   `json:"agent_known"`
	CIKnown         bool   `json:"ci_known"`
	AgentError      string `json:"agent_error,omitempty"`
	CIError         string `json:"ci_error,omitempty"`
	Error           string `json:"error,omitempty"`
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request, privateRoute bool) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	hosts, err := s.Store.Hosts(ctx)
	if err != nil {
		errorResponse(w, 500, "Não foi possível ler os hosts.")
		return
	}
	projects, err := s.Store.Projects(ctx)
	if err != nil {
		errorResponse(w, 500, "Não foi possível ler os projetos.")
		return
	}
	runners, err := s.Store.Runners(ctx)
	if err != nil {
		errorResponse(w, 500, "Não foi possível ler os runners.")
		return
	}
	sessions, err := s.Store.Sessions(ctx)
	if err != nil {
		errorResponse(w, 500, "Não foi possível ler as sessões.")
		return
	}
	queued := map[string][]githubapp.WorkflowRun{}
	errors := map[string]string{}
	var fleetAt time.Time
	if s.Collector != nil {
		queued, errors, fleetAt = s.Collector.Snapshot()
	}
	repos := make([]repoView, 0, len(s.Config.Repositories))
	for _, cfg := range s.Config.Repositories {
		view := repoView{Name: cfg.Name, AgentSwitchable: cfg.AgentSwitchable, CISwitchable: cfg.CISwitchable}
		view.AgentRunner, view.CIRunner, view.AgentKnown, view.CIKnown, view.AgentError, view.CIError, view.Error = s.repositoryVariables(cfg.Name)
		repos = append(repos, view)
	}
	fleetSeenAt := int64(0)
	if !fleetAt.IsZero() {
		fleetSeenAt = fleetAt.Unix()
	}
	unitOps, err := s.Store.LatestUnitOps(ctx)
	if err != nil {
		errorResponse(w, 500, "Não foi possível ler as operações das units.")
		return
	}
	fileRoots := map[string][]string{}
	if s.Files != nil {
		for _, host := range s.Config.Hosts {
			if host.Kind == "vps" && len(host.FileRoots) > 0 {
				fileRoots[host.ID] = host.FileRoots
			}
		}
	}
	jsonResponse(w, 200, map[string]any{"hosts": hosts, "projects": projects, "runners": runners, "sessions": sessions, "queued": queued, "repositories": repos, "collector_errors": errors, "fleet_seen_at": fleetSeenAt, "smtp_provisioned": s.SMTPProvisioned, "private": privateRoute, "unit_ops": unitOps, "file_roots": fileRoots})
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	until := time.Now().Unix()
	minSince := until - int64((30 * 24 * time.Hour).Seconds())
	since := minSince
	if raw := r.URL.Query().Get("since"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			errorResponse(w, 400, "Período inválido.")
			return
		}
		since = value
	}
	if since < minSince {
		since = minSince
	}
	if since > until {
		errorResponse(w, 400, "Período inválido.")
		return
	}
	points, err := s.Store.MetricHistory(r.Context(), r.PathValue("id"), since, until)
	if err != nil {
		errorResponse(w, 500, "Não foi possível ler o histórico.")
		return
	}
	jsonResponse(w, 200, points)
}

func (s *Server) projectIncidents(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		errorResponse(w, 400, "Projeto inválido.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	history, err := s.Store.ProjectIncidents(ctx, id, time.Now())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			errorResponse(w, 404, "Projeto não encontrado.")
		} else {
			errorResponse(w, 500, "Não foi possível ler o histórico de incidentes.")
		}
		return
	}
	jsonResponse(w, 200, history)
}

// minutes reports job minutes per runner backend as a cost proxy. It reads only
// the local store; the collector fills it in the background.
func (s *Server) minutes(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	usage, err := s.Store.BackendMinutes(r.Context(), now)
	if err != nil {
		errorResponse(w, 500, "Não foi possível ler os minutos por backend.")
		return
	}
	repos := make([]store.RepoMinutes, 0, len(s.Config.Repositories))
	for _, cfg := range s.Config.Repositories {
		view, ok := usage[cfg.Name]
		if !ok {
			view = store.RepoMinutes{Repo: cfg.Name, Backends: []store.BackendUsage{}}
		}
		repos = append(repos, view)
	}
	jsonResponse(w, 200, map[string]any{
		"generated_at":      now.Unix(),
		"collector_enabled": s.Collector != nil && s.Collector.Minutes != nil,
		"interval_s":        int64(collect.MinutesInterval.Seconds()),
		"repositories":      repos,
	})
}

func (s *Server) updateProject(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		errorResponse(w, 400, "Projeto inválido.")
		return
	}
	projects, err := s.Store.Projects(r.Context())
	if err != nil {
		errorResponse(w, 500, "Não foi possível ler os projetos.")
		return
	}
	for _, project := range projects {
		if project.ID == id && project.Native {
			errorResponse(w, 403, "Esta unit é monitorada automaticamente.")
			return
		}
	}
	var body struct {
		Monitored bool     `json:"monitored"`
		HealthURL string   `json:"health_url"`
		Expected  []string `json:"expected"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		errorResponse(w, 400, "Configuração inválida.")
		return
	}
	if body.Monitored && body.HealthURL == "" && len(body.Expected) == 0 {
		errorResponse(w, 400, "Defina uma URL ou serviços esperados para monitorar.")
		return
	}
	if body.HealthURL != "" && collect.ValidateHealthURL(r.Context(), s.Config.Hosts, body.HealthURL) != nil {
		errorResponse(w, 400, "A URL de health deve usar HTTP(S) e apontar para um endereço público ou um host do inventário.")
		return
	}
	expected := ""
	if len(body.Expected) > 0 {
		encoded, _ := json.Marshal(body.Expected)
		expected = string(encoded)
	}
	err = s.Store.SetMonitored(r.Context(), id, body.Monitored, body.HealthURL, expected)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			errorResponse(w, 404, "Projeto não encontrado.")
		} else {
			errorResponse(w, 500, "Não foi possível salvar o projeto.")
		}
		return
	}
	jsonResponse(w, 200, map[string]bool{"saved": true})
}

func (s *Server) configuredRepo(name, variable string) bool {
	for _, repo := range s.Config.Repositories {
		if repo.Name == name {
			return (variable == "AGENT_RUNNER" && repo.AgentSwitchable) || (variable == "CI_RUNNER" && repo.CISwitchable)
		}
	}
	return false
}

func (s *Server) switchRunner(w http.ResponseWriter, r *http.Request) {
	if s.GitHub == nil {
		errorResponse(w, 503, "GitHub App não provisionado.")
		return
	}
	repo := r.PathValue("owner") + "/" + r.PathValue("repo")
	var body struct {
		Variable string `json:"variable"`
		Label    string `json:"label"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		errorResponse(w, 400, "Seleção inválida.")
		return
	}
	if !s.configuredRepo(repo, body.Variable) {
		errorResponse(w, 403, "Este workflow ainda não usa o switch de runner.")
		return
	}
	if err := s.GitHub.SetVariable(r.Context(), repo, body.Variable, body.Label); err != nil {
		errorResponse(w, 502, "GitHub recusou a troca de runner.")
		return
	}
	s.noteVariableSet(repo, body.Variable, body.Label)
	jsonResponse(w, 200, map[string]any{"repo": repo, "variable": body.Variable, "label": body.Label})
}

func (s *Server) bulkSwitch(w http.ResponseWriter, r *http.Request) {
	if s.GitHub == nil {
		errorResponse(w, 503, "GitHub App não provisionado.")
		return
	}
	var body struct {
		Variable     string   `json:"variable"`
		Label        string   `json:"label"`
		Repositories []string `json:"repositories"`
	}
	if err := decodeJSON(w, r, &body); err != nil || len(body.Repositories) == 0 {
		errorResponse(w, 400, "Seleção inválida.")
		return
	}
	for _, repo := range body.Repositories {
		if !s.configuredRepo(repo, body.Variable) {
			errorResponse(w, 403, "Um dos repositórios ainda não usa o switch.")
			return
		}
	}
	results := map[string]string{}
	for _, repo := range body.Repositories {
		if err := s.GitHub.SetVariable(r.Context(), repo, body.Variable, body.Label); err != nil {
			results[repo] = "falhou"
		} else {
			results[repo] = "ok"
			s.noteVariableSet(repo, body.Variable, body.Label)
		}
	}
	jsonResponse(w, 200, map[string]any{"results": results})
}

func (s *Server) preset(w http.ResponseWriter, r *http.Request) {
	if s.GitHub == nil {
		errorResponse(w, 503, "GitHub App não provisionado.")
		return
	}
	labels := map[string]string{"economia": "self-hosted", "rapidez": "depot-ubuntu-24.04-4", "fallback": "ubuntu-latest"}
	label, ok := labels[r.PathValue("name")]
	if !ok {
		errorResponse(w, 404, "Preset desconhecido.")
		return
	}
	results := map[string]string{}
	for _, repo := range s.Config.Repositories {
		for _, variable := range []string{"AGENT_RUNNER", "CI_RUNNER"} {
			if !s.configuredRepo(repo.Name, variable) {
				continue
			}
			key := repo.Name + ":" + variable
			if err := s.GitHub.SetVariable(r.Context(), repo.Name, variable, label); err != nil {
				results[key] = "falhou"
			} else {
				results[key] = "ok"
				s.noteVariableSet(repo.Name, variable, label)
			}
		}
	}
	jsonResponse(w, 200, map[string]any{"preset": r.PathValue("name"), "label": label, "results": results})
}

// runnerUnitOp starts a restart or drain of one gh-agents runner unit. The
// request carries no body; host and unit come from the path and are checked
// against the observed native units before anything reaches SSH.
func (s *Server) runnerUnitOp(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Collector == nil {
			errorResponse(w, 503, "Coletor indisponível.")
			return
		}
		op, err := s.Collector.StartRunnerUnitOp(r.Context(), r.PathValue("host"), r.PathValue("unit"), action)
		switch {
		case err == nil:
			log.Printf("runner unit %s accepted from %s: %s on %s", action, s.loginClientIP(r), op.Unit, op.HostID)
			jsonResponse(w, http.StatusAccepted, op)
		case errors.Is(err, collect.ErrUnknownRunnerUnit):
			errorResponse(w, 404, "Unit de runner não encontrada neste host.")
		case errors.Is(err, collect.ErrUnitOpRunning):
			errorResponse(w, 409, "Já existe uma operação em andamento nesta unit.")
		default:
			log.Printf("runner unit %s: %v", action, err)
			errorResponse(w, 500, "Não foi possível registrar a operação.")
		}
	}
}

func (s *Server) cancelDrain(w http.ResponseWriter, r *http.Request) {
	if s.Collector == nil {
		errorResponse(w, 503, "Coletor indisponível.")
		return
	}
	if err := s.Collector.CancelDrain(r.PathValue("host"), r.PathValue("unit")); err != nil {
		errorResponse(w, 409, "Nenhuma drenagem em andamento nesta unit.")
		return
	}
	jsonResponse(w, http.StatusAccepted, map[string]bool{"cancelling": true})
}

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Método não permitido.", http.StatusMethodNotAllowed)
		return
	}
	if s.Static == nil {
		http.NotFound(w, r)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	asset, err := s.loadStatic(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, fs.ErrInvalid) {
			errorResponse(w, 500, "Arquivo indisponível.")
			return
		}
		if !strings.Contains(r.Header.Get("Accept"), "text/html") || strings.Contains(path, ".") || strings.HasPrefix(path, "assets/") || strings.HasPrefix(path, "icons/") {
			http.NotFound(w, r)
			return
		}
		asset, err = s.loadStatic("index.html")
		if err != nil {
			errorResponse(w, 500, "Arquivo indisponível.")
			return
		}
		path = "index.html"
	}
	if strings.HasSuffix(path, ".js") {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	}
	if strings.HasSuffix(path, ".css") {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	}
	if strings.HasSuffix(path, ".html") {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	if strings.HasSuffix(path, ".svg") {
		w.Header().Set("Content-Type", "image/svg+xml")
	}
	if strings.HasSuffix(path, ".woff2") {
		w.Header().Set("Content-Type", "font/woff2")
	}
	if strings.HasSuffix(path, ".woff") {
		w.Header().Set("Content-Type", "font/woff")
	}
	if strings.HasSuffix(path, ".webmanifest") {
		w.Header().Set("Content-Type", "application/manifest+json")
	}
	if path == "index.html" || path == "sw.js" || strings.HasSuffix(path, ".webmanifest") {
		w.Header().Set("Cache-Control", "no-cache")
	} else if strings.HasPrefix(path, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else if strings.HasPrefix(path, "icons/") {
		w.Header().Set("Cache-Control", "public, max-age=3600")
	}
	w.Header().Set("ETag", asset.etag)
	http.ServeContent(w, r, path, time.Time{}, bytes.NewReader(asset.data))
}

func (s *Server) loadStatic(path string) (staticAsset, error) {
	s.staticMu.RLock()
	asset, ok := s.staticCache[path]
	s.staticMu.RUnlock()
	if ok {
		return asset, nil
	}
	f, err := s.Static.Open(path)
	if err != nil {
		return staticAsset{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return staticAsset{}, err
	}
	if info.IsDir() {
		return staticAsset{}, fs.ErrNotExist
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return staticAsset{}, err
	}
	asset = staticAsset{data: data, etag: fmt.Sprintf("\"%x\"", sha256.Sum256(data))}
	s.staticMu.Lock()
	if s.staticCache == nil {
		s.staticCache = map[string]staticAsset{}
	}
	if existing, found := s.staticCache[path]; found {
		asset = existing
	} else {
		s.staticCache[path] = asset
	}
	s.staticMu.Unlock()
	return asset, nil
}
