package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/thingzio/devbox/internal/auth"
	"github.com/thingzio/devbox/internal/hub"
	"github.com/thingzio/devbox/internal/rules"
	"github.com/thingzio/devbox/internal/store"
)

func (s *Server) settingsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /settings", s.handleSettings)
	mux.HandleFunc("POST /accounts/device", s.handleDeviceStart)
	mux.HandleFunc("GET /accounts/device/{flow}", s.handleDeviceStatus)
	mux.HandleFunc("POST /accounts/pat", s.handleAddPAT)
	mux.HandleFunc("POST /accounts/{login}/delete", s.handleRemoveAccount)
	mux.HandleFunc("POST /repos", s.handleAddRepo)
	mux.HandleFunc("POST /repos/{id}/{action}", s.handleRepoAction)
	mux.HandleFunc("POST /import", s.handleImport)
}

// reservedOwners are github.com top-level paths that can never be a user or
// organization, so a pasted URL under them is not a repository.
var reservedOwners = map[string]bool{
	"about": true, "apps": true, "collections": true, "contact": true, "customer-stories": true,
	"enterprise": true, "enterprises": true, "events": true, "explore": true, "features": true,
	"issues": true, "login": true, "logout": true, "marketplace": true, "new": true,
	"notifications": true, "organizations": true, "orgs": true, "pricing": true, "pulls": true,
	"readme": true, "search": true, "security": true, "sessions": true, "settings": true,
	"site": true, "sponsors": true, "team": true, "topics": true, "trending": true, "users": true,
}

