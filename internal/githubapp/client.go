package githubapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Client struct {
	appID         int64
	key           *rsa.PrivateKey
	installations map[string]int64
	baseURL       string
	http          *http.Client
	mu            sync.Mutex
	tokens        map[string]cachedToken
	tokenFlights  map[string]*tokenFlight
	budgets       map[string]rateBudget
}

//go:embed api-version.txt
var apiVersion string

type cachedToken struct {
	value string
	until time.Time
}

type tokenFlight struct {
	done  chan struct{}
	value string
	err   error
}

type Runner struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Busy   bool   `json:"busy"`
}

type WorkflowRun struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	DisplayTitle string `json:"display_title"`
	HTMLURL      string `json:"html_url"`
	Status       string `json:"status"`
	Conclusion   string `json:"conclusion"`
}

type WorkflowJob struct {
	Name       string `json:"name"`
	RunnerName string `json:"runner_name"`
	Status     string `json:"status"`
	HTMLURL    string `json:"html_url"`
}

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)
var allowedLabels = map[string]bool{
	"self-hosted": true, "ubuntu-latest": true, "depot-ubuntu-24.04": true,
	"depot-ubuntu-24.04-4": true, "depot-ubuntu-24.04-8": true, "ubicloud-standard-2": true,
}

func New(appID int64, keyPEM []byte, installations map[string]int64, baseURL string, httpClient *http.Client) (*Client, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("invalid GitHub App private key PEM")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		parsed, pkcs8Err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if pkcs8Err != nil {
			return nil, err
		}
		var ok bool
		key, ok = parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("GitHub App key must be RSA")
		}
	}
	if appID <= 0 {
		return nil, errors.New("GitHub App ID must be positive")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	return &Client{appID: appID, key: key, installations: installations, baseURL: strings.TrimSuffix(baseURL, "/"), http: httpClient, tokens: map[string]cachedToken{}, tokenFlights: map[string]*tokenFlight{}, budgets: map[string]rateBudget{}}, nil
}

func (c *Client) appJWT(now time.Time) (string, error) {
	encode := func(value any) string { b, _ := json.Marshal(value); return base64.RawURLEncoding.EncodeToString(b) }
	unsigned := encode(map[string]string{"alg": "RS256", "typ": "JWT"}) + "." + encode(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": c.appID})
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, c.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (c *Client) request(ctx context.Context, method, path, token string, body any, out any) (int, error) {
	status, _, err := c.requestHeaders(ctx, method, path, token, body, out)
	return status, err
}

func (c *Client) requestHeaders(ctx context.Context, method, path, token string, body any, out any) (int, http.Header, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", strings.TrimSpace(apiVersion))
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return resp.StatusCode, resp.Header, fmt.Errorf("GitHub API %s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(message)))
	}
	if out != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(out); err != nil {
			return resp.StatusCode, resp.Header, err
		}
	}
	return resp.StatusCode, resp.Header, nil
}

func (c *Client) token(ctx context.Context, owner string) (string, error) {
	if c == nil {
		return "", errors.New("GitHub App is not configured")
	}
	id, ok := c.installations[owner]
	if !ok || id <= 0 {
		return "", fmt.Errorf("GitHub App is not installed for %s", owner)
	}
	c.mu.Lock()
	if cached, ok := c.tokens[owner]; ok && time.Now().Before(cached.until) {
		c.mu.Unlock()
		return cached.value, nil
	}
	if flight := c.tokenFlights[owner]; flight != nil {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-flight.done:
			return flight.value, flight.err
		}
	}
	flight := &tokenFlight{done: make(chan struct{})}
	c.tokenFlights[owner] = flight
	c.mu.Unlock()

	value, until, err := c.fetchToken(ctx, id)
	c.mu.Lock()
	if err == nil {
		c.tokens[owner] = cachedToken{value: value, until: until}
	}
	flight.value, flight.err = value, err
	delete(c.tokenFlights, owner)
	close(flight.done)
	c.mu.Unlock()
	return value, err
}

