// Package files implements read-only file browsing confined to the roots
// configured for each host. Remote hosts enforce the same rules in
// scripts/ssh-readonly.py; keep both implementations identical.
package files

import (
	"strings"
	"unicode/utf8"
)

const (
	// ReadLimit caps one page of file content.
	ReadLimit = 512 * 1024
	// ListLimit caps the entries returned for one directory.
	ListLimit = 1000
)

// Denial codes shared with the SSH bridge.
const (
	CodeDisabled      = "disabled"
	CodeRootsInsecure = "roots_insecure"
	CodeInvalid       = "invalid"
	CodeOutside       = "outside"
	CodeBlocked       = "blocked"
	CodeNotFound      = "not_found"
	CodeNotDir        = "not_dir"
	CodeNotFile       = "not_file"
	CodeUnreadable    = "unreadable"
)

var secretNames = map[string]bool{
	".ssh": true, ".gnupg": true, ".git": true, ".aws": true, ".azure": true, ".kube": true, ".docker": true,
	".password-store": true, ".netrc": true, ".npmrc": true, ".pypirc": true, ".pgpass": true, ".htpasswd": true,
	".vault-token": true, "shadow": true, "gshadow": true,
}

var secretSuffixes = []string{".pem", ".key", ".p12", ".pfx", ".jks", ".keystore", ".kdbx", ".gpg", ".env"}
var secretWords = []string{"secret", "credential", "password", "passwd"}

// SecretName reports whether a path component looks like it holds secrets.
// Such entries stay visible in listings, marked blocked, and are never read.
func SecretName(name string) bool {
	lower := strings.ToLower(name)
	if secretNames[lower] || strings.HasPrefix(lower, ".env") || strings.HasPrefix(lower, "id_") || strings.HasSuffix(lower, "_history") {
		return true
	}
	for _, suffix := range secretSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	for _, word := range secretWords {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return false
}

// CleanPath accepts only absolute, already-normalized paths: no empty, "."
// or ".." components and no NUL. The root directory itself is refused.
func CleanPath(path string) bool {
	if !strings.HasPrefix(path, "/") || path == "/" || len(path) > 4096 || strings.ContainsRune(path, 0) || !utf8.ValidString(path) {
		return false
	}
	for _, part := range strings.Split(path, "/")[1:] {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func under(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+"/")
}

func relativeParts(path, root string) []string {
	var parts []string
	for _, part := range strings.Split(strings.TrimPrefix(path, root), "/") {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

// UnderRoots reports whether path is lexically inside one of roots.
func UnderRoots(path string, roots []string) bool {
	for _, root := range roots {
		if under(path, root) {
			return true
		}
	}
	return false
}

func displayName(name string) (string, bool) {
	if utf8.ValidString(name) {
		return name, true
	}
	fixed := strings.ToValidUTF8(name, "�")
	for strings.Contains(fixed, "��") {
		fixed = strings.ReplaceAll(fixed, "��", "�")
	}
	return fixed, false
}

// Entry is one directory item. Blocked entries carry no size or time.
type Entry struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Size    int64  `json:"size"`
	Mtime   int64  `json:"mtime"`
	Link    bool   `json:"link"`
	Blocked bool   `json:"blocked"`
	Reason  string `json:"reason"`
}

type Listing struct {
	Path      string  `json:"path"`
	Entries   []Entry `json:"entries"`
	Truncated bool    `json:"truncated"`
}

// Content is one page of a file. Binary pages carry no content; truncated
// means bytes remain after NextOffset.
type Content struct {
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	Mtime      int64  `json:"mtime"`
	Offset     int64  `json:"offset"`
	Length     int64  `json:"length"`
	NextOffset int64  `json:"next_offset"`
	Truncated  bool   `json:"truncated"`
	Binary     bool   `json:"binary"`
	Content    string `json:"content"`
}

// Denied is a refusal with a code shared by both implementations.
type Denied struct{ Code string }

func (d *Denied) Error() string { return "file access denied: " + d.Code }

func deny(code string) error { return &Denied{Code: code} }
