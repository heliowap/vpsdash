package api

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/heliowap/vpsdash/internal/store"
	"github.com/heliowap/vpsdash/internal/webpush"
)

const vapidMissing = "Chaves VAPID não configuradas no servidor."

func (s *Server) pushStatus(w http.ResponseWriter, r *http.Request) {
	status := map[string]any{"configured": s.Push != nil, "public_key": ""}
	if s.Push != nil {
		status["public_key"] = s.Push.Keys.Public
	}
	jsonResponse(w, http.StatusOK, status)
}

type pushEndpointBody struct {
	Endpoint string `json:"endpoint"`
}

func (s *Server) pushSubscribe(w http.ResponseWriter, r *http.Request) {
	if s.Push == nil {
		errorResponse(w, http.StatusServiceUnavailable, vapidMissing)
		return
	}
	// Mirrors PushSubscription.toJSON() in the browser.
	var body struct {
		Endpoint       string `json:"endpoint"`
		ExpirationTime *int64 `json:"expirationTime"`
		Keys           struct {
			P256DH string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		errorResponse(w, http.StatusBadRequest, "Inscrição inválida.")
		return
	}
	if s.Push.ValidateEndpoint(body.Endpoint) != nil {
		errorResponse(w, http.StatusBadRequest, "O navegador indicou um serviço de push desconhecido.")
		return
	}
	if webpush.ValidateKeys(body.Keys.P256DH, body.Keys.Auth) != nil {
		errorResponse(w, http.StatusBadRequest, "Chaves da inscrição inválidas.")
		return
	}
	err := s.Store.SavePushSubscription(r.Context(), store.PushSubscription{Endpoint: body.Endpoint, P256DH: body.Keys.P256DH, Auth: body.Keys.Auth}, time.Now())
	if errors.Is(err, store.ErrTooManySubscriptions) {
		errorResponse(w, http.StatusConflict, "Limite de dispositivos atingido. Desative as notificações em outro dispositivo.")
		return
	}
	if err != nil {
		errorResponse(w, http.StatusInternalServerError, "Não foi possível salvar a inscrição.")
		return
	}
	jsonResponse(w, http.StatusCreated, map[string]bool{"subscribed": true})
}

func (s *Server) pushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	var body pushEndpointBody
	if err := decodeJSON(w, r, &body); err != nil || body.Endpoint == "" {
		errorResponse(w, http.StatusBadRequest, "Inscrição inválida.")
		return
	}
	sub, err := s.Store.PushSubscriptionByEndpoint(r.Context(), body.Endpoint)
	if err == nil {
		err = s.Store.DeletePushSubscription(r.Context(), sub.ID, time.Now())
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		errorResponse(w, http.StatusInternalServerError, "Não foi possível remover a inscrição.")
		return
	}
	jsonResponse(w, http.StatusOK, map[string]bool{"subscribed": false})
}

func (s *Server) pushTest(w http.ResponseWriter, r *http.Request) {
	if s.Push == nil {
		errorResponse(w, http.StatusServiceUnavailable, vapidMissing)
		return
	}
	var body pushEndpointBody
	if err := decodeJSON(w, r, &body); err != nil || body.Endpoint == "" {
		errorResponse(w, http.StatusBadRequest, "Inscrição inválida.")
		return
	}
	err := s.Push.SendTest(r.Context(), body.Endpoint)
	var statusErr *webpush.StatusError
	switch {
	case err == nil:
		jsonResponse(w, http.StatusOK, map[string]bool{"sent": true})
	case errors.Is(err, sql.ErrNoRows):
		errorResponse(w, http.StatusNotFound, "Este dispositivo não está inscrito. Ative as notificações novamente.")
	case errors.Is(err, webpush.ErrGone):
		errorResponse(w, http.StatusGone, "O serviço de push encerrou esta inscrição. Ative as notificações novamente.")
	case errors.As(err, &statusErr) && statusErr.Status == http.StatusForbidden:
		errorResponse(w, http.StatusBadGateway, "O serviço de push recusou as chaves VAPID. Desative e ative as notificações novamente.")
	default:
		errorResponse(w, http.StatusBadGateway, "O serviço de push não confirmou o envio. Tente novamente em instantes.")
	}
}
