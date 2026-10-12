package classify

import (
	"fmt"
	"strings"
	"time"

	"github.com/thingzio/devbox/internal/rules"
)

// Result is the action owed for one pull request.
type Result struct {
	Bucket       Bucket
	Reason       string
	Action       string
	WaitingSince *time.Time
	Also         []string
	Notes        []string
}

// Classify derives the result for rec as seen by viewer at now (§5.2).
func Classify(rec Record, rl rules.Rules, viewer string, now time.Time) Result {
	if rec.Error != "" {
		return Result{Bucket: Unclassified, Reason: rec.Error, Action: "check the PR on GitHub"}
	}
	var res Result
	switch {
	case rec.Draft:
		res = Result{Bucket: Draft, Reason: "draft", WaitingSince: timePtr(rec.CreatedAt)}
	case rec.Author == viewer:
		res = Result{Bucket: Own, Reason: "your pull request", WaitingSince: timePtr(rec.CreatedAt)}
	default:
		causes := mergeBlockers(rec)
		if rec.IsBot {
			res = botResult(rec, causes)
		} else {
			res = candidateResult(rec, rl, now, causes)
		}
		res.Notes = append(res.Notes, commonNotes(rec)...)
	}
	res.Notes = append(res.Notes, evidenceNotes(rec)...)
	return res
}

type candidate struct {
	bucket Bucket
	result *Result
}

func candidateResult(rec Record, rl rules.Rules, now time.Time, causes []cause) Result {
	review := reviewable(rec)
	rereview := needsRereview(rec)
	candidates := []candidate{
		{WaitingTestApproval, awaitingTestApproval(rec, rl.TestApproval)},
		{WaitingOnMe, onlyIf(review, waitingOnMe(rec))},
		{NeedsRereview, rereview},
		{ApprovedBlocked, approvedBlocked(rec, causes)},
		{NeedsInitialReview, onlyIf(review, needsInitialReview(rec))},
		{StaleChangesRequested, onlyIf(rereview == nil, staleChangesRequested(rec, now, rl.StaleDays))},
	}
	var primary *Result
	var also []string
	for _, c := range candidates {
		if c.result == nil {
			continue
		}
		if primary == nil {
			primary = c.result
			primary.Bucket = c.bucket
			continue
		}
		also = append(also, c.bucket.Title())
	}
	if primary != nil {
		primary.Also = also
		return *primary
	}
	if rec.ReviewDecision == "APPROVED" && len(causes) == 0 {
		return Result{Bucket: ReadyToMerge, Reason: "approved; ready to merge", Action: "merge",
			WaitingSince: firstTime(rec.ApprovedAt, rec.UpdatedAt)}
	}
	return Result{Bucket: WaitingOnAuthor, Reason: "waiting on author", WaitingSince: firstTime(rec.MyLastCommentAt, rec.UpdatedAt)}
}

func onlyIf(ok bool, r *Result) *Result {
	if !ok {
		return nil
	}
	return r
}

// authorMoved reports whether the PR is new to the viewer or the author acted after the viewer's last comment.
func authorMoved(rec Record) bool {
	return rec.MyLastCommentAt == nil || (rec.AuthorUpdatedAt != nil && rec.AuthorUpdatedAt.After(*rec.MyLastCommentAt))
}

func reviewable(rec Record) bool { return rec.MergeState != "DIRTY" && authorMoved(rec) }

func awaitingTestApproval(rec Record, ta rules.TestApproval) *Result {
	// GitHub runs no pull_request workflows while a PR conflicts.
	if rec.MergeState == "DIRTY" {
		return nil
	}
	since := timePtr(rec.HeadActivityAt())
	if n := rec.AwaitingRuns; n > 0 {
		return &Result{Reason: fmt.Sprintf("%d workflow run%s awaiting approval", n, plural(n)),
			Action: "approve and run workflows after reading the diff", WaitingSince: since}
	}
	if !rec.Fork || len(ta.MarkerChecks) == 0 || rec.MarkerPresent == nil || *rec.MarkerPresent {
		return nil
	}
	names := make([]string, len(ta.MarkerChecks))
	for i, name := range ta.MarkerChecks {
		names[i] = "`" + name + "`"
	}
	reason := fmt.Sprintf("fork PR; no %s check on %s", strings.Join(names, " or "), short(rec.HeadOID))
	command := ta.Command
	if len(ta.CIPaths) > 0 && (rec.CIPathsTouched == nil || *rec.CIPathsTouched) {
		command = ta.CIPathsCommand
		if rec.CIPathsTouched == nil {
			reason += "; file list unreadable"
		} else {
			reason += "; changes CI files"
		}
	}
	return &Result{Reason: reason, Action: fmt.Sprintf("comment `%s` after reading the diff", command), WaitingSince: since}
}

func waitingOnMe(rec Record) *Result {
	threads := rec.MyThreadsWaiting
	if !rec.ReviewRequestedFromMe && len(threads) == 0 {
		return nil
	}
	var parts []string
	action := "reply in the linked threads"
	if rec.ReviewRequestedFromMe {
		parts = append(parts, "your review is requested")
		action = "review"
	}
	if len(threads) > 0 {
		parts = append(parts, fmt.Sprintf("author replied in your threads (%d)", len(threads)))
	}
	since := rec.UpdatedAt
	notes := make([]string, 0, len(threads))
	for i, th := range threads {
		if i == 0 || th.LastAt.After(since) {
			since = th.LastAt
		}
		notes = append(notes, th.URL)
	}
	return &Result{Reason: strings.Join(parts, "; "), Action: action, WaitingSince: timePtr(since), Notes: notes}
}

