package alerts

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/heliowap/vpsdash/internal/store"
)

type Config struct {
	Host     string
	Port     int
	User     string
	Password string
	To       string
	Auth     string
}

type Mailer struct {
	Config Config
	Store  *store.Store
}

func FromEnv(values map[string]string, s *store.Store) (*Mailer, error) {
	if len(values) == 0 {
		return nil, nil
	}
	port := 587
	if values["SMTP_PORT"] != "" {
		parsed, err := strconv.Atoi(values["SMTP_PORT"])
		if err != nil {
			return nil, err
		}
		port = parsed
	}
	c := Config{Host: values["SMTP_HOST"], Port: port, User: values["SMTP_USER"], Password: values["SMTP_PASSWORD"], To: values["SMTP_TO"], Auth: strings.ToLower(values["SMTP_AUTH"])}
	if c.Host == "" || c.User == "" || c.Password == "" || c.To == "" {
		return nil, errors.New("SMTP credentials are incomplete")
	}
	if c.Auth == "" {
		c.Auth = "plain"
	}
	if c.Auth != "plain" && c.Auth != "login" {
		return nil, errors.New("SMTP_AUTH must be plain or login")
	}
	return &Mailer{Config: c, Store: s}, nil
}

func (m *Mailer) Start(ctx context.Context) {
	if m == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			if err := m.SendPending(ctx); err != nil {
				log.Printf("smtp: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (m *Mailer) SendPending(ctx context.Context) error {
	if m == nil {
		return nil
	}
	items, err := m.Store.PendingAlerts(ctx)
	if err != nil {
		return err
	}
	for _, item := range items {
		if err := m.send(item); err != nil {
			return err
		}
		if err := m.Store.MarkAlertSent(ctx, item.ID, time.Now()); err != nil {
			return err
		}
	}
	return nil
}

func (m *Mailer) send(item store.Alert) error {
	cfg := m.Config
	address := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var client *smtp.Client
	var err error
	if cfg.Port == 465 {
		var conn *tls.Conn
		conn, err = tls.DialWithDialer(dialer, "tcp", address, &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12})
		if err == nil {
			client, err = smtp.NewClient(conn, cfg.Host)
		}
	} else {
		var conn net.Conn
		conn, err = dialer.Dial("tcp", address)
		if err == nil {
			client, err = smtp.NewClient(conn, cfg.Host)
		}
		if err == nil {
			if ok, _ := client.Extension("STARTTLS"); !ok {
				_ = client.Close()
				return errors.New("SMTP server does not offer STARTTLS")
			}
			err = client.StartTLS(&tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12})
		}
	}
	if err != nil {
		return err
	}
	defer client.Close()
	var auth smtp.Auth = smtp.PlainAuth("", cfg.User, cfg.Password, cfg.Host)
	if cfg.Auth == "login" {
		auth = &loginAuth{user: cfg.User, password: cfg.Password}
	}
	if err := client.Auth(auth); err != nil {
		return err
	}
	if err := client.Mail(cfg.User); err != nil {
		return err
	}
	if err := client.Rcpt(cfg.To); err != nil {
		return err
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	subject := mime.QEncoding.Encode("utf-8", strings.ReplaceAll(strings.ReplaceAll(item.Subject, "\r", " "), "\n", " "))
	message := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n", cfg.User, cfg.To, subject, strings.ReplaceAll(item.Body, "\n", "\r\n"))
	if _, err := io.WriteString(w, message); err != nil {
		_ = w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return client.Quit()
}

type loginAuth struct {
	user, password string
	step           int
}

func (a *loginAuth) Start(_ *smtp.ServerInfo) (string, []byte, error) { return "LOGIN", nil, nil }
func (a *loginAuth) Next(_ []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	a.step++
	if a.step == 1 {
		return []byte(a.user), nil
	}
	if a.step == 2 {
		return []byte(a.password), nil
	}
	return nil, errors.New("unexpected SMTP LOGIN challenge")
}
