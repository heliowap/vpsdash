package githubapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func writeToken(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"token":"installation-token","expires_at":"2030-01-01T00:00:00Z"}`))
}

func TestJobLogTailDoesNotSendTokenToSignedURLHost(t *testing.T) {
	log := strings.Repeat("linha antiga\n", 20) + "2026-09-27T10:00:00.0000000Z \x1b[32mok\x1b[0m <script>alert(1)</script>\n"
	var blobAuth, blobRange string
	blob := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		blobAuth, blobRange = r.Header.Get("Authorization"), r.Header.Get("Range")
		var size int
		if _, err := fmt.Sscanf(blobRange, "bytes=-%d", &size); err != nil || size <= 0 {
			t.Errorf("range = %q", blobRange)
		}
		start := len(log) - size
		if start < 0 {
			start = 0
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(log)-1, len(log)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte(log[start:]))
	}))
	defer blob.Close()
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/installations/7/access_tokens":
			writeToken(w)
		case "/repos/heliowap/vpsdash/actions/jobs/99/logs":
			if r.Header.Get("Authorization") != "Bearer installation-token" {
				t.Errorf("logs auth = %q", r.Header.Get("Authorization"))
			}
			http.Redirect(w, r, blob.URL+"/signed/job-99.txt?sig=secret", http.StatusFound)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	tail, err := c.JobLogTail(context.Background(), "heliowap/vpsdash", 99, 120)
	if err != nil {
		t.Fatal(err)
	}
	if blobAuth != "" {
		t.Fatalf("signed URL received Authorization %q", blobAuth)
	}
	if blobRange != "bytes=-120" {
		t.Fatalf("range = %q", blobRange)
	}
	if !tail.Truncated || tail.Size != int64(len(log)) {
		t.Fatalf("tail = %+v", tail)
	}
	if strings.Contains(tail.Text, "\x1b") || strings.HasPrefix(tail.Text, "inha") {
		t.Fatalf("text not sanitized or partial first line kept: %q", tail.Text)
	}
	if !strings.HasSuffix(tail.Text, "ok <script>alert(1)</script>\n") {
		t.Fatalf("text = %q", tail.Text)
	}
}

func TestJobLogTailReportsMissingAndExpiredLogs(t *testing.T) {
	status := http.StatusNotFound
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/installations/7/access_tokens" {
			writeToken(w)
			return
		}
		w.WriteHeader(status)
	})
	if _, err := c.JobLogTail(context.Background(), "heliowap/vpsdash", 5, MaxLogTail); !errors.Is(err, ErrLogUnavailable) {
		t.Fatalf("404 err = %v", err)
	}
	status = http.StatusGone
	if _, err := c.JobLogTail(context.Background(), "heliowap/vpsdash", 5, MaxLogTail); !errors.Is(err, ErrLogExpired) {
		t.Fatalf("410 err = %v", err)
	}
	if _, err := c.JobLogTail(context.Background(), "heliowap/vpsdash", 0, MaxLogTail); err == nil {
		t.Fatal("job ID 0 accepted")
	}
	if _, err := c.JobLogTail(context.Background(), "heliowap/../x", 5, MaxLogTail); err == nil {
		t.Fatal("invalid repository accepted")
	}
}

func TestJobLogTailRejectsRedirectToPlainHTTPFromHTTPSAPI(t *testing.T) {
	c := &Client{baseURL: "https://api.github.com"}
	for _, raw := range []string{"http://blob.example.invalid/x", "ftp://blob.example.invalid/x", "https://user:pw@blob.example.invalid/x"} {
		u, _ := http.NewRequest("GET", raw, nil)
		if err := c.checkLogLocation(u.URL); err == nil {
			t.Fatalf("%s accepted", raw)
		}
	}
	u, _ := http.NewRequest("GET", "https://blob.example.invalid/x?sig=1", nil)
	if err := c.checkLogLocation(u.URL); err != nil {
		t.Fatal(err)
	}
}

func TestJobLogTailKeepsEndWhenRangeIgnored(t *testing.T) {
	log := strings.Repeat("x", 100<<10) + "\nfim do job\n"
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/installations/7/access_tokens" {
			writeToken(w)
			return
		}
		_, _ = w.Write([]byte(log))
	})
	tail, err := c.JobLogTail(context.Background(), "heliowap/vpsdash", 5, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if tail.Text != "fim do job\n" || !tail.Truncated || tail.Size != int64(len(log)) {
		t.Fatalf("tail = %q %+v", tail.Text, tail.Size)
	}
}

func TestJobUsesRepositoryPath(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/installations/7/access_tokens":
			writeToken(w)
		case "/repos/heliowap/vpsdash/actions/jobs/12":
			_, _ = w.Write([]byte(`{"id":12,"run_id":3,"name":"build","status":"in_progress","conclusion":null,"started_at":"2026-09-27T10:00:00Z","completed_at":null}`))
		default:
			http.NotFound(w, r)
		}
	})
	job, err := c.Job(context.Background(), "heliowap/vpsdash", 12)
	if err != nil || job.Name != "build" || job.RunID != 3 || !job.CompletedAt.IsZero() {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	if _, err := c.Job(context.Background(), "heliowap/vpsdash", 13); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign job err = %v", err)
	}
}

func TestPlainLogRemovesTerminalControls(t *testing.T) {
	raw := []byte("\uFEFFa\x1b[1;31mred\x1b[0m\x1b]0;title\x07b\r\nc\x00d\u202Ee\xff\n")
	if got := PlainLog(raw); got != "aredb\ncde\uFFFD\n" {
		t.Fatalf("PlainLog = %q", got)
	}
}
