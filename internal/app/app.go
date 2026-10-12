// Package app wires devbox's components and runs the server with an orderly
// shutdown (§3.3).
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/thingzio/devbox/internal/auth"
	"github.com/thingzio/devbox/internal/collect"
	"github.com/thingzio/devbox/internal/github"
	"github.com/thingzio/devbox/internal/hub"
	"github.com/thingzio/devbox/internal/metrics"
	"github.com/thingzio/devbox/internal/store"
	"github.com/thingzio/devbox/internal/syncer"
	"github.com/thingzio/devbox/internal/version"
	"github.com/thingzio/devbox/internal/web"
)

// MinPollInterval is the shortest allowed fingerprint poll interval (§7).
const MinPollInterval = time.Minute

// Config is everything `devbox serve` needs.
type Config struct {
	Addr         string
	DBPath       string
	PollInterval time.Duration
	AllowRemote  bool
	GitHubAPIURL string
	GitHubURL    string
	ImportDir    string
	Logger       *slog.Logger
	Secrets      auth.Secrets      // nil uses the OS keychain
	Ready        func(addr string) // called with the bound address once serving
	EngineConfig syncer.Config     // zero values take engine defaults; PollInterval is overridden
}

// Validate rejects configurations that are unsafe or unusable.
func (c Config) Validate() error {
	if c.PollInterval < MinPollInterval {
		return fmt.Errorf("poll interval %s is below the %s minimum", c.PollInterval, MinPollInterval)
	}
	host, _, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return fmt.Errorf("invalid address %q: %w", c.Addr, err)
	}
	if !c.AllowRemote && !isLoopback(host) {
		return fmt.Errorf("address %s is not loopback; pass --allow-remote to accept remote connections", c.Addr)
	}
	for _, raw := range []string{c.GitHubAPIURL, c.GitHubURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return fmt.Errorf("invalid GitHub URL %q", raw)
		}
	}
	if c.DBPath == "" {
		return errors.New("database path is required")
	}
	return nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Run serves until ctx is cancelled, then stops HTTP (10s grace), pollers,
// in-flight enrichment, the hub, and the database, in that order.
func Run(ctx context.Context, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if cfg.AllowRemote {
		log.Warn("accepting non-loopback connections; the UI has no login", "addr", cfg.Addr)
	}
	db, err := store.Open(ctx, cfg.DBPath, log)
	if err != nil {
		return err
	}
	defer db.Close()
	if n, err := db.Purge(ctx, time.Now()); err != nil {
		return err
	} else if n > 0 {
		log.Info("purged expired cache entries", "rows", n)
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Addr, err)
	}

	h := hub.New()
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	m := metrics.New(reg)
	metrics.RegisterPRGauge(reg, db)
	secrets := cfg.Secrets
	if secrets == nil {
		secrets = auth.Keyring{}
	}

	var engine *syncer.Engine
	newClient := func(token, account string) (*github.Client, error) {
		return github.New(github.Options{
			APIURL: cfg.GitHubAPIURL, Token: token, UserAgent: "devbox/" + version.Version, Logger: log,
			OnRequest: m.ObserveRequest,
			OnRate: func(r github.Rate) {
				if account != "" {
					engine.ObserveRate(account, r)
				}
			},
		})
	}
	sources := func(account string) (syncer.Source, error) {
		token, err := secrets.Get(account)
		if err != nil {
			return nil, err
		}
		c, err := newClient(token, account)
		if err != nil {
			return nil, err
		}
		return collect.NewSource(collect.GitHubReader{Client: c}, db, log), nil
	}
	ecfg := cfg.EngineConfig
	ecfg.PollInterval = cfg.PollInterval
	engine = syncer.New(ecfg, syncer.Deps{Store: db, Hub: h, Metrics: m, Log: log, Sources: sources})

	mgrCtx, cancelMgr := context.WithCancel(context.Background())
	defer cancelMgr()
	mgr := auth.NewManager(mgrCtx, auth.ManagerOptions{
		Store: db, Secrets: secrets, Log: log,
		Device:    &auth.DeviceClient{BaseURL: cfg.GitHubURL, ClientID: auth.ClientID()},
		NewClient: func(token string) (*github.Client, error) { return newClient(token, "") },
	})
	srv, err := web.New(web.Options{
		Store: db, Hub: h, Auth: mgr, Engine: engine, Metrics: promhttp.HandlerFor(reg, promhttp.HandlerOpts{}),
		Log: log, Addr: ln.Addr().String(), AllowRemote: cfg.AllowRemote, PollInterval: cfg.PollInterval, ImportDir: cfg.ImportDir,
	})
	if err != nil {
		_ = ln.Close()
		return err
	}
	baseCtx, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()
	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	httpSrv.RegisterOnShutdown(cancelBase) // ends SSE streams so Shutdown can finish

	engineCtx, cancelEngine := context.WithCancel(context.Background())
	defer cancelEngine()
	engineDone := make(chan error, 1)
	go func() { engineDone <- engine.Run(engineCtx) }()
	serveDone := make(chan error, 1)
	go func() { serveDone <- httpSrv.Serve(ln) }()

	accounts, _ := db.Accounts(ctx)
	repos, _ := db.Repos(ctx)
	log.Info("devbox started", "version", version.Version, "url", "http://"+ln.Addr().String(), "db", cfg.DBPath,
		"poll_interval", cfg.PollInterval, "accounts", len(accounts), "repos", len(repos), "device_flow", mgr.DeviceConfigured())
	if cfg.Ready != nil {
		cfg.Ready(ln.Addr().String())
	}

	var runErr error
	select {
	case <-ctx.Done():
	case err := <-serveDone:
		if !errors.Is(err, http.ErrServerClosed) {
			runErr = fmt.Errorf("http server: %w", err)
		}
	}
	log.Info("shutting down")
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Warn("http shutdown incomplete", "err", err)
	}
	cancelShutdown()
	cancelEngine()
	if err := <-engineDone; err != nil {
		log.Warn("engine stopped with error", "err", err)
	}
	mgr.Close()
	h.Close()
	log.Info("stopped")
	return runErr
}
