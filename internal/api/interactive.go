package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/heliowap/vpsdash/internal/config"
	"github.com/heliowap/vpsdash/internal/interactive"
	"github.com/heliowap/vpsdash/internal/store"
)

const (
	stepUpLifetime     = 10 * time.Minute
	ticketLifetime     = 30 * time.Second
	defaultMaxSessions = 4
	defaultIdleTimeout = 15 * time.Minute
	snippetOutputLimit = 64 << 10
	terminalReadLimit  = 64 << 10
)

// interactiveState holds step-up grants, terminal tickets, and the session
// counter. It lives only in memory: a restart revokes every grant.
type interactiveState struct {
	interactiveMu sync.Mutex
	stepUps       map[string]time.Time
	tickets       map[string]terminalTicket
	activeCount   int
	// MaxSessions bounds open terminals, attaches, and running snippets.
	MaxSessions int
	// IdleTimeout closes a terminal without keyboard input for this long.
	IdleTimeout time.Duration
	now         func() time.Time
}

type terminalTicket struct {
	sessionID string
	host      config.Host
	argv      []string
	action    string
	target    string
	expires   time.Time
}

func (s *Server) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Server) maxSessions() int {
	if s.MaxSessions > 0 {
		return s.MaxSessions
	}
	return defaultMaxSessions
}

func (s *Server) idleTimeout() time.Duration {
	if s.IdleTimeout > 0 {
		return s.IdleTimeout
	}
	return defaultIdleTimeout
}

func (s *Server) registerInteractive(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/interactive", s.interactiveInfo)
	mux.HandleFunc("POST /api/step-up", s.stepUp)
	mux.HandleFunc("POST /api/terminal/tickets", s.terminalTicket)
	mux.HandleFunc("GET /api/terminal/ws", s.terminalSocket)
	mux.HandleFunc("POST /api/hosts/{id}/snippets/run", s.runSnippet)
}

func (s *Server) recordAudit(r *http.Request, action, hostID, target, outcome string) {
	if s.Store == nil {
		return
	}
	entry := store.AuditEntry{Action: action, HostID: auditText(hostID), Target: auditText(target), ClientIP: s.loginClientIP(r), Outcome: outcome}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Store.RecordAudit(ctx, entry, s.clock()); err != nil {
		log.Printf("audit %s %s/%s: %v", action, hostID, target, err)
	}
}

// auditText bounds request-supplied names before they are stored.
func auditText(value string) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.ToValidUTF8(value, ""))
	if len(value) > 128 {
		value = strings.ToValidUTF8(value[:128], "")
	}
	return value
}

func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	entries, err := s.Store.RecentAudit(r.Context(), 30)
	if err != nil {
		errorResponse(w, 500, "Não foi possível ler o registro de acesso.")
		return
	}
	jsonResponse(w, 200, entries)
}

func (s *Server) stepUpUntil(sessionID string) time.Time {
	s.interactiveMu.Lock()
	defer s.interactiveMu.Unlock()
	until := s.stepUps[sessionID]
	if !until.After(s.clock()) {
		return time.Time{}
	}
	return until
}

func (s *Server) clearStepUp(sessionID string) {
	s.interactiveMu.Lock()
	defer s.interactiveMu.Unlock()
	delete(s.stepUps, sessionID)
	for key, ticket := range s.tickets {
		if ticket.sessionID == sessionID {
			delete(s.tickets, key)
		}
	}
}

