package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heliowap/vpsdash/internal/auth"
	"github.com/heliowap/vpsdash/internal/config"
	"github.com/heliowap/vpsdash/internal/files"
	"github.com/heliowap/vpsdash/internal/store"
)

func TestFileEndpointsServeConfinedReads(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "Projetos")
	for name, data := range map[string]string{
		"readme.md":   "# Painel\n",
		".env":        "TOKEN=não-exibir\n",
		"app/main.go": "package main\n",
		"image.bin":   "PNG\x00\x01",
		"large.log":   strings.Repeat("linha\n", files.ReadLimit/6+100),
		"../fora.txt": "fora\n",
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("../fora.txt", filepath.Join(root, "atalho")); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(filepath.Join(t.TempDir(), "vpsdash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	hash, err := auth.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.New(hash, []byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Hosts: []config.Host{
		{ID: "local", TailnetName: "local.example.invalid", Kind: "vps", Local: true, FileRoots: []string{root}},
		{ID: "sem-raiz", TailnetName: "other.example.invalid", Kind: "vps", Local: true},
	}}
	server := New(cfg, s, nil, nil, a)
	server.Files = &files.Service{Config: cfg}
	h := server.Handler()
	request := func(method, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	list := func(host, path string) string {
		return "/api/hosts/" + host + "/files?path=" + url.QueryEscape(path)
	}
	read := func(host, path, offset string) string {
		return "/api/hosts/" + host + "/file?path=" + url.QueryEscape(path) + "&offset=" + offset
	}
	if got := request("GET", list("local", root), nil).Code; got != 401 {
		t.Fatalf("unauthenticated listing = %d", got)
	}
	login := httptest.NewRequest("POST", "/api/login", bytes.NewReader([]byte(`{"password":"correct horse battery staple"}`)))
	loginResponse := httptest.NewRecorder()
	h.ServeHTTP(loginResponse, login)
	if loginResponse.Code != 200 {
		t.Fatalf("login = %d", loginResponse.Code)
	}
	cookie := loginResponse.Result().Cookies()[0]

	var dashboard struct {
		FileRoots map[string][]string `json:"file_roots"`
	}
	if err := json.Unmarshal(request("GET", "/api/dashboard", cookie).Body.Bytes(), &dashboard); err != nil {
		t.Fatal(err)
	}
	if len(dashboard.FileRoots) != 1 || dashboard.FileRoots["local"][0] != root {
		t.Fatalf("dashboard file roots = %v", dashboard.FileRoots)
	}

	response := request("GET", list("local", root), cookie)
	var listing files.Listing
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &listing) != nil {
		t.Fatalf("listing = %d %s", response.Code, response.Body)
	}
	blocked := map[string]string{}
	for _, entry := range listing.Entries {
		if entry.Blocked {
			blocked[entry.Name] = entry.Reason
		}
	}
	if blocked[".env"] != "secret" || blocked["atalho"] != files.CodeOutside || len(blocked) != 2 {
		t.Fatalf("blocked entries = %v", blocked)
	}

	response = request("GET", read("local", root+"/readme.md", "0"), cookie)
	var content files.Content
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &content) != nil || content.Content != "# Painel\n" {
		t.Fatalf("readme = %d %s", response.Code, response.Body)
	}
	response = request("GET", read("local", root+"/large.log", "0"), cookie)
	if json.Unmarshal(response.Body.Bytes(), &content) != nil || !content.Truncated || content.Length != files.ReadLimit {
		t.Fatalf("large page = %d truncated=%v length=%d", response.Code, content.Truncated, content.Length)
	}
	response = request("GET", read("local", root+"/image.bin", "0"), cookie)
	if json.Unmarshal(response.Body.Bytes(), &content) != nil || !content.Binary || content.Content != "" {
		t.Fatalf("binary = %d %s", response.Code, response.Body)
	}

	for _, tc := range []struct {
		name, path string
		status     int
		code       string
	}{
		{"secret", read("local", root+"/.env", "0"), 403, files.CodeBlocked},
		{"symlink escape", read("local", root+"/atalho", "0"), 403, files.CodeOutside},
		{"dot dot", read("local", root+"/../fora.txt", "0"), 400, files.CodeInvalid},
		{"outside roots", list("local", "/etc"), 403, files.CodeOutside},
		{"missing", read("local", root+"/nada.txt", "0"), 404, files.CodeNotFound},
		{"directory read", read("local", root+"/app", "0"), 400, files.CodeNotFile},
		{"file listing", list("local", root+"/readme.md"), 400, files.CodeNotDir},
		{"bad offset", read("local", root+"/readme.md", "-5"), 400, files.CodeInvalid},
		{"huge offset", read("local", root+"/readme.md", "1000000000000000"), 400, files.CodeInvalid},
		{"no roots", list("sem-raiz", root), 403, files.CodeDisabled},
		{"unknown host", list("desconhecido", root), 404, "unknown_host"},
	} {
		response := request("GET", tc.path, cookie)
		var body struct{ Error, Code string }
		_ = json.Unmarshal(response.Body.Bytes(), &body)
		if response.Code != tc.status || body.Code != tc.code || body.Error == "" {
			t.Errorf("%s = %d %s", tc.name, response.Code, response.Body)
		}
		if strings.Contains(response.Body.String(), "não-exibir") {
			t.Errorf("%s leaked secret content", tc.name)
		}
	}
	for _, method := range []string{"POST", "PUT", "DELETE"} {
		if got := request(method, read("local", root+"/readme.md", "0"), cookie).Code; got != http.StatusMethodNotAllowed && got != http.StatusForbidden {
			t.Errorf("%s file = %d", method, got)
		}
	}
}
