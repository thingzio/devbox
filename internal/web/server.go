// Package web serves the devbox UI.
package web

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/thingzio/devbox/internal/auth"
	"github.com/thingzio/devbox/internal/github"
	"github.com/thingzio/devbox/internal/hub"
	"github.com/thingzio/devbox/internal/store"
	"github.com/thingzio/devbox/internal/syncer"
	"github.com/thingzio/devbox/internal/version"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Engine is the part of the sync engine the UI drives; *syncer.Engine implements it.
type Engine interface {
	ReposChanged()
	RulesChanged(ctx context.Context, repoID int64, collectionChanged bool) error
	Pauses() map[string]syncer.Pause
	RateLimits() map[string][]github.Rate
	Polling() syncer.Polling
	SetPolling(syncer.Polling)
	Refresh()
}

// Options configures the server.
type Options struct {
	Store          *store.DB
	Hub            *hub.Hub
	Auth           *auth.Manager
	Engine         Engine
	Metrics        http.Handler
	Log            *slog.Logger
	Addr           string // actual listen address, for Host and Origin checks
	AllowRemote    bool
	AccessToken    string         // required with AllowRemote: the secret in the startup URL
	DefaultPolling syncer.Polling // from --poll-interval; a saved UI choice overrides it
	ImportDir      string
	// ResolveRepo reads a repository with an account's credentials and returns
	// GitHub's own owner and name for it, following renames. Required.
	ResolveRepo func(ctx context.Context, account, owner, name string) (string, string, error)
	// TestNotification sends a sample desktop notification; nil where the
	// platform has none, which also hides the setting.
	TestNotification func(ctx context.Context) error
	Now              func() time.Time
	Location         *time.Location // display time zone, default time.Local
}

// Server renders pages and handles UI actions.
type Server struct {
	o            Options
	pages        map[string]*template.Template
	allowedHosts map[string]bool
	session      string // expected session cookie; empty when no access token is required
}

var funcs = template.FuncMap{"join": strings.Join, "profile": profileURL}

// New parses templates and prepares the Host allow-list.
func New(o Options) (*Server, error) {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Location == nil {
		o.Location = time.Local
	}
	pages := map[string]*template.Template{}
	for _, name := range []string{"queue", "repo", "pr", "settings"} {
		t, err := template.New(name).Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/partials.html", "templates/"+name+".html")
		if err != nil {
			return nil, fmt.Errorf("web: parse %s templates: %w", name, err)
		}
		pages[name] = t
	}
	if o.AllowRemote && o.AccessToken == "" {
		return nil, errors.New("web: remote access needs an access token")
	}
	if o.ResolveRepo == nil {
		return nil, errors.New("web: a repository resolver is required")
	}
	_, port, err := net.SplitHostPort(o.Addr)
	if err != nil {
		return nil, fmt.Errorf("web: listen address %q: %w", o.Addr, err)
	}
	hosts := map[string]bool{}
	for _, h := range []string{"localhost", "127.0.0.1", "[::1]"} {
		hosts[h+":"+port] = true
	}
	srv := &Server{o: o, pages: pages, allowedHosts: hosts}
	if o.AccessToken != "" {
		srv.session = sessionValue(o.AccessToken)
	}
	return srv, nil
}

// Handler returns the full HTTP handler with security and logging middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.viewRoutes(mux)
	s.settingsRoutes(mux)
	return s.logRequests(s.secure(mux))
}

func (s *Server) viewRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", s.handleQueue)
	mux.HandleFunc("GET /fragments/queue", s.handleQueueFragment)
	mux.HandleFunc("GET /r/{owner}/{name}", s.handleRepo)
	mux.HandleFunc("GET /fragments/r/{owner}/{name}", s.handleRepoFragment)
	mux.HandleFunc("GET /r/{owner}/{name}/{number}", s.handleExplain)
	mux.HandleFunc("GET /fragments/status", s.handleStatusFragment)
	mux.HandleFunc("GET /events", s.handleEvents)
	mux.HandleFunc("POST /refresh", s.handleRefresh)
	mux.HandleFunc("POST /snooze", s.handleSnooze)
	mux.HandleFunc("POST /unsnooze", s.handleUnsnooze)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	if s.o.Metrics != nil {
		mux.Handle("GET /metrics", s.o.Metrics)
	}
	static, _ := fs.Sub(staticFS, "static") // the embedded directory always exists
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
}

// secure applies the local-server rules: security headers, Host allow-listing,
// the access token for remote connections, and same-origin POSTs.
func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		if !s.o.AllowRemote && !s.allowedHosts[r.Host] {
			http.Error(w, "invalid host", http.StatusForbidden)
			return
		}
		if !s.admit(w, r) {
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !sameOrigin(r) {
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin accepts a matching Origin, or a browser-asserted same-origin
// request. Under Referrer-Policy: no-referrer browsers send "Origin: null" on
// form and fetch POSTs, but always send Sec-Fetch-Site, which pages cannot forge.
func sameOrigin(r *http.Request) bool {
	switch origin := r.Header.Get("Origin"); origin {
	case "http://" + r.Host:
		return true
	case "", "null":
		return r.Header.Get("Sec-Fetch-Site") == "same-origin"
	default:
		return false
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.o.Log.Enabled(r.Context(), slog.LevelDebug) {
			next.ServeHTTP(w, r)
			return
		}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)
		s.o.Log.Debug("http request", "method", r.Method, "path", r.URL.Path, "status", rec.status, "duration", time.Since(start))
	})
}

type pageData struct {
	Title   string
	Nav     string // "queue" or "settings"; repo and explain pages belong to the queue
	Path    string // request path and query, where Refresh returns to
	Version string
	Year    int
	Status  statusView
	Body    any
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, page string, status int, data pageData) {
	data.Status = s.buildStatus(r.Context())
	data.Path, data.Version, data.Year = r.URL.RequestURI(), version.Version, s.o.Now().Year()
	data.Nav = "queue"
	if page == "settings" {
		data.Nav = "settings"
	}
	var buf bytes.Buffer
	if err := s.pages[page].ExecuteTemplate(&buf, "layout", data); err != nil {
		s.o.Log.Error("render page", "page", page, "err", err)
		http.Error(w, "render failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func (s *Server) renderFragment(w http.ResponseWriter, page, name string, data any) {
	var buf bytes.Buffer
	if err := s.pages[page].ExecuteTemplate(&buf, name, data); err != nil {
		s.o.Log.Error("render fragment", "fragment", name, "err", err)
		http.Error(w, "render failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

func (s *Server) serverError(w http.ResponseWriter, err error) {
	s.o.Log.Error("request failed", "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// githubURL returns u only when it is a https://github.com/ URL.
func githubURL(u string) string {
	if strings.HasPrefix(u, "https://github.com/") {
		return u
	}
	return ""
}
