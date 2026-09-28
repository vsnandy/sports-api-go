package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

type ctxKey struct{}

func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" || len(id) > 64 {
			id = newID()
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		slog.InfoContext(r.Context(), "request",
			"method", r.Method, "path", r.URL.Path, "status", rec.status, "ms", time.Since(start).Milliseconds())
	})
}

func withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				slog.ErrorContext(r.Context(), "panic", "value", v, "stack", string(debug.Stack()))
				writeJSON(w, http.StatusInternalServerError, errorEnvelope{Error: apiError{Code: "internal", Message: "internal error"}})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// requireAPIKey rejects requests whose X-API-Key does not match. An empty
// configured key rejects everything.
func requireAPIKey(apiKey string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-API-Key")
		if apiKey == "" || subtle.ConstantTimeCompare([]byte(got), []byte(apiKey)) != 1 {
			writeJSON(w, http.StatusUnauthorized, errorEnvelope{Error: apiError{Code: "unauthorized", Message: "missing or invalid X-API-Key"}})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// logHandler adds the request ID from the context to every log record.
type logHandler struct{ slog.Handler }

func NewLogHandler(h slog.Handler) slog.Handler { return logHandler{h} }

func (h logHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := RequestID(ctx); id != "" {
		r.AddAttrs(slog.String("request_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

func (h logHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return logHandler{h.Handler.WithAttrs(as)}
}
func (h logHandler) WithGroup(name string) slog.Handler { return logHandler{h.Handler.WithGroup(name)} }
