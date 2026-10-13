// Command devbox runs the maintainer queue server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/thingzio/devbox/internal/app"
	"github.com/thingzio/devbox/internal/browser"
	"github.com/thingzio/devbox/internal/github"
	"github.com/thingzio/devbox/internal/logging"
	"github.com/thingzio/devbox/internal/syncer"
	"github.com/thingzio/devbox/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "serve":
		return serve(args[1:], stderr)
	case "version":
		fmt.Fprintln(stdout, version.Version)
		return 0
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "devbox: unknown command %q\n", args[0])
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `usage: devbox <command> [flags]

commands:
  serve     run the maintainer queue server
  version   print the version
`)
}

// hiddenFlags are for tests against fake GitHub servers and are omitted from help.
var hiddenFlags = map[string]bool{"github-api-url": true, "github-url": true}

func serve(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", "127.0.0.1:8484", "listen address")
	dbPath := fs.String("db", defaultDBPath(), "SQLite database path")
	poll := fs.String("poll-interval", syncer.DefaultPolling().String(), "default repository check interval, 1m to 15m, or manual; Settings can override it")
	debug := fs.Bool("debug", false, "log at debug level, including every GitHub request")
	logFormat := fs.String("log-format", "auto", "log format: auto, text, or json")
	allowRemote := fs.Bool("allow-remote", false, "allow a non-loopback --addr")
	noBrowser := fs.Bool("no-browser", false, "do not open the UI in the default browser on start")
	apiURL := fs.String("github-api-url", github.DefaultAPIURL, "GitHub API base URL")
	webURL := fs.String("github-url", "https://github.com", "GitHub web base URL")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: devbox serve [flags]")
		fs.VisitAll(func(f *flag.Flag) {
			if !hiddenFlags[f.Name] {
				fmt.Fprintf(stderr, "  --%s\t%s (default %q)\n", f.Name, f.Usage, f.DefValue)
			}
		})
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "devbox serve: unexpected arguments %v\n", fs.Args())
		return 2
	}
	log, err := logging.New(stderr, *logFormat, *debug)
	if err != nil {
		fmt.Fprintln(stderr, "devbox serve:", err)
		return 2
	}
	polling, err := syncer.ParsePolling(*poll)
	if err != nil {
		fmt.Fprintln(stderr, "devbox serve:", err)
		return 2
	}
	cfg := app.Config{
		Addr: *addr, DBPath: *dbPath, Polling: polling, AllowRemote: *allowRemote,
		GitHubAPIURL: *apiURL, GitHubURL: *webURL, ImportDir: defaultImportDir(), Logger: log,
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(stderr, "devbox serve:", err)
		return 2
	}
	if !*noBrowser {
		cfg.Ready = func(addr string) { browser.Open(log, runtime.GOOS, addr, browser.Launch) }
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx, cfg); err != nil {
		log.Error("devbox stopped with an error", "err", err)
		return 1
	}
	return 0
}

func defaultDBPath() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "devbox.db"
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "devbox", "devbox.db")
}

func defaultImportDir() string {
	if dir := os.Getenv("MAINTAINER_SWEEP_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "repos")
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "maintainer-sweep", "repos")
}
