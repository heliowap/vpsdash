package files

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"unicode/utf8"
)

type root struct{ configured, resolved string }

// Local browses this machine with the rules of the SSH bridge. It runs with
// the panel's own OS permissions.
type Local struct{ Roots []string }

func (l Local) roots() ([]root, error) {
	var roots []root
	for _, configured := range l.Roots {
		if !CleanPath(configured) {
			return nil, deny(CodeRootsInsecure)
		}
		resolved, err := filepath.EvalSymlinks(configured)
		if err != nil {
			continue
		}
		roots = append(roots, root{configured, resolved})
	}
	if len(roots) == 0 {
		return nil, deny(CodeDisabled)
	}
	return roots, nil
}

func contained(path string, roots []root) (string, error) {
	var lexical []string
	for _, r := range roots {
		if under(path, r.configured) {
			lexical = append(lexical, r.configured)
		}
	}
	if len(lexical) == 0 {
		return "", deny(CodeOutside)
	}
	if secretBelow(path, lexical) {
		return "", deny(CodeBlocked)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return "", deny(CodeUnreadable)
		}
		return "", deny(CodeNotFound)
	}
	var matches []string
	for _, r := range roots {
		if under(real, r.resolved) {
			matches = append(matches, r.resolved)
		}
	}
	if len(matches) == 0 {
		return "", deny(CodeOutside)
	}
	if secretBelow(real, matches) {
		return "", deny(CodeBlocked)
	}
	return real, nil
}

func secretBelow(path string, roots []string) bool {
	for _, r := range roots {
		for _, part := range relativeParts(path, r) {
			if SecretName(part) {
				return true
			}
		}
	}
	return false
}

// openConfined opens the resolved path without following a final symlink
// and confirms the kernel opened that same path, so a component swapped for
// a symlink after resolution is refused.
func openConfined(real string, directory bool) (*os.File, error) {
	flags := syscall.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK | syscall.O_CLOEXEC
	if directory {
		flags |= syscall.O_DIRECTORY
	}
	fd, err := syscall.Open(real, flags, 0)
	if err != nil {
		switch {
		case errors.Is(err, syscall.ENOENT):
			return nil, deny(CodeNotFound)
		case errors.Is(err, syscall.ENOTDIR):
			return nil, deny(CodeNotDir)
		default:
			return nil, deny(CodeUnreadable)
		}
	}
	file := os.NewFile(uintptr(fd), real)
	opened, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
	if err != nil {
		file.Close()
		return nil, deny(CodeUnreadable)
	}
	if opened != real {
		file.Close()
		return nil, deny(CodeOutside)
	}
	return file, nil
}

func entryKind(mode fs.FileMode) string {
	switch {
	case mode.IsDir():
		return "dir"
	case mode.IsRegular():
		return "file"
	default:
		return "other"
	}
}

func listEntry(path, real, name string, roots []root) Entry {
	shown, valid := displayName(name)
	entry := Entry{Name: shown, Type: "other"}
	info, err := os.Lstat(filepath.Join(real, name))
	if err != nil {
		entry.Blocked, entry.Reason = true, CodeUnreadable
		return entry
	}
	entry.Link = info.Mode()&fs.ModeSymlink != 0
	if !valid {
		entry.Blocked, entry.Reason = true, "name"
		return entry
	}
	if SecretName(name) {
		if !entry.Link {
			entry.Type = entryKind(info.Mode())
		}
		entry.Blocked, entry.Reason = true, "secret"
		return entry
	}
	if entry.Link {
		target, err := contained(path+"/"+name, roots)
		if err != nil {
			var denied *Denied
			if errors.As(err, &denied) && denied.Code != CodeNotFound {
				entry.Blocked, entry.Reason = true, denied.Code
				if denied.Code == CodeBlocked {
					entry.Reason = "secret"
				}
			}
			return entry
		}
		if info, err = os.Stat(target); err != nil {
			return entry
		}
	}
	entry.Type, entry.Mtime = entryKind(info.Mode()), info.ModTime().Unix()
	if info.Mode().IsRegular() {
		entry.Size = info.Size()
	}
	return entry
}

// List returns the entries of a directory inside the roots.
func (l Local) List(path string) (Listing, error) {
	if !CleanPath(path) {
		return Listing{}, deny(CodeInvalid)
	}
	roots, err := l.roots()
	if err != nil {
		return Listing{}, err
	}
	real, err := contained(path, roots)
	if err != nil {
		return Listing{}, err
	}
	dir, err := openConfined(real, true)
	if err != nil {
		return Listing{}, err
	}
	names, err := dir.Readdirnames(ListLimit + 1)
	dir.Close()
	if err != nil && err != io.EOF {
		return Listing{}, deny(CodeUnreadable)
	}
	listing := Listing{Path: path, Entries: []Entry{}}
	if len(names) > ListLimit {
		names, listing.Truncated = names[:ListLimit], true
	}
	for _, name := range names {
		listing.Entries = append(listing.Entries, listEntry(path, real, name, roots))
	}
	sort.SliceStable(listing.Entries, func(i, j int) bool {
		a, b := listing.Entries[i], listing.Entries[j]
		if (a.Type == "dir") != (b.Type == "dir") {
			return a.Type == "dir"
		}
		return a.Name < b.Name
	})
	return listing, nil
}

// Read returns up to ReadLimit bytes of a regular file from offset.
func (l Local) Read(path string, offset int64) (Content, error) {
	if !CleanPath(path) || offset < 0 {
		return Content{}, deny(CodeInvalid)
	}
	roots, err := l.roots()
	if err != nil {
		return Content{}, err
	}
	real, err := contained(path, roots)
	if err != nil {
		return Content{}, err
	}
	file, err := openConfined(real, false)
	if err != nil {
		return Content{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Content{}, deny(CodeUnreadable)
	}
	if !info.Mode().IsRegular() {
		return Content{}, deny(CodeNotFile)
	}
	if offset > info.Size() {
		return Content{}, deny(CodeInvalid)
	}
	data := make([]byte, ReadLimit)
	n, err := io.ReadFull(io.NewSectionReader(file, offset, ReadLimit), data)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return Content{}, deny(CodeUnreadable)
	}
	return page(path, info.Size(), info.ModTime().Unix(), offset, data[:n]), nil
}

func page(path string, size, mtime, offset int64, data []byte) Content {
	result := Content{Path: path, Size: size, Mtime: mtime, Offset: offset, NextOffset: offset, Binary: true}
	for _, b := range data {
		if b == 0 {
			return result
		}
	}
	// A page may end inside a multibyte character; the next page starts there.
	trims := []int{0, 1, 2, 3}
	if offset+int64(len(data)) >= size {
		trims = trims[:1]
	}
	for _, trim := range trims {
		if trim > len(data) {
			break
		}
		text := data[:len(data)-trim]
		if utf8.Valid(text) {
			end := offset + int64(len(text))
			result.Length, result.NextOffset, result.Truncated = int64(len(text)), end, end < size
			result.Binary, result.Content = false, string(text)
			break
		}
	}
	return result
}