func needsRereview(rec Record) *Result {
	if rec.MergeState == "DIRTY" {
		return nil
	}
	baseline := ReviewBaseline(rec.MyReviews)
	if baseline == nil || baseline.Commit == rec.HeadOID || (rec.DiffChanged != nil && !*rec.DiffChanged) {
		return nil
	}
	reason := "could not confirm the diff is unchanged since your review"
	if rec.DiffChanged != nil {
		reason = "diff changed since your review"
	}
	commit := baseline.Commit
	if commit == "" {
		commit = "unknown"
	}
	state := strings.ReplaceAll(strings.ToLower(baseline.State), "_", " ")
	since := rec.HeadActivityAt()
	if baseline.SubmittedAt.After(since) {
		since = baseline.SubmittedAt
	}
	return &Result{Reason: fmt.Sprintf("%s (%s at %s)", reason, state, short(commit)), Action: "re-review", WaitingSince: timePtr(since)}
}

func approvedBlocked(rec Record, causes []cause) *Result {
	if rec.ReviewDecision != "APPROVED" || len(causes) == 0 {
		return nil
	}
	reason, action := summarize(causes)
	return &Result{Reason: reason, Action: action, WaitingSince: firstTime(rec.ApprovedAt, rec.UpdatedAt)}
}

func needsInitialReview(rec Record) *Result {
	if len(rec.MyReviews) > 0 {
		return nil
	}
	return &Result{Reason: "not reviewed by you", Action: "review", WaitingSince: timePtr(rec.CreatedAt)}
}

func staleChangesRequested(rec Record, now time.Time, staleDays int) *Result {
	decisive := latestReview(rec.MyReviews, decisiveStates)
	if decisive == nil || decisive.State != "CHANGES_REQUESTED" || authorMoved(rec) {
		return nil
	}
	var handed *time.Time
	for _, r := range rec.MyReviews {
		if rec.AuthorUpdatedAt != nil && !r.SubmittedAt.After(*rec.AuthorUpdatedAt) {
			continue
		}
		if handed == nil || r.SubmittedAt.Before(*handed) {
			handed = timePtr(r.SubmittedAt)
		}
	}
	if handed == nil && rec.MyLastCommentAt != nil {
		handed = timePtr(*rec.MyLastCommentAt)
	}
	if handed == nil {
		return nil
	}
	// Without an observed push the history is unknown: a push may have followed earlier feedback.
	if rec.HeadChangedAt == nil && handed.Before(decisive.SubmittedAt) {
		handed = timePtr(decisive.SubmittedAt)
	}
	age := now.Sub(*handed)
	if age < time.Duration(staleDays)*24*time.Hour {
		return nil
	}
	return &Result{
		Reason:       fmt.Sprintf("changes requested; no author activity in %d days since your feedback", int(age/(24*time.Hour))),
		Action:       "nudge the author, or close",
		WaitingSince: handed,
	}
}

func botResult(rec Record, causes []cause) Result {
	since := timePtr(rec.CreatedAt)
	switch {
	case rec.ReviewDecision == "APPROVED" && len(causes) == 0:
		return Result{Bucket: Bot, Reason: "approved; ready to merge", Action: "merge", WaitingSince: since}
	case rec.ReviewDecision == "APPROVED":
		reason, action := summarize(causes)
		return Result{Bucket: Bot, Reason: reason, Action: action, WaitingSince: since}
	}
	decision := "no review decision"
	if rec.ReviewDecision != "" {
		decision = strings.ReplaceAll(strings.ToLower(rec.ReviewDecision), "_", " ")
	}
	return Result{Bucket: Bot, Reason: decision, Action: "review in batch", WaitingSince: since}
}

func commonNotes(rec Record) []string {
	var notes []string
	for _, o := range rec.OthersDecisive {
		switch o.State {
		case "APPROVED":
			notes = append(notes, "approved by @"+o.Author)
		case "CHANGES_REQUESTED":
			notes = append(notes, "changes requested by @"+o.Author)
		}
	}
	if b := ReviewBaseline(rec.MyReviews); b != nil && b.Commit != rec.HeadOID && rec.DiffChanged != nil && !*rec.DiffChanged {
		notes = append(notes, "branch updated, diff unchanged")
	}
	if rec.MergeState == "UNSTABLE" {
		notes = append(notes, "non-required checks failing")
	}
	return notes
}

func evidenceNotes(rec Record) []string {
	var notes []string
	for _, e := range rec.EvidenceErrors {
		note := fmt.Sprintf("%s unavailable: %s", e.Field, e.Message)
		if e.Retryable {
			note += " (retry pending)"
		}
		notes = append(notes, note)
	}
	if rec.CoreRetry != nil {
		notes = append(notes, fmt.Sprintf("evidence stale since %s, retrying: %s",
			rec.CoreRetry.Since.UTC().Format(time.RFC3339), rec.CoreRetry.Message))
	}
	return notes
}

func firstTime(p *time.Time, fallback time.Time) *time.Time {
	if p != nil {
		return timePtr(*p)
	}
	return timePtr(fallback)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func timePtr(t time.Time) *time.Time { return &t }
