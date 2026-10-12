package rules

import (
	"strings"
	"testing"
)

const aicrOverlay = `{
  "test_approval": {
    "marker_checks": ["tests / Test", "tests-skip"],
    "command": "/ok-to-test",
    "ci_paths": [".github/"],
    "ci_paths_command": "/ok-to-test confirm-ci"
  },
  "stale_days": 7
}`

func TestParseOverlay(t *testing.T) {
	r, err := Parse([]byte(aicrOverlay))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := r.TestApproval.MarkerChecks; len(got) != 2 || got[0] != "tests / Test" {
		t.Fatalf("marker checks = %v", got)
	}
	if r.TestApproval.CIPathsCommand != "/ok-to-test confirm-ci" || r.StaleDays != 7 {
		t.Fatalf("unexpected rules %+v", r)
	}
}

func TestParseDefaults(t *testing.T) {
	for _, in := range []string{"", "  ", "{}", `{"test_approval": null}`} {
		r, err := Parse([]byte(in))
		if err != nil {
			t.Fatalf("Parse(%q): %v", in, err)
		}
		if r.StaleDays != DefaultStaleDays || len(r.TestApproval.MarkerChecks) != 0 {
			t.Fatalf("Parse(%q) = %+v, want defaults", in, r)
		}
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"unknown top-level key":     `{"stale": 3}`,
		"unknown nested key":        `{"test_approval": {"markers": ["x"]}}`,
		"stale days zero":           `{"stale_days": 0}`,
		"marker without command":    `{"test_approval": {"marker_checks": ["x"]}}`,
		"ci paths without command":  `{"test_approval": {"ci_paths": [".github/"]}}`,
		"empty marker entry":        `{"test_approval": {"marker_checks": [" "], "command": "/ok"}}`,
		"empty ci path entry":       `{"test_approval": {"ci_paths": [""], "ci_paths_command": "/ok"}}`,
		"trailing data":             `{} {}`,
		"not an object":             `[1]`,
		"wrong type for stale days": `{"stale_days": "7"}`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(in)); err == nil {
				t.Fatalf("Parse(%s) succeeded, want error", in)
			}
		})
	}
}

func TestCollectionHash(t *testing.T) {
	a, _ := Parse([]byte(`{"test_approval": {"marker_checks": ["a", "b"], "command": "/ok"}}`))
	b, _ := Parse([]byte(`{"test_approval": {"marker_checks": ["b", "a"], "command": "/ok"}, "stale_days": 30}`))
	if a.CollectionHash() != b.CollectionHash() {
		t.Fatal("marker order or stale_days changed the collection hash")
	}
	c, _ := Parse([]byte(`{"test_approval": {"marker_checks": ["a", "b"], "command": "/ok-to-test"}}`))
	if a.CollectionHash() == c.CollectionHash() {
		t.Fatal("command change did not change the collection hash")
	}
	if Default().CollectionHash() != (Rules{StaleDays: 1, TestApproval: TestApproval{MarkerChecks: []string{}}}).CollectionHash() {
		t.Fatal("nil and empty slices hash differently")
	}
	if len(a.CollectionHash()) != 64 {
		t.Fatalf("hash %q is not hex sha256", a.CollectionHash())
	}
}

func TestJSONRoundTrip(t *testing.T) {
	r, _ := Parse([]byte(aicrOverlay))
	back, err := Parse([]byte(r.JSON()))
	if err != nil {
		t.Fatalf("Parse(JSON()): %v", err)
	}
	if back.CollectionHash() != r.CollectionHash() || back.StaleDays != r.StaleDays {
		t.Fatalf("round trip changed rules: %+v vs %+v", back, r)
	}
	if !strings.Contains(Default().JSON(), `"stale_days": 7`) {
		t.Fatalf("default JSON %q lacks stale_days", Default().JSON())
	}
}
