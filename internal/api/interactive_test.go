package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/heliowap/vpsdash/internal/auth"
	"github.com/heliowap/vpsdash/internal/config"
	"github.com/heliowap/vpsdash/internal/interactive"
	"github.com/heliowap/vpsdash/internal/interactive/sshtest"
	"github.com/heliowap/vpsdash/internal/store"
)

const testPassword = "correct horse battery staple"

type interactiveFixture struct {
	t       *testing.T
	server  *Server
	store   *store.Store
	ssh     *sshtest.Server
	public  *httptest.Server
	private *httptest.Server
}

type client struct {
	fx     *interactiveFixture
	base   string
	cookie *http.Cookie
	csrf   string
}

func newInteractiveFixture(t *testing.T) *interactiveFixture {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "vpsdash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	key := filepath.Join(dir, "id_ed25519_interactive_vps")
	public, err := sshtest.GenerateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(dir, "work")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	fake, err := sshtest.Start(public, work)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fake.Close() })
	known, err := fake.WriteKnownHosts(dir)
	if err != nil {
		t.Fatal(err)
	}
	host := config.Host{
		ID: "vps", TailnetName: "127.0.0.1", Kind: "vps", SSHUser: "helio", SSHPort: fake.Port(),
		SSHKeyFile: filepath.Join(dir, "collector"), InteractiveKeyFile: key,
		Snippets: []config.Snippet{
			{Name: "literal", Argv: []string{"printf", "%s\n", "$(touch pwned)", "a; touch pwned"}},
			{Name: "falha", Argv: []string{"sh", "-c", "echo parcial; exit 4"}},
		},
	}
	ctx := context.Background()
	if err := st.UpsertHost(ctx, store.Host{ID: "vps", TailnetName: "127.0.0.1", Kind: "vps", SSHUser: "helio"}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceSessions(ctx, "vps", []store.Session{{HostID: "vps", Name: "dev"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.New(hash, []byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Hosts: []config.Host{host, {ID: "semchave", TailnetName: "other.example.invalid", Kind: "vps", SSHUser: "helio", SSHKeyFile: "/keys/other"}}}
	s := New(cfg, st, nil, nil, a)
	s.verifyPassword = func(password string) bool { return password == testPassword }
	s.Interactive = &interactive.Dialer{KnownHostsFile: known, Timeout: 5 * time.Second}
	fx := &interactiveFixture{t: t, server: s, store: st, ssh: fake, public: httptest.NewServer(s.Handler()), private: httptest.NewServer(s.PrivateHandler())}
	t.Cleanup(fx.public.Close)
	t.Cleanup(fx.private.Close)
	return fx
}

func (fx *interactiveFixture) login(base string) *client {
	fx.t.Helper()
	c := &client{fx: fx, base: base}
	response, body := c.do("POST", "/api/login", `{"password":"`+testPassword+`"}`, false)
	if response.StatusCode != 200 {
		fx.t.Fatalf("login = %d %s", response.StatusCode, body)
	}
	for _, cookie := range response.Cookies() {
		if cookie.Name == auth.CookieName {
			c.cookie = cookie
		}
	}
	var session struct {
		CSRF string `json:"csrf"`
	}
	if err := json.Unmarshal(body, &session); err != nil || c.cookie == nil {
		fx.t.Fatalf("login response: %v %s", err, body)
	}
	c.csrf = session.CSRF
	return c
}

func (c *client) do(method, path, body string, csrf bool) (*http.Response, []byte) {
	c.fx.t.Helper()
	request, err := http.NewRequest(method, c.base+path, bytes.NewReader([]byte(body)))
	if err != nil {
		c.fx.t.Fatal(err)
	}
	if c.cookie != nil {
		request.AddCookie(c.cookie)
	}
	if csrf {
		request.Header.Set("X-CSRF-Token", c.csrf)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		c.fx.t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	return response, data
}

func (c *client) stepUp() {
	c.fx.t.Helper()
	if response, body := c.do("POST", "/api/step-up", `{"password":"`+testPassword+`"}`, true); response.StatusCode != 200 {
		c.fx.t.Fatalf("step-up = %d %s", response.StatusCode, body)
	}
}

func (c *client) ticket(body string) (int, string) {
	c.fx.t.Helper()
	response, data := c.do("POST", "/api/terminal/tickets", body, true)
	var value struct {
		Ticket string `json:"ticket"`
		Code   string `json:"code"`
	}
	_ = json.Unmarshal(data, &value)
	if value.Code != "" {
		return response.StatusCode, value.Code
	}
	return response.StatusCode, value.Ticket
}

func (c *client) dial(ticket, origin string) (*websocket.Conn, *http.Response, error) {
	header := http.Header{}
	header.Set("Cookie", c.cookie.Name+"="+c.cookie.Value)
	if origin != "" {
		header.Set("Origin", origin)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(c.base, "http") + "/api/terminal/ws?cols=90&rows=20&ticket=" + ticket
	return websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
}

func (fx *interactiveFixture) audit() []store.AuditEntry {
	fx.t.Helper()
	entries, err := fx.store.RecentAudit(context.Background(), 100)
	if err != nil {
		fx.t.Fatal(err)
	}
	return entries
}

func hasAudit(entries []store.AuditEntry, action, target, outcome string) bool {
	for _, entry := range entries {
		if entry.Action == action && entry.Target == target && entry.Outcome == outcome {
			return true
		}
	}
	return false
}

func TestInteractiveRoutesExistOnlyOnPrivateListener(t *testing.T) {
	fx := newInteractiveFixture(t)
	routes := []struct{ method, path, body string }{
		{"GET", "/api/interactive", ""},
		{"POST", "/api/step-up", `{"password":"` + testPassword + `"}`},
		{"POST", "/api/terminal/tickets", `{"host":"vps","kind":"terminal"}`},
		{"GET", "/api/terminal/ws", ""},
		{"POST", "/api/hosts/vps/snippets/run", `{"name":"literal"}`},
	}
	publicClient := fx.login(fx.public.URL)
	for _, route := range routes {
		if response, _ := publicClient.do(route.method, route.path, route.body, true); response.StatusCode != http.StatusNotFound {
			t.Fatalf("public %s %s = %d, want 404", route.method, route.path, response.StatusCode)
		}
	}
	// Headers that a proxy or client might add never select the private mux.
	request, _ := http.NewRequest("GET", fx.public.URL+"/api/interactive", nil)
	request.AddCookie(publicClient.cookie)
	request.Header.Set("X-Forwarded-For", "100.64.0.9")
	request.Header.Set("Tailscale-User-Login", "helio@example.invalid")
	request.Host = "vps.example.ts.net"
	if response, err := http.DefaultClient.Do(request); err != nil || response.StatusCode != http.StatusNotFound {
		t.Fatalf("public with tailnet headers = %v %v", response.StatusCode, err)
	}
	for base, want := range map[string]bool{fx.public.URL: false, fx.private.URL: true} {
		c := fx.login(base)
		_, body := c.do("GET", "/api/session", "", false)
		var session struct {
			Private bool `json:"private"`
		}
		if err := json.Unmarshal(body, &session); err != nil || session.Private != want {
			t.Fatalf("%s session private = %s", base, body)
		}
	}
	privateClient := fx.login(fx.private.URL)
	response, body := privateClient.do("GET", "/api/interactive", "", false)
	if response.StatusCode != 200 || !strings.Contains(string(body), `"terminal":true`) || !strings.Contains(string(body), `"name":"literal"`) || !strings.Contains(string(body), `"dev":"ssh -p `+strconv.Itoa(fx.ssh.Port())+` -t helio@127.0.0.1 tmux attach-session -r -t =dev"`) {
		t.Fatalf("private interactive = %d %s", response.StatusCode, body)
	}
	if csp := response.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "'unsafe-inline'") {
		t.Fatalf("private CSP = %q", csp)
	}
	response, _ = publicClient.do("GET", "/api/dashboard", "", false)
	if strings.Contains(response.Header.Get("Content-Security-Policy"), "unsafe-inline") {
		t.Fatal("public listener relaxed its CSP")
	}
	anonymous := &client{fx: fx, base: fx.private.URL}
	if response, _ := anonymous.do("GET", "/api/interactive", "", false); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous private = %d", response.StatusCode)
	}
}

func TestLocalCommandQuotesForLocalAndRemoteShell(t *testing.T) {
	host := config.Host{SSHUser: "helio", TailnetName: "vps.example.invalid."}
	if got := localCommand(host, nil); got != "ssh helio@vps.example.invalid" {
		t.Fatalf("shell command = %q", got)
	}
	got := localCommand(host, interactive.AttachArgv("agente 1", true))
	if got != `ssh -t helio@vps.example.invalid tmux attach-session -r -t ''\''=agente 1'\'''` {
		t.Fatalf("attach command = %q", got)
	}
	// The local shell removes one layer; ssh joins the words for the remote
	// shell, which must see the name as one literal argument.
	output, err := exec.Command("sh", "-c", `printf '%s\n' `+strings.TrimPrefix(got, "ssh -t helio@vps.example.invalid ")).Output()
	if err != nil || strings.TrimSpace(string(output)) != "tmux\nattach-session\n-r\n-t\n'=agente 1'" {
		t.Fatalf("local shell words = %q, %v", output, err)
	}
}

func TestStepUpIsBoundToSessionAndExpires(t *testing.T) {
	fx := newInteractiveFixture(t)
	now := time.Now()
	fx.server.now = func() time.Time { return now }
	c := fx.login(fx.private.URL)
	if status, code := c.ticket(`{"host":"vps","kind":"terminal"}`); status != 403 || code != "step_up_required" {
		t.Fatalf("ticket without step-up = %d %s", status, code)
	}
	if response, _ := c.do("POST", "/api/step-up", `{"password":"`+testPassword+`"}`, false); response.StatusCode != 403 {
		t.Fatalf("step-up without CSRF = %d", response.StatusCode)
	}
	if response, _ := c.do("POST", "/api/step-up", `{"password":"errada errada"}`, true); response.StatusCode != 401 {
		t.Fatalf("wrong step-up = %d", response.StatusCode)
	}
	c.stepUp()
	if status, ticket := c.ticket(`{"host":"vps","kind":"terminal"}`); status != 200 || ticket == "" {
		t.Fatalf("ticket after step-up = %d %q", status, ticket)
	}
	if response, _ := c.do("POST", "/api/terminal/tickets", `{"host":"vps","kind":"terminal"}`, false); response.StatusCode != 403 {
		t.Fatalf("ticket without CSRF = %d", response.StatusCode)
	}
	other := fx.login(fx.private.URL)
	if status, code := other.ticket(`{"host":"vps","kind":"terminal"}`); status != 403 || code != "step_up_required" {
		t.Fatalf("second session inherited step-up: %d %s", status, code)
	}
	now = now.Add(stepUpLifetime + time.Second)
	if status, code := c.ticket(`{"host":"vps","kind":"terminal"}`); status != 403 || code != "step_up_required" {
		t.Fatalf("expired step-up = %d %s", status, code)
	}
	entries := fx.audit()
	if !hasAudit(entries, "step_up", "", "ok") || !hasAudit(entries, "step_up", "", "denied") || !hasAudit(entries, "terminal", "", "denied") {
		t.Fatalf("audit = %+v", entries)
	}
}

func readUntil(t *testing.T, conn *websocket.Conn, want string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	seen := ""
	for !strings.Contains(seen, want) {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("waiting for %q, saw %q: %v", want, seen, err)
		}
		seen += string(data)
	}
	return seen
}

func TestTerminalWebSocketChecksOriginAndBridgesPTY(t *testing.T) {
	fx := newInteractiveFixture(t)
	c := fx.login(fx.private.URL)
	c.stepUp()
	_, ticket := c.ticket(`{"host":"vps","kind":"terminal"}`)
	if _, response, err := c.dial(ticket, "https://evil.example"); err == nil || response == nil || response.StatusCode != 403 {
		t.Fatalf("cross-origin socket accepted: %v", err)
	}
	if _, response, err := c.dial(ticket, fx.private.URL); err == nil || response.StatusCode != 403 {
		t.Fatal("ticket was reusable after a rejected origin")
	}
	_, ticket = c.ticket(`{"host":"vps","kind":"terminal"}`)
	if _, response, err := c.dial(ticket, ""); err == nil || response.StatusCode != 403 {
		t.Fatal("socket without Origin accepted")
	}
	_, ticket = c.ticket(`{"host":"vps","kind":"terminal"}`)
	conn, _, err := c.dial(ticket, fx.private.URL)
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, conn, "fake-shell$")
	ctx := context.Background()
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":132,"rows":43}`)); err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, []byte("segredo\r")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, conn, "eco: segredo")
	if err := conn.Write(ctx, websocket.MessageBinary, []byte("exit\r")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, conn, `"type":"exit"`)
	if got := fx.ssh.PTYs(); len(got) != 1 || got[0] != "xterm-256color 90x20" {
		t.Fatalf("pty = %v", got)
	}
	if got := fx.ssh.Resizes(); len(got) != 1 || got[0] != (sshtest.Resize{Cols: 132, Rows: 43}) {
		t.Fatalf("resizes = %v", got)
	}
	if _, response, err := c.dial(ticket, fx.private.URL); err == nil || response.StatusCode != 403 {
		t.Fatal("ticket reused")
	}
	deadline := time.Now().Add(3 * time.Second)
	for !hasAudit(fx.audit(), "terminal", "", "closed") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	entries := fx.audit()
	if !hasAudit(entries, "terminal", "", "ok") || !hasAudit(entries, "terminal", "", "closed") || !hasAudit(entries, "terminal", "", "denied") {
		t.Fatalf("audit = %+v", entries)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Target+entry.HostID, "segredo") {
			t.Fatalf("audit stored terminal input: %+v", entry)
		}
		if entry.ClientIP == "" {
			t.Fatalf("audit without client IP: %+v", entry)
		}
	}
}

