package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type harness struct {
	client *Client
	server *httptest.Server
	calls  atomic.Int32
	sleeps []time.Duration
	rates  []Rate
	kinds  []string
}

func newHarness(t *testing.T, h http.HandlerFunc) *harness {
	t.Helper()
	hs := &harness{}
	hs.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hs.calls.Add(1)
		h(w, r)
	}))
	t.Cleanup(hs.server.Close)
	c, err := New(Options{
		APIURL: hs.server.URL,
		Token:  "secret-token",
		Sleep: func(ctx context.Context, d time.Duration) error {
			hs.sleeps = append(hs.sleeps, d)
			return ctx.Err()
		},
		OnRate:    func(r Rate) { hs.rates = append(hs.rates, r) },
		OnRequest: func(kind string, status int) { hs.kinds = append(hs.kinds, fmt.Sprintf("%s:%d", kind, status)) },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	hs.client = c
	return hs
}

func TestGetSendsHeadersAndReportsRate(t *testing.T) {
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/repos/o/r" || r.URL.Query().Get("x") != "1" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Errorf("Authorization = %q", got)
		}
		if r.Header.Get("Accept") != "application/vnd.github+json" || r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
			t.Errorf("missing API headers: %v", r.Header)
		}
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.Header().Set("X-RateLimit-Reset", "1790000000")
		w.Header().Set("X-RateLimit-Resource", "core")
		fmt.Fprint(w, `{"full_name":"o/r"}`)
	})
	var out struct {
		FullName string `json:"full_name"`
	}
	if _, err := hs.client.Get(context.Background(), "repos/o/r?x=1", &out); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if out.FullName != "o/r" {
		t.Fatalf("decoded %+v", out)
	}
	want := Rate{Resource: "core", Limit: 5000, Remaining: 4999, Reset: time.Unix(1790000000, 0).UTC()}
	if len(hs.rates) != 1 || hs.rates[0] != want {
		t.Fatalf("rates = %+v, want %+v", hs.rates, want)
	}
	if len(hs.kinds) != 1 || hs.kinds[0] != "rest:200" {
		t.Fatalf("request kinds = %v", hs.kinds)
	}
}

func TestGetRejectsInvalidPaths(t *testing.T) {
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {})
	for _, p := range []string{"", "-X", "/repos/o/r", "https://evil.example/x"} {
		if _, err := hs.client.Get(context.Background(), p, nil); err == nil {
			t.Errorf("Get(%q) succeeded", p)
		}
	}
	if hs.calls.Load() != 0 {
		t.Fatal("invalid paths reached the network")
	}
}