var (
	ownerPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
	namePattern   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	importPattern = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9-]{0,38})/([A-Za-z0-9._-]{1,100})\.json$`)
)

// parseRepoReference accepts owner/name or any github.com URL inside the repository.
func parseRepoReference(input string) (string, string, error) {
	s := strings.TrimSpace(input)
	for _, prefix := range []string{"https://", "http://"} {
		s = strings.TrimPrefix(s, prefix)
	}
	s = strings.TrimPrefix(s, "www.")
	if rest, ok := strings.CutPrefix(s, "github.com/"); ok {
		s = rest
	} else if first, _, _ := strings.Cut(s, "/"); strings.ContainsAny(first, ".:@") {
		return "", "", fmt.Errorf("%q is not a github.com repository", input)
	}
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(strings.Trim(s, "/"), "/")
	if len(parts) < 2 {
		return "", "", fmt.Errorf("%q is not owner/name", input)
	}
	owner, name := parts[0], strings.TrimSuffix(parts[1], ".git")
	if reservedOwners[strings.ToLower(owner)] {
		return "", "", fmt.Errorf("%q is a GitHub %s page, not a repository", input, owner)
	}
	if !ownerPattern.MatchString(owner) || !namePattern.MatchString(name) || name == "." || name == ".." {
		return "", "", fmt.Errorf("%q is not a valid owner/name", input)
	}
	return owner, name, nil
}

type accountView struct {
	Login, Kind, Scopes, Status, Detail, PausedUntil, Rates string
}

type repoSettingsView struct {
	ID        int64
	Name      string
	Account   string
	Enabled   bool
	RulesJSON string
	LastError string
}

type settingsView struct {
	Error              string
	Accounts           []accountView
	AccountLogins      []string
	Repos              []repoSettingsView
	Imports            []string
	PollInterval       string
	ClientIDConfigured bool
}

func (s *Server) buildSettings(ctx context.Context) (settingsView, error) {
	v := settingsView{PollInterval: s.o.PollInterval.String(), Imports: s.importCandidates()}
	v.ClientIDConfigured = s.o.Auth != nil && s.o.Auth.DeviceConfigured()
	accounts, err := s.o.Store.Accounts(ctx)
	if err != nil {
		return v, err
	}
	paused := map[string]string{}
	rates := map[string]string{}
	if s.o.Engine != nil {
		for account, until := range s.o.Engine.RateStatus() {
			paused[account] = until.In(s.o.Location).Format("15:04")
		}
		for account, readings := range s.o.Engine.RateLimits() {
			parts := make([]string, 0, len(readings))
			for _, r := range readings {
				parts = append(parts, fmt.Sprintf("%s %d/%d", r.Resource, r.Remaining, r.Limit))
			}
			rates[account] = strings.Join(parts, ", ")
		}
	}
	for _, a := range accounts {
		v.Accounts = append(v.Accounts, accountView{Login: a.Login, Kind: a.Kind, Scopes: strings.Join(a.Scopes, ", "),
			Status: a.Status, Detail: a.StatusDetail, PausedUntil: paused[a.Login], Rates: rates[a.Login]})
		v.AccountLogins = append(v.AccountLogins, a.Login)
	}
	statuses, err := s.o.Store.RepoStatuses(ctx)
	if err != nil {
		return v, err
	}
	for _, st := range statuses {
		v.Repos = append(v.Repos, repoSettingsView{ID: st.Repo.ID, Name: st.Repo.FullName(), Account: st.Repo.Account,
			Enabled: st.Repo.Enabled, RulesJSON: st.Repo.Rules.JSON(), LastError: st.Sync.LastError})
	}
	return v, nil
}

func (s *Server) renderSettings(w http.ResponseWriter, r *http.Request, status int, message string) {
	v, err := s.buildSettings(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	v.Error = message
	s.render(w, r, "settings", status, pageData{Title: "Settings", Body: v})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	s.renderSettings(w, r, http.StatusOK, "")
}

// errNoAccount means a form was submitted before any account was chosen.
var errNoAccount = errors.New("no account chosen")

// chosenAccount resolves the form's account, distinguishing "none chosen" from "gone".
func (s *Server) chosenAccount(r *http.Request) (store.Account, error) {
	login := r.PostFormValue("account")
	if login == "" {
		return store.Account{}, errNoAccount
	}
	return s.o.Store.Account(r.Context(), login)
}

// reject re-renders settings with a validation message (never echoing tokens).
func (s *Server) reject(w http.ResponseWriter, r *http.Request, err error) {
	s.o.Log.Info("settings change rejected", "path", r.URL.Path, "err", err)
	msg := err.Error()
	switch {
	case errors.Is(err, errNoAccount):
		msg = "Add a GitHub account first, then choose which account monitors the repository."
	case errors.Is(err, store.ErrExists):
		msg = "That repository is already monitored by that account."
	case errors.Is(err, store.ErrNotFound):
		msg = "That account or repository no longer exists; refresh the page."
	}
	s.renderSettings(w, r, http.StatusUnprocessableEntity, msg)
}

func (s *Server) backToSettings(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (s *Server) handleAddPAT(w http.ResponseWriter, r *http.Request) {
	acct, err := s.o.Auth.AddPAT(r.Context(), r.PostFormValue("token"))
	if err != nil {
		s.reject(w, r, err)
		return
	}
	s.o.Log.Info("account added", "account", acct.Login, "kind", acct.Kind)
	s.o.Engine.ReposChanged()
	s.backToSettings(w, r)
}

func (s *Server) handleRemoveAccount(w http.ResponseWriter, r *http.Request) {
	if err := s.o.Auth.Remove(r.Context(), r.PathValue("login")); err != nil {
		s.reject(w, r, err)
		return
	}
	s.o.Engine.ReposChanged()
	s.backToSettings(w, r)
}

type deviceView struct {
	ID, UserCode, VerificationURI, VerificationURL, Login, Error string
	Pending, Done                                                bool
}

func newDeviceView(f auth.FlowView) deviceView {
	return deviceView{ID: f.ID, UserCode: f.UserCode, VerificationURI: f.VerificationURI, VerificationURL: githubURL(f.VerificationURI),
		Login: f.Login, Error: f.Error, Pending: f.State == auth.FlowPending, Done: f.State == auth.FlowDone}
}

func (s *Server) handleDeviceStart(w http.ResponseWriter, r *http.Request) {
	f, err := s.o.Auth.StartDevice(r.Context())
	if err != nil {
		s.renderFragment(w, "settings", "device", deviceView{Error: err.Error()})
		return
	}
	s.renderFragment(w, "settings", "device", newDeviceView(f))
}

func (s *Server) handleDeviceStatus(w http.ResponseWriter, r *http.Request) {
	f, ok := s.o.Auth.Flow(r.PathValue("flow"))
	if !ok {
		s.renderFragment(w, "settings", "device", deviceView{Error: "This sign-in expired or was replaced; start again."})
		return
	}
	if f.State == auth.FlowDone {
		s.o.Engine.ReposChanged()
	}
	s.renderFragment(w, "settings", "device", newDeviceView(f))
}

func (s *Server) handleAddRepo(w http.ResponseWriter, r *http.Request) {
	acct, err := s.chosenAccount(r)
	if err != nil {
		s.reject(w, r, err)
		return
	}
	owner, name, err := parseRepoReference(r.PostFormValue("repo"))
	if err != nil {
		s.reject(w, r, err)
		return
	}
	if _, err := s.o.Store.AddRepo(r.Context(), acct.ID, owner, name, rules.Default(), s.o.Now()); err != nil {
		s.reject(w, r, err)
		return
	}
	s.o.Log.Info("repository added", "repo", owner+"/"+name, "account", acct.Login)
	s.o.Engine.ReposChanged()
	s.backToSettings(w, r)
}

func (s *Server) handleRepoAction(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	switch action := r.PathValue("action"); action {
	case "enable", "disable":
		if _, err := s.o.Store.SetRepoEnabled(ctx, id, action == "enable"); err != nil {
			s.reject(w, r, err)
			return
		}
		s.o.Log.Info("repository "+action+"d", "repo_id", id)
		s.o.Engine.ReposChanged()
	case "delete":
		if err := s.o.Store.DeleteRepo(ctx, id); err != nil {
			s.reject(w, r, err)
			return
		}
		s.o.Log.Info("repository removed", "repo_id", id)
		s.o.Engine.ReposChanged()
	case "rules":
		rl, err := rules.Parse([]byte(r.PostFormValue("rules")))
		if err != nil {
			s.reject(w, r, err)
			return
		}
		upd, err := s.o.Store.UpdateRepoRules(ctx, id, rl, s.o.Now())
		if err != nil {
			s.reject(w, r, err)
			return
		}
		for _, tr := range upd.Transitions {
			s.o.Hub.Publish(hub.Event{RepoID: id, Number: tr.Number})
		}
		if err := s.o.Engine.RulesChanged(ctx, id, upd.CollectionChanged); err != nil {
			s.serverError(w, err)
			return
		}
		s.o.Log.Info("repository rules updated", "repo", upd.Repo.FullName(), "collection_changed", upd.CollectionChanged)
	default:
		http.NotFound(w, r)
		return
	}
	s.backToSettings(w, r)
}

// importCandidates lists <owner>/<repo>.json overlays under the import directory.
func (s *Server) importCandidates() []string {
	if s.o.ImportDir == "" {
		return nil
	}
	owners, err := os.ReadDir(s.o.ImportDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, owner := range owners {
		if !owner.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(s.o.ImportDir, owner.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			if rel := owner.Name() + "/" + f.Name(); !f.IsDir() && importPattern.MatchString(rel) {
				out = append(out, rel)
			}
		}
	}
	slices.Sort(out)
	return out
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	file := r.PostFormValue("file")
	m := importPattern.FindStringSubmatch(file)
	if m == nil || m[2] == "." || m[2] == ".." || s.o.ImportDir == "" {
		s.reject(w, r, fmt.Errorf("%q is not an importable overlay", file))
		return
	}
	data, err := os.ReadFile(filepath.Join(s.o.ImportDir, m[1], m[2]+".json")) //nolint:gosec // owner and name are validated and joined under the import directory
	if err != nil {
		s.reject(w, r, fmt.Errorf("read %s: %w", file, err))
		return
	}
	rl, err := rules.Parse(data)
	if err != nil {
		s.reject(w, r, fmt.Errorf("%s: %w", file, err))
		return
	}
	acct, err := s.chosenAccount(r)
	if err != nil {
		s.reject(w, r, err)
		return
	}
	if _, err := s.o.Store.AddRepo(r.Context(), acct.ID, m[1], m[2], rl, s.o.Now()); err != nil {
		s.reject(w, r, err)
		return
	}
	s.o.Log.Info("repository imported", "repo", m[1]+"/"+m[2], "account", acct.Login, "file", file)
	s.o.Engine.ReposChanged()
	s.backToSettings(w, r)
}