// requireStepUp returns the session ID when the session re-entered the
// password within the last ten minutes.
func (s *Server) requireStepUp(w http.ResponseWriter, r *http.Request) (string, bool) {
	sessionID, ok := s.Auth.SessionID(r, s.clock())
	if !ok {
		errorResponse(w, http.StatusUnauthorized, "Sessão expirada. Entre novamente.")
		return "", false
	}
	if s.stepUpUntil(sessionID).IsZero() {
		jsonResponse(w, http.StatusForbidden, map[string]string{"error": "Confirme a senha para continuar.", "code": "step_up_required"})
		return "", false
	}
	return sessionID, true
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

type snippetView struct {
	Name    string   `json:"name"`
	Argv    []string `json:"argv"`
	Timeout int      `json:"timeout_seconds"`
}

type interactiveHost struct {
	ID       string `json:"id"`
	Terminal bool   `json:"terminal"`
	// KeyMissing reports an interactive_key_file that vpsdash cannot read.
	KeyMissing   bool   `json:"key_missing,omitempty"`
	LocalCommand string `json:"local_command"`
	// AttachCommands holds the read-only local ssh command per observed
	// tmux session, for "abrir no terminal local".
	AttachCommands map[string]string `json:"attach_commands"`
	Snippets       []snippetView     `json:"snippets"`
}

func localCommand(host config.Host, argv []string) string {
	words := []string{"ssh"}
	if host.SSHPort != 0 && host.SSHPort != 22 {
		words = append(words, "-p", strconv.Itoa(host.SSHPort))
	}
	if len(argv) > 0 {
		words = append(words, "-t")
	}
	words = append(words, host.SSHUser+"@"+strings.TrimSuffix(host.TailnetName, "."))
	// ssh joins its arguments for the remote shell, so a word that needs
	// quoting is quoted twice: once for the local shell, once for the remote.
	for _, arg := range argv {
		words = append(words, displayWord(displayWord(arg)))
	}
	return strings.Join(words, " ")
}

var plainWord = regexp.MustCompile(`^[A-Za-z0-9_./=:@%+,-]+$`)

func displayWord(word string) string {
	if plainWord.MatchString(word) {
		return word
	}
	return interactive.Quote(word)
}

func snippetTimeout(snippet config.Snippet) int {
	if snippet.TimeoutSeconds > 0 {
		return snippet.TimeoutSeconds
	}
	return config.DefaultSnippetTimeout
}

func (s *Server) interactiveInfo(w http.ResponseWriter, r *http.Request) {
	sessionID, _ := s.Auth.SessionID(r, s.clock())
	sessions, err := s.Store.Sessions(r.Context())
	if err != nil {
		errorResponse(w, 500, "Não foi possível ler as sessões.")
		return
	}
	hosts := []interactiveHost{}
	for _, host := range s.Config.Hosts {
		if host.Kind != "vps" {
			continue
		}
		_, keyErr := os.Stat(host.InteractiveKeyFile)
		keyMissing := host.Interactive() && keyErr != nil
		view := interactiveHost{ID: host.ID, Terminal: host.Interactive() && !keyMissing && s.Interactive != nil, KeyMissing: keyMissing, AttachCommands: map[string]string{}, Snippets: []snippetView{}}
		if host.SSHUser != "" && !host.Local {
			view.LocalCommand = localCommand(host, nil)
			for _, session := range sessions {
				if session.HostID == host.ID && interactive.ValidSessionName(session.Name) {
					view.AttachCommands[session.Name] = localCommand(host, interactive.AttachArgv(session.Name, true))
				}
			}
		}
		for _, snippet := range host.Snippets {
			view.Snippets = append(view.Snippets, snippetView{Name: snippet.Name, Argv: snippet.Argv, Timeout: snippetTimeout(snippet)})
		}
		hosts = append(hosts, view)
	}
	s.interactiveMu.Lock()
	active := s.activeCount
	s.interactiveMu.Unlock()
	jsonResponse(w, 200, map[string]any{
		"hosts":         hosts,
		"step_up_until": unixOrZero(s.stepUpUntil(sessionID)),
		"active":        active,
		"max_sessions":  s.maxSessions(),
		"idle_minutes":  int(s.idleTimeout().Minutes()),
	})
}

func (s *Server) stepUp(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := s.Auth.SessionID(r, s.clock())
	if !ok {
		errorResponse(w, http.StatusUnauthorized, "Sessão expirada. Entre novamente.")
		return
	}
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
	// Step-up failures share the login limits per client and globally.
	ip := s.loginClientIP(r)
	if !s.reserveLogin(ip, time.Now()) {
		s.recordAudit(r, "step_up", "", "", "denied")
		errorResponse(w, http.StatusTooManyRequests, "Muitas tentativas. Tente novamente em cinco minutos.")
		return
	}
	verified := s.verifyPassword(body.Password)
	s.finishLogin(ip, verified, time.Now())
	if !verified {
		s.recordAudit(r, "step_up", "", "", "denied")
		errorResponse(w, http.StatusUnauthorized, "Senha incorreta.")
		return
	}
	now := s.clock()
	until := now.Add(stepUpLifetime)
	s.interactiveMu.Lock()
	if s.stepUps == nil {
		s.stepUps = map[string]time.Time{}
	}
	for key, expires := range s.stepUps {
		if !expires.After(now) {
			delete(s.stepUps, key)
		}
	}
	s.stepUps[sessionID] = until
	s.interactiveMu.Unlock()
	s.recordAudit(r, "step_up", "", "", "ok")
	jsonResponse(w, 200, map[string]int64{"step_up_until": until.Unix()})
}

func (s *Server) interactiveHost(id string) (config.Host, bool) {
	for _, host := range s.Config.Hosts {
		if host.ID == id && host.Kind == "vps" && host.Interactive() {
			return host, s.Interactive != nil
		}
	}
	return config.Host{}, false
}

func randomToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func (s *Server) terminalTicket(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Host         string `json:"host"`
		Kind         string `json:"kind"`
		Session      string `json:"session"`
		Write        bool   `json:"write"`
		ConfirmWrite bool   `json:"confirm_write"`
	}
	if err := decodeJSON(w, r, &body); err != nil || (body.Kind != "terminal" && body.Kind != "attach") {
		errorResponse(w, 400, "Pedido de terminal inválido.")
		return
	}
	action, target := "terminal", ""
	if body.Kind == "attach" {
		action, target = "attach_ro", body.Session
		if body.Write {
			action = "attach_rw"
		}
	}
	sessionID, ok := s.requireStepUp(w, r)
	if !ok {
		s.recordAudit(r, action, body.Host, target, "denied")
		return
	}
	host, ok := s.interactiveHost(body.Host)
	if !ok {
		s.recordAudit(r, action, body.Host, target, "denied")
		errorResponse(w, 404, "Terminal não configurado para este host.")
		return
	}
	var argv []string
	if body.Kind == "attach" {
		if body.Write && !body.ConfirmWrite {
			s.recordAudit(r, action, host.ID, target, "denied")
			errorResponse(w, 400, "Confirme o controle da sessão antes de abrir o attach com escrita.")
			return
		}
		if !interactive.ValidSessionName(body.Session) || !s.sessionObserved(r.Context(), host.ID, body.Session) {
			s.recordAudit(r, action, host.ID, "", "denied")
			errorResponse(w, 404, "Sessão tmux não observada neste host.")
			return
		}
		argv = interactive.AttachArgv(body.Session, !body.Write)
	}
	ticket, err := randomToken()
	if err != nil {
		errorResponse(w, 500, "Não foi possível preparar o terminal.")
		return
	}
	now := s.clock()
	s.interactiveMu.Lock()
	if s.tickets == nil {
		s.tickets = map[string]terminalTicket{}
	}
	for key, existing := range s.tickets {
		if !existing.expires.After(now) {
			delete(s.tickets, key)
		}
	}
	s.tickets[ticket] = terminalTicket{sessionID: sessionID, host: host, argv: argv, action: action, target: target, expires: now.Add(ticketLifetime)}
	s.interactiveMu.Unlock()
	jsonResponse(w, 200, map[string]any{"ticket": ticket, "local_command": localCommand(host, argv)})
}

