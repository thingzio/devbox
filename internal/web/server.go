// Package web serves the devbox UI (§9).
package web

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/thingzio/devbox/internal/auth"
	"github.com/thingzio/devbox/internal/hub"
	"github.com/thingzio/devbox/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Engine is the part of the sync engine the UI drives; *syncer.Engine implements it.
type Engine interface {
	ReposChanged()
	RulesChanged(ctx context.Context, repoID int64, collectionChanged bool) error
	RateStatus() map[string]time.Time
}

// Options configures the server.
type Options struct {
	Store        *store.DB
	Hub          *hub.Hub
	Auth         *auth.Manager
	Engine       Engine
	Metrics      http.Handler
	Log          *slog.Logger
	Addr         string // actual listen address, for Host and Origin checks
	AllowRemote  bool
	PollInterval time.Duration
	ImportDir    string
	Now          func() time.Time
	Location     *time.Location // display time zone, default time.Local
}

// Server renders pages and handles UI actions.
type Server struct {
	o            Options
	pages        map[string]*template.Template
	allowedHosts map[string]bool
}

var funcs = template.FuncMap{"join": strings.Join}

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
	_, port, err := net.SplitHostPort(o.Addr)
	if err != nil {
		return nil, fmt.Errorf("web: listen address %q: %w", o.Addr, err)
	}
	hosts := map[string]bool{}
	for _, h := range []string{"localhost", "127.0.0.1", "[::1]"} {
		hosts[h+":"+port] = true
	}
	return &Server{o: o, pages: pages, allowedHosts: hosts}, nil
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
	mux.HandleFunc("GET /events", s.handleEvents)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	if s.o.Metrics != nil {
		mux.Handle("GET /metrics", s.o.Metrics)
	}
	static, _ := fs.Sub(staticFS, "static") // the embedded directory always exists
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
}

// secure applies §9.1: security headers, Host allow-listing, and same-origin POSTs.
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
	Title  string
	Status statusView
	Body   any
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, page string, status int, data pageData) {
	data.Status = s.buildStatus(r.Context())
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

// githubURL returns u only when it is a https://github.com/ URL (§9.1).
func githubURL(u string) string {
	if strings.HasPrefix(u, "https://github.com/") {
		return u
	}
	return ""
}
