package collect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/github"
)

type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

func newReader(t *testing.T, h http.HandlerFunc) GitHubReader {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := github.New(github.Options{APIURL: srv.URL, Token: "t", Sleep: func(context.Context, time.Duration) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	return GitHubReader{Client: c}
}

func decodeGQL(t *testing.T, r *http.Request) gqlRequest {
	t.Helper()
	body, _ := io.ReadAll(r.Body)
	var req gqlRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("bad graphql body: %v", err)
	}
	return req
}

func fpNodeJSON(number int, head string) string {
	return fmt.Sprintf(`{"number":%d,"updatedAt":"2026-10-01T09:30:00Z","baseRefOid":"b0","headRefOid":%q,
"mergeStateStatus":"CLEAN","reviewDecision":null,"isDraft":false,"commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"SUCCESS"}}}]}}`, number, head)
}

func TestFingerprintsPaginates(t *testing.T) {
	var cursors []any
	reader := newReader(t, func(w http.ResponseWriter, r *http.Request) {
		req := decodeGQL(t, r)
		if !strings.HasPrefix(strings.TrimSpace(req.Query), "query Fingerprints") {
			t.Errorf("unexpected query %q", req.Query)
		}
		cursors = append(cursors, req.Variables["endCursor"])
		if req.Variables["endCursor"] == nil {
			fmt.Fprintf(w, `{"data":{"repository":{"pullRequests":{"pageInfo":{"hasNextPage":true,"endCursor":"c1"},"nodes":[%s]}}}}`, fpNodeJSON(1, "h1"))
			return
		}
		fmt.Fprintf(w, `{"data":{"repository":{"pullRequests":{"pageInfo":{"hasNextPage":false,"endCursor":"c2"},"nodes":[%s]}}}}`, fpNodeJSON(2, "h2"))
	})
	obs, err := reader.Fingerprints(context.Background(), "o", "r")
	if err != nil {
		t.Fatalf("Fingerprints: %v", err)
	}
	if len(obs) != 2 || obs[0].Number != 1 || obs[1].HeadOID != "h2" {
		t.Fatalf("observed = %+v", obs)
	}
	want := Fingerprint(FingerprintFields{UpdatedAt: time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC), BaseOID: "b0", HeadOID: "h1", MergeState: "CLEAN", Rollup: "SUCCESS"})
	if obs[0].Fingerprint != want {
		t.Fatalf("fingerprint mismatch")
	}
	if len(cursors) != 2 || cursors[1] != "c1" {
		t.Fatalf("cursors = %v", cursors)
	}
}

func TestFingerprintsRejectsRepeatedCursor(t *testing.T) {
	reader := newReader(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"data":{"repository":{"pullRequests":{"pageInfo":{"hasNextPage":true,"endCursor":"same"},"nodes":[%s]}}}}`, fpNodeJSON(1, "h1"))
	})
	if _, err := reader.Fingerprints(context.Background(), "o", "r"); err == nil || !strings.Contains(err.Error(), "repeated cursor") {
		t.Fatalf("err = %v", err)
	}
}

func TestFingerprintsMissingRepository(t *testing.T) {
	reader := newReader(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":{"repository":null}}`) })
	if _, err := reader.Fingerprints(context.Background(), "o", "gone"); err == nil {
		t.Fatal("missing repository accepted")
	}
}

func TestDetailFixtureMatchesFingerprint(t *testing.T) {
	fixture, err := os.ReadFile("testdata/detail.json")
	if err != nil {
		t.Fatal(err)
	}
	reader := newReader(t, func(w http.ResponseWriter, r *http.Request) {
		req := decodeGQL(t, r)
		if req.Variables["viewer"] != "me" || req.Variables["number"] != float64(42) {
			t.Errorf("variables = %v", req.Variables)
		}
		_, _ = w.Write(fixture)
	})
	d, err := reader.Detail(context.Background(), "NVIDIA", "aicr", 42, "me")
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	want := Fingerprint(FingerprintFields{
		UpdatedAt: time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC), BaseOID: "b000000000000000000000000000000000000000",
		HeadOID: "h111111111111111111111111111111111111111", MergeState: "BLOCKED", ReviewDecision: "APPROVED", Rollup: "FAILURE",
	})
	if Fingerprint(d.FingerprintFields()) != want {
		t.Fatalf("detail fingerprint differs from fingerprint-query fingerprint for identical fields")
	}
}

