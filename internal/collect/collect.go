package collect

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/thingzio/devbox/internal/classify"
	"github.com/thingzio/devbox/internal/github"
	"github.com/thingzio/devbox/internal/rules"
	"github.com/thingzio/devbox/internal/store"
)

const policyTTL = time.Hour

// Cache is the read side of the evidence caches; *store.DB implements it.
type Cache interface {
	CachedDiff(ctx context.Context, repoID int64, base, reviewed, head string) (bool, bool, error)
	CachedComment(ctx context.Context, repoID, commentID int64, updatedAt time.Time) (bool, bool, error)
	CachedPolicy(ctx context.Context, repoID int64, baseRef string, now time.Time, ttl time.Duration) (string, bool, error)
}

// Input identifies one enrichment attempt.
type Input struct {
	RepoID            int64
	Owner, Name       string
	Viewer            string
	Number            int
	Rules             rules.Rules
	QueuedFingerprint string
	HeadChangedAt     *time.Time
	Now               time.Time
}

// Kind classifies an enrichment outcome.
type Kind int

// Outcome kinds.
const (
	Collected Kind = iota + 1
	Mismatch
	Failed
)

// Outcome is the result of one enrichment attempt.
type Outcome struct {
	Kind              Kind
	Record            classify.Record
	Fingerprint       string // Mismatch: the detail's fingerprint
	HeadOID           string // Mismatch: the detail's head
	Caches            store.Caches
	Err               error
	Retryable         bool // Failed: a later attempt may succeed
	RetryableEvidence bool // Collected: an optional read failed transiently
}

// Collector gathers evidence for one PR.
type Collector struct {
	Reader Reader
	Cache  Cache
	Log    *slog.Logger
}

// Collect reads detail and follow-up evidence for in.
func (c *Collector) Collect(ctx context.Context, in Input) Outcome {
	log := c.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	log = log.With("repo", in.Owner+"/"+in.Name, "pr", in.Number)
	partial := classify.Record{Version: classify.RecordVersion, CollectionRulesHash: in.Rules.CollectionHash(), Number: in.Number}
	d, err := c.Reader.Detail(ctx, in.Owner, in.Name, in.Number, in.Viewer)
	if err != nil {
		return failed(partial, fmt.Errorf("pull request detail: %w", err))
	}
	log = log.With("head", d.HeadRefOid)
	if fp := Fingerprint(d.FingerprintFields()); fp != in.QueuedFingerprint {
		log.Debug("detail differs from queued fingerprint")
		return Outcome{Kind: Mismatch, Fingerprint: fp, HeadOID: d.HeadRefOid}
	}
	rec, err := Normalize(d, in.Viewer, in.HeadChangedAt)
	rec.Version, rec.CollectionRulesHash = partial.Version, partial.CollectionRulesHash
	if err != nil {
		return failed(rec, err)
	}
	if rec.Draft || rec.Author == in.Viewer {
		return Outcome{Kind: Collected, Record: rec}
	}
	f := &followUp{c: c, in: in, d: d, rec: &rec, log: log}
	if err := f.run(ctx); err != nil {
		return failed(rec, err)
	}
	log.Debug("evidence collected", "evidence_errors", len(rec.EvidenceErrors),
		"staged_caches", len(f.caches.Diffs)+len(f.caches.Comments)+len(f.caches.Policies))
	return Outcome{Kind: Collected, Record: rec, Caches: f.caches, RetryableEvidence: f.retryable}
}

func failed(rec classify.Record, err error) Outcome {
	rec.Error = err.Error()
	return Outcome{Kind: Failed, Record: rec, Err: err, Retryable: github.Retryable(err)}
}

type followUp struct {
	c         *Collector
	in        Input
	d         *Detail
	rec       *classify.Record
	caches    store.Caches
	retryable bool
	log       *slog.Logger
}

func (f *followUp) run(ctx context.Context) error {
	rec := f.rec
	if rec.Fork && !rec.IsBot {
		if err := f.testApproval(ctx); err != nil {
			return fmt.Errorf("test approval evidence: %w", err)
		}
	}
	last, err := f.lastCommentAt(ctx)
	if err != nil {
		return fmt.Errorf("comment evidence: %w", err)
	}
	rec.MyLastCommentAt = last
	f.diffChanged(ctx)
	if (rec.ReviewDecision == "APPROVED" || rec.IsBot) && (rec.MergeState == "BLOCKED" || rec.MergeState == "UNSTABLE") {
		checks, err := f.c.Reader.Checks(ctx, f.in.Owner, f.in.Name, rec.HeadOID, rec.Number)
		if err != nil {
			return fmt.Errorf("required checks: %w", err)
		}
		rec.Checks = checks
	}
	rec.SignaturePolicy = f.signaturePolicy(ctx)
	return nil
}