func (s *Server) sessionObserved(ctx context.Context, hostID, name string) bool {
	sessions, err := s.Store.Sessions(ctx)
	if err != nil {
		return false
	}
	for _, session := range sessions {
		if session.HostID == hostID && session.Name == name {
			return true
		}
	}
	return false
}

// sameOrigin requires the browser Origin to name the host the request was
// sent to. Plain HTTP is accepted only for loopback development.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || !strings.EqualFold(u.Host, r.Host) {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	if u.Scheme != "http" {
		return false
	}
	hostname := u.Hostname()
	ip := net.ParseIP(hostname)
	return hostname == "localhost" || (ip != nil && ip.IsLoopback())
}

func (s *Server) takeTicket(value string) (terminalTicket, bool) {
	s.interactiveMu.Lock()
	defer s.interactiveMu.Unlock()
	ticket, ok := s.tickets[value]
	if ok {
		delete(s.tickets, value)
	}
	return ticket, ok && ticket.expires.After(s.clock())
}

func (s *Server) acquireSlot() bool {
	s.interactiveMu.Lock()
	defer s.interactiveMu.Unlock()
	if s.activeCount >= s.maxSessions() {
		return false
	}
	s.activeCount++
	return true
}

func (s *Server) releaseSlot() {
	s.interactiveMu.Lock()
	s.activeCount--
	s.interactiveMu.Unlock()
}

