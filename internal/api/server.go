package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/heliowap/vpsdash/internal/auth"
	"github.com/heliowap/vpsdash/internal/collect"
	"github.com/heliowap/vpsdash/internal/config"
	"github.com/heliowap/vpsdash/internal/githubapp"
	"github.com/heliowap/vpsdash/internal/store"
)

type GitHub interface {
	Variable(context.Context, string, string) (string, error)
	SetVariable(context.Context, string, string, string) error
	Rerun(context.Context, string, int64) error
}

type Server struct {
	Config          config.Config
	Store           *store.Store
	Collector       *collect.Collector
	GitHub          GitHub
	Auth            *auth.Authenticator
	Static          http.FileSystem
	SMTPProvisioned bool
	loginMu         sync.Mutex
	loginFailures   map[string][]time.Time
}

func New(cfg config.Config, s *store.Store, c *collect.Collector, gh GitHub, a *auth.Authenticator) *Server {
	return &Server{Config: cfg, Store: s, Collector: c, GitHub: gh, Auth: a, loginFailures: map[string][]time.Time{}}
}

func (s *Server) Handler() http.Handler {
	public := http.NewServeMux()
	public.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	public.HandleFunc("GET /api/session", s.session)
	public.HandleFunc("POST /api/login", s.login)
	private := http.NewServeMux()
	private.HandleFunc("GET /api/dashboard", s.dashboard)
	private.HandleFunc("GET /api/hosts/{id}/metrics", s.metrics)
	private.HandleFunc("PATCH /api/projects/{id}", s.updateProject)
	private.HandleFunc("POST /api/repos/{owner}/{repo}/switch", s.switchRunner)
	private.HandleFunc("POST /api/switches/bulk", s.bulkSwitch)
	private.HandleFunc("POST /api/presets/{name}", s.preset)
	private.HandleFunc("POST /api/repos/{owner}/{repo}/rerun/{id}", s.rerun)
	private.HandleFunc("POST /api/logout", s.logout)
	public.Handle("/api/", s.authorize(private))
	public.HandleFunc("/", s.static)
	return securityHeaders(public)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self' wss:; img-src 'self' data:; font-src 'self'; frame-ancestors 'none'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
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

func decodeJSON(r *http.Request, value any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 32<<10))
	decoder.DisallowUnknownFields()
	return decoder.Decode(value)
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	csrf, ok := s.Auth.Validate(r, time.Now())
	jsonResponse(w, http.StatusOK, map[string]any{"authenticated": ok, "csrf": csrf})
}

func (s *Server) tooManyLogins(ip string) bool {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	cutoff := time.Now().Add(-5 * time.Minute)
	recent := s.loginFailures[ip][:0]
	for _, t := range s.loginFailures[ip] {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	s.loginFailures[ip] = recent
	return len(recent) >= 5
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip := r.RemoteAddr
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	if s.tooManyLogins(ip) {
		errorResponse(w, http.StatusTooManyRequests, "Muitas tentativas. Tente novamente em cinco minutos.")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errorResponse(w, http.StatusBadRequest, "Senha inválida.")
		return
	}
	if !s.Auth.VerifyPassword(body.Password) {
		s.loginMu.Lock()
		s.loginFailures[ip] = append(s.loginFailures[ip], time.Now())
		s.loginMu.Unlock()
		errorResponse(w, http.StatusUnauthorized, "Senha incorreta.")
		return
	}
	s.loginMu.Lock()
	delete(s.loginFailures, ip)
	s.loginMu.Unlock()
	csrf := s.Auth.Issue(w, time.Now())
	if csrf == "" {
		errorResponse(w, http.StatusInternalServerError, "Não foi possível iniciar a sessão.")
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"authenticated": true, "csrf": csrf})
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
	auth.Clear(w)
	jsonResponse(w, http.StatusOK, map[string]bool{"authenticated": false})
}

type repoView struct {
	Name            string `json:"name"`
	AgentSwitchable bool   `json:"agent_switchable"`
	CISwitchable    bool   `json:"ci_switchable"`
	AgentRunner     string `json:"agent_runner"`
	CIRunner        string `json:"ci_runner"`
	Error           string `json:"error,omitempty"`
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
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
		if s.GitHub != nil {
			view.AgentRunner, err = s.GitHub.Variable(ctx, cfg.Name, "AGENT_RUNNER")
			if err == nil {
				view.CIRunner, err = s.GitHub.Variable(ctx, cfg.Name, "CI_RUNNER")
			}
			if err != nil {
				view.Error = "GitHub indisponível"
			}
		} else {
			view.Error = "GitHub App não provisionado"
		}
		repos = append(repos, view)
	}
	fleetSeenAt := int64(0)
	if !fleetAt.IsZero() {
		fleetSeenAt = fleetAt.Unix()
	}
	jsonResponse(w, 200, map[string]any{"hosts": hosts, "projects": projects, "runners": runners, "sessions": sessions, "queued": queued, "repositories": repos, "collector_errors": errors, "fleet_seen_at": fleetSeenAt, "smtp_provisioned": s.SMTPProvisioned})
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
	if err := decodeJSON(r, &body); err != nil {
		errorResponse(w, 400, "Configuração inválida.")
		return
	}
	if body.Monitored && body.HealthURL == "" && len(body.Expected) == 0 {
		errorResponse(w, 400, "Defina uma URL ou serviços esperados para monitorar.")
		return
	}
	if body.HealthURL != "" && !strings.HasPrefix(body.HealthURL, "http://") && !strings.HasPrefix(body.HealthURL, "https://") {
		errorResponse(w, 400, "A URL de health deve começar com http:// ou https://.")
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
	if err := decodeJSON(r, &body); err != nil {
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
	if err := decodeJSON(r, &body); err != nil || len(body.Repositories) == 0 {
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
			}
		}
	}
	jsonResponse(w, 200, map[string]any{"preset": r.PathValue("name"), "label": label, "results": results})
}

func (s *Server) rerun(w http.ResponseWriter, r *http.Request) {
	if s.GitHub == nil {
		errorResponse(w, 503, "GitHub App não provisionado.")
		return
	}
	repo := r.PathValue("owner") + "/" + r.PathValue("repo")
	if !s.repoKnown(repo) {
		errorResponse(w, 404, "Repositório desconhecido.")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		errorResponse(w, 400, "Run inválido.")
		return
	}
	if err := s.GitHub.Rerun(r.Context(), repo, id); err != nil {
		errorResponse(w, 502, "GitHub recusou o re-run.")
		return
	}
	jsonResponse(w, 200, map[string]bool{"requested": true})
}

func (s *Server) repoKnown(name string) bool {
	for _, repo := range s.Config.Repositories {
		if repo.Name == name {
			return true
		}
	}
	return false
}

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if s.Static == nil {
		http.NotFound(w, r)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	f, err := s.Static.Open(path)
	if err != nil {
		f, err = s.Static.Open("index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		path = "index.html"
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		errorResponse(w, 500, "Arquivo indisponível.")
		return
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
	if strings.HasSuffix(path, ".webmanifest") {
		w.Header().Set("Content-Type", "application/manifest+json")
	}
	if path == "index.html" || path == "sw.js" || strings.HasSuffix(path, ".webmanifest") {
		w.Header().Set("Cache-Control", "no-cache")
	}
	_, _ = w.Write(data)
}
