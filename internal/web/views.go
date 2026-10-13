package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/thingzio/devbox/internal/classify"
	"github.com/thingzio/devbox/internal/store"
)

const staleAfter = 3 * time.Minute

type noteView struct{ Text, URL string }

type rowView struct {
	Repo       string
	Account    string
	Number     int
	Title      string
	URL        string
	ExplainURL string
	Author     string
	Age        string
	Reason     string
	Action     string
	Also       []string
	Notes      []noteView
	FirstTime  bool
	Size       string
	Milestone  string
	rank       classify.Rankable
}

type bucketView struct {
	ID, Title, Tone, Handoff string
	Link, LinkLabel          string
	Rows                     []rowView
}

// tileView is one stat tile; tiles double as the bucket filter.
type tileView struct {
	Title, Tone, URL string
	Count            int
	Active           bool
}

// countLinkView is a sidebar entry: a link with a count beside it.
type countLinkView struct {
	Title, URL string
	Count      int
	Active     bool
}

type queueView struct {
	Tiles        []tileView
	Buckets      []bucketView
	Ready        []rowView
	ReadyURL     string
	Others       []countLinkView
	Repos        []countLinkView
	Unclassified []rowView
	EmptyMessage string
	FragmentURL  string
	Filter       store.QueueFilter
	RepoFilter   string
	Accounts     []string
	RepoOptions  []string
}

// bucketTones maps buckets onto the severity scale; unlisted buckets are muted.
var bucketTones = map[classify.Bucket]string{
	classify.WaitingOnMe:         "critical",
	classify.WaitingTestApproval: "high",
	classify.NeedsRereview:       "high",
	classify.ApprovedBlocked:     "medium",
	classify.NeedsInitialReview:  "low",
	classify.ReadyToMerge:        "ok",
}

func tone(b classify.Bucket) string {
	if t, ok := bucketTones[b]; ok {
		return t
	}
	return "muted"
}

// queueQuery is what a queue page shows and where its links point.
type queueQuery struct {
	filter  store.QueueFilter // Bucket is applied after counting, so every tile keeps its count
	repo    string            // raw repository filter, echoed into links
	base    string            // page path that tiles and sidebar links point to
	perRepo bool              // repository pages offer hand-off commands
}

func (q queueQuery) link(bucket string) string {
	return queueLink(q.base, q.filter.Account, q.repo, bucket)
}

// toggle links to bucket, or back to every bucket when bucket is already selected.
func (q queueQuery) toggle(bucket string) string {
	if q.filter.Bucket == bucket {
		return q.link("")
	}
	return q.link(bucket)
}

func (q queueQuery) filtered() bool {
	return q.filter.Account != "" || q.repo != "" || q.filter.Bucket != ""
}

func (s *Server) buildQueue(ctx context.Context, q queueQuery) (queueView, error) {
	all, err := s.o.Store.Queue(ctx, store.QueueFilter{})
	if err != nil {
		return queueView{}, err
	}
	scoped := all
	if f := q.filter; f.Account != "" || f.Owner != "" || f.Name != "" {
		if scoped, err = s.o.Store.Queue(ctx, store.QueueFilter{Owner: f.Owner, Name: f.Name, Account: f.Account}); err != nil {
			return queueView{}, err
		}
	}
	byBucket := s.groupRows(scoped)
	selected := q.filter.Bucket
	view := queueView{Filter: q.filter}
	view.Tiles = append(view.Tiles, tileView{Title: "All actions", Tone: "accent", URL: q.link(""), Active: selected == ""})
	for _, b := range classify.ActionBuckets() {
		n := len(byBucket[b])
		view.Tiles[0].Count += n
		view.Tiles = append(view.Tiles, tileView{Title: b.Title(), Tone: tone(b), URL: q.toggle(string(b)), Count: n, Active: selected == string(b)})
	}
	shown := classify.ActionBuckets()
	if selected != "" {
		shown = []classify.Bucket{classify.Bucket(selected)}
	}
	for _, b := range shown {
		rs := byBucket[b]
		if len(rs) == 0 {
			continue
		}
		bv := bucketView{ID: string(b), Title: b.Title(), Tone: tone(b), Rows: rs, Link: q.toggle(string(b)), LinkLabel: "Only these"}
		if selected != "" {
			bv.LinkLabel = "All buckets"
		}
		if q.perRepo {
			var urls []string
			for _, r := range rs {
				if r.URL != "" {
					urls = append(urls, r.URL)
				}
			}
			bv.Handoff = classify.Handoff(b, urls)
		}
		view.Buckets = append(view.Buckets, bv)
	}
	view.Ready = byBucket[classify.ReadyToMerge]
	view.ReadyURL = q.toggle(string(classify.ReadyToMerge))
	for _, b := range []classify.Bucket{classify.WaitingOnAuthor, classify.Own, classify.Draft} {
		view.Others = append(view.Others, countLinkView{Title: b.Title(), URL: q.toggle(string(b)), Count: len(byBucket[b]), Active: selected == string(b)})
	}
	view.Unclassified = byBucket[classify.Unclassified]
	if view.Repos, err = s.repoLinks(ctx, all, q.filter); err != nil {
		return queueView{}, err
	}
	if len(view.Buckets) == 0 {
		if q.filtered() {
			view.EmptyMessage = "Nothing matches these filters."
		} else if view.EmptyMessage, err = s.emptyMessage(ctx); err != nil {
			return queueView{}, err
		}
	}
	return view, nil
}