func TestAttachValidatesObservedSessionAndDefaultsToReadOnly(t *testing.T) {
	fx := newInteractiveFixture(t)
	c := fx.login(fx.private.URL)
	c.stepUp()
	for body, want := range map[string]int{
		`{"host":"vps","kind":"attach","session":"outra"}`:                                 404,
		`{"host":"vps","kind":"attach","session":"dev; reboot"}`:                           404,
		`{"host":"vps","kind":"attach","session":"dev","write":true}`:                      400,
		`{"host":"semchave","kind":"attach","session":"dev"}`:                              404,
		`{"host":"vps","kind":"attach","session":"dev","write":true,"confirm_write":true}`: 200,
	} {
		if status, _ := c.ticket(body); status != want {
			t.Fatalf("%s = %d, want %d", body, status, want)
		}
	}
	_, ticket := c.ticket(`{"host":"vps","kind":"attach","session":"dev"}`)
	conn, _, err := c.dial(ticket, fx.private.URL)
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, conn, "fake-shell$")
	_ = conn.Close(websocket.StatusNormalClosure, "")
	commands := fx.ssh.Commands()
	if len(commands) != 1 || commands[0] != `'tmux' 'attach-session' '-r' '-t' '=dev'` {
		t.Fatalf("attach commands = %q", commands)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !hasAudit(fx.audit(), "attach_ro", "dev", "closed") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !hasAudit(fx.audit(), "attach_ro", "dev", "closed") || !hasAudit(fx.audit(), "attach_rw", "dev", "denied") {
		t.Fatalf("audit = %+v", fx.audit())
	}
}

