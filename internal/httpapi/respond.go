package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

type envelope struct {
	Data any         `json:"data"`
	Meta domain.Meta `json:"meta"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorEnvelope struct {
	Error apiError `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func respond(w http.ResponseWriter, r *http.Request, data any, meta domain.Meta, err error) {
	if err != nil {
		writeError(w, r, err)
		return
	}
	if meta.Warnings == nil {
		meta.Warnings = []string{}
	}
	writeJSON(w, http.StatusOK, envelope{Data: data, Meta: meta})
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, msg := classify(err)
	if status >= 500 {
		slog.ErrorContext(r.Context(), "request failed", "status", status, "code", code, "err", err)
	}
	writeJSON(w, status, errorEnvelope{Error: apiError{Code: code, Message: msg}})
}

// classify maps domain errors to HTTP status, error code, and a client-safe message.
func classify(err error) (int, string, string) {
	var ip *domain.InvalidParamError
	var ue *domain.UpstreamError
	switch {
	case errors.As(err, &ip):
		return http.StatusBadRequest, "invalid_param", ip.Error()
	case errors.Is(err, domain.ErrNotFound):
		return http.StatusNotFound, "not_found", err.Error()
	case errors.Is(err, domain.ErrESPNAuth):
		return http.StatusBadGateway, "espn_auth_failed", "ESPN rejected the configured espn_s2/SWID cookies; rotate them in SSM"
	case errors.Is(err, domain.ErrUpstreamTimeout):
		return http.StatusGatewayTimeout, "upstream_timeout", "upstream provider timed out"
	case errors.As(err, &ue):
		return http.StatusBadGateway, "upstream_error", ue.Provider + " returned an error"
	default:
		return http.StatusInternalServerError, "internal", "internal error"
	}
}
