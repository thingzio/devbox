// Package github is a bounded, read-only GitHub API client (§4.1).
package github

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultAPIURL is the public GitHub REST and GraphQL base.
const DefaultAPIURL = "https://api.github.com/"

const (
	apiVersion     = "2022-11-28"
	maxAttempts    = 3
	requestTimeout = 30 * time.Second
	maxBackoff     = 5 * time.Second
	maxRetryAfter  = 60 * time.Second
	maxBodyBytes   = 64 << 20
	maxPages       = 100
)

// Rate is the latest rate-limit state GitHub reported for one resource.
type Rate struct {
	Resource  string
	Limit     int
	Remaining int
	Reset     time.Time
}

// Options configures a Client.
type Options struct {
	APIURL     string
	Token      string
	UserAgent  string
	HTTPClient *http.Client
	Logger     *slog.Logger
	Sleep      func(context.Context, time.Duration) error
	OnRate     func(Rate)
	OnRequest  func(kind string, status int)
}

// Client performs read-only GitHub API calls with bounded retries.
type Client struct {
	api       *url.URL
	token     string
	userAgent string
	hc        *http.Client
	log       *slog.Logger
	sleep     func(context.Context, time.Duration) error
	onRate    func(Rate)
	onRequest func(string, int)
}

// New validates options and returns a Client.
func New(o Options) (*Client, error) {
	raw := o.APIURL
	if raw == "" {
		raw = DefaultAPIURL
	}
	if !strings.HasSuffix(raw, "/") {
		raw += "/"
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("github: invalid API URL %q", o.APIURL)
	}
	c := &Client{
		api: u, token: o.Token, userAgent: o.UserAgent, hc: o.HTTPClient, log: o.Logger,
		sleep: o.Sleep, onRate: o.OnRate, onRequest: o.OnRequest,
	}
	if c.userAgent == "" {
		c.userAgent = "devbox"
	}
	if c.hc == nil {
		c.hc = &http.Client{}
	}
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	if c.sleep == nil {
		c.sleep = sleepContext
	}
	return c, nil
}

type request struct {
	kind   string // metrics label: rest, graphql, diff
	label  string // log label: REST path without query, or GraphQL operation
	method string
	path   string
	accept string
	body   []byte
}

type response struct {
	header http.Header
	body   []byte
}

func (c *Client) do(ctx context.Context, rq request) (*response, error) {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err := c.once(ctx, rq, attempt)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempt == maxAttempts || !retryNow(err) {
			break
		}
		wait := min(time.Duration(1<<(attempt-1))*time.Second, maxBackoff)
		var se *StatusError
		if errors.As(err, &se) && se.RetryAfter > 0 {
			wait = se.RetryAfter
		}
		if err := c.sleep(ctx, wait); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func (c *Client) once(ctx context.Context, rq request, attempt int) (*response, error) {
	ref, err := url.Parse(rq.path)
	if err != nil {
		return nil, fmt.Errorf("github: bad path %q: %w", rq.label, err)
	}
	u := c.api.ResolveReference(ref)
	if u.Scheme != c.api.Scheme || u.Host != c.api.Host {
		return nil, fmt.Errorf("github: refusing cross-host URL %s", u.Redacted())
	}
	rctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	var body io.Reader
	if rq.body != nil {
		body = bytes.NewReader(rq.body)
	}
	req, err := http.NewRequestWithContext(rctx, rq.method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("github: build request: %w", err)
	}
	req.Header.Set("Accept", rq.accept)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", c.userAgent)
	if rq.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	start := time.Now()
	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		c.observe(ctx, rq, attempt, 0, time.Since(start), nil)
		return nil, &TransportError{Err: err}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &TransportError{Err: err}
	}
	c.observe(ctx, rq, attempt, resp.StatusCode, time.Since(start), resp.Header)
	if len(data) > maxBodyBytes {
		return nil, fmt.Errorf("github: response for %s exceeds %d bytes", rq.label, maxBodyBytes)
	}
	if resp.StatusCode >= 300 {
		return nil, statusError(resp, data)
	}
	return &response{header: resp.Header, body: data}, nil
}

func (c *Client) observe(ctx context.Context, rq request, attempt, status int, d time.Duration, h http.Header) {
	if c.onRequest != nil {
		c.onRequest(rq.kind, status)
	}
	if h != nil && c.onRate != nil {
		if r, ok := rateFromHeader(h); ok {
			c.onRate(r)
		}
	}
	if !c.log.Enabled(ctx, slog.LevelDebug) {
		return
	}
	attrs := []slog.Attr{
		slog.String("kind", rq.kind), slog.String("target", rq.label), slog.String("method", rq.method),
		slog.Int("status", status), slog.Duration("duration", d), slog.Int("attempt", attempt),
	}
	if h != nil {
		attrs = append(attrs,
			slog.String("rate_remaining", h.Get("X-RateLimit-Remaining")),
			slog.String("request_id", h.Get("X-GitHub-Request-Id")))
	}
	c.log.LogAttrs(ctx, slog.LevelDebug, "github request", attrs...)
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
