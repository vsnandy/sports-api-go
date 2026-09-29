// Package viewer renders a local, read-only web view of the owner's leagues by
// calling the sports API server-side (the API key never reaches the browser).
package viewer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// APIError is a non-2xx API response, decoded from its error envelope.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("API %d %s: %s", e.Status, e.Code, e.Message)
}

type Meta struct {
	Season   int      `json:"season"`
	Week     int      `json:"week"`
	Warnings []string `json:"warnings"`
}

// Get fetches path with query q and decodes the envelope's data into out.
func (c *Client) Get(ctx context.Context, path string, q url.Values, out any) (Meta, error) {
	u := strings.TrimRight(c.BaseURL, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Meta{}, err
	}
	req.Header.Set("X-API-Key", c.APIKey)
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Meta{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return Meta{}, &APIError{Status: resp.StatusCode, Code: e.Error.Code, Message: e.Error.Message}
	}
	var env struct {
		Data json.RawMessage `json:"data"`
		Meta Meta            `json:"meta"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return Meta{}, fmt.Errorf("decoding %s: %w", path, err)
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return Meta{}, fmt.Errorf("decoding %s data: %w", path, err)
	}
	return env.Meta, nil
}
