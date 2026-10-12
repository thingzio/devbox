package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thingzio/devbox/internal/version"
)

func TestRunVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"version"}, &out, &errOut); code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, errOut.String())
	}
	if got := strings.TrimSpace(out.String()); got != version.Version {
		t.Fatalf("version output = %q, want %q", got, version.Version)
	}
}

func TestRunUsageErrors(t *testing.T) {
	cases := map[string][]string{
		"no command":      nil,
		"unknown command": {"frobnicate"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := run(args, &out, &errOut); code != 2 {
				t.Fatalf("exit code = %d, want 2", code)
			}
			if !strings.Contains(errOut.String(), "usage: devbox") {
				t.Fatalf("stderr %q does not contain usage", errOut.String())
			}
		})
	}
}

func TestRunHelp(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"help"}, &out, &errOut); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "serve") {
		t.Fatalf("help %q does not list serve", out.String())
	}
}

func TestServeUsageErrors(t *testing.T) {
	cases := map[string][]string{
		"unknown flag":            {"serve", "--nope"},
		"poll interval too short": {"serve", "--poll-interval", "10s"},
		"bad log format":          {"serve", "--log-format", "xml"},
		"non-loopback address":    {"serve", "--addr", "0.0.0.0:8484"},
		"unexpected argument":     {"serve", "extra"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := run(args, &out, &errOut); code != 2 {
				t.Fatalf("exit code = %d, want 2 (stderr %q)", code, errOut.String())
			}
		})
	}
}

func TestServeHelpHidesTestFlags(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"serve", "--help"}, &out, &errOut); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	help := errOut.String()
	if !strings.Contains(help, "--poll-interval") || strings.Contains(help, "github-api-url") || strings.Contains(help, "github-url") {
		t.Fatalf("help = %q", help)
	}
}

func TestDefaultPaths(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/data")
	t.Setenv("XDG_CONFIG_HOME", "/config")
	t.Setenv("MAINTAINER_SWEEP_CONFIG_DIR", "")
	if got := defaultDBPath(); got != filepath.Join("/data", "devbox", "devbox.db") {
		t.Fatalf("defaultDBPath = %s", got)
	}
	if got := defaultImportDir(); got != filepath.Join("/config", "maintainer-sweep", "repos") {
		t.Fatalf("defaultImportDir = %s", got)
	}
	t.Setenv("MAINTAINER_SWEEP_CONFIG_DIR", "/sweep")
	if got := defaultImportDir(); got != filepath.Join("/sweep", "repos") {
		t.Fatalf("override import dir = %s", got)
	}
}
