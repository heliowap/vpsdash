package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/heliowap/vpsdash/internal/files"
)

// FileBrowser serves read-only listings and file pages confined to the
// roots configured for each host.
type FileBrowser interface {
	List(ctx context.Context, hostID, path string) (files.Listing, error)
	Read(ctx context.Context, hostID, path string, offset int64) (files.Content, error)
}

var fileDenials = map[string]struct {
	status  int
	message string
}{
	files.CodeDisabled:      {http.StatusForbidden, "Acesso a arquivos desativado para este host."},
	files.CodeRootsInsecure: {http.StatusServiceUnavailable, "As raízes de arquivos no host têm permissões inseguras."},
	files.CodeInvalid:       {http.StatusBadRequest, "Caminho inválido."},
	files.CodeOutside:       {http.StatusForbidden, "Caminho fora das raízes autorizadas."},
	files.CodeBlocked:       {http.StatusForbidden, "Arquivo bloqueado: o nome indica credenciais ou segredos."},
	files.CodeNotFound:      {http.StatusNotFound, "Caminho não encontrado."},
	files.CodeNotDir:        {http.StatusBadRequest, "O caminho não é uma pasta."},
	files.CodeNotFile:       {http.StatusBadRequest, "O caminho não é um arquivo comum."},
	files.CodeUnreadable:    {http.StatusForbidden, "Sem permissão de leitura no host."},
}

func (s *Server) fileError(w http.ResponseWriter, hostID string, err error) {
	var denied *files.Denied
	switch {
	case errors.Is(err, files.ErrUnknownHost):
		jsonResponse(w, http.StatusNotFound, map[string]string{"error": "Host desconhecido.", "code": "unknown_host"})
	case errors.As(err, &denied):
		if known, ok := fileDenials[denied.Code]; ok {
			jsonResponse(w, known.status, map[string]string{"error": known.message, "code": denied.Code})
			return
		}
		jsonResponse(w, http.StatusBadGateway, map[string]string{"error": "O host recusou a leitura.", "code": "refused"})
	default:
		log.Printf("files %s: %v", hostID, err)
		jsonResponse(w, http.StatusBadGateway, map[string]string{"error": "O host não respondeu à leitura de arquivos.", "code": "unavailable"})
	}
}

func (s *Server) listFiles(w http.ResponseWriter, r *http.Request) {
	if s.Files == nil {
		errorResponse(w, http.StatusServiceUnavailable, "Acesso a arquivos indisponível.")
		return
	}
	hostID := r.PathValue("id")
	listing, err := s.Files.List(r.Context(), hostID, r.URL.Query().Get("path"))
	if err != nil {
		s.fileError(w, hostID, err)
		return
	}
	jsonResponse(w, http.StatusOK, listing)
}

func (s *Server) readFile(w http.ResponseWriter, r *http.Request) {
	if s.Files == nil {
		errorResponse(w, http.StatusServiceUnavailable, "Acesso a arquivos indisponível.")
		return
	}
	hostID := r.PathValue("id")
	offset := int64(0)
	if raw := r.URL.Query().Get("offset"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 0 || value >= 1e15 {
			s.fileError(w, hostID, &files.Denied{Code: files.CodeInvalid})
			return
		}
		offset = value
	}
	content, err := s.Files.Read(r.Context(), hostID, r.URL.Query().Get("path"), offset)
	if err != nil {
		s.fileError(w, hostID, err)
		return
	}
	jsonResponse(w, http.StatusOK, content)
}
