package collect

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/classify"
	"github.com/thingzio/devbox/internal/github"
	"github.com/thingzio/devbox/internal/rules"
)

type fakeReader struct {
	detail      *Detail
	detailErr   error
	awaiting    int
	awaitingErr error
	markers     map[string][]string
	files       []string
	filesErr    error
	diffs       map[string][]byte
	diffErr     error
	bodies      map[int64]string
	bodyErr     error
	rulesets    []string
	rulesetErr  error
	classic     bool
	classicErr  error
	checks      []classify.Check
	checksErr   error
	calls       map[string]int
}

func (f *fakeReader) count(name string) {
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[name]++
}

func (f *fakeReader) Fingerprints(context.Context, string, string) ([]Observed, error) {
	f.count("Fingerprints")
	return nil, nil
}
func (f *fakeReader) Detail(context.Context, string, string, int, string) (*Detail, error) {
	f.count("Detail")
	return f.detail, f.detailErr
}
func (f *fakeReader) Checks(context.Context, string, string, string, int) ([]classify.Check, error) {
	f.count("Checks")
	return f.checks, f.checksErr
}
func (f *fakeReader) AwaitingRuns(context.Context, string, string, string) (int, error) {
	f.count("AwaitingRuns")
	return f.awaiting, f.awaitingErr
}
func (f *fakeReader) MarkerConclusions(_ context.Context, _, _, _, check string) ([]string, error) {
	f.count("MarkerConclusions")
	return f.markers[check], nil
}
func (f *fakeReader) ChangedFiles(context.Context, string, string, string, string) ([]string, error) {
	f.count("ChangedFiles")
	return f.files, f.filesErr
}
func (f *fakeReader) Diff(_ context.Context, _, _, base, commit string) ([]byte, error) {
	f.count("Diff")
	if f.diffErr != nil {
		return nil, f.diffErr
	}
	return f.diffs[base+"..."+commit], nil
}
func (f *fakeReader) CommentBody(_ context.Context, _, _ string, id int64) (string, error) {
	f.count("CommentBody")
	return f.bodies[id], f.bodyErr
}
func (f *fakeReader) RulesetTypes(context.Context, string, string, string) ([]string, error) {
	f.count("RulesetTypes")
	return f.rulesets, f.rulesetErr
}
func (f *fakeReader) ClassicSignatures(context.Context, string, string, string) (bool, error) {
	f.count("ClassicSignatures")
	return f.classic, f.classicErr
}

type fakeCache struct {
	diffs    map[string]bool
	comments map[int64]bool
	policy   string
}

func (c *fakeCache) CachedDiff(_ context.Context, _ int64, base, reviewed, head string) (bool, bool, error) {
	v, ok := c.diffs[base+reviewed+head]
	return v, ok, nil
}
func (c *fakeCache) CachedComment(_ context.Context, _, id int64, _ time.Time) (bool, bool, error) {
	v, ok := c.comments[id]
	return v, ok, nil
}
func (c *fakeCache) CachedPolicy(context.Context, int64, string, time.Time, time.Duration) (string, bool, error) {
	return c.policy, c.policy != "", nil
}

var created = time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)

func baseDetail() *Detail {
	return &Detail{
		Number: 7, Title: "Fix", URL: "https://github.com/o/r/pull/7", CreatedAt: created, UpdatedAt: created.Add(time.Hour),
		Author: &Actor{Login: "alice", Typename: "User"}, BaseRefName: "main", BaseRefOid: "base", HeadRefOid: "head",
		MergeStateStatus: "CLEAN",
		Commits:          Nodes[CommitNode]{TotalCount: 1, Nodes: []CommitNode{{Commit: Commit{OID: "head", Signature: &Signature{State: "VALID"}}}}},
		Head:             Nodes[CommitNode]{Nodes: []CommitNode{{Commit: Commit{OID: "head", CommittedDate: created}}}},
	}
}

func inputFor(d *Detail, rl rules.Rules) Input {
	return Input{RepoID: 1, Owner: "o", Name: "r", Viewer: "me", Number: d.Number, Rules: rl,
		QueuedFingerprint: Fingerprint(d.FingerprintFields()), Now: created.Add(2 * time.Hour)}
}

func collect(reader *fakeReader, cache *fakeCache, in Input) Outcome {
	if cache == nil {
		cache = &fakeCache{}
	}
	return (&Collector{Reader: reader, Cache: cache}).Collect(context.Background(), in)
}

