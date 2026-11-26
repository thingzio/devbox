// Package notify sends desktop notifications when pull requests become new
// action items. It only reads local state; nothing is written to GitHub.
package notify

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"
	"unicode"

	"github.com/thingzio/devbox/internal/classify"
	"github.com/thingzio/devbox/internal/store"
)

// SettingKey holds "on" when desktop notifications are enabled; they are off by default.
const SettingKey = "notifications"

const (
	defaultWindow = 10 * time.Second
	queueSize     = 256
	maxTitle      = 120
	maxBody       = 240
	listed        = 3
	sendTimeout   = 10 * time.Second
)

// Message is one desktop notification.
type Message struct{ Title, Subtitle, Body string }

// Sender delivers a message to the desktop.
type Sender func(ctx context.Context, m Message) error

// Store is the local state the notifier reads; *store.DB implements it.
type Store interface {
	Setting(ctx context.Context, key string) (string, bool, error)
	PRSummary(ctx context.Context, repoID int64, number int) (repo, title string, err error)
}

// Options configure a Notifier.
type Options struct {
	Store  Store
	Send   Sender
	Log    *slog.Logger
	Window time.Duration // how long to gather a burst into one message, default 10s
}

type item struct {
	repoID int64
	number int
	bucket string
}

// Notifier gathers new action items and sends one message per burst.
type Notifier struct {
	o  Options
	in chan item
}

// New returns a notifier; Run must be started for it to send.
func New(o Options) *Notifier {
	if o.Window <= 0 {
		o.Window = defaultWindow
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &Notifier{o: o, in: make(chan item, queueSize)}
}

// worthNotifying is a PR entering an action bucket other than Bot PRs from
// outside the action buckets; moving between action buckets is not new work.
func worthNotifying(tr store.Transition) bool {
	to := classify.Bucket(tr.To)
	if !to.IsAction() || to == classify.Bot {
		return false
	}
	if tr.From == nil {
		return true
	}
	from := classify.Bucket(*tr.From)
	return !from.IsAction() || from == classify.Bot
}

// Transition offers a bucket change; it never blocks the caller and drops the
// item if the queue is full.
func (n *Notifier) Transition(repoID int64, number int, tr store.Transition) {
	if !worthNotifying(tr) {
		return
	}
	select {
	case n.in <- item{repoID: repoID, number: number, bucket: tr.To}:
	default:
		n.o.Log.Warn("notification dropped: queue full", "repo_id", repoID, "pr", number)
	}
}

// Run gathers items for a window after the first one arrives, then sends one
// message, until ctx ends.
func (n *Notifier) Run(ctx context.Context) {
	var pending []item
	var flush <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case it := <-n.in:
			pending = append(pending, it)
			if flush == nil {
				flush = time.After(n.o.Window)
			}
		case <-flush:
			n.send(ctx, pending)
			pending, flush = nil, nil
		}
	}
}

type entry struct {
	repo, title, bucket string
	number              int
}

func (n *Notifier) send(ctx context.Context, items []item) {
	on, _, err := n.o.Store.Setting(ctx, SettingKey)
	if err != nil {
		n.o.Log.Warn("read notification setting", "err", err)
		return
	}
	if on != "on" {
		return
	}
	seen := map[item]bool{}
	var entries []entry
	for _, it := range items {
		key := item{repoID: it.repoID, number: it.number}
		if seen[key] {
			continue
		}
		seen[key] = true
		repo, title, err := n.o.Store.PRSummary(ctx, it.repoID, it.number)
		if err != nil {
			continue // closed or removed since; nothing to announce
		}
		entries = append(entries, entry{repo: repo, title: title, bucket: it.bucket, number: it.number})
	}
	if len(entries) == 0 {
		return
	}
	sctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	if err := n.o.Send(sctx, message(entries)); err != nil {
		n.o.Log.Warn("desktop notification failed", "err", err)
		return
	}
	n.o.Log.Debug("desktop notification sent", "items", len(entries))
}

func message(entries []entry) Message {
	if len(entries) == 1 {
		e := entries[0]
		return Message{
			Title:    clean("DevBox · "+classify.Bucket(e.bucket).Title(), maxTitle),
			Subtitle: clean(fmt.Sprintf("%s#%d", e.repo, e.number), maxTitle),
			Body:     clean(e.title, maxBody),
		}
	}
	parts := make([]string, 0, listed+1)
	for i, e := range entries {
		if i == listed {
			parts = append(parts, fmt.Sprintf("and %d more", len(entries)-listed))
			break
		}
		parts = append(parts, fmt.Sprintf("%s#%d %s", e.repo, e.number, e.title))
	}
	return Message{Title: fmt.Sprintf("DevBox · %d new action items", len(entries)), Body: clean(strings.Join(parts, "; "), maxBody)}
}

// clean makes attacker-authored text safe to display: control characters
// become spaces and the length is capped.
func clean(s string, limit int) string {
	r := []rune(strings.Map(func(c rune) rune {
		if unicode.IsControl(c) {
			return ' '
		}
		return c
	}, s))
	if len(r) > limit {
		r = append(r[:limit-1], '…')
	}
	return string(r)
}

// script reads the message from argv, so PR titles never become AppleScript
// source; "--" ends osascript's options so a title cannot pose as one.
const script = `on run argv
display notification (item 1 of argv) with title (item 2 of argv) subtitle (item 3 of argv)
end run`

// OSAScript sends messages through macOS Notification Center with run.
func OSAScript(run func(ctx context.Context, name string, args ...string) error) Sender {
	return func(ctx context.Context, m Message) error {
		if err := run(ctx, "osascript", "-e", script, "--", m.Body, m.Title, m.Subtitle); err != nil {
			return fmt.Errorf("osascript: %w", err)
		}
		return nil
	}
}

// Native returns the desktop sender for goos, or nil where none exists.
func Native(goos string) Sender {
	if goos != "darwin" {
		return nil
	}
	return OSAScript(func(ctx context.Context, name string, args ...string) error {
		return exec.CommandContext(ctx, name, args...).Run() //nolint:gosec // fixed program and script; message text is passed as data after "--"
	})
}