func TestDetailNotFound(t *testing.T) {
	reader := newReader(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"repository":{"pullRequest":null}}}`)
	})
	if _, err := reader.Detail(context.Background(), "o", "r", 1, "me"); err == nil {
		t.Fatal("missing pull request accepted")
	}
}

func TestChecksDecodesRunsAndStatuses(t *testing.T) {
	reader := newReader(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"repository":{"object":{"statusCheckRollup":{"contexts":{"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[
{"__typename":"CheckRun","name":"tests","status":"COMPLETED","conclusion":"FAILURE","isRequired":true,"checkSuite":{"workflowRun":{"databaseId":42}}},
{"__typename":"CheckRun","name":"e2e","status":"IN_PROGRESS","conclusion":null,"isRequired":true,"checkSuite":{"workflowRun":null}},
{"__typename":"StatusContext","context":"ci/legacy","state":"PENDING","isRequired":false}]}}}}}}`)
	})
	checks, err := reader.Checks(context.Background(), "o", "r", "head", 7)
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	if len(checks) != 3 || checks[0].RunID != 42 || checks[0].State != "FAILURE" || checks[1].State != "IN_PROGRESS" ||
		checks[2].Name != "ci/legacy" || checks[2].Required {
		t.Fatalf("checks = %+v", checks)
	}
}

func TestRESTReads(t *testing.T) {
	var base string
	reader := newReader(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/actions/runs":
			if r.URL.Query().Get("head_sha") != "head" || r.URL.Query().Get("status") != "action_required" {
				t.Errorf("runs query = %s", r.URL.RawQuery)
			}
			fmt.Fprint(w, `{"total_count":2}`)
		case "/repos/o/r/commits/head/check-runs":
			if r.URL.Query().Get("check_name") != "tests / Test" {
				t.Errorf("check_name = %q", r.URL.Query().Get("check_name"))
			}
			fmt.Fprint(w, `{"check_runs":[{"conclusion":"skipped"},{"conclusion":null}]}`)
		case "/repos/o/r/compare/base...head":
			fmt.Fprint(w, `{"files":[{"filename":"a.go"},{"filename":".github/x.yml","previous_filename":"ci/x.yml"}]}`)
		case "/repos/o/r/compare/base...huge":
			files := make([]string, 300)
			for i := range files {
				files[i] = fmt.Sprintf(`{"filename":"f%d"}`, i)
			}
			fmt.Fprintf(w, `{"files":[%s]}`, strings.Join(files, ","))
		case "/repos/o/r/issues/comments/9":
			fmt.Fprint(w, `{"body":"/ok-to-test"}`)
		case "/repos/o/r/rules/branches/release/v1":
			if r.URL.Query().Get("page") == "" {
				w.Header().Set("Link", fmt.Sprintf(`<%s/repos/o/r/rules/branches/release/v1?per_page=100&page=2>; rel="next"`, base))
				fmt.Fprint(w, `[{"type":"pull_request"}]`)
				return
			}
			fmt.Fprint(w, `[{"type":"required_signatures"}]`)
		case "/repos/o/r/branches/main/protection/required_signatures":
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Branch not protected"}`)
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(http.StatusTeapot)
		}
	})
	base = strings.TrimSuffix(reader.Client.BaseURL(), "/")
	ctx := context.Background()
	if n, err := reader.AwaitingRuns(ctx, "o", "r", "head"); err != nil || n != 2 {
		t.Fatalf("AwaitingRuns = %d, %v", n, err)
	}
	if c, err := reader.MarkerConclusions(ctx, "o", "r", "head", "tests / Test"); err != nil || !slices.Equal(c, []string{"skipped", ""}) {
		t.Fatalf("MarkerConclusions = %v, %v", c, err)
	}
	if f, err := reader.ChangedFiles(ctx, "o", "r", "base", "head"); err != nil || !slices.Equal(f, []string{"a.go", ".github/x.yml", "ci/x.yml"}) {
		t.Fatalf("ChangedFiles = %v, %v", f, err)
	}
	if _, err := reader.ChangedFiles(ctx, "o", "r", "base", "huge"); !errors.Is(err, ErrTooManyFiles) {
		t.Fatalf("huge compare err = %v", err)
	}
	if b, err := reader.CommentBody(ctx, "o", "r", 9); err != nil || b != "/ok-to-test" {
		t.Fatalf("CommentBody = %q, %v", b, err)
	}
	if types, err := reader.RulesetTypes(ctx, "o", "r", "release/v1"); err != nil || !slices.Equal(types, []string{"pull_request", "required_signatures"}) {
		t.Fatalf("RulesetTypes = %v, %v", types, err)
	}
	_, err := reader.ClassicSignatures(ctx, "o", "r", "main")
	var se *github.StatusError
	if !errors.As(err, &se) || se.Status != 404 || se.Message != "Branch not protected" {
		t.Fatalf("ClassicSignatures err = %v", err)
	}
}
