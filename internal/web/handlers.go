package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/thingzio/devbox/internal/classify"
	"github.com/thingzio/devbox/internal/hub"
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

// queueLink builds a queue page or fragment URL carrying the given filters.
func queueLink(path, account, repo, bucket string) string {
	v := url.Values{}
	for k, val := range map[string]string{"account": account, "bucket": bucket, "repo": repo} {
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
	view, err := s.buildQueue(r.Context(), queueQuery{filter: f, repo: repo, base: "/"})
	if err != nil {
		return queueView{}, err
	}
	view.RepoFilter = repo
	view.FragmentURL = queueLink("/fragments/queue", f.Account, repo, f.Bucket)
	for _, l := range view.Repos {
		view.RepoOptions = append(view.RepoOptions, l.Title)
	}
	if repo != "" && !slices.Contains(view.RepoOptions, repo) {
		view.RepoOptions = append(view.RepoOptions, repo)
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
	http.Redirect(w, r, localPath(r.PostFormValue("next")), http.StatusSeeOther) //nolint:gosec // localPath allows only paths on this server

}

// localPath returns next when it is a path on this server, otherwise "/", so a
// redirect can never leave the app.
func localPath(next string) string {
	// "//host" and "/\host" are protocol-relative to browsers.
	if next == "" || next[0] != '/' || (len(next) > 1 && (next[1] == '/' || next[1] == '\\')) {
		return "/"
	}
	if u, err := url.Parse(next); err != nil || u.Scheme != "" || u.Host != "" || strings.Contains(next, `\`) {
		return "/"
	}
	return next
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
	f := store.QueueFilter{Owner: owner, Name: name, Bucket: r.URL.Query().Get("bucket")}
	base := repoURL(owner, name)
	q, err := s.buildQueue(r.Context(), queueQuery{filter: f, base: base, perRepo: true})
	if err != nil {
		return repoView{}, false, err
	}
	q.FragmentURL = queueLink("/fragments"+base, "", "", f.Bucket)
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
	RepoURL     string
	Tone        string
	BucketTitle string
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
			Link:    linkView{Repo: d.Repo.FullName(), Number: number, URL: githubURL(d.URL)},
			RepoURL: repoURL(d.Repo.Owner, d.Repo.Name), Tone: "muted",
			Title: titleOr(d.Title, number), Account: d.Repo.Account, State: d.State, Revision: d.Revision, Result: d.Result,
			Evidence: s.evidence(d),
		}
		if d.Result != nil {
			b := classify.Bucket(d.Result.Bucket)
			v.Tone, v.BucketTitle = tone(b), b.Title()
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

// evidence summarizes the stored record for the explain view.
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

// sseMessage names the event for the browser: "change" refreshes the queue and
// the status, "poll" refreshes only the status.
func sseMessage(ev hub.Event) string {
	if ev.Kind == hub.Polled {
		return fmt.Sprintf("event: poll\ndata: {\"repo\":%d}\n\n", ev.RepoID)
	}
	return fmt.Sprintf("event: change\ndata: {\"repo\":%d,\"number\":%d}\n\n", ev.RepoID, ev.Number)
}

// handleStatusFragment renders the header heartbeat plus the status banners,
// which the page swaps out of band, so both stay current without a reload.
func (s *Server) handleStatusFragment(w http.ResponseWriter, r *http.Request) {
	v := s.buildStatus(r.Context())
	v.OOB = true
	s.renderFragment(w, "queue", "live-status", v)
}

// handleEvents streams change events until the client or server goes away.
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
			if _, err := io.WriteString(w, sseMessage(ev)); err != nil || rc.Flush() != nil {
				return
			}
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		}
	}
}