func terminalSize(r *http.Request) (int, int) {
	cols, errCols := strconv.Atoi(r.URL.Query().Get("cols"))
	rows, errRows := strconv.Atoi(r.URL.Query().Get("rows"))
	if errCols != nil || errRows != nil {
		return 80, 24
	}
	return clamp(cols, 10, 500), clamp(rows, 5, 200)
}

func clamp(value, low, high int) int {
	return max(low, min(high, value))
}

type controlMessage struct {
	Type string `json:"type"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

func (s *Server) terminalSocket(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := s.Auth.SessionID(r, s.clock())
	if !ok {
		errorResponse(w, http.StatusUnauthorized, "Sessão expirada. Entre novamente.")
		return
	}
	ticket, ok := s.takeTicket(r.URL.Query().Get("ticket"))
	if !ok || ticket.sessionID != sessionID {
		errorResponse(w, http.StatusForbidden, "Pedido de terminal expirado. Tente novamente.")
		return
	}
	if !sameOrigin(r) {
		s.recordAudit(r, ticket.action, ticket.host.ID, ticket.target, "denied")
		errorResponse(w, http.StatusForbidden, "Origem não permitida.")
		return
	}
	if s.stepUpUntil(sessionID).IsZero() {
		s.recordAudit(r, ticket.action, ticket.host.ID, ticket.target, "denied")
		jsonResponse(w, http.StatusForbidden, map[string]string{"error": "Confirme a senha para continuar.", "code": "step_up_required"})
		return
	}
	if !s.acquireSlot() {
		s.recordAudit(r, ticket.action, ticket.host.ID, ticket.target, "denied")
		errorResponse(w, http.StatusTooManyRequests, "Limite de terminais abertos atingido. Feche um terminal e tente novamente.")
		return
	}
	defer s.releaseSlot()
	// Hijacked connections keep the server's read deadline; a terminal is
	// long-lived and is bounded by the idle timeout instead.
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Time{})
	_ = controller.SetWriteDeadline(time.Time{})
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		s.recordAudit(r, ticket.action, ticket.host.ID, ticket.target, "denied")
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(terminalReadLimit)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cols, rows := terminalSize(r)
	openCtx, openCancel := context.WithTimeout(ctx, 15*time.Second)
	pty, err := s.Interactive.OpenPTY(openCtx, ticket.host, ticket.argv, cols, rows)
	openCancel()
	if err != nil {
		log.Printf("terminal %s: %v", ticket.host.ID, err)
		s.recordAudit(r, ticket.action, ticket.host.ID, ticket.target, "failed")
		sendControl(ctx, conn, map[string]string{"type": "exit", "reason": "Não foi possível abrir o SSH interativo neste host."})
		_ = conn.Close(websocket.StatusNormalClosure, "")
		return
	}
	defer pty.Close()
	s.recordAudit(r, ticket.action, ticket.host.ID, ticket.target, "ok")
	reason := s.bridge(ctx, conn, pty)
	_ = pty.Close()
	s.recordAudit(r, ticket.action, ticket.host.ID, ticket.target, "closed")
	sendControl(ctx, conn, map[string]string{"type": "exit", "reason": reason})
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

func sendControl(ctx context.Context, conn *websocket.Conn, value any) {
	encoded, _ := json.Marshal(value)
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = conn.Write(writeCtx, websocket.MessageText, encoded)
}

// bridge copies bytes both ways until one side ends, the browser stays idle
// past IdleTimeout, or a ping fails. It returns the reason shown to the user.
// Keystrokes and output pass through memory only; nothing is logged.
func (s *Server) bridge(ctx context.Context, conn *websocket.Conn, pty *interactive.PTY) string {
	// Cancelling a Read or Write context closes a coder/websocket connection,
	// so the loops keep the handler context and the caller sends the exit
	// message before closing.
	var once sync.Once
	finished := make(chan string, 1)
	stopped := make(chan struct{})
	defer close(stopped)
	finish := func(reason string) {
		once.Do(func() { finished <- reason })
	}
	var lastInput atomic.Int64
	lastInput.Store(s.clock().UnixNano())
	sendControl(ctx, conn, map[string]string{"type": "ready"})
	go func() {
		buffer := make([]byte, 32<<10)
		for {
			n, err := pty.Read(buffer)
			if n > 0 {
				if writeErr := conn.Write(ctx, websocket.MessageBinary, buffer[:n]); writeErr != nil {
					finish("Conexão do navegador encerrada.")
					return
				}
			}
			if err != nil {
				finish("Sessão encerrada no host.")
				return
			}
		}
	}()
	go func() {
		for {
			kind, data, err := conn.Read(ctx)
			if err != nil {
				finish("Conexão do navegador encerrada.")
				return
			}
			if kind == websocket.MessageBinary {
				lastInput.Store(s.clock().UnixNano())
				if _, err := pty.Write(data); err != nil {
					finish("Sessão encerrada no host.")
					return
				}
				continue
			}
			var message controlMessage
			if json.Unmarshal(data, &message) == nil && message.Type == "resize" {
				_ = pty.Resize(clamp(message.Cols, 10, 500), clamp(message.Rows, 5, 200))
			}
		}
	}()
	go func() {
		select {
		case <-pty.Done():
			finish("Sessão encerrada no host.")
		case <-stopped:
		}
	}()
	tick := s.idleTimeout() / 4
	tick = min(max(tick, 50*time.Millisecond), 30*time.Second)
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case reason := <-finished:
			return reason
		case <-ticker.C:
			if s.clock().Sub(time.Unix(0, lastInput.Load())) >= s.idleTimeout() {
				finish("Terminal fechado após " + strconv.Itoa(int(s.idleTimeout().Minutes())) + " min sem digitação.")
				continue
			}
			pingCtx, pingCancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Ping(pingCtx)
			pingCancel()
			if err != nil {
				finish("Conexão do navegador sem resposta.")
			}
		}
	}
}

func (s *Server) runSnippet(w http.ResponseWriter, r *http.Request) {
	hostID := r.PathValue("id")
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		errorResponse(w, 400, "Comando inválido.")
		return
	}
	if _, ok := s.requireStepUp(w, r); !ok {
		s.recordAudit(r, "snippet", hostID, body.Name, "denied")
		return
	}
	host, ok := s.interactiveHost(hostID)
	var snippet config.Snippet
	found := false
	if ok {
		for _, candidate := range host.Snippets {
			if candidate.Name == body.Name {
				snippet, found = candidate, true
				break
			}
		}
	}
	if !found {
		s.recordAudit(r, "snippet", hostID, body.Name, "denied")
		errorResponse(w, 404, "Comando não definido no inventário deste host.")
		return
	}
	if !s.acquireSlot() {
		s.recordAudit(r, "snippet", host.ID, snippet.Name, "denied")
		errorResponse(w, http.StatusTooManyRequests, "Limite de sessões interativas atingido. Tente novamente em instantes.")
		return
	}
	defer s.releaseSlot()
	started := time.Now()
	result, err := s.Interactive.Run(r.Context(), host, snippet.Argv, time.Duration(snippetTimeout(snippet))*time.Second, snippetOutputLimit)
	if err != nil {
		log.Printf("snippet %s/%s: %v", host.ID, snippet.Name, err)
		s.recordAudit(r, "snippet", host.ID, snippet.Name, "failed")
		errorResponse(w, 502, "Não foi possível executar o comando por SSH.")
		return
	}
	outcome := "ok"
	if result.ExitCode != 0 || result.TimedOut {
		outcome = "failed"
	}
	s.recordAudit(r, "snippet", host.ID, snippet.Name, outcome)
	jsonResponse(w, 200, map[string]any{"name": snippet.Name, "result": result, "duration_ms": time.Since(started).Milliseconds()})
}