var forkRules = rules.Rules{StaleDays: 7, TestApproval: rules.TestApproval{
	MarkerChecks: []string{"tests / Test", "tests-skip"}, Command: "/ok-to-test",
	CIPaths: []string{".github/"}, CIPathsCommand: "/ok-to-test confirm-ci",
}}

func TestCollectMismatchStopsBeforeFollowUps(t *testing.T) {
	d := baseDetail()
	in := inputFor(d, rules.Default())
	in.QueuedFingerprint = "stale"
	r := &fakeReader{detail: d}
	out := collect(r, nil, in)
	if out.Kind != Mismatch || out.Fingerprint != Fingerprint(d.FingerprintFields()) || out.HeadOID != "head" {
		t.Fatalf("outcome = %+v", out)
	}
	if len(r.calls) != 1 {
		t.Fatalf("follow-up reads after mismatch: %v", r.calls)
	}
}

func TestCollectDraftAndOwnSkipFollowUps(t *testing.T) {
	for name, mod := range map[string]func(*Detail){
		"draft": func(d *Detail) { d.IsDraft = true },
		"own":   func(d *Detail) { d.Author = &Actor{Login: "me"} },
	} {
		t.Run(name, func(t *testing.T) {
			d := baseDetail()
			mod(d)
			r := &fakeReader{detail: d}
			out := collect(r, nil, inputFor(d, rules.Default()))
			if out.Kind != Collected || len(r.calls) != 1 || out.Record.Version != classify.RecordVersion {
				t.Fatalf("outcome = %+v calls = %v", out, r.calls)
			}
		})
	}
}

func TestCollectForkTestApproval(t *testing.T) {
	d := baseDetail()
	d.IsCrossRepository = true
	r := &fakeReader{detail: d, markers: map[string][]string{"tests / Test": {"skipped"}}, files: []string{"a.go", ".github/workflows/ci.yml"}}
	out := collect(r, nil, inputFor(d, forkRules))
	rec := out.Record
	if out.Kind != Collected || rec.MarkerPresent == nil || *rec.MarkerPresent || rec.CIPathsTouched == nil || !*rec.CIPathsTouched {
		t.Fatalf("record = %+v", rec)
	}
	if rec.CollectionRulesHash != forkRules.CollectionHash() {
		t.Fatal("collection rules hash not recorded")
	}
	if r.calls["MarkerConclusions"] != 2 || r.calls["AwaitingRuns"] != 1 {
		t.Fatalf("calls = %v", r.calls)
	}
}

func TestCollectCIPathsFallbacks(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		retryable bool
	}{
		{"transient", &github.StatusError{Status: 502, Message: "bad gateway"}, true},
		{"too many files", ErrTooManyFiles, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := baseDetail()
			d.IsCrossRepository = true
			r := &fakeReader{detail: d, filesErr: tc.err}
			out := collect(r, nil, inputFor(d, forkRules))
			rec := out.Record
			if out.Kind != Collected || rec.CIPathsTouched != nil || len(rec.EvidenceErrors) != 1 ||
				rec.EvidenceErrors[0].Field != "ci_paths" || rec.EvidenceErrors[0].Retryable != tc.retryable || out.RetryableEvidence != tc.retryable {
				t.Fatalf("outcome = %+v", out)
			}
		})
	}
}

func TestCollectFatalFollowUp(t *testing.T) {
	d := baseDetail()
	d.IsCrossRepository = true
	r := &fakeReader{detail: d, awaitingErr: &github.TransportError{Err: errors.New("reset")}}
	out := collect(r, nil, inputFor(d, forkRules))
	if out.Kind != Failed || !out.Retryable || out.Record.Error == "" || out.Record.Title != "Fix" || out.Record.Version != classify.RecordVersion {
		t.Fatalf("outcome = %+v", out)
	}
	r = &fakeReader{detailErr: &github.StatusError{Status: 404, Message: "Not Found"}}
	out = collect(r, nil, inputFor(baseDetail(), rules.Default()))
	if out.Kind != Failed || out.Retryable || out.Record.Number != 7 || out.Record.CollectionRulesHash == "" {
		t.Fatalf("detail failure outcome = %+v", out)
	}
}

