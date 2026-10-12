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
	ID, Title, Handoff string
	Rows               []rowView
}

type bucketOption struct{ ID, Title string }

type countsView struct{ WaitingOnAuthor, Own, Draft int }

type queueView struct {
	Buckets       []bucketView
	Ready         []rowView
	Unclassified  []rowView
	Counts        countsView
	Totals        string
	EmptyMessage  string
	FragmentURL   string
	Filter        store.QueueFilter
	RepoFilter    string
	Accounts      []string
	BucketOptions []bucketOption
}

func (s *Server) buildQueue(ctx context.Context, f store.QueueFilter, perRepo bool) (queueView, error) {
	rows, err := s.o.Store.Queue(ctx, f)
	if err != nil {
		return queueView{}, err
	}
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
	view := queueView{Filter: f}
	var totals []string
	actions := 0
	for _, b := range classify.ActionBuckets() {
		view.BucketOptions = append(view.BucketOptions, bucketOption{ID: string(b), Title: b.Title()})
		rs := byBucket[b]
		if len(rs) == 0 {
			continue
		}
		bv := bucketView{ID: string(b), Title: b.Title(), Rows: rs}
		if perRepo {
			var urls []string
			for _, r := range rs {
				if r.URL != "" {
					urls = append(urls, r.URL)
				}
			}
			bv.Handoff = classify.Handoff(b, urls)
		}
		view.Buckets = append(view.Buckets, bv)
		totals = append(totals, fmt.Sprintf("%d %s", len(rs), b.Title()))
		actions += len(rs)
	}
	for _, b := range []classify.Bucket{classify.ReadyToMerge, classify.WaitingOnAuthor, classify.Own, classify.Draft, classify.Unclassified} {
		view.BucketOptions = append(view.BucketOptions, bucketOption{ID: string(b), Title: b.Title()})
	}
	if actions > 0 {
		view.Totals = fmt.Sprintf("%d actions: %s", actions, strings.Join(totals, ", "))
	}
	view.Ready = byBucket[classify.ReadyToMerge]
	view.Unclassified = byBucket[classify.Unclassified]
	view.Counts = countsView{
		WaitingOnAuthor: len(byBucket[classify.WaitingOnAuthor]),
		Own:             len(byBucket[classify.Own]),
		Draft:           len(byBucket[classify.Draft]),
	}
	if actions == 0 {
		view.EmptyMessage, err = s.emptyMessage(ctx)
		if err != nil {
			return queueView{}, err
		}
	}
	return view, nil
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

func explainURL(owner, name string, number int) string {
	return "/r/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/" + strconv.Itoa(number)
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

type statusView struct {
	Accounts []accountStatus
	Paused   []pausedView
	Repos    []repoStatusView
}

// buildStatus feeds the header bar: auth problems, rate pauses, staleness,
// and enrichment backlog (§9). Disabled repositories are omitted.
func (s *Server) buildStatus(ctx context.Context) statusView {
	var v statusView
	now := s.o.Now()
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
	for _, st := range statuses {
		if !st.Repo.Enabled {
			continue
		}
		var msgs []string
		level := "info"
		last := st.Sync.LastSuccessAt
		switch {
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
		}
	}
	return v
}
