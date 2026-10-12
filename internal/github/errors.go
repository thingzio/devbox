package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ErrUnauthorized means GitHub rejected the token (HTTP 401).
var ErrUnauthorized = errors.New("github: token rejected")

// ErrReadOnly means a caller attempted a non-read operation.
var ErrReadOnly = errors.New("github: only read operations are allowed")

// StatusError is a non-success HTTP response.
type StatusError struct {
	Status      int
	Message     string
	RetryAfter  time.Duration
	RateLimited bool
	Reset       time.Time
}

func (e *StatusError) Error() string { return fmt.Sprintf("github: HTTP %d: %s", e.Status, e.Message) }

// SSORequiredError means the token must be authorized for an organization's SAML SSO.
type SSORequiredError struct{ URL string }

func (e *SSORequiredError) Error() string {
	if e.URL == "" {
		return "github: organization SAML SSO requires authorizing this token"
	}
	return "github: organization SAML SSO requires authorizing this token at " + e.URL
}

// OAuthRestrictedError means an organization blocks this OAuth App.
type OAuthRestrictedError struct{ Org string }

func (e *OAuthRestrictedError) Error() string {
	return fmt.Sprintf("github: organization %q restricts OAuth App access; request approval or use a classic token", e.Org)
}

// GraphQLError carries the errors array of a GraphQL response.
type GraphQLError struct {
	Messages []string
	Types    []string
}

func (e *GraphQLError) Error() string { return "github: graphql: " + strings.Join(e.Messages, "; ") }

// TransportError is a network-level failure.
type TransportError struct{ Err error }

func (e *TransportError) Error() string { return "github: transport: " + e.Err.Error() }
func (e *TransportError) Unwrap() error { return e.Err }

var transientStatus = map[int]bool{
	http.StatusRequestTimeout: true, http.StatusTooManyRequests: true, http.StatusInternalServerError: true,
	http.StatusBadGateway: true, http.StatusServiceUnavailable: true, http.StatusGatewayTimeout: true,
}

// retryNow reports whether the client should retry inside the same call.
func retryNow(err error) bool {
	var te *TransportError
	if errors.As(err, &te) {
		return true
	}
	var se *StatusError
	if errors.As(err, &se) {
		switch {
		case se.RateLimited:
			return false
		case se.RetryAfter > 0:
			return se.RetryAfter <= maxRetryAfter
		}
		return transientStatus[se.Status]
	}
	return false
}

// Retryable reports whether a failed read may succeed if attempted again later.
func Retryable(err error) bool {
	var te *TransportError
	if errors.As(err, &te) {
		return true
	}
	var se *StatusError
	if errors.As(err, &se) {
		return se.RateLimited || se.RetryAfter > 0 || transientStatus[se.Status]
	}
	var ge *GraphQLError
	if errors.As(err, &ge) {
		return slices.Contains(ge.Types, "RATE_LIMITED")
	}
	return false
}

// RateLimitReset returns when an exhausted primary rate limit resets.
func RateLimitReset(err error) (time.Time, bool) {
	var se *StatusError
	if errors.As(err, &se) && se.RateLimited {
		return se.Reset, true
	}
	return time.Time{}, false
}

var oauthRestricted = regexp.MustCompile("the `([^`]+)` organization has enabled OAuth App access restrictions")

func statusError(resp *http.Response, body []byte) error {
	msg := apiMessage(body)
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("%w: %s", ErrUnauthorized, msg)
	case http.StatusForbidden:
		if u, ok := ssoRequired(resp.Header); ok {
			return &SSORequiredError{URL: u}
		}
		if m := oauthRestricted.FindStringSubmatch(msg); m != nil {
			return &OAuthRestrictedError{Org: m[1]}
		}
	}
	se := &StatusError{Status: resp.StatusCode, Message: msg}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		if d := parseRetryAfter(resp.Header.Get("Retry-After")); d > 0 {
			se.RetryAfter = d
		} else if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			se.RateLimited = true
			se.Reset = resetTime(resp.Header)
		}
	}
	return se
}

func graphQLError(h http.Header, errs []gqlError) error {
	if u, ok := ssoRequired(h); ok {
		return &SSORequiredError{URL: u}
	}
	ge := &GraphQLError{}
	for _, e := range errs {
		if m := oauthRestricted.FindStringSubmatch(e.Message); m != nil {
			return &OAuthRestrictedError{Org: m[1]}
		}
		ge.Messages = append(ge.Messages, e.Message)
		ge.Types = append(ge.Types, e.Type)
	}
	return ge
}

type gqlError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func ssoRequired(h http.Header) (string, bool) {
	v := h.Get("X-GitHub-SSO")
	if !strings.HasPrefix(v, "required") {
		return "", false
	}
	if i := strings.Index(v, "url="); i >= 0 {
		return strings.TrimSpace(v[i+len("url="):]), true
	}
	return "", true
}

func apiMessage(body []byte) string {
	var m struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &m) == nil && m.Message != "" {
		return m.Message
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		s = s[:200]
	}
	if s == "" {
		s = "no response body"
	}
	return s
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

func resetTime(h http.Header) time.Time {
	v, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(v, 0).UTC()
}

func rateFromHeader(h http.Header) (Rate, bool) {
	limit, errL := strconv.Atoi(h.Get("X-RateLimit-Limit"))
	remaining, errR := strconv.Atoi(h.Get("X-RateLimit-Remaining"))
	if errL != nil || errR != nil {
		return Rate{}, false
	}
	return Rate{Resource: h.Get("X-RateLimit-Resource"), Limit: limit, Remaining: remaining, Reset: resetTime(h)}, true
}