func TestTerminalIdleTimeoutAndSessionLimit(t *testing.T) {
	fx := newInteractiveFixture(t)
	fx.server.MaxSessions = 1
	fx.server.IdleTimeout = 300 * time.Millisecond
	c := fx.login(fx.private.URL)
	c.stepUp()
	_, ticket := c.ticket(`{"host":"vps","kind":"terminal"}`)
	conn, _, err := c.dial(ticket, fx.private.URL)
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, conn, "fake-shell$")
	_, second := c.ticket(`{"host":"vps","kind":"terminal"}`)
	if _, response, err := c.dial(second, fx.private.URL); err == nil || response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second terminal over the limit: %v", err)
	}
	if seen := readUntil(t, conn, `"type":"exit"`); !strings.Contains(seen, "sem digitação") {
		t.Fatalf("idle exit = %q", seen)
	}
}

func TestSnippetRunsFixedArgvWithoutShell(t *testing.T) {
	fx := newInteractiveFixture(t)
	c := fx.login(fx.private.URL)
	if response, _ := c.do("POST", "/api/hosts/vps/snippets/run", `{"name":"literal"}`, true); response.StatusCode != 403 {
		t.Fatalf("snippet without step-up = %d", response.StatusCode)
	}
	c.stepUp()
	if response, _ := c.do("POST", "/api/hosts/vps/snippets/run", `{"name":"literal"}`, false); response.StatusCode != 403 {
		t.Fatalf("snippet without CSRF = %d", response.StatusCode)
	}
	for _, body := range []string{`{"name":"rm -rf /"}`, `{"name":"literal","argv":["id"]}`} {
		if response, _ := c.do("POST", "/api/hosts/vps/snippets/run", body, true); response.StatusCode == 200 {
			t.Fatalf("snippet %s accepted", body)
		}
	}
	response, body := c.do("POST", "/api/hosts/vps/snippets/run", `{"name":"literal"}`, true)
	var result struct {
		Result interactive.Result `json:"result"`
	}
	if err := json.Unmarshal(body, &result); err != nil || response.StatusCode != 200 {
		t.Fatalf("snippet = %d %s", response.StatusCode, body)
	}
	if result.Result.Output != "$(touch pwned)\na; touch pwned\n" || result.Result.ExitCode != 0 {
		t.Fatalf("snippet output = %+v", result.Result)
	}
	if _, err := os.Stat(filepath.Join(fx.ssh.Dir, "pwned")); !os.IsNotExist(err) {
		t.Fatal("snippet argument was interpreted by a shell")
	}
	_, body = c.do("POST", "/api/hosts/vps/snippets/run", `{"name":"falha"}`, true)
	if !strings.Contains(string(body), `"exit_code":4`) {
		t.Fatalf("failing snippet = %s", body)
	}
	entries := fx.audit()
	if !hasAudit(entries, "snippet", "literal", "ok") || !hasAudit(entries, "snippet", "falha", "failed") || !hasAudit(entries, "snippet", "rm -rf /", "denied") {
		t.Fatalf("audit = %+v", entries)
	}
	_, body = c.do("GET", "/api/audit", "", false)
	if strings.Contains(string(body), "parcial") || !strings.Contains(string(body), `"action":"snippet"`) {
		t.Fatalf("audit API = %s", body)
	}
}
