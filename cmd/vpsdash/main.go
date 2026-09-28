package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/heliowap/vpsdash/internal/alerts"
	"github.com/heliowap/vpsdash/internal/api"
	"github.com/heliowap/vpsdash/internal/auth"
	"github.com/heliowap/vpsdash/internal/collect"
	"github.com/heliowap/vpsdash/internal/config"
	"github.com/heliowap/vpsdash/internal/files"
	"github.com/heliowap/vpsdash/internal/githubapp"
	"github.com/heliowap/vpsdash/internal/interactive"
	"github.com/heliowap/vpsdash/internal/store"
	"github.com/heliowap/vpsdash/internal/web"
	"github.com/heliowap/vpsdash/internal/webpush"
	"golang.org/x/term"
)

var version = "0.1.0-dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Printf("vpsdash %s\n", version)
		return
	}
	var err error
	if len(os.Args) > 1 && os.Args[1] == "init-auth" {
		err = initAuth(os.Args[2:])
	} else if len(os.Args) > 1 && os.Args[1] == "init-webpush" {
		err = initWebPush(os.Args[2:])
	} else if len(os.Args) > 1 && os.Args[1] == "check-config" {
		err = checkConfig(os.Args[2:])
	} else {
		err = serve(os.Args[1:])
	}
	if err != nil {
		log.Fatal(err)
	}
}

func checkConfig(args []string) error {
	flags := flag.NewFlagSet("check-config", flag.ContinueOnError)
	path := flags.String("config", defaultPath("config.json"), "inventory configuration")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if _, err := config.Load(*path); err != nil {
		return fmt.Errorf("inventário %s: %w", *path, err)
	}
	fmt.Println("Inventário válido.")
	return nil
}

func defaultPath(name string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "vpsdash", name)
}

func initAuth(args []string) error {
	flags := flag.NewFlagSet("init-auth", flag.ContinueOnError)
	out := flags.String("out", defaultPath("auth.env"), "private auth environment file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("init-auth requires a terminal")
	}
	fmt.Fprint(os.Stderr, "Nova senha (mínimo 12 caracteres): ")
	first, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return err
	}
	fmt.Fprint(os.Stderr, "Confirme a senha: ")
	second, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return err
	}
	if string(first) != string(second) {
		return errors.New("passwords differ")
	}
	hash, err := auth.HashPassword(string(first))
	if err != nil {
		return err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0700); err != nil {
		return err
	}
	if _, err := os.Stat(*out); err == nil {
		return errors.New("auth file already exists")
	}
	content := "VPSDASH_PASSWORD_HASH=" + hash + "\nVPSDASH_SESSION_KEY=" + base64.RawStdEncoding.EncodeToString(key) + "\n"
	if err := os.WriteFile(*out, []byte(content), 0600); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Credenciais salvas em %s (0600).\n", *out)
	return nil
}

