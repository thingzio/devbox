package collect

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/classify"
	"github.com/thingzio/devbox/internal/github"
	"github.com/thingzio/devbox/internal/rules"
)

var (
	updateGolden = flag.Bool("update", false, "rewrite golden expected.json files")
	recordGolden = flag.Bool("record", false, "record a golden fixture from the live GitHub API (needs GITHUB_TOKEN)")
	recordRepo   = flag.String("record-repo", "", "owner/name to record")
	recordPR     = flag.Int("record-pr", 0, "pull request number to record")
	recordViewer = flag.String("record-viewer", "", "viewer login")
	recordName   = flag.String("record-name", "", "fixture directory name")
	recordRules  = flag.String("record-rules", "", "optional rules overlay JSON file")
)

const goldenBase = "BASE"

var goldenNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

type exchange struct {
	Key    string `json:"key"`
	Status int    `json:"status"`
	Link   string `json:"link,omitempty"`
	Body   string `json:"body"`
}

type goldenFixture struct {
	Repo      string          `json:"repo"`
	Number    int             `json:"number"`
	Viewer    string          `json:"viewer"`
	Rules     json.RawMessage `json:"rules,omitempty"`
	Exchanges []exchange      `json:"exchanges"`
}

type goldenResult struct {
	Kind   Kind            `json:"kind"`
	Error  string          `json:"error,omitempty"`
	Record classify.Record `json:"record"`
}

type noCache struct{}

func (noCache) CachedDiff(context.Context, int64, string, string, string) (bool, bool, error) {
	return false, false, nil
}
func (noCache) CachedComment(context.Context, int64, int64, time.Time) (bool, bool, error) {
	return false, false, nil
}
func (noCache) CachedPolicy(context.Context, int64, string, time.Time, time.Duration) (string, bool, error) {
	return "", false, nil
}

// requestKey identifies a request independent of host: GraphQL by operation
// name and variables, REST by path and query.
func requestKey(r *http.Request, body []byte) string {
	if strings.HasSuffix(r.URL.Path, "/graphql") {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(body, &req)
		op := ""
		if fields := strings.Fields(req.Query); len(fields) > 1 {
			op, _, _ = strings.Cut(fields[1], "(")
		}
		vars, _ := json.Marshal(req.Variables)
		return "POST graphql " + op + " " + string(vars)
	}
	return r.Method + " " + r.URL.Path + "?" + r.URL.RawQuery
}

type recorder struct {
	base      http.RoundTripper
	apiBase   string
	mu        sync.Mutex
	exchanges []exchange
}

func (rec *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		var err error
		if body, err = io.ReadAll(req.Body); err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	resp, err := rec.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(data))
	rec.mu.Lock()
	rec.exchanges = append(rec.exchanges, exchange{
		Key: requestKey(req, body), Status: resp.StatusCode,
		Link: strings.ReplaceAll(resp.Header.Get("Link"), rec.apiBase, goldenBase), Body: string(data),
	})
	rec.mu.Unlock()
	return resp, nil
}

