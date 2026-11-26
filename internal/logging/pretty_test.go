package logging

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	esc       = "\x1b["
	green     = esc + "32m"
	blue      = esc + "34m"
	red       = esc + "31m"
	gray      = esc + "90m"
	ansiReset = esc + "0m"
)

func prettyLogger(buf *bytes.Buffer, color bool, level slog.Level) *slog.Logger {
	h := newPretty(buf, level, color)
	h.now = func() time.Time { return time.Date(2026, 11, 26, 13, 54, 9, 0, time.Local) }
	return slog.New(h)
}

func TestPrettyColorsByLevel(t *testing.T) {
	cases := []struct {
		level slog.Level
		color string
	}{
		{slog.LevelDebug, gray},
		{slog.LevelInfo, green},
		{slog.LevelWarn, blue},
		{slog.LevelError, red},
	}
	for _, tc := range cases {
		t.Run(tc.level.String(), func(t *testing.T) {
			var buf bytes.Buffer
			prettyLogger(&buf, true, slog.LevelDebug).Log(context.Background(), tc.level, "something happened")
			if want := tc.color + "something happened" + ansiReset; !strings.Contains(buf.String(), want) {
				t.Fatalf("line %q lacks %q", buf.String(), want)
			}
		})
	}
}

func TestPrettyLayout(t *testing.T) {
	var buf bytes.Buffer
	log := prettyLogger(&buf, false, slog.LevelInfo).With("repo", "mchmarny/cluster")
	log.Info("bucket transition", "pr", 66, "reason", "review required", "from", "", "took", 1500*time.Millisecond)
	want := `13:54:09 bucket transition  repo=mchmarny/cluster pr=66 reason="review required" from="" took=1.5s` + "\n"
	if buf.String() != want {
		t.Fatalf("line =\n%q\nwant\n%q", buf.String(), want)
	}
	buf.Reset()
	log.Info("plain")
	if buf.String() != "13:54:09 plain  repo=mchmarny/cluster\n" {
		t.Fatalf("line = %q", buf.String())
	}
	buf.Reset()
	prettyLogger(&buf, false, slog.LevelInfo).Info("bare")
	if buf.String() != "13:54:09 bare\n" {
		t.Fatalf("line = %q", buf.String())
	}
}

func TestPrettyGroupsAndLevels(t *testing.T) {
	var buf bytes.Buffer
	log := prettyLogger(&buf, false, slog.LevelInfo)
	log.WithGroup("http").Info("request", "status", 200, slog.Group("rate", "left", 9))
	if !strings.Contains(buf.String(), "http.status=200 http.rate.left=9") {
		t.Fatalf("groups = %q", buf.String())
	}
	buf.Reset()
	log.Debug("hidden")
	if buf.Len() != 0 {
		t.Fatalf("debug shown at info level: %q", buf.String())
	}
}

func TestPrettyWithoutColorHasNoEscapes(t *testing.T) {
	var buf bytes.Buffer
	prettyLogger(&buf, false, slog.LevelInfo).Error("boom", "err", "dial tcp")
	if strings.Contains(buf.String(), esc) {
		t.Fatalf("escape codes without color: %q", buf.String())
	}
}

func TestPrettyIsSafeForConcurrentUse(t *testing.T) {
	var buf bytes.Buffer
	log := prettyLogger(&buf, true, slog.LevelInfo)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); log.Info("line", "i", i) }()
	}
	wg.Wait()
	if got := strings.Count(buf.String(), "\n"); got != 20 {
		t.Fatalf("lines = %d", got)
	}
}

func TestFormats(t *testing.T) {
	var buf bytes.Buffer
	log, err := New(&buf, "pretty", false)
	if err != nil {
		t.Fatal(err)
	}
	log.Info("hello", "k", "v")
	if strings.Contains(buf.String(), "level=") || !strings.Contains(buf.String(), "hello  k=v") {
		t.Fatalf("pretty = %q", buf.String())
	}
	if strings.Contains(buf.String(), esc) {
		t.Fatal("colored output into a non-terminal")
	}
}