func (f *followUp) evidence(field string, err error) {
	retry := github.Retryable(err)
	f.rec.EvidenceErrors = append(f.rec.EvidenceErrors, classify.EvidenceError{Field: field, Message: err.Error(), Retryable: retry})
	if retry {
		f.retryable = true
	}
	f.log.Debug("optional evidence unavailable", "field", field, "retryable", retry, "err", err)
}

func (f *followUp) testApproval(ctx context.Context) error {
	rec, in, ta := f.rec, f.in, f.in.Rules.TestApproval
	n, err := f.c.Reader.AwaitingRuns(ctx, in.Owner, in.Name, rec.HeadOID)
	if err != nil {
		return err
	}
	rec.AwaitingRuns = n
	if len(ta.MarkerChecks) > 0 {
		present := false
		for _, check := range ta.MarkerChecks {
			conclusions, err := f.c.Reader.MarkerConclusions(ctx, in.Owner, in.Name, rec.HeadOID, check)
			if err != nil {
				return err
			}
			for _, c := range conclusions {
				// A run that concluded "skipped" was filtered out by its workflow, not approved to run.
				if c != "skipped" {
					present = true
				}
			}
		}
		rec.MarkerPresent = &present
	}
	if len(ta.CIPaths) > 0 && (rec.AwaitingRuns > 0 || (rec.MarkerPresent != nil && !*rec.MarkerPresent)) {
		files, err := f.c.Reader.ChangedFiles(ctx, in.Owner, in.Name, rec.BaseOID, rec.HeadOID)
		if err != nil {
			f.evidence("ci_paths", err)
			return nil
		}
		touched := false
		for _, name := range files {
			for _, prefix := range ta.CIPaths {
				if strings.HasPrefix(name, prefix) {
					touched = true
				}
			}
		}
		rec.CIPathsTouched = &touched
	}
	return nil
}

// lastCommentAt is the viewer's latest hand-off to the author: the newest issue
// comment after the viewer's latest review that is not a slash command, else
// that review's time.
func (f *followUp) lastCommentAt(ctx context.Context) (*time.Time, error) {
	var reviewed *time.Time
	for _, r := range f.rec.MyReviews {
		if reviewed == nil || r.SubmittedAt.After(*reviewed) {
			t := r.SubmittedAt
			reviewed = &t
		}
	}
	nodes := f.d.Comments.Nodes
	for i := len(nodes) - 1; i >= 0; i-- {
		cm := nodes[i]
		if login(cm.Author) != f.in.Viewer {
			continue
		}
		if reviewed != nil && !cm.CreatedAt.After(*reviewed) {
			break
		}
		isCommand, ok, err := f.c.Cache.CachedComment(ctx, f.in.RepoID, cm.DatabaseID, cm.UpdatedAt)
		if err != nil {
			f.log.Warn("comment cache read failed", "err", err)
		}
		f.log.Debug("comment cache", "comment", cm.DatabaseID, "hit", err == nil && ok)
		if err != nil || !ok {
			body, err := f.c.Reader.CommentBody(ctx, f.in.Owner, f.in.Name, cm.DatabaseID)
			if err != nil {
				return nil, err
			}
			isCommand = strings.HasPrefix(strings.TrimLeftFunc(body, unicode.IsSpace), "/")
			f.caches.Comments = append(f.caches.Comments, store.CommentCache{CommentID: cm.DatabaseID, UpdatedAt: cm.UpdatedAt, IsCommand: isCommand})
		}
		if !isCommand {
			t := cm.CreatedAt
			return &t, nil
		}
	}
	return reviewed, nil
}

