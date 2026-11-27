// Package app wires devbox's components and runs the server with an orderly
// shutdown.
package app

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/thingzio/devbox/internal/auth"
	"github.com/thingzio/devbox/internal/collect"
	"github.com/thingzio/devbox/internal/github"
	"github.com/thingzio/devbox/internal/hub"
	"github.com/thingzio/devbox/internal/metrics"
	"github.com/thingzio/devbox/internal/notify"
	"github.com/thingzio/devbox/internal/store"
	"github.com/thingzio/devbox/internal/syncer"
	"github.com/thingzio/devbox/internal/version"
	"github.com/thingzio/devbox/internal/web"
)

// Config is everything `devbox serve` needs.
type Config struct {
	Addr         string
	DBPath       string
	Polling      syncer.Polling // default from --poll-interval; a choice saved in Settings overrides it
	AllowRemote  bool
	GitHubAPIURL string
	GitHubURL    string
	ImportDir    string
	Logger       *slog.Logger
	Secrets      auth.Secrets      // nil uses the OS keychain
	Desktop      notify.Sender     // nil uses the platform's notifications (macOS only)
	Ready        func(addr string) // called with the bound address once serving
	Notice       io.Writer         // receives the access URL under AllowRemote; never the log
	EngineConfig syncer.Config     // zero values take engine defaults; polling is overridden
}

// Validate rejects configurations that are unsafe or unusable.
func (c Config) Validate() error {
	if err := c.Polling.Validate(); err != nil {
		return err
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

// effectivePolling returns the saved Settings choice when valid, else the default.
func effectivePolling(ctx context.Context, db *store.DB, def syncer.Polling, log *slog.Logger) (syncer.Polling, string) {
	saved, ok, err := db.Setting(ctx, store.SettingPolling)
	if err != nil || !ok {
		return def, "default"
	}
	p, err := syncer.ParsePolling(saved)
	if err != nil {
		log.Warn("ignoring invalid saved polling setting", "value", saved, "err", err)
		return def, "default"
	}
	return p, "settings"
}

// accountToken reads an account's token, renewing it when it expires. Only a
// missing token or a rejected renewal means the account needs
// re-authentication; other failures are worth retrying.
func accountToken(ctx context.Context, tokens *auth.Tokens, account string) (string, error) {
	token, err := tokens.Token(ctx, account)
	if errors.Is(err, auth.ErrNoSecret) || errors.Is(err, auth.ErrRefreshRejected) {
		return "", fmt.Errorf("%w: %w", syncer.ErrNoToken, err)
	}
	return token, err
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
		log.Warn("accepting non-loopback connections; pages need the access token printed at startup", "addr", cfg.Addr)
	}
	db, err := store.Open(ctx, cfg.DBPath, log)
	if err != nil {
		return err
	}
	defer db.Close()
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Addr, err)
	}

	h := hub.New()
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	m := metrics.New(reg)
	metrics.RegisterPRGauge(reg, db)
	var secrets auth.Secrets = auth.Keyring{}
	if cfg.Secrets != nil {
		secrets = cfg.Secrets
	}
	secrets = auth.NewCachedSecrets(secrets)
	device := &auth.DeviceClient{BaseURL: cfg.GitHubURL, ClientID: auth.ClientID()}
	var refresher auth.Refresher
	if device.ClientID != "" {
		refresher = device
	}
	tokens := auth.NewTokens(secrets, refresher, nil)

	var engine *syncer.Engine
	newClient := func(token, account string) (*github.Client, error) {
		return github.New(github.Options{
			APIURL: cfg.GitHubAPIURL, Token: token, UserAgent: "devbox/" + version.Version, Logger: log,
			OnRequest: m.ObserveRequest, OnGraphQLCost: m.ObserveGraphQLCost,
			OnRate: func(r github.Rate) {
				if account != "" {
					engine.ObserveRate(account, r)
				}
			},
		})
	}
	sources := func(ctx context.Context, account string) (syncer.Source, error) {
		token, err := accountToken(ctx, tokens, account)
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
	polling, source := effectivePolling(ctx, db, cfg.Polling, log)
	ecfg.PollInterval, ecfg.Manual = polling.Interval, polling.Manual
	if polling.Manual {
		ecfg.PollInterval = syncer.DefaultPollInterval
	}
	desktop := cfg.Desktop
	if desktop == nil {
		desktop = notify.Native(runtime.GOOS)
	}
	deps := syncer.Deps{Store: db, Hub: h, Metrics: m, Log: log, Sources: sources}
	var notifier *notify.Notifier
	var testNotification func(context.Context) error
	if desktop != nil {
		notifier = notify.New(notify.Options{Store: db, Send: desktop, Log: log})
		deps.OnTransition = notifier.Transition
		testNotification = func(ctx context.Context) error {
			return desktop(ctx, notify.Message{Title: "DevBox", Subtitle: "Test notification", Body: "Desktop notifications are working."})
		}
	}
	engine = syncer.New(ecfg, deps)
	metrics.RegisterStateGauges(reg, func() int { return len(engine.Pauses()) }, h.Subscribers)

	mgrCtx, cancelMgr := context.WithCancel(context.Background())
	defer cancelMgr()
	mgr := auth.NewManager(mgrCtx, auth.ManagerOptions{
		Store: db, Secrets: secrets, Log: log,
		Device:    device,
		NewClient: func(token string) (*github.Client, error) { return newClient(token, "") },
	})
	var accessToken string
	if cfg.AllowRemote {
		accessToken = rand.Text()
	}
	srv, err := web.New(web.Options{
		Store: db, Hub: h, Auth: mgr, Engine: engine, Metrics: promhttp.HandlerFor(reg, promhttp.HandlerOpts{}),
		Log: log, Addr: ln.Addr().String(), AllowRemote: cfg.AllowRemote, AccessToken: accessToken, DefaultPolling: cfg.Polling, ImportDir: cfg.ImportDir,
		TestNotification: testNotification,
		ResolveRepo: func(ctx context.Context, account, owner, name string) (string, string, error) {
			token, err := accountToken(ctx, tokens, account)
			if err != nil {
				return "", "", err
			}
			c, err := newClient(token, account)
			if err != nil {
				return "", "", err
			}
			return collect.GitHubReader{Client: c}.Repository(ctx, owner, name)
		},
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
	notifierDone := make(chan struct{})
	go func() {
		defer close(notifierDone)
		if notifier != nil {
			notifier.Run(engineCtx)
		}
	}()
	serveDone := make(chan error, 1)
	go func() { serveDone <- httpSrv.Serve(ln) }()

	accounts, _ := db.Accounts(ctx)
	repos, _ := db.Repos(ctx)
	log.Info("devbox started", "version", version.Version, "url", "http://"+ln.Addr().String(), "db", cfg.DBPath,
		"polling", polling.String(), "polling_source", source, "accounts", len(accounts), "repos", len(repos), "device_flow", mgr.DeviceConfigured())
	if accessToken != "" && cfg.Notice != nil {
		// The URL carries a credential, so it goes to the terminal, not the structured log.
		fmt.Fprintf(cfg.Notice, "devbox: remote access is on; open http://%s/?token=%s\n", ln.Addr(), accessToken)
	}
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
	<-notifierDone
	if err := <-engineDone; err != nil {
		log.Warn("engine stopped with error", "err", err)
	}
	mgr.Close()
	h.Close()
	log.Info("stopped")
	return runErr
}
