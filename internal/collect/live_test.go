package collect

import (
	"cmp"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/github"
	"github.com/thingzio/devbox/internal/rules"
)

// TestLiveCollect reads one open PR from a public repository with the real
// API. It is opt-in (DEVBOX_LIVE_TEST=1) and never runs in CI.
func TestLiveCollect(t *testing.T) {
	if os.Getenv("DEVBOX_LIVE_TEST") != "1" {
		t.Skip("set DEVBOX_LIVE_TEST=1 to run against the live GitHub API")
	}
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		t.Fatal("DEVBOX_LIVE_TEST=1 needs GITHUB_TOKEN")
	}
	owner, name, _ := strings.Cut(cmp.Or(os.Getenv("DEVBOX_LIVE_REPO"), "NVIDIA/aicr"), "/")
	ctx := context.Background()
	c, err := github.New(github.Options{Token: token})
	if err != nil {
		t.Fatal(err)
	}
	var me struct {
		Login string `json:"login"`
	}
	if _, err := c.Get(ctx, "user", &me); err != nil {
		t.Fatalf("identify: %v", err)
	}
	reader := GitHubReader{Client: c}
	observed, err := reader.Fingerprints(ctx, owner, name)
	if err != nil {
		t.Fatalf("fingerprints: %v", err)
	}
	if len(observed) == 0 {
		t.Skipf("%s/%s has no open pull requests", owner, name)
	}
	out := (&Collector{Reader: reader, Cache: noCache{}}).Collect(ctx, Input{
		RepoID: 1, Owner: owner, Name: name, Viewer: me.Login, Number: observed[0].Number, Rules: rules.Default(),
		QueuedFingerprint: observed[0].Fingerprint, Now: time.Now(),
	})
	if out.Kind == Failed {
		t.Fatalf("live collection failed: %v", out.Err)
	}
	t.Logf("%s/%s#%d kind=%d evidence_errors=%d", owner, name, observed[0].Number, out.Kind, len(out.Record.EvidenceErrors))
}
