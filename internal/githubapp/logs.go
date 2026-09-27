package githubapp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// MaxLogTail bounds the log bytes returned for one job. The panel shows the
// end of the log; the full file stays on GitHub.
const MaxLogTail = 64 << 10

// maxLogStream bounds a download when the log host ignores the Range header.
const maxLogStream = 32 << 20

// logDownloadTimeout bounds the signed-URL download, so a slow log host
// releases the caller's log slot well before the shared client timeout.
var logDownloadTimeout = 10 * time.Second

var (
	ErrNotFound       = errors.New("GitHub resource not found")
	ErrLogUnavailable = errors.New("job log is not available yet")
	ErrLogExpired     = errors.New("job log is no longer available")
	ErrLogTooLarge    = errors.New("job log exceeds the download bound")
)

// LogTail is the end of a job log as plain text. Size is the full log size in
// bytes when the log host reported it.
type LogTail struct {
	Text      string
	Size      int64
	Truncated bool
}

// RecentRuns lists the latest workflow runs of a repository in any state.
func (c *Client) RecentRuns(ctx context.Context, repo string, limit int) ([]WorkflowRun, error) {
	if limit <= 0 || limit > 20 {
		return nil, errors.New("unsupported run limit")
	}
	token, err := c.repoToken(ctx, repo)
	if err != nil {
		return nil, err
	}
	var result struct {
		WorkflowRuns []WorkflowRun `json:"workflow_runs"`
	}
	_, err = c.request(ctx, "GET", fmt.Sprintf("/repos/%s/actions/runs?per_page=%d&exclude_pull_requests=true", repo, limit), token, nil, &result)
	return result.WorkflowRuns, err
}

// Job reads one job through the repository path, so a job ID from another
// repository is reported as ErrNotFound.
func (c *Client) Job(ctx context.Context, repo string, jobID int64) (WorkflowJob, error) {
	if jobID <= 0 {
		return WorkflowJob{}, errors.New("invalid job ID")
	}
	token, err := c.repoToken(ctx, repo)
	if err != nil {
		return WorkflowJob{}, err
	}
	var job WorkflowJob
	status, err := c.request(ctx, "GET", fmt.Sprintf("/repos/%s/actions/jobs/%d", repo, jobID), token, nil, &job)
	if status == http.StatusNotFound {
		return WorkflowJob{}, ErrNotFound
	}
	if err == nil && job.ID != jobID {
		return WorkflowJob{}, errors.New("GitHub returned a different job")
	}
	return job, err
}

func (c *Client) withoutRedirects() *http.Client {
	client := *c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}

