package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/thingzio/devbox/internal/classify"
	"github.com/thingzio/devbox/internal/store"
)

func queueFilter(r *http.Request) (store.QueueFilter, string) {
	q := r.URL.Query()
	f := store.QueueFilter{Account: q.Get("account"), Bucket: q.Get("bucket")}
	repo := strings.TrimSpace(q.Get("repo"))
	if owner, name, ok := strings.Cut(repo, "/"); ok {
		f.Owner, f.Name = owner, name
	}
	return f, repo
}

func fragmentURL(path string, f store.QueueFilter, repo string) string {
	v := url.Values{}
	for k, val := range map[string]string{"account": f.Account, "bucket": f.Bucket, "repo": repo} {
		if val != "" {
			v.Set(k, val)
		}
	}
	if len(v) == 0 {
		return path
	}
	return path + "?" + v.Encode()
}

func (s *Server) queueForRequest(r *http.Request) (queueView, error) {
	f, repo := queueFilter(r)
	view, err := s.buildQueue(r.Context(), f, false)
	if err != nil {
		return queueView{}, err
	}
	view.RepoFilter = repo
	view.FragmentURL = fragmentURL("/fragments/queue", f, repo)
	if err := s.fillRefresh(r.Context(), &view); err != nil {
		return queueView{}, err
	}
	accounts, err := s.o.Store.Accounts(r.Context())
	if err != nil {
		return queueView{}, err
	}
	for _, a := range accounts {
		view.Accounts = append(view.Accounts, a.Login)
	}
	return view, nil
}

// fillRefresh reports when repositories were last checked and how polling runs.
func (s *Server) fillRefresh(ctx context.Context, view *queueView) error {
	statuses, err := s.o.Store.RepoStatuses(ctx)
	if err != nil {
		return err
	}
	var last *time.Time
	for _, st := range statuses {
		if st.Repo.Enabled && st.Sync.LastPollAt != nil && (last == nil || st.Sync.LastPollAt.After(*last)) {
			last = st.Sync.LastPollAt
		}
	}
	view.LastChecked = "never"
	if last != nil {
		view.LastChecked = sinceLabel(s.o.Now().Sub(*last))
	}
	view.PollingLabel = "manual: press Refresh"
	if p := s.o.Engine.Polling(); !p.Manual {
		view.PollingLabel = "checking every " + p.String()
	}
	return nil
}

func sinceLabel(d time.Duration) string {
	switch m := int(d / time.Minute); {
	case m < 1:
		return "just now"
	case m < 60:
		return fmt.Sprintf("%d min ago", m)
	default:
		return fmt.Sprintf("%dh ago", m/60)
	}
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	s.o.Engine.Refresh()
	s.o.Log.Info("manual refresh requested")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	view, err := s.queueForRequest(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, r, "queue", http.StatusOK, pageData{Title: "Queue", Body: view})
}