// groupRows converts queue rows to row views grouped by bucket, each group ranked.
func (s *Server) groupRows(rows []store.QueueRow) map[classify.Bucket][]rowView {
	now := s.o.Now()
	byBucket := map[classify.Bucket][]rowView{}
	for _, q := range rows {
		var rec classify.Record
		_ = json.Unmarshal(q.Record, &rec) // an undecodable record only loses badges
		bucket := classify.Bucket(q.Result.Bucket)
		repo := q.Owner + "/" + q.Name
		rv := rowView{
			Repo: repo, Account: q.Account, Number: q.Number, Title: titleOr(q.Title, q.Number), URL: githubURL(q.URL),
			ExplainURL: explainURL(q.Owner, q.Name, q.Number), Author: q.Author, Age: age(now, q.Result.WaitingSince),
			Reason: q.Result.Reason, Action: q.Result.Action, Also: q.Result.Also, Notes: notes(q.Result.Notes),
			FirstTime: classify.IsFirstTime(rec.AuthorAssociation),
			rank:      classify.RankKey(rec, bucket, q.Result.WaitingSince, repo),
		}
		rv.rank.Number = q.Number
		if rec.Additions+rec.Deletions > 0 {
			rv.Size = classify.SizeBadge(rec.Additions, rec.Deletions)
		}
		if rec.Milestone != nil {
			rv.Milestone = rec.Milestone.Title
		}
		byBucket[bucket] = append(byBucket[bucket], rv)
	}
	for b := range byBucket {
		slices.SortFunc(byBucket[b], func(x, y rowView) int { return classify.Compare(x.rank, y.rank) })
	}
	return byBucket
}

// repoLinks lists each enabled repository once, with its action count across
// every account, so the sidebar navigates regardless of the current filter.
func (s *Server) repoLinks(ctx context.Context, all []store.QueueRow, current store.QueueFilter) ([]countLinkView, error) {
	repos, err := s.o.Store.Repos(ctx)
	if err != nil {
		return nil, err
	}
	actions := map[string]int{}
	for _, r := range all {
		if classify.Bucket(r.Result.Bucket).IsAction() {
			actions[strings.ToLower(r.Owner+"/"+r.Name)]++
		}
	}
	var out []countLinkView
	seen := map[string]bool{}
	for _, r := range repos {
		key := strings.ToLower(r.FullName())
		if !r.Enabled || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, countLinkView{Title: r.FullName(), URL: repoURL(r.Owner, r.Name), Count: actions[key],
			Active: strings.EqualFold(r.Owner, current.Owner) && strings.EqualFold(r.Name, current.Name)})
	}
	return out, nil
}

func (s *Server) emptyMessage(ctx context.Context) (string, error) {
	accounts, err := s.o.Store.Accounts(ctx)
	if err != nil {
		return "", err
	}
	if len(accounts) == 0 {
		return "Add a GitHub account in Settings to get started.", nil
	}
	repos, err := s.o.Store.Repos(ctx)
	if err != nil {
		return "", err
	}
	if !slices.ContainsFunc(repos, func(r store.Repo) bool { return r.Enabled }) {
		return "Add a repository in Settings to start monitoring.", nil
	}
	return "Nothing is waiting on you.", nil
}

func titleOr(title string, number int) string {
	if title == "" {
		return fmt.Sprintf("(loading PR #%d)", number)
	}
	return title
}

func repoURL(owner, name string) string {
	return "/r/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
}

func explainURL(owner, name string, number int) string {
	return repoURL(owner, name) + "/" + strconv.Itoa(number)
}

func notes(items []string) []noteView {
	out := make([]noteView, 0, len(items))
	for _, n := range items {
		out = append(out, noteView{Text: n, URL: githubURL(n)})
	}
	return out
}