func TestGraphQLRefusesWrites(t *testing.T) {
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"viewer":{"login":"me"}}}`)
	})
	for _, q := range []string{
		`mutation { addStar(input:{}) { clientMutationId } }`,
		`subscription { x }`,
		`{ viewer { login } }`,
		`query A { viewer { login } } mutation B { x }`,
	} {
		if err := hs.client.GraphQL(context.Background(), "op", q, nil, &struct{}{}); !errors.Is(err, ErrReadOnly) {
			t.Errorf("GraphQL(%q) err = %v, want ErrReadOnly", q, err)
		}
	}
	if hs.calls.Load() != 0 {
		t.Fatal("write operations reached the network")
	}
	allowed := "# mutation in a comment\nquery Q { search(query:\"mutation\") { issueCount } viewer { login } }"
	var out struct {
		Viewer struct {
			Login string `json:"login"`
		} `json:"viewer"`
	}
	if err := hs.client.GraphQL(context.Background(), "Q", allowed, map[string]any{"n": 1}, &out); err != nil {
		t.Fatalf("read query refused: %v", err)
	}
	if out.Viewer.Login != "me" {
		t.Fatalf("decoded %+v", out)
	}
}

func TestGraphQLErrors(t *testing.T) {
	cases := []struct {
		name   string
		header map[string]string
		body   string
		check  func(t *testing.T, err error)
	}{
		{"generic errors fail", nil, `{"data":{"x":1},"errors":[{"type":"NOT_FOUND","message":"nope"}]}`,
			func(t *testing.T, err error) {
				var ge *GraphQLError
				if !errors.As(err, &ge) || ge.Messages[0] != "nope" || Retryable(err) {
					t.Fatalf("err = %v", err)
				}
			}},
		{"rate limited is retryable", nil, `{"errors":[{"type":"RATE_LIMITED","message":"slow down"}]}`,
			func(t *testing.T, err error) {
				if !Retryable(err) {
					t.Fatalf("RATE_LIMITED not retryable: %v", err)
				}
			}},
		{"oauth restriction", nil,
			"{\"errors\":[{\"type\":\"FORBIDDEN\",\"message\":\"Although you appear to have the correct authorization credentials, the `NVIDIA` organization has enabled OAuth App access restrictions, meaning that data access to third-parties is limited.\"}]}",
			func(t *testing.T, err error) {
				var oe *OAuthRestrictedError
				if !errors.As(err, &oe) || oe.Org != "NVIDIA" {
					t.Fatalf("err = %v", err)
				}
			}},
		{"sso required", map[string]string{"X-GitHub-SSO": "required; url=https://github.com/orgs/x/sso?authorization_request=abc"},
			`{"errors":[{"type":"FORBIDDEN","message":"Resource protected by organization SAML enforcement."}]}`,
			func(t *testing.T, err error) {
				var se *SSORequiredError
				if !errors.As(err, &se) || se.URL != "https://github.com/orgs/x/sso?authorization_request=abc" {
					t.Fatalf("err = %v", err)
				}
			}},
		{"null data fails", nil, `{"data":null}`,
			func(t *testing.T, err error) {
				if err == nil {
					t.Fatal("null data accepted")
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tc.header {
					w.Header().Set(k, v)
				}
				fmt.Fprint(w, tc.body)
			})
			tc.check(t, hs.client.GraphQL(context.Background(), "Q", "query Q { x }", nil, &struct{}{}))
		})
	}
}

func TestStatusErrorsAreTyped(t *testing.T) {
	cases := []struct {
		name   string
		status int
		header map[string]string
		body   string
		calls  int32
		check  func(t *testing.T, err error)
	}{
		{"401", 401, nil, `{"message":"Bad credentials"}`, 1, func(t *testing.T, err error) {
			if !errors.Is(err, ErrUnauthorized) || Retryable(err) {
				t.Fatalf("err = %v", err)
			}
		}},
		{"403 sso", 403, map[string]string{"X-GitHub-SSO": "required; url=https://github.com/orgs/o/sso"}, `{"message":"SAML"}`, 1,
			func(t *testing.T, err error) {
				var se *SSORequiredError
				if !errors.As(err, &se) || se.URL != "https://github.com/orgs/o/sso" {
					t.Fatalf("err = %v", err)
				}
			}},
		{"403 oauth restriction", 403, nil,
			"{\"message\":\"Although you appear to have the correct authorization credentials, the `acme` organization has enabled OAuth App access restrictions, meaning that data access to third-parties is limited.\"}", 1,
			func(t *testing.T, err error) {
				var oe *OAuthRestrictedError
				if !errors.As(err, &oe) || oe.Org != "acme" {
					t.Fatalf("err = %v", err)
				}
			}},
		{"404 is final", 404, nil, `{"message":"Not Found"}`, 1, func(t *testing.T, err error) {
			var se *StatusError
			if !errors.As(err, &se) || se.Status != 404 || se.Message != "Not Found" || Retryable(err) {
				t.Fatalf("err = %v", err)
			}
		}},
		{"primary rate limit is not retried in place", 403,
			map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Limit": "5000", "X-RateLimit-Reset": "1790000000"},
			`{"message":"API rate limit exceeded"}`, 1, func(t *testing.T, err error) {
				reset, ok := RateLimitReset(err)
				if !ok || !reset.Equal(time.Unix(1790000000, 0)) || !Retryable(err) {
					t.Fatalf("err = %v reset = %v", err, reset)
				}
			}},
		{"long retry-after is deferred", 403, map[string]string{"Retry-After": "120"}, `{"message":"secondary"}`, 1,
			func(t *testing.T, err error) {
				if !Retryable(err) {
					t.Fatalf("err = %v", err)
				}
			}},
		{"503 retried three times", 503, nil, `oops`, 3, func(t *testing.T, err error) {
			var se *StatusError
			if !errors.As(err, &se) || se.Status != 503 || !Retryable(err) {
				t.Fatalf("err = %v", err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tc.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			_, err := hs.client.Get(context.Background(), "repos/o/r", nil)
			tc.check(t, err)
			if got := hs.calls.Load(); got != tc.calls {
				t.Fatalf("calls = %d, want %d", got, tc.calls)
			}
		})
	}
}

func TestRetriesTransientStatusWithBackoff(t *testing.T) {
	var n atomic.Int32
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, `{}`)
	})
	if _, err := hs.client.Get(context.Background(), "x", nil); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(hs.sleeps) != 2 || hs.sleeps[0] != time.Second || hs.sleeps[1] != 2*time.Second {
		t.Fatalf("sleeps = %v, want [1s 2s]", hs.sleeps)
	}
}

func TestRetryAfterHonored(t *testing.T) {
	var n atomic.Int32
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `{}`)
	})
	if _, err := hs.client.Get(context.Background(), "x", nil); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(hs.sleeps) != 1 || hs.sleeps[0] != 7*time.Second {
		t.Fatalf("sleeps = %v, want [7s]", hs.sleeps)
	}
}

func TestTransportErrorRetried(t *testing.T) {
	var n atomic.Int32
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		fmt.Fprint(w, `{}`)
	})
	if _, err := hs.client.Get(context.Background(), "x", nil); err != nil {
		t.Fatalf("Get after dropped connection: %v", err)
	}
	if hs.calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", hs.calls.Load())
	}
}

func TestCancelledContextStopsRetries(t *testing.T) {
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := hs.client.Get(ctx, "x", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestGetPages(t *testing.T) {
	var base string
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page") {
		case "":
			w.Header().Set("Link", fmt.Sprintf(`<%s/items?page=2>; rel="next", <%s/items?page=2>; rel="last"`, base, base))
			fmt.Fprint(w, `[1,2]`)
		case "2":
			fmt.Fprint(w, `[3]`)
		}
	})
	base = hs.server.URL
	var pages []string
	err := hs.client.GetPages(context.Background(), "items?per_page=2", func(p []byte) error {
		pages = append(pages, string(p))
		return nil
	})
	if err != nil {
		t.Fatalf("GetPages: %v", err)
	}
	if strings.Join(pages, "|") != "[1,2]|[3]" {
		t.Fatalf("pages = %v", pages)
	}
}

func TestGetPagesRejectsLoopsAndForeignHosts(t *testing.T) {
	var base string
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("foreign") == "1" {
			w.Header().Set("Link", `<https://evil.example/x>; rel="next"`)
		} else {
			w.Header().Set("Link", fmt.Sprintf(`<%s/items?page=1>; rel="next"`, base))
		}
		fmt.Fprint(w, `[]`)
	})
	base = hs.server.URL
	noop := func([]byte) error { return nil }
	if err := hs.client.GetPages(context.Background(), "items?page=1", noop); err == nil {
		t.Fatal("repeated next link accepted")
	}
	if err := hs.client.GetPages(context.Background(), "items?foreign=1", noop); err == nil {
		t.Fatal("cross-host next link accepted")
	}
}

func TestGetDiffUsesDiffMediaType(t *testing.T) {
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/vnd.github.diff" {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}
		fmt.Fprint(w, "diff --git a/x b/x\n")
	})
	got, err := hs.client.GetDiff(context.Background(), "repos/o/r/compare/a...b")
	if err != nil || string(got) != "diff --git a/x b/x\n" {
		t.Fatalf("GetDiff = %q, %v", got, err)
	}
	if hs.kinds[0] != "diff:200" {
		t.Fatalf("kind = %v", hs.kinds)
	}
}

func TestNewRejectsBadURL(t *testing.T) {
	for _, u := range []string{"ftp://x", "://", "http://"} {
		if _, err := New(Options{APIURL: u}); err == nil {
			t.Errorf("New(%q) succeeded", u)
		}
	}
}