func (c *Client) fetchToken(ctx context.Context, id int64) (string, time.Time, error) {
	jwt, err := c.appJWT(time.Now())
	if err != nil {
		return "", time.Time{}, err
	}
	var result struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	_, err = c.request(ctx, "POST", fmt.Sprintf("/app/installations/%d/access_tokens", id), jwt, map[string]any{}, &result)
	if err != nil {
		return "", time.Time{}, err
	}
	if result.Token == "" {
		return "", time.Time{}, errors.New("GitHub returned an empty installation token")
	}
	until := time.Now().Add(50 * time.Minute)
	if limit := result.ExpiresAt.Add(-5 * time.Minute); !result.ExpiresAt.IsZero() && limit.Before(until) {
		until = limit
	}
	return result.Token, until, nil
}

func (c *Client) repoToken(ctx context.Context, repo string) (string, error) {
	if !repoPattern.MatchString(repo) {
		return "", errors.New("invalid repository name")
	}
	owner, _, _ := strings.Cut(repo, "/")
	return c.token(ctx, owner)
}

func (c *Client) SetVariable(ctx context.Context, repo, name, value string) error {
	if name != "AGENT_RUNNER" && name != "CI_RUNNER" {
		return errors.New("unsupported runner variable")
	}
	if !allowedLabels[value] {
		return fmt.Errorf("unsupported runner label %q", value)
	}
	token, err := c.repoToken(ctx, repo)
	if err != nil {
		return err
	}
	path := "/repos/" + repo + "/actions/variables/" + name
	status, err := c.request(ctx, "PATCH", path, token, map[string]string{"value": value}, nil)
	if status != http.StatusNotFound {
		return err
	}
	_, err = c.request(ctx, "POST", "/repos/"+repo+"/actions/variables", token, map[string]string{"name": name, "value": value}, nil)
	return err
}

func (c *Client) Variable(ctx context.Context, repo, name string) (string, error) {
	if name != "AGENT_RUNNER" && name != "CI_RUNNER" {
		return "", errors.New("unsupported runner variable")
	}
	token, err := c.repoToken(ctx, repo)
	if err != nil {
		return "", err
	}
	var result struct {
		Value string `json:"value"`
	}
	status, err := c.request(ctx, "GET", "/repos/"+repo+"/actions/variables/"+name, token, nil, &result)
	if status == http.StatusNotFound {
		return "", nil
	}
	return result.Value, err
}

func (c *Client) Runners(ctx context.Context, repo string) ([]Runner, error) {
	token, err := c.repoToken(ctx, repo)
	if err != nil {
		return nil, err
	}
	var result struct {
		Runners []Runner `json:"runners"`
	}
	_, err = c.request(ctx, "GET", "/repos/"+repo+"/actions/runners?per_page=100", token, nil, &result)
	return result.Runners, err
}

func (c *Client) Runs(ctx context.Context, repo, status string) ([]WorkflowRun, error) {
	if status != "queued" && status != "in_progress" && status != "failure" {
		return nil, errors.New("unsupported run status")
	}
	token, err := c.repoToken(ctx, repo)
	if err != nil {
		return nil, err
	}
	var result struct {
		WorkflowRuns []WorkflowRun `json:"workflow_runs"`
	}
	_, err = c.request(ctx, "GET", "/repos/"+repo+"/actions/runs?status="+status+"&per_page=100", token, nil, &result)
	return result.WorkflowRuns, err
}

func (c *Client) Jobs(ctx context.Context, repo string, runID int64) ([]WorkflowJob, error) {
	if runID <= 0 {
		return nil, errors.New("invalid run ID")
	}
	token, err := c.repoToken(ctx, repo)
	if err != nil {
		return nil, err
	}
	var result struct {
		Jobs []WorkflowJob `json:"jobs"`
	}
	_, err = c.request(ctx, "GET", fmt.Sprintf("/repos/%s/actions/runs/%d/jobs?per_page=100", repo, runID), token, nil, &result)
	return result.Jobs, err
}