func (s *Server) handleQueueFragment(w http.ResponseWriter, r *http.Request) {
	view, err := s.queueForRequest(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderFragment(w, "queue", "queue-body", view)
}

type repoView struct {
	Name      string
	GitHubURL string
	Accounts  []string
	Queue     queueView
}

func (s *Server) repoForRequest(r *http.Request) (repoView, bool, error) {
	owner, name := r.PathValue("owner"), r.PathValue("name")
	repos, err := s.o.Store.ReposByName(r.Context(), owner, name)
	if err != nil || len(repos) == 0 {
		return repoView{}, false, err
	}
	f := store.QueueFilter{Owner: owner, Name: name}
	q, err := s.buildQueue(r.Context(), f, true)
	if err != nil {
		return repoView{}, false, err
	}
	q.FragmentURL = "/fragments/r/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
	v := repoView{Name: repos[0].FullName(), GitHubURL: "https://github.com/" + repos[0].FullName() + "/pulls", Queue: q}
	for _, repo := range repos {
		v.Accounts = append(v.Accounts, repo.Account)
	}
	return v, true, nil
}

func (s *Server) handleRepo(w http.ResponseWriter, r *http.Request) {
	v, ok, err := s.repoForRequest(r)
	switch {
	case err != nil:
		s.serverError(w, err)
	case !ok:
		http.NotFound(w, r)
	default:
		s.render(w, r, "repo", http.StatusOK, pageData{Title: v.Name, Body: v})
	}
}

func (s *Server) handleRepoFragment(w http.ResponseWriter, r *http.Request) {
	v, ok, err := s.repoForRequest(r)
	switch {
	case err != nil:
		s.serverError(w, err)
	case !ok:
		http.NotFound(w, r)
	default:
		s.renderFragment(w, "repo", "queue-body", v.Queue)
	}
}

type linkView struct {
	Repo   string
	Number int
	URL    string
}

type kv struct{ Label, Value string }

type transitionView struct{ At, From, To, Reason string }

type explainView struct {
	Link        linkView
	Title       string
	Account     string
	State       string
	Revision    int64
	Result      *store.ResultRow
	Evidence    []kv
	Transitions []transitionView
}

func (s *Server) handleExplain(w http.ResponseWriter, r *http.Request) {
	number, err := strconv.Atoi(r.PathValue("number"))
	if err != nil || number <= 0 {
		http.NotFound(w, r)
		return
	}
	details, err := s.o.Store.PRDetails(r.Context(), r.PathValue("owner"), r.PathValue("name"), number)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if len(details) == 0 {
		http.NotFound(w, r)
		return
	}
	var views []explainView
	for _, d := range details {
		trs, err := s.o.Store.Transitions(r.Context(), d.Repo.ID, number)
		if err != nil {
			s.serverError(w, err)
			return
		}
		v := explainView{
			Link:  linkView{Repo: d.Repo.FullName(), Number: number, URL: githubURL(d.URL)},
			Title: titleOr(d.Title, number), Account: d.Repo.Account, State: d.State, Revision: d.Revision, Result: d.Result,
			Evidence: s.evidence(d),
		}
		for _, tr := range trs {
			from := "(new)"
			if tr.From != nil {
				from = *tr.From
			}
			v.Transitions = append(v.Transitions, transitionView{At: s.when(&tr.At), From: from, To: tr.To, Reason: tr.Reason})
		}
		views = append(views, v)
	}
	s.render(w, r, "pr", http.StatusOK, pageData{Title: fmt.Sprintf("%s#%d", details[0].Repo.FullName(), number), Body: views})
}

func yesNo(b *bool) string {
	switch {
	case b == nil:
		return "unknown"
	case *b:
		return "yes"
	}
	return "no"
}

func (s *Server) when(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return t.In(s.o.Location).Format(time.DateTime)
}

// evidence summarizes the stored record for the explain view (§9).
func (s *Server) evidence(d store.PRDetail) []kv {
	out := []kv{
		{"First seen", s.when(&d.FirstSeenAt)},
		{"Last collected", s.when(d.RecordAt)},
		{"Last attempt", s.when(d.LastEnrichAttemptAt)},
		{"Next retry", s.when(d.RetryAt)},
		{"Head", d.HeadOID},
		{"Head changed (observed)", s.when(d.HeadChangedAt)},
	}
	if d.Error != "" {
		out = append(out, kv{"Error", d.Error})
	}
	var rec classify.Record
	if len(d.Record) == 0 || json.Unmarshal(d.Record, &rec) != nil {
		return out
	}
	fork := rec.Fork
	out = append(out,
		kv{"Author", rec.Author}, kv{"Fork", yesNo(&fork)}, kv{"Merge state", rec.MergeState},
		kv{"Review decision", rec.ReviewDecision}, kv{"My reviews", strconv.Itoa(len(rec.MyReviews))},
		kv{"My last hand-off", s.when(rec.MyLastCommentAt)}, kv{"Author last active", s.when(rec.AuthorUpdatedAt)},
		kv{"Diff changed since my review", yesNo(rec.DiffChanged)}, kv{"Awaiting workflow runs", strconv.Itoa(rec.AwaitingRuns)},
		kv{"Test marker present", yesNo(rec.MarkerPresent)}, kv{"Touches CI paths", yesNo(rec.CIPathsTouched)},
		kv{"Signature policy", rec.SignaturePolicy}, kv{"Unverified commits", strconv.Itoa(len(rec.UnverifiedCommits))},
	)
	for _, e := range rec.EvidenceErrors {
		msg := e.Field + ": " + e.Message
		if e.Retryable {
			msg += " (retry pending)"
		}
		out = append(out, kv{"Evidence error", msg})
	}
	if rec.CoreRetry != nil {
		out = append(out, kv{"Stale evidence", fmt.Sprintf("since %s: %s", s.when(&rec.CoreRetry.Since), rec.CoreRetry.Message)})
	}
	return out
}

// handleEvents streams change events (§9) until the client or server goes away.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	events, unsubscribe := s.o.Hub.Subscribe()
	defer unsubscribe()
	s.o.Log.Debug("sse subscriber connected", "subscribers", s.o.Hub.Subscribers())
	defer s.o.Log.Debug("sse subscriber disconnected")
	if _, err := fmt.Fprint(w, "retry: 3000\n\n"); err != nil || rc.Flush() != nil {
		return
	}
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			if _, err := fmt.Fprintf(w, "event: change\ndata: {\"repo\":%d,\"number\":%d}\n\n", ev.RepoID, ev.Number); err != nil || rc.Flush() != nil {
				return
			}
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		}
	}
}
