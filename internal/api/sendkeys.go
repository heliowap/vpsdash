package api

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/heliowap/vpsdash/internal/interactive"
	"github.com/heliowap/vpsdash/internal/store"
)

const (
	sendKeysTimeout     = 10 * time.Second
	sendKeysOutputLimit = 16 << 10
)

// sendKeys answers an agent prompt with tmux send-keys through the host's
// interactive key. Only the private listener registers it. The audit row
// names the host and session; the keys and text are never stored or logged.
func (s *Server) sendKeys(w http.ResponseWriter, r *http.Request) {
	hostID := r.PathValue("id")
	var body struct {
		Session string `json:"session"`
		Key     string `json:"key"`
		Text    string `json:"text"`
		Enter   bool   `json:"enter"`
		Confirm bool   `json:"confirm"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		s.recordAudit(r, "send_keys", hostID, "", "denied")
		errorResponse(w, 400, "Resposta inválida.")
		return
	}
	// The audit names a session only when the collector observed it on this
	// host, so request text never reaches the log through the target.
	session, observed := store.Session{}, false
	if interactive.ValidSessionName(body.Session) {
		session, observed = s.observedSession(r.Context(), hostID, body.Session)
	}
	target := ""
	if observed {
		target = session.Name
	}
	deny := func(status int, message string) {
		s.recordAudit(r, "send_keys", hostID, target, "denied")
		errorResponse(w, status, message)
	}
	if _, ok := s.requireStepUp(w, r); !ok {
		s.recordAudit(r, "send_keys", hostID, target, "denied")
		return
	}
	if !body.Confirm {
		deny(400, "Confirme a sessão e as teclas antes de enviar.")
		return
	}
	host, ok := s.interactiveHost(hostID)
	if !ok {
		deny(404, "Resposta não configurada para este host.")
		return
	}
	if !observed {
		deny(404, "Sessão tmux não observada neste host.")
		return
	}
	reply := interactive.Reply{Key: body.Key, Text: body.Text, Enter: body.Enter}
	if err := reply.Validate(); err != nil {
		deny(400, "Resposta recusada: use uma tecla da lista ou até 200 caracteres em uma linha, sem caracteres de controle.")
		return
	}
	if session.PanePID <= 0 {
		deny(409, "A coleta ainda não identificou o pane desta sessão. Atualize e tente de novo.")
		return
	}
	if !s.acquireSlot() {
		deny(http.StatusTooManyRequests, "Limite de sessões interativas atingido. Tente novamente em instantes.")
		return
	}
	defer s.releaseSlot()
	failed := func(message string) {
		s.recordAudit(r, "send_keys", host.ID, target, "failed")
		errorResponse(w, http.StatusBadGateway, message)
	}
	panes, err := s.Interactive.Run(r.Context(), host, interactive.ListPanesArgv(session.Name), sendKeysTimeout, sendKeysOutputLimit)
	if err != nil || panes.ExitCode != 0 || panes.TimedOut {
		log.Printf("send-keys %s/%s: list panes: %v (exit %d)", host.ID, session.Name, err, panes.ExitCode)
		failed("Não foi possível localizar a sessão no host.")
		return
	}
	pane, ok := interactive.PaneFor(panes.Output, session.PanePID)
	if !ok {
		deny(409, "O pane observado não existe mais. Atualize e confira a sessão antes de responder.")
		return
	}
	argv, err := interactive.SendKeysArgv(pane, reply)
	if err != nil {
		deny(400, "Resposta recusada.")
		return
	}
	result, err := s.Interactive.Run(r.Context(), host, argv, sendKeysTimeout, sendKeysOutputLimit)
	if err != nil || result.ExitCode != 0 || result.TimedOut {
		// The error names neither the keys nor the text.
		log.Printf("send-keys %s/%s: send: %v (exit %d)", host.ID, session.Name, err, result.ExitCode)
		failed("O host não confirmou o envio das teclas.")
		return
	}
	s.recordAudit(r, "send_keys", host.ID, target, "ok")
	jsonResponse(w, 200, map[string]any{"sent": true, "state": session.State})
}

// observedSession returns the latest collected snapshot of one session.
func (s *Server) observedSession(ctx context.Context, hostID, name string) (store.Session, bool) {
	sessions, err := s.Store.Sessions(ctx)
	if err != nil {
		return store.Session{}, false
	}
	for _, session := range sessions {
		if session.HostID == hostID && session.Name == name {
			return session, true
		}
	}
	return store.Session{}, false
}