func replayServer(t *testing.T, fx goldenFixture) *httptest.Server {
	t.Helper()
	byKey := map[string]exchange{}
	for _, ex := range fx.Exchanges {
		byKey[ex.Key] = ex
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ex, ok := byKey[requestKey(r, body)]
		if !ok {
			t.Errorf("unrecorded request %s", requestKey(r, body))
			w.WriteHeader(http.StatusNotImplemented)
			return
		}
		if ex.Link != "" {
			w.Header().Set("Link", strings.ReplaceAll(ex.Link, goldenBase, srv.URL))
		}
		w.WriteHeader(ex.Status)
		_, _ = io.WriteString(w, ex.Body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func collectGolden(ctx context.Context, c *github.Client, fx goldenFixture) (goldenResult, error) {
	owner, name, ok := strings.Cut(fx.Repo, "/")
	if !ok {
		return goldenResult{}, fmt.Errorf("repo %q is not owner/name", fx.Repo)
	}
	rl, err := rules.Parse(fx.Rules)
	if err != nil {
		return goldenResult{}, err
	}
	reader := GitHubReader{Client: c}
	observed, err := reader.Fingerprints(ctx, owner, name)
	if err != nil {
		return goldenResult{}, err
	}
	fingerprint := ""
	for _, o := range observed {
		if o.Number == fx.Number {
			fingerprint = o.Fingerprint
		}
	}
	if fingerprint == "" {
		return goldenResult{}, fmt.Errorf("PR #%d is not open in %s", fx.Number, fx.Repo)
	}
	out := (&Collector{Reader: reader, Cache: noCache{}}).Collect(ctx, Input{
		RepoID: 1, Owner: owner, Name: name, Viewer: fx.Viewer, Number: fx.Number, Rules: rl,
		QueuedFingerprint: fingerprint, Now: goldenNow,
	})
	res := goldenResult{Kind: out.Kind, Record: out.Record}
	if out.Err != nil {
		res.Error = out.Err.Error()
	}
	return res, nil
}

func TestGolden(t *testing.T) {
	paths, err := filepath.Glob("testdata/golden/*/fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no golden fixtures; record one with make fixtures REPO=owner/name PR=n")
	}
	for _, path := range paths {
		dir := filepath.Dir(path)
		t.Run(filepath.Base(dir), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var fx goldenFixture
			if err := json.Unmarshal(raw, &fx); err != nil {
				t.Fatal(err)
			}
			srv := replayServer(t, fx)
			c, err := github.New(github.Options{APIURL: srv.URL, Sleep: func(context.Context, time.Duration) error { return nil }})
			if err != nil {
				t.Fatal(err)
			}
			got, err := collectGolden(context.Background(), c, fx)
			if err != nil {
				t.Fatalf("replay: %v", err)
			}
			gotJSON, _ := json.MarshalIndent(got, "", "  ")
			expectedPath := filepath.Join(dir, "expected.json")
			if *updateGolden {
				if err := os.WriteFile(expectedPath, append(gotJSON, '\n'), 0o600); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(expectedPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(bytes.TrimSpace(want), bytes.TrimSpace(gotJSON)) {
				t.Fatalf("golden mismatch in %s; rerun with -args -update and review the diff\n%s", dir, gotJSON)
			}
		})
	}
}

func TestRecordGolden(t *testing.T) {
	if !*recordGolden {
		t.Skip("recording is opt-in: go test -run TestRecordGolden -args -record ...")
	}
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" || *recordRepo == "" || *recordPR == 0 || *recordViewer == "" || *recordName == "" {
		t.Fatal("recording needs GITHUB_TOKEN, -record-repo, -record-pr, -record-viewer, and -record-name")
	}
	var rulesJSON json.RawMessage
	if *recordRules != "" {
		data, err := os.ReadFile(*recordRules)
		if err != nil {
			t.Fatal(err)
		}
		rulesJSON = data
	}
	rec := &recorder{base: http.DefaultTransport, apiBase: strings.TrimSuffix(github.DefaultAPIURL, "/")}
	c, err := github.New(github.Options{Token: token, HTTPClient: &http.Client{Transport: rec}})
	if err != nil {
		t.Fatal(err)
	}
	fx := goldenFixture{Repo: *recordRepo, Number: *recordPR, Viewer: *recordViewer, Rules: rulesJSON}
	got, err := collectGolden(context.Background(), c, fx)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	fx.Exchanges = rec.exchanges
	dir := filepath.Join("testdata", "golden", *recordName)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	fixture, _ := json.MarshalIndent(fx, "", "  ")
	expected, _ := json.MarshalIndent(got, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "fixture.json"), append(fixture, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "expected.json"), append(expected, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("recorded %d exchanges into %s", len(fx.Exchanges), dir)
}