// age renders whole days since t, "today" under one day (§9).
func age(now time.Time, since *time.Time) string {
	if since == nil {
		return "—"
	}
	days := int(now.Sub(*since) / (24 * time.Hour))
	switch {
	case days < 1:
		return "today"
	case days == 1:
		return "1 day"
	}
	return fmt.Sprintf("%d days", days)
}

func minutesAgo(d time.Duration) string {
	return fmt.Sprintf("%dm", int(d.Round(time.Minute)/time.Minute))
}

type accountStatus struct{ Login, Detail string }

type pausedView struct{ Account, Until string }

type repoStatusView struct {
	Name     string
	Level    string
	Messages []string
}

// syncView is the header heartbeat: when repositories were last checked and how.
// State is ok, warn, paused, manual, or idle (never checked).
type syncView struct{ State, Checked, Mode string }

type statusView struct {
	Accounts []accountStatus
	Paused   []pausedView
	Repos    []repoStatusView
	Sync     syncView
}

// buildStatus feeds the header: auth problems, rate pauses, staleness,
// enrichment backlog (§9), and the sync heartbeat. Disabled repositories are omitted.
func (s *Server) buildStatus(ctx context.Context) statusView {
	var v statusView
	now := s.o.Now()
	manual := s.o.Engine != nil && s.o.Engine.Polling().Manual
	v.Sync = syncView{State: "idle", Checked: "never", Mode: "manual: press Refresh"}
	if s.o.Engine != nil && !manual {
		v.Sync.Mode = "checking every " + s.o.Engine.Polling().String()
	}
	if accounts, err := s.o.Store.Accounts(ctx); err == nil {
		for _, a := range accounts {
			if a.Status != store.StatusOK {
				v.Accounts = append(v.Accounts, accountStatus{Login: a.Login, Detail: a.StatusDetail})
			}
		}
	}
	if s.o.Engine != nil {
		for account, until := range s.o.Engine.RateStatus() {
			v.Paused = append(v.Paused, pausedView{Account: account, Until: until.In(s.o.Location).Format("15:04")})
		}
		slices.SortFunc(v.Paused, func(a, b pausedView) int { return strings.Compare(a.Account, b.Account) })
	}
	statuses, err := s.o.Store.RepoStatuses(ctx)
	if err != nil {
		s.o.Log.Error("repo statuses", "err", err)
		return v
	}
	var lastPoll *time.Time
	warn := false
	for _, st := range statuses {
		if !st.Repo.Enabled {
			continue
		}
		if p := st.Sync.LastPollAt; p != nil && (lastPoll == nil || p.After(*lastPoll)) {
			lastPoll = p
		}
		var msgs []string
		level := "info"
		last := st.Sync.LastSuccessAt
		switch {
		case manual:
			// Staleness is expected between refreshes; the queue shows when repositories were last checked.
		case last == nil && now.Sub(st.Repo.AddedAt) > staleAfter:
			msgs, level = append(msgs, "no successful poll yet"), "warn"
		case last != nil && now.Sub(*last) > staleAfter:
			msgs, level = append(msgs, fmt.Sprintf("last successful poll %s ago", minutesAgo(now.Sub(*last)))), "warn"
		}
		if st.Sync.LastError != "" {
			msgs, level = append(msgs, "last error: "+st.Sync.LastError), "warn"
		}
		if st.Pending > 0 {
			msgs = append(msgs, fmt.Sprintf("%d awaiting enrichment", st.Pending))
		}
		if st.Errored > 0 {
			msgs, level = append(msgs, fmt.Sprintf("%d with evidence errors", st.Errored)), "warn"
		}
		if st.Retrying > 0 {
			detail := fmt.Sprintf("%d retrying", st.Retrying)
			if st.NextRetryAt != nil {
				detail += " (next " + st.NextRetryAt.In(s.o.Location).Format("15:04:05")
				if st.LastAttemptAt != nil {
					detail += ", last attempt " + st.LastAttemptAt.In(s.o.Location).Format("15:04:05")
				}
				detail += ")"
			}
			msgs = append(msgs, detail)
		}
		if len(msgs) > 0 {
			v.Repos = append(v.Repos, repoStatusView{Name: st.Repo.FullName() + " @" + st.Repo.Account, Level: level, Messages: msgs})
			warn = warn || level == "warn"
		}
	}
	if lastPoll != nil {
		v.Sync.Checked = sinceLabel(now.Sub(*lastPoll))
	}
	v.Sync.State = syncState(len(v.Paused) > 0, manual, lastPoll != nil, warn)
	return v
}

func syncState(paused, manual, polled, warn bool) string {
	switch {
	case paused:
		return "paused"
	case manual:
		return "manual"
	case !polled:
		return "idle"
	case warn:
		return "warn"
	}
	return "ok"
}
