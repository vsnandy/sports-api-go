package httpx

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

func TestGetJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			if r.Header.Get("X-Test") != "yes" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			fmt.Fprint(w, `{"name":"x"}`)
		case "/missing":
			http.NotFound(w, r)
		case "/boom":
			w.WriteHeader(http.StatusInternalServerError)
		case "/html":
			fmt.Fprint(w, "<html>login</html>")
		case "/slow":
			time.Sleep(200 * time.Millisecond)
			fmt.Fprint(w, `{}`)
		}
	}))
	defer srv.Close()
	c := New("test", 100*time.Millisecond)
	ctx := context.Background()

	t.Run("decodes and sends headers", func(t *testing.T) {
		var out struct{ Name string }
		err := c.GetJSON(ctx, srv.URL+"/ok", http.Header{"X-Test": {"yes"}}, &out)
		if err != nil || out.Name != "x" {
			t.Fatalf("out = %+v, err = %v", out, err)
		}
	})
	t.Run("404 is not found", func(t *testing.T) {
		if err := c.GetJSON(ctx, srv.URL+"/missing", nil, &struct{}{}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
	t.Run("500 is upstream error", func(t *testing.T) {
		var ue *domain.UpstreamError
		err := c.GetJSON(ctx, srv.URL+"/boom", nil, &struct{}{})
		if !errors.As(err, &ue) || ue.Status != 500 || ue.Provider != "test" {
			t.Fatalf("err = %v, want UpstreamError status 500", err)
		}
	})
	t.Run("non-JSON body is bad body", func(t *testing.T) {
		var ue *domain.UpstreamError
		err := c.GetJSON(ctx, srv.URL+"/html", nil, &struct{}{})
		if !errors.As(err, &ue) || !ue.BadBody || ue.Status != 200 {
			t.Fatalf("err = %v, want BadBody with status 200", err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		if err := c.GetJSON(ctx, srv.URL+"/slow", nil, &struct{}{}); !errors.Is(err, domain.ErrUpstreamTimeout) {
			t.Fatalf("err = %v, want ErrUpstreamTimeout", err)
		}
	})
	t.Run("canceled context is not an upstream error", func(t *testing.T) {
		cctx, cancel := context.WithCancel(context.Background())
		cancel()
		var ue *domain.UpstreamError
		err := c.GetJSON(cctx, srv.URL+"/ok", nil, &struct{}{})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if errors.As(err, &ue) {
			t.Fatalf("err = %v, want it not to classify as an UpstreamError", err)
		}
	})
}