func (f *followUp) diffChanged(ctx context.Context) {
	rec, in := f.rec, f.in
	baseline := classify.ReviewBaseline(rec.MyReviews)
	if baseline == nil || baseline.Commit == "" || baseline.Commit == rec.HeadOID {
		return
	}
	changed, ok, err := f.c.Cache.CachedDiff(ctx, in.RepoID, rec.BaseOID, baseline.Commit, rec.HeadOID)
	if err != nil {
		f.log.Warn("diff cache read failed", "err", err)
	}
	f.log.Debug("diff cache", "reviewed", baseline.Commit, "hit", err == nil && ok)
	if err == nil && ok {
		rec.DiffChanged = &changed
		return
	}
	reviewed, err := f.c.Reader.Diff(ctx, in.Owner, in.Name, rec.BaseOID, baseline.Commit)
	if err != nil {
		f.evidence("diff", err)
		return
	}
	current, err := f.c.Reader.Diff(ctx, in.Owner, in.Name, rec.BaseOID, rec.HeadOID)
	if err != nil {
		f.evidence("diff", err)
		return
	}
	changed = !bytes.Equal(EffectiveDiff(reviewed), EffectiveDiff(current))
	rec.DiffChanged = &changed
	f.caches.Diffs = append(f.caches.Diffs, store.DiffCache{BaseOID: rec.BaseOID, ReviewedOID: baseline.Commit, HeadOID: rec.HeadOID, Changed: changed})
}

func (f *followUp) signaturePolicy(ctx context.Context) string {
	rec, in := f.rec, f.in
	policy, ok, err := f.c.Cache.CachedPolicy(ctx, in.RepoID, rec.BaseRef, in.Now, policyTTL)
	if err != nil {
		f.log.Warn("policy cache read failed", "err", err)
	}
	f.log.Debug("policy cache", "base_ref", rec.BaseRef, "hit", err == nil && ok)
	if err == nil && ok {
		if policy == "unavailable" {
			f.evidence("signature_policy", errors.New("signature policy unreadable (cached)"))
		}
		return policy
	}
	types, err := f.c.Reader.RulesetTypes(ctx, in.Owner, in.Name, rec.BaseRef)
	if err != nil {
		return f.unavailablePolicy(err)
	}
	for _, t := range types {
		if t == "required_signatures" {
			return f.definitePolicy("required")
		}
	}
	enabled, err := f.c.Reader.ClassicSignatures(ctx, in.Owner, in.Name, rec.BaseRef)
	if err != nil {
		var se *github.StatusError
		if errors.As(err, &se) && se.Status == 404 && strings.Contains(se.Message, "Branch not protected") {
			return f.definitePolicy("absent")
		}
		return f.unavailablePolicy(err)
	}
	if enabled {
		return f.definitePolicy("required")
	}
	return f.definitePolicy("absent")
}

func (f *followUp) definitePolicy(policy string) string {
	f.caches.Policies = append(f.caches.Policies, store.PolicyCache{BaseRef: f.rec.BaseRef, Policy: policy, FetchedAt: f.in.Now})
	return policy
}

func (f *followUp) unavailablePolicy(err error) string {
	f.evidence("signature_policy", err)
	if !github.Retryable(err) {
		f.caches.Policies = append(f.caches.Policies, store.PolicyCache{BaseRef: f.rec.BaseRef, Policy: "unavailable", FetchedAt: f.in.Now})
	}
	return "unavailable"
}

// EffectiveDiff drops "index" lines, whose blob hashes change on rebase, unless
// the diff has a binary section whose identity lives only in those lines.
func EffectiveDiff(diff []byte) []byte {
	lines := bytes.Split(diff, []byte("\n"))
	for _, l := range lines {
		if bytes.HasPrefix(l, []byte("Binary files ")) || bytes.HasPrefix(l, []byte("GIT binary patch")) {
			return diff
		}
	}
	kept := make([][]byte, 0, len(lines))
	for _, l := range lines {
		if !bytes.HasPrefix(l, []byte("index ")) {
			kept = append(kept, l)
		}
	}
	return bytes.Join(kept, []byte("\n"))
}

// Source adapts a Reader and Cache to the poller and enrichment interface.
type Source struct {
	reader    Reader
	collector *Collector
}

// NewSource returns a Source backed by r and cache.
func NewSource(r Reader, cache Cache, log *slog.Logger) *Source {
	return &Source{reader: r, collector: &Collector{Reader: r, Cache: cache, Log: log}}
}

// Fingerprints lists the open PRs of a repository.
func (s *Source) Fingerprints(ctx context.Context, owner, name string) ([]Observed, error) {
	return s.reader.Fingerprints(ctx, owner, name)
}

// Collect enriches one PR.
func (s *Source) Collect(ctx context.Context, in Input) Outcome {
	return s.collector.Collect(ctx, in)
}