// JobLogTail returns up to maxBytes from the end of a job log. GitHub answers
// the logs endpoint with a redirect to a signed URL; that URL is fetched
// without the installation token and with a suffix Range request.
func (c *Client) JobLogTail(ctx context.Context, repo string, jobID int64, maxBytes int) (LogTail, error) {
	if jobID <= 0 {
		return LogTail{}, errors.New("invalid job ID")
	}
	if maxBytes <= 0 || maxBytes > MaxLogTail {
		maxBytes = MaxLogTail
	}
	token, err := c.repoToken(ctx, repo)
	if err != nil {
		return LogTail{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/repos/%s/actions/jobs/%d/logs", c.baseURL, repo, jobID), nil)
	if err != nil {
		return LogTail{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", strings.TrimSpace(apiVersion))
	req.Header.Set("Authorization", "Bearer "+token)
	client := c.withoutRedirects()
	resp, err := client.Do(req)
	if err != nil {
		return LogTail{}, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		defer resp.Body.Close()
		return readLogTail(resp, maxBytes)
	case http.StatusNotFound:
		resp.Body.Close()
		return LogTail{}, ErrLogUnavailable
	case http.StatusGone:
		resp.Body.Close()
		return LogTail{}, ErrLogExpired
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
	default:
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		resp.Body.Close()
		return LogTail{}, fmt.Errorf("GitHub API GET job %d logs: %d %s", jobID, resp.StatusCode, strings.TrimSpace(string(message)))
	}
	location, err := resp.Location()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	if err != nil {
		return LogTail{}, fmt.Errorf("GitHub log redirect: %w", err)
	}
	if err := c.checkLogLocation(location); err != nil {
		return LogTail{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, logDownloadTimeout)
	defer cancel()
	blob, err := http.NewRequestWithContext(ctx, "GET", location.String(), nil)
	if err != nil {
		return LogTail{}, err
	}
	blob.Header.Set("Range", "bytes=-"+strconv.Itoa(maxBytes))
	blobResp, err := client.Do(blob)
	if err != nil {
		return LogTail{}, fmt.Errorf("GitHub log download failed: %w", redactURLError(err))
	}
	defer blobResp.Body.Close()
	switch blobResp.StatusCode {
	case http.StatusOK, http.StatusPartialContent:
		return readLogTail(blobResp, maxBytes)
	case http.StatusRequestedRangeNotSatisfiable:
		return LogTail{}, nil // empty log
	case http.StatusNotFound:
		return LogTail{}, ErrLogUnavailable
	case http.StatusGone:
		return LogTail{}, ErrLogExpired
	default:
		return LogTail{}, fmt.Errorf("GitHub log download: %d", blobResp.StatusCode)
	}
}

// checkLogLocation accepts only absolute HTTPS log URLs. Plain HTTP is allowed
// only when the API itself is plain HTTP (test servers).
func (c *Client) checkLogLocation(location *url.URL) error {
	if location.Host == "" || location.User != nil {
		return errors.New("GitHub log redirect is not an absolute URL")
	}
	if location.Scheme == "https" || (location.Scheme == "http" && strings.HasPrefix(c.baseURL, "http://")) {
		return nil
	}
	return errors.New("GitHub log redirect must use HTTPS")
}

// redactURLError drops the signed URL from transport errors so it never
// reaches logs or responses.
func redactURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

var contentRange = regexp.MustCompile(`^bytes (\d+)-(\d+)/(\d+)$`)

func readLogTail(resp *http.Response, maxBytes int) (LogTail, error) {
	if resp.StatusCode == http.StatusPartialContent {
		match := contentRange.FindStringSubmatch(strings.TrimSpace(resp.Header.Get("Content-Range")))
		if match == nil {
			return LogTail{}, errors.New("GitHub log download: invalid Content-Range")
		}
		start, _ := strconv.ParseInt(match[1], 10, 64)
		total, _ := strconv.ParseInt(match[3], 10, 64)
		data, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)))
		if err != nil {
			return LogTail{}, err
		}
		return newLogTail(data, total, start > 0), nil
	}
	// The host ignored Range: keep only the end while streaming, within a bound.
	if resp.ContentLength > maxLogStream {
		return LogTail{}, ErrLogTooLarge
	}
	var tail []byte
	var total int64
	buf := make([]byte, 32<<10)
	body := io.LimitReader(resp.Body, maxLogStream+1)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			total += int64(n)
			tail = append(tail, buf[:n]...)
			if len(tail) > 2*maxBytes {
				tail = append(tail[:0], tail[len(tail)-maxBytes:]...)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return LogTail{}, err
		}
	}
	if total > maxLogStream {
		return LogTail{}, ErrLogTooLarge
	}
	truncated := total > int64(maxBytes)
	if len(tail) > maxBytes {
		tail = tail[len(tail)-maxBytes:]
	}
	return newLogTail(tail, total, truncated), nil
}

func newLogTail(data []byte, size int64, truncated bool) LogTail {
	if truncated {
		// Drop the partial first line cut by the byte bound, unless the bound
		// fell inside the last line.
		if i := bytes.IndexByte(data, '\n'); i >= 0 && i < len(data)-1 {
			data = data[i+1:]
		}
	}
	return LogTail{Text: PlainLog(data), Size: size, Truncated: truncated}
}

var terminalEscape = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)?|\x1b[P^_][^\x1b]*(?:\x1b\\)?|\x1b[ -/]*[0-~]?`)

// PlainLog turns raw runner output into plain text: invalid UTF-8 is replaced,
// terminal escape sequences and control characters other than newline and tab
// are removed.
func PlainLog(raw []byte) string {
	text := strings.ToValidUTF8(string(raw), "\uFFFD")
	text = strings.TrimPrefix(text, "\uFEFF")
	text = terminalEscape.ReplaceAllString(text, "")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r == '\r':
			return '\n'
		case r == '\uFEFF' || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) && r != '\u200d':
			return -1
		}
		return r
	}, text)
}
