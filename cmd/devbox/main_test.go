package main

import (
	"bytes"
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
