package provider

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const MaxBody = 32 << 20

func Handler(service Service, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		fail := func(status int, code, msg string) { w.WriteHeader(status); json.NewEncoder(w).Encode(Error{code, msg}) }
		if r.Method != "GET" {
			w.Header().Set("Allow", "GET")
			fail(405, "method_not_allowed", "use GET")
			return
		}
		if r.URL.Path == "/health" {
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			return
		}
		if token != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			fail(401, "unauthorized", "invalid service credential")
			return
		}
		var out any
		var err error
		switch r.URL.Path {
		case "/v1/info":
			out, err = service.Info(r.Context())
		case "/v1/catalog":
			out, err = service.Catalog(r.Context())
		case "/v1/teachers":
			out, err = service.Directory(r.Context())
		case "/v1/schedule":
			q, e := url.ParseQuery(r.URL.RawQuery)
			if e != nil {
				fail(400, "invalid_request", "invalid query")
				return
			}
			for key, values := range q {
				if len(values) != 1 || (key != "group" && key != "from" && key != "to") {
					fail(400, "invalid_request", "invalid query")
					return
				}
			}
			if !validID(q.Get("group")) || ValidateRange(q.Get("from"), q.Get("to")) != nil {
				fail(400, "invalid_request", "group and range of at most 31 days required")
				return
			}
			out, err = service.Schedule(r.Context(), q.Get("group"), q.Get("from"), q.Get("to"))
		default:
			fail(404, "not_found", "unknown endpoint")
			return
		}
		if err != nil {
			var e *Error
			if errors.As(err, &e) {
				status := 502
				switch e.Code {
				case "not_supported":
					status = 501
				case "not_found":
					status = 404
				case "rate_limited":
					status = 429
				case "unavailable":
					status = 503
				}
				fail(status, e.Code, e.Message)
			} else {
				fail(502, "upstream_error", "source request failed")
			}
			return
		}
		json.NewEncoder(w).Encode(out)
	})
}

type Client struct {
	URL, Token string
	HTTP       *http.Client
}

func NewClient(base, token string) (*Client, error) {
	u, e := url.Parse(base)
	if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("invalid provider URL")
	}
	return &Client{strings.TrimRight(base, "/"), token, &http.Client{Timeout: 110 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) get(ctx context.Context, path string, out any) error {
	r, e := http.NewRequestWithContext(ctx, "GET", c.URL+path, nil)
	if e != nil {
		return e
	}
	if c.Token != "" {
		r.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, e := c.HTTP.Do(r)
	if e != nil {
		return fmt.Errorf("provider unavailable: %w", e)
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(io.LimitReader(resp.Body, MaxBody+1))
	if e != nil {
		return e
	}
	if len(b) > MaxBody {
		return fmt.Errorf("provider response exceeds limit")
	}
	if resp.StatusCode != 200 {
		var out Error
		if json.Unmarshal(b, &out) != nil || out.Code == "" {
			out = Error{"upstream_error", fmt.Sprintf("HTTP %d", resp.StatusCode)}
		}
		return &out
	}
	if e = json.Unmarshal(b, out); e != nil {
		return fmt.Errorf("invalid provider JSON: %w", e)
	}
	return nil
}
func (c *Client) Info(ctx context.Context) (v Info, e error) {
	e = c.get(ctx, "/v1/info", &v)
	if e == nil && (v.Version != Version || !validID(v.Source)) {
		e = fmt.Errorf("unsupported provider version or source")
	}
	return
}
func (c *Client) Catalog(ctx context.Context) (v Catalog, e error) {
	e = c.get(ctx, "/v1/catalog", &v)
	if e == nil {
		e = ValidateCatalog(v)
	}
	return
}
func (c *Client) Schedule(ctx context.Context, g, f, t string) (v Snapshot, e error) {
	e = c.get(ctx, "/v1/schedule?"+url.Values{"group": {g}, "from": {f}, "to": {t}}.Encode(), &v)
	if e == nil {
		e = ValidateSnapshot(v, g, f, t)
	}
	return
}
func (c *Client) Directory(ctx context.Context) (v Directory, e error) {
	e = c.get(ctx, "/v1/teachers", &v)
	if e == nil {
		e = ValidateDirectory(v)
	}
	return
}
