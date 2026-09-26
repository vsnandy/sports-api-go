// Package httpx is the shared JSON GET used by provider adapters. It maps HTTP
// failures onto domain errors and logs each upstream call.
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

type Client struct {
	http     *http.Client
	provider string
}

func New(provider string, timeout time.Duration) *Client {
	return &Client{http: &http.Client{Timeout: timeout}, provider: provider}
}

// GetJSON GETs url and decodes the JSON body into out. header may be nil.
func (c *Client) GetJSON(ctx context.Context, url string, header http.Header, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("Accept", "application/json")

	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		if isTimeout(err) {
			return fmt.Errorf("%s %s: %w", c.provider, req.URL.Path, domain.ErrUpstreamTimeout)
		}
		return &domain.UpstreamError{Provider: c.provider, Err: err}
	}
	defer resp.Body.Close()
	slog.InfoContext(ctx, "upstream call",
		"provider", c.provider, "path", req.URL.Path,
		"status", resp.StatusCode, "ms", time.Since(start).Milliseconds())

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%s %s: %w", c.provider, req.URL.Path, domain.ErrNotFound)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return &domain.UpstreamError{Provider: c.provider, Status: resp.StatusCode}
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		if isTimeout(err) {
			return fmt.Errorf("%s %s: %w", c.provider, req.URL.Path, domain.ErrUpstreamTimeout)
		}
		return &domain.UpstreamError{Provider: c.provider, Status: resp.StatusCode, BadBody: true, Err: err}
	}
	return nil
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