// initWebPush writes a new VAPID key pair. It never replaces an existing
// file: new keys invalidate every browser subscription.
func initWebPush(args []string) error {
	flags := flag.NewFlagSet("init-webpush", flag.ContinueOnError)
	out := flags.String("out", defaultPath("webpush.env"), "private Web Push environment file")
	subject := flags.String("subject", "", "VAPID contact, mailto: or https: URI")
	if err := flags.Parse(args); err != nil {
		return err
	}
	private, public, err := webpush.GenerateKeys()
	if err != nil {
		return err
	}
	if _, err := webpush.ParseKeys(private, public, *subject); err != nil {
		return fmt.Errorf("informe -subject mailto:voce@example.com ou https://painel.example.com: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return errors.New("web push file already exists; new keys would invalidate every subscribed device")
	}
	if err != nil {
		return err
	}
	content := "VAPID_SUBJECT=" + *subject + "\nVAPID_PUBLIC_KEY=" + public + "\nVAPID_PRIVATE_KEY=" + private + "\n"
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Chaves VAPID salvas em %s (0600). Reinicie o serviço.\n", *out)
	return nil
}

func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := flags.String("config", defaultPath("config.json"), "inventory configuration")
	authPath := flags.String("auth-env", defaultPath("auth.env"), "private authentication environment file")
	githubPath := flags.String("github-env", defaultPath("github-app.env"), "private GitHub App environment file")
	smtpPath := flags.String("smtp-env", defaultPath("smtp.env"), "private SMTP environment file")
	webpushPath := flags.String("webpush-env", defaultPath("webpush.env"), "private Web Push (VAPID) environment file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer st.Close()
	authVars, err := config.ReadEnvFile(*authPath)
	if err != nil {
		return err
	}
	key, err := base64.RawStdEncoding.DecodeString(authVars["VPSDASH_SESSION_KEY"])
	if err != nil {
		return err
	}
	a, err := auth.New(authVars["VPSDASH_PASSWORD_HASH"], key)
	if err != nil {
		return err
	}
	gh, err := loadGitHub(*githubPath)
	if err != nil {
		return err
	}
	mailer, err := loadMailer(*smtpPath, st)
	if err != nil {
		return err
	}
	push, err := loadWebPush(*webpushPath, st)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	collector := collect.New(cfg, st, gh)
	if err := collector.Start(ctx); err != nil {
		return err
	}
	defer collector.Close()
	mailer.Start(ctx)
	push.Start(ctx)
	server := api.New(cfg, st, collector, gh, a)
	server.SMTPProvisioned = mailer != nil
	server.Push = push
	server.Static = http.FS(web.Dist())
	fileExecutor := collect.NewExecutor()
	defer fileExecutor.Close()
	server.Files = &files.Service{Config: cfg, Remote: fileExecutor}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	// Interactive sessions use each host's interactive_key_file and the same
	// known_hosts as the collector. Their routes exist only on private_listen.
	server.Interactive = &interactive.Dialer{KnownHostsFile: filepath.Join(home, ".ssh", "known_hosts")}
	type endpoint struct {
		name    string
		address string
		handler http.Handler
	}
	endpoints := []endpoint{{"public", cfg.Listen, server.Handler()}}
	if cfg.PrivateListen != "" {
		endpoints = append(endpoints, endpoint{"private", cfg.PrivateListen, server.PrivateHandler()})
	} else {
		log.Printf("private_listen not set: terminal, tmux attach, and snippets are disabled")
	}
	servers := make([]*http.Server, 0, len(endpoints))
	errs := make(chan error, len(endpoints))
	for _, e := range endpoints {
		listener, err := net.Listen("tcp", e.address)
		if err != nil {
			for _, started := range servers {
				_ = started.Close()
			}
			return err
		}
		httpServer := &http.Server{Handler: e.handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 90 * time.Second}
		servers = append(servers, httpServer)
		log.Printf("vpsdash %s listening on %s (%s)", version, e.address, e.name)
		go func() { errs <- httpServer.Serve(listener) }()
	}
	shutdown := func() {
		ctx, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		for _, httpServer := range servers {
			_ = httpServer.Shutdown(ctx)
		}
	}
	select {
	case <-ctx.Done():
		shutdown()
		return nil
	case err := <-errs:
		shutdown()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

type githubAPI interface {
	collect.FleetAPI
	api.GitHub
}

func loadGitHub(path string) (githubAPI, error) {
	vars, err := config.ReadEnvFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	id, err := strconv.ParseInt(vars["GITHUB_APP_ID"], 10, 64)
	if err != nil {
		return nil, err
	}
	keyFile := vars["GITHUB_PRIVATE_KEY_FILE"]
	if keyFile == "" {
		return nil, errors.New("GITHUB_PRIVATE_KEY_FILE is missing")
	}
	info, err := os.Stat(keyFile)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("GitHub private key must be mode 0600")
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, err
	}
	var installations map[string]int64
	if err := json.Unmarshal([]byte(vars["GITHUB_INSTALLATIONS_JSON"]), &installations); err != nil {
		return nil, fmt.Errorf("GITHUB_INSTALLATIONS_JSON: %w", err)
	}
	return githubapp.New(id, keyPEM, installations, "", nil)
}

func loadMailer(path string, st *store.Store) (*alerts.Mailer, error) {
	vars, err := config.ReadEnvFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return alerts.FromEnv(vars, st)
}

// loadWebPush returns nil when webpush.env does not exist; the panel then
// reports that VAPID keys are not configured.
func loadWebPush(path string, st *store.Store) (*webpush.Service, error) {
	vars, err := config.ReadEnvFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	keys, err := webpush.FromEnv(vars)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return webpush.New(keys, st), nil
}