func TestCollectLastCommentAt(t *testing.T) {
	d := baseDetail()
	reviewAt := created.Add(2 * time.Hour)
	d.MyReviews = Nodes[ReviewNode]{Nodes: []ReviewNode{{State: "COMMENTED", SubmittedAt: &reviewAt, Commit: &OID{OID: "head"}}}}
	me := &Actor{Login: "me"}
	d.Comments = Nodes[CommentNode]{Nodes: []CommentNode{
		{Author: me, CreatedAt: created.Add(time.Hour), UpdatedAt: created.Add(time.Hour), DatabaseID: 1},
		{Author: me, CreatedAt: created.Add(3 * time.Hour), UpdatedAt: created.Add(3 * time.Hour), DatabaseID: 2},
	}}
	r := &fakeReader{detail: d, bodies: map[int64]string{2: "  /ok-to-test"}}
	out := collect(r, nil, inputFor(d, rules.Default()))
	if out.Record.MyLastCommentAt == nil || !out.Record.MyLastCommentAt.Equal(reviewAt) {
		t.Fatalf("slash command counted as hand-off: %v", out.Record.MyLastCommentAt)
	}
	if len(out.Caches.Comments) != 1 || !out.Caches.Comments[0].IsCommand || r.calls["CommentBody"] != 1 {
		t.Fatalf("comment cache staging = %+v calls %v", out.Caches.Comments, r.calls)
	}
	d.Comments.Nodes = append(d.Comments.Nodes, CommentNode{Author: me, CreatedAt: created.Add(4 * time.Hour), UpdatedAt: created.Add(4 * time.Hour), DatabaseID: 3})
	r = &fakeReader{detail: d, bodies: map[int64]string{3: "looks good"}}
	out = collect(r, &fakeCache{comments: map[int64]bool{2: true}}, inputFor(d, rules.Default()))
	if !out.Record.MyLastCommentAt.Equal(created.Add(4*time.Hour)) || r.calls["CommentBody"] != 1 {
		t.Fatalf("last comment = %v calls %v", out.Record.MyLastCommentAt, r.calls)
	}
	r = &fakeReader{detail: d, bodyErr: &github.StatusError{Status: 500, Message: "x"}}
	if out := collect(r, nil, inputFor(d, rules.Default())); out.Kind != Failed || !out.Retryable {
		t.Fatalf("comment read failure outcome = %+v", out)
	}
}

func approvedOn(d *Detail, commit string) {
	at := created.Add(30 * time.Minute)
	d.MyReviews = Nodes[ReviewNode]{Nodes: []ReviewNode{{State: "APPROVED", SubmittedAt: &at, Commit: &OID{OID: commit}}}}
}

func TestCollectDiffChanged(t *testing.T) {
	cases := []struct {
		name      string
		old, new  string
		changed   bool
		cacheHit  bool
		diffErr   error
		wantNil   bool
		diffCalls int
	}{
		{name: "index-only difference is unchanged", old: "diff\nindex 1..2\n+a\n", new: "diff\nindex 3..4\n+a\n", changed: false, diffCalls: 2},
		{name: "content difference is changed", old: "diff\n+a\n", new: "diff\n+b\n", changed: true, diffCalls: 2},
		{name: "binary keeps index lines", old: "Binary files a and b differ\nindex 1..2\n", new: "Binary files a and b differ\nindex 3..4\n", changed: true, diffCalls: 2},
		{name: "cache hit skips reads", cacheHit: true, changed: true, diffCalls: 0},
		{name: "read failure is unknown", diffErr: &github.StatusError{Status: 502}, wantNil: true, diffCalls: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := baseDetail()
			approvedOn(d, "old")
			r := &fakeReader{detail: d, diffErr: tc.diffErr, diffs: map[string][]byte{"base...old": []byte(tc.old), "base...head": []byte(tc.new)}}
			cache := &fakeCache{}
			if tc.cacheHit {
				cache.diffs = map[string]bool{"baseoldhead": true}
			}
			out := collect(r, cache, inputFor(d, rules.Default()))
			rec := out.Record
			if tc.wantNil {
				if rec.DiffChanged != nil || len(rec.EvidenceErrors) != 1 || !out.RetryableEvidence {
					t.Fatalf("record = %+v", rec)
				}
				return
			}
			if rec.DiffChanged == nil || *rec.DiffChanged != tc.changed || r.calls["Diff"] != tc.diffCalls {
				t.Fatalf("diff changed = %v calls = %v", rec.DiffChanged, r.calls)
			}
			if !tc.cacheHit && (len(out.Caches.Diffs) != 1 || out.Caches.Diffs[0].Changed != tc.changed) {
				t.Fatalf("diff cache staging = %+v", out.Caches.Diffs)
			}
		})
	}
}

