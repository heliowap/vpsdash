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
	"github.com/heliowap/vpsdash/internal/githubapp"
	"github.com/heliowap/vpsdash/internal/store"
	"github.com/heliowap/vpsdash/internal/web"
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
	} else {
		err = serve(os.Args[1:])
	}
	if err != nil {
		log.Fatal(err)
	}
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

func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := flags.String("config", defaultPath("config.json"), "inventory configuration")
	authPath := flags.String("auth-env", defaultPath("auth.env"), "private authentication environment file")
	githubPath := flags.String("github-env", defaultPath("github-app.env"), "private GitHub App environment file")
	smtpPath := flags.String("smtp-env", defaultPath("smtp.env"), "private SMTP environment file")
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	collector := collect.New(cfg, st, gh)
	if err := collector.Start(ctx); err != nil {
		return err
	}
	defer collector.Close()
	mailer.Start(ctx)
	server := api.New(cfg, st, collector, gh, a)
	server.SMTPProvisioned = mailer != nil
	server.Static = http.FS(web.Dist())
	httpServer := &http.Server{Addr: cfg.Listen, Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 90 * time.Second}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	log.Printf("vpsdash %s listening on %s", version, cfg.Listen)
	go func() {
		<-ctx.Done()
		shutdown, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_ = httpServer.Shutdown(shutdown)
	}()
	err = httpServer.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
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
