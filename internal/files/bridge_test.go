package files

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

// fixture is a tree with secrets, symlink escapes, binary, oversized and
// non-UTF-8 content. Both implementations must give identical answers.
type fixture struct {
	base, root, rootsFile string
}

func buildFixture(t *testing.T) fixture {
	t.Helper()
	base := t.TempDir()
	real := filepath.Join(base, "projects")
	write := func(rel string, data []byte) {
		t.Helper()
		path := filepath.Join(base, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, rel string) {
		t.Helper()
		if err := os.Symlink(target, filepath.Join(base, rel)); err != nil {
			t.Fatal(err)
		}
	}
	write("outside/notes.txt", []byte("fora da raiz\n"))
	write("projects/readme.md", []byte("# Olá, operador\n"))
	write("projects/src/main.go", []byte("package main\n"))
	write("projects/.github/workflows/ci.yml", []byte("on: push\n"))
	write("projects/.gitignore", []byte("bin/\n"))
	write("projects/empty.txt", nil)
	for _, secret := range []string{".env", ".env.production", ".envrc", "config/app.env", "certs/server.pem", "certs/tls.KEY", "id_ed25519", "db_password.txt", "client_secret.json", "aws-credentials", ".git/HEAD", ".git/config", ".ssh/authorized_keys", ".bash_history", ".netrc", "keys.p12"} {
		write("projects/"+secret, []byte("TOKEN=não-exibir\n"))
	}
	write("projects/binary.bin", []byte("ELF\x00\x01\x02"))
	write("projects/latin1.txt", []byte("caf\xe9\n"))
	// A three-byte character straddles the first page boundary.
	big := append(bytes.Repeat([]byte("a"), ReadLimit-1), []byte("€ fim\n")...)
	big = append(big, bytes.Repeat([]byte("b"), 4096)...)
	write("projects/big.txt", big)
	write("projects/bad\xff\xfename.txt", []byte("x"))
	link("../outside", "projects/escape")
	link("../outside/notes.txt", "projects/escape-file")
	link(filepath.Join(base, "outside", "notes.txt"), "projects/absolute-escape")
	link("src/main.go", "projects/inside-link")
	link(".env", "projects/secret-link")
	link("missing", "projects/broken")
	link("projects", "rootlink")
	if err := syscall.Mkfifo(filepath.Join(real, "pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(base, "config")
	if err := os.Mkdir(config, 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "rootlink") // A symlinked root, as operators often configure.
	rootsFile := filepath.Join(config, "roots")
	if err := os.WriteFile(rootsFile, []byte("# raízes\n"+root+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return fixture{base: base, root: root, rootsFile: rootsFile}
}

func runBridge(t *testing.T, rootsFile, command string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command("python3", "-I", filepath.Join("..", "..", "scripts", "ssh-readonly.py"), "--file-roots", rootsFile)
	cmd.Env = append(os.Environ(), "SSH_ORIGINAL_COMMAND="+command)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil {
		return append(stdout.Bytes(), stderr.Bytes()...), err
	}
	if stderr.Len() > 0 {
		t.Fatalf("bridge wrote to stderr: %s", stderr.Bytes())
	}
	return stdout.Bytes(), nil
}

func bridgeList(t *testing.T, f fixture, path string) (Listing, error) {
	output, err := runBridge(t, f.rootsFile, ListCommand(path))
	if err != nil {
		t.Fatalf("bridge list %s: %v: %s", path, err, output)
	}
	var listing Listing
	return listing, DecodeBridge(output, &listing)
}

func bridgeRead(t *testing.T, f fixture, path string, offset int64) (Content, error) {
	output, err := runBridge(t, f.rootsFile, ReadCommand(path, offset))
	if err != nil {
		t.Fatalf("bridge read %s: %v: %s", path, err, output)
	}
	var content Content
	return content, DecodeBridge(output, &content)
}

func code(err error) string {
	var denied *Denied
	if errors.As(err, &denied) {
		return denied.Code
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

type fileCase struct {
	name   string
	list   bool
	path   string
	offset int64
	want   string // denial code, or "" for success
}

func fileCases(f fixture) []fileCase {
	r := f.root
	return []fileCase{
		{"list root", true, r, 0, ""},
		{"list subdir", true, r + "/src", 0, ""},
		{"list dotdir allowed", true, r + "/.github", 0, ""},
		{"list escape dir", true, r + "/escape", 0, CodeOutside},
		{"list through escape", true, r + "/escape/", 0, CodeInvalid},
		{"list git internals", true, r + "/.git", 0, CodeBlocked},
		{"list ssh", true, r + "/.ssh", 0, CodeBlocked},
		{"list file", true, r + "/readme.md", 0, CodeNotDir},
		{"list parent", true, f.base, 0, CodeOutside},
		{"list real root path", true, filepath.Join(f.base, "projects"), 0, CodeOutside},
		{"read text", false, r + "/readme.md", 0, ""},
		{"read nested dotfile allowed", false, r + "/.github/workflows/ci.yml", 0, ""},
		{"read gitignore allowed", false, r + "/.gitignore", 0, ""},
		{"read empty", false, r + "/empty.txt", 0, ""},
		{"read inside link", false, r + "/inside-link", 0, ""},
		{"read binary", false, r + "/binary.bin", 0, ""},
		{"read latin1", false, r + "/latin1.txt", 0, ""},
		{"read big first page", false, r + "/big.txt", 0, ""},
		{"read big second page", false, r + "/big.txt", ReadLimit - 1, ""},
		{"read dotdot", false, r + "/../outside/notes.txt", 0, CodeInvalid},
		{"read dot", false, r + "/./readme.md", 0, CodeInvalid},
		{"read escape file", false, r + "/escape-file", 0, CodeOutside},
		{"read absolute escape", false, r + "/absolute-escape", 0, CodeOutside},
		{"read through escape dir", false, r + "/escape/notes.txt", 0, CodeOutside},
		{"read env", false, r + "/.env", 0, CodeBlocked},
		{"read env variant", false, r + "/.env.production", 0, CodeBlocked},
		{"read envrc", false, r + "/.envrc", 0, CodeBlocked},
		{"read app env", false, r + "/config/app.env", 0, CodeBlocked},
		{"read pem", false, r + "/certs/server.pem", 0, CodeBlocked},
		{"read key uppercase", false, r + "/certs/tls.KEY", 0, CodeBlocked},
		{"read ssh key", false, r + "/id_ed25519", 0, CodeBlocked},
		{"read password file", false, r + "/db_password.txt", 0, CodeBlocked},
		{"read secret json", false, r + "/client_secret.json", 0, CodeBlocked},
		{"read credentials", false, r + "/aws-credentials", 0, CodeBlocked},
		{"read git head", false, r + "/.git/HEAD", 0, CodeBlocked},
		{"read authorized keys", false, r + "/.ssh/authorized_keys", 0, CodeBlocked},
		{"read history", false, r + "/.bash_history", 0, CodeBlocked},
		{"read netrc", false, r + "/.netrc", 0, CodeBlocked},
		{"read p12", false, r + "/keys.p12", 0, CodeBlocked},
		{"read link to secret", false, r + "/secret-link", 0, CodeBlocked},
		{"read fifo", false, r + "/pipe", 0, CodeNotFile},
		{"read dir", false, r + "/src", 0, CodeNotFile},
		{"read missing", false, r + "/missing.txt", 0, CodeNotFound},
		{"read broken link", false, r + "/broken", 0, CodeNotFound},
		{"read past end", false, r + "/readme.md", 1 << 20, CodeInvalid},
		{"read system file", false, "/etc/hostname", 0, CodeOutside},
		{"read root slash", false, "/", 0, CodeInvalid},
		{"read relative", false, "readme.md", 0, CodeInvalid},
	}
}

func TestBridgeAndLocalAgree(t *testing.T) {
	f := buildFixture(t)
	local := Local{Roots: []string{f.root}}
	for _, tc := range fileCases(f) {
		t.Run(tc.name, func(t *testing.T) {
			var bridgeValue, localValue any
			var bridgeErr, localErr error
			if tc.list {
				bridgeValue, bridgeErr = bridgeList(t, f, tc.path)
				localValue, localErr = local.List(tc.path)
			} else {
				bridgeValue, bridgeErr = bridgeRead(t, f, tc.path, tc.offset)
				localValue, localErr = local.Read(tc.path, tc.offset)
			}
			if code(bridgeErr) != tc.want || code(localErr) != tc.want {
				t.Fatalf("bridge=%q local=%q, want %q", code(bridgeErr), code(localErr), tc.want)
			}
			if tc.want == "" && !reflect.DeepEqual(bridgeValue, localValue) {
				t.Fatalf("implementations differ:\nbridge %+v\nlocal  %+v", bridgeValue, localValue)
			}
		})
	}
}

func TestListingMarksBlockedEntries(t *testing.T) {
	f := buildFixture(t)
	listing, err := Local{Roots: []string{f.root}}.List(f.root)
	if err != nil {
		t.Fatal(err)
	}
	entries := map[string]Entry{}
	for _, entry := range listing.Entries {
		entries[entry.Name] = entry
	}
	for name, reason := range map[string]string{".env": "secret", ".git": "secret", ".ssh": "secret", "id_ed25519": "secret", "secret-link": "secret", "escape": CodeOutside, "escape-file": CodeOutside, "absolute-escape": CodeOutside, "bad�name.txt": "name"} {
		entry, ok := entries[name]
		if !ok || !entry.Blocked || entry.Reason != reason || entry.Size != 0 || entry.Mtime != 0 {
			t.Errorf("%s = %+v, want blocked (%s) without metadata", name, entry, reason)
		}
	}
	for _, name := range []string{"readme.md", ".github", ".gitignore", "inside-link", "src", "big.txt"} {
		if entry := entries[name]; entry.Blocked || entry.Mtime == 0 {
			t.Errorf("%s = %+v, want visible", name, entry)
		}
	}
	if link := entries["inside-link"]; !link.Link || link.Type != "file" || link.Size != int64(len("package main\n")) {
		t.Errorf("inside-link = %+v", link)
	}
	if broken := entries["broken"]; broken.Blocked || broken.Type != "other" || !broken.Link {
		t.Errorf("broken = %+v", broken)
	}
	if listing.Entries[0].Type != "dir" {
		t.Errorf("directories must come first: %+v", listing.Entries[0])
	}
}

func TestReadPagesReportTruncationAndBinary(t *testing.T) {
	f := buildFixture(t)
	local := Local{Roots: []string{f.root}}
	first, err := local.Read(f.root+"/big.txt", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Truncated || first.Binary || first.Length != ReadLimit-1 || first.NextOffset != ReadLimit-1 || first.Size != ReadLimit+2+len64(" fim\n")+4096 {
		t.Fatalf("first page = size %d length %d next %d truncated %v", first.Size, first.Length, first.NextOffset, first.Truncated)
	}
	second, err := local.Read(f.root+"/big.txt", first.NextOffset)
	if err != nil {
		t.Fatal(err)
	}
	if second.Truncated || !strings.HasPrefix(second.Content, "€ fim") || second.NextOffset != second.Size {
		t.Fatalf("second page = %q… truncated %v next %d", second.Content[:min(10, len(second.Content))], second.Truncated, second.NextOffset)
	}
	for _, name := range []string{"binary.bin", "latin1.txt"} {
		content, err := local.Read(f.root+"/"+name, 0)
		if err != nil || !content.Binary || content.Content != "" || content.Length != 0 || content.Truncated {
			t.Fatalf("%s = %+v, %v", name, content, err)
		}
	}
	text, err := local.Read(f.root+"/readme.md", 0)
	if err != nil || text.Content != "# Olá, operador\n" || text.Truncated {
		t.Fatalf("readme = %+v, %v", text, err)
	}
}

func len64(s string) int64 { return int64(len(s)) }

func TestBridgeRejectsUnsafeRootsFile(t *testing.T) {
	f := buildFixture(t)
	if err := os.Chmod(f.rootsFile, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := bridgeList(t, f, f.root); code(err) != CodeRootsInsecure {
		t.Fatalf("world-writable roots file = %v", err)
	}
	if err := os.Chmod(f.rootsFile, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(f.rootsFile), 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := bridgeList(t, f, f.root); code(err) != CodeRootsInsecure {
		t.Fatalf("world-writable roots directory = %v", err)
	}
	if err := os.Chmod(filepath.Dir(f.rootsFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.rootsFile, []byte(f.root+"/../outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := bridgeList(t, f, f.root); code(err) != CodeRootsInsecure {
		t.Fatalf("unclean root = %v", err)
	}
	missing := f
	missing.rootsFile = filepath.Join(f.base, "config", "absent")
	if _, err := bridgeList(t, missing, f.root); code(err) != CodeDisabled {
		t.Fatalf("missing roots file = %v", err)
	}
	if _, err := (Local{}).List(f.root); code(err) != CodeDisabled {
		t.Fatalf("local without roots = %v", err)
	}
}

func TestBridgeRejectsMalformedFileCommands(t *testing.T) {
	f := buildFixture(t)
	for _, command := range []string{
		"vpsdash-files",
		"vpsdash-files list",
		"vpsdash-files write " + ListCommand(f.root)[len("vpsdash-files list "):],
		"vpsdash-files list not+base64/",
		"vpsdash-files read " + ListCommand(f.root)[len("vpsdash-files list "):],
		ReadCommand(f.root+"/readme.md", 0) + " extra",
		ReadCommand(f.root+"/readme.md", 0)[:len(ReadCommand(f.root+"/readme.md", 0))-1] + "-1",
		"vpsdash-files list " + ListCommand(f.root)[len("vpsdash-files list "):] + " ; cat /etc/passwd",
	} {
		output, err := runBridge(t, f.rootsFile, command)
		if err == nil || !strings.Contains(string(output), "comando SSH não permitido") {
			t.Errorf("bridge accepted %q: %s", command, output)
		}
	}
	// The runner-units mode never reads files.
	cmd := exec.Command("python3", "-I", filepath.Join("..", "..", "scripts", "ssh-readonly.py"), "runner-units")
	cmd.Env = append(os.Environ(), "SSH_ORIGINAL_COMMAND="+ListCommand(f.root))
	if output, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(output), "comando SSH não permitido") {
		t.Fatalf("runner bridge answered a file read: %s", output)
	}
}