func TestCollectChecksOnlyWhenApprovedAndBlocked(t *testing.T) {
	d := baseDetail()
	d.ReviewDecision = "APPROVED"
	d.MergeStateStatus = "BLOCKED"
	r := &fakeReader{detail: d, checks: []classify.Check{{Name: "tests", State: "FAILURE", Required: true}}}
	out := collect(r, nil, inputFor(d, rules.Default()))
	if len(out.Record.Checks) != 1 || r.calls["Checks"] != 1 {
		t.Fatalf("checks = %+v calls %v", out.Record.Checks, r.calls)
	}
	d = baseDetail()
	d.MergeStateStatus = "BLOCKED"
	r = &fakeReader{detail: d}
	collect(r, nil, inputFor(d, rules.Default()))
	if r.calls["Checks"] != 0 {
		t.Fatal("checks read for an unapproved PR")
	}
}

func TestCollectSignaturePolicy(t *testing.T) {
	cases := []struct {
		name      string
		reader    fakeReader
		cache     string
		want      string
		cached    bool
		evidence  bool
		retryable bool
	}{
		{name: "ruleset requires signatures", reader: fakeReader{rulesets: []string{"pull_request", "required_signatures"}}, want: "required", cached: true},
		{name: "classic enabled", reader: fakeReader{classic: true}, want: "required", cached: true},
		{name: "branch not protected", reader: fakeReader{classicErr: &github.StatusError{Status: 404, Message: "Branch not protected"}}, want: "absent", cached: true},
		{name: "no admin permission", reader: fakeReader{classicErr: &github.StatusError{Status: 403, Message: "Must have admin rights"}},
			want: "unavailable", cached: true, evidence: true},
		{name: "transient failure not cached", reader: fakeReader{rulesetErr: &github.StatusError{Status: 502}},
			want: "unavailable", evidence: true, retryable: true},
		{name: "cached answer", cache: "absent", want: "absent"},
		{name: "cached unavailable stays visible", cache: "unavailable", want: "unavailable", evidence: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := baseDetail()
			r := tc.reader
			r.detail = d
			out := collect(&r, &fakeCache{policy: tc.cache}, inputFor(d, rules.Default()))
			rec := out.Record
			if rec.SignaturePolicy != tc.want {
				t.Fatalf("policy = %q, want %q", rec.SignaturePolicy, tc.want)
			}
			if got := len(out.Caches.Policies) == 1; got != tc.cached {
				t.Fatalf("policy cached = %v, want %v", got, tc.cached)
			}
			if got := len(rec.EvidenceErrors) == 1; got != tc.evidence {
				t.Fatalf("evidence errors = %+v", rec.EvidenceErrors)
			}
			if tc.evidence && rec.EvidenceErrors[0].Retryable != tc.retryable {
				t.Fatalf("retryable = %v", rec.EvidenceErrors[0].Retryable)
			}
		})
	}
}

func TestEffectiveDiff(t *testing.T) {
	in := []byte("diff --git a/x b/x\nindex 111..222 100644\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b")
	want := "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b"
	if got := string(EffectiveDiff(in)); got != want {
		t.Fatalf("EffectiveDiff = %q", got)
	}
	bin := []byte("diff --git a/i b/i\nindex 1..2\nGIT binary patch\n")
	if string(EffectiveDiff(bin)) != string(bin) {
		t.Fatal("binary diff lost index lines")
	}
}

func TestSourceDelegates(t *testing.T) {
	d := baseDetail()
	r := &fakeReader{detail: d}
	s := NewSource(r, &fakeCache{}, nil)
	if _, err := s.Fingerprints(context.Background(), "o", "r"); err != nil || r.calls["Fingerprints"] != 1 {
		t.Fatalf("Fingerprints delegation: %v %v", err, r.calls)
	}
	if out := s.Collect(context.Background(), inputFor(d, rules.Default())); out.Kind != Collected {
		t.Fatalf("Collect delegation = %+v", out)
	}
}
