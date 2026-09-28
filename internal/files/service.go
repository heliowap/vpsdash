package files

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/heliowap/vpsdash/internal/config"
)

// ErrUnknownHost means the host ID is not a VPS in the inventory.
var ErrUnknownHost = errors.New("unknown host")

// Remote runs one `vpsdash-files` command through the host's SSH bridge.
type Remote interface {
	FileCommand(ctx context.Context, host config.Host, command string) (string, error)
}

// Service dispatches reads to the local implementation or the SSH bridge.
// The inventory roots gate requests here; the host applies its own roots.
type Service struct {
	Config config.Config
	Remote Remote
}

func (s *Service) host(id string) (config.Host, error) {
	for _, host := range s.Config.Hosts {
		if host.ID == id && host.Kind == "vps" {
			if len(host.FileRoots) == 0 {
				return host, deny(CodeDisabled)
			}
			return host, nil
		}
	}
	return config.Host{}, ErrUnknownHost
}

func (s *Service) check(hostID, path string) (config.Host, error) {
	host, err := s.host(hostID)
	if err != nil {
		return host, err
	}
	if !CleanPath(path) {
		return host, deny(CodeInvalid)
	}
	if !UnderRoots(path, host.FileRoots) {
		return host, deny(CodeOutside)
	}
	return host, nil
}

// ListCommand and ReadCommand build the forced-command protocol.
func ListCommand(path string) string {
	return "vpsdash-files list " + base64.RawURLEncoding.EncodeToString([]byte(path))
}

func ReadCommand(path string, offset int64) string {
	return "vpsdash-files read " + base64.RawURLEncoding.EncodeToString([]byte(path)) + " " + strconv.FormatInt(offset, 10)
}

func (s *Service) remote(ctx context.Context, host config.Host, command string, value any) error {
	if s.Remote == nil {
		return errors.New("remote file access unavailable")
	}
	output, err := s.Remote.FileCommand(ctx, host, command)
	if err != nil {
		return err
	}
	return DecodeBridge([]byte(output), value)
}

// DecodeBridge parses a bridge reply, turning {"error": code} into Denied.
func DecodeBridge(output []byte, value any) error {
	var refusal struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(output, &refusal); err != nil {
		return err
	}
	if refusal.Error != "" {
		return deny(refusal.Error)
	}
	return json.Unmarshal(output, value)
}

func (s *Service) List(ctx context.Context, hostID, path string) (Listing, error) {
	host, err := s.check(hostID, path)
	if err != nil {
		return Listing{}, err
	}
	if host.Local {
		return Local{Roots: host.FileRoots}.List(path)
	}
	var listing Listing
	if err := s.remote(ctx, host, ListCommand(path), &listing); err != nil {
		return Listing{}, err
	}
	if listing.Path != path {
		return Listing{}, errors.New("bridge answered for another path")
	}
	if listing.Entries == nil {
		listing.Entries = []Entry{}
	}
	return listing, nil
}

func (s *Service) Read(ctx context.Context, hostID, path string, offset int64) (Content, error) {
	host, err := s.check(hostID, path)
	if err != nil {
		return Content{}, err
	}
	if offset < 0 {
		return Content{}, deny(CodeInvalid)
	}
	if host.Local {
		return Local{Roots: host.FileRoots}.Read(path, offset)
	}
	var content Content
	if err := s.remote(ctx, host, ReadCommand(path, offset), &content); err != nil {
		return Content{}, err
	}
	if content.Path != path || content.Offset != offset || int64(len(content.Content)) > ReadLimit {
		return Content{}, errors.New("bridge answered for another read")
	}
	return content, nil
}
