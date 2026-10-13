package main

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestBrowserURL(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:8484":    "http://127.0.0.1:8484/",
		"[::1]:8484":        "http://[::1]:8484/",
		"0.0.0.0:8484":      "http://127.0.0.1:8484/",
		"[::]:8484":         "http://127.0.0.1:8484/",
		"192.168.1.20:8484": "http://192.168.1.20:8484/",
	}
	for addr, want := range cases {
		if got := browserURL(addr); got != want {
			t.Errorf("browserURL(%q) = %q, want %q", addr, got, want)
		}
	}
}

func TestBrowserCommand(t *testing.T) {
	const u = "http://127.0.0.1:8484/"
	cases := map[string][]string{
		"darwin":  {"open", u},
		"linux":   {"xdg-open", u},
		"freebsd": {"xdg-open", u},
		"windows": {"rundll32", "url.dll,FileProtocolHandler", u},
	}
	for goos, want := range cases {
		name, args, err := browserCommand(goos, u)
		if err != nil || !slices.Equal(append([]string{name}, args...), want) {
			t.Errorf("browserCommand(%q) = %s %v, %v; want %v", goos, name, args, err, want)
		}
	}
	if _, _, err := browserCommand("plan9", u); err == nil {
		t.Error("unsupported OS should fail")
	}
}

func TestOpenBrowser(t *testing.T) {
	var got []string
	launch := func(name string, args ...string) error {
		got = append([]string{name}, args...)
		return nil
	}
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	openBrowser(log, "darwin", "127.0.0.1:8484", launch)
	if !slices.Equal(got, []string{"open", "http://127.0.0.1:8484/"}) {
		t.Fatalf("launched %v", got)
	}
	openBrowser(log, "linux", "127.0.0.1:8484", func(string, ...string) error { return errors.New("xdg-open: not found") })
	if out := logs.String(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, "http://127.0.0.1:8484/") || !strings.Contains(out, "--no-browser") {
		t.Fatalf("failure not reported with the URL to open by hand:\n%s", out)
	}
}

func TestServeHelpListsNoBrowser(t *testing.T) {
	var stderr bytes.Buffer
	if code := run([]string{"serve", "--help"}, &bytes.Buffer{}, &stderr); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stderr.String(), "--no-browser") {
		t.Fatalf("help missing --no-browser:\n%s", stderr.String())
	}
}

func TestLaunchDetached(t *testing.T) {
	if err := launchDetached(os.Args[0], "-test.run=^$"); err != nil {
		t.Fatalf("launch the test binary: %v", err)
	}
	if err := launchDetached("devbox-no-such-opener"); err == nil {
		t.Fatal("missing command should fail")
	}
}
