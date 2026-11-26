package logging

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// ANSI colors by level: errors red, warnings blue, information green, debug gray.
var levelColors = []struct {
	min   slog.Level
	color string
}{
	{slog.LevelError, "\x1b[31m"},
	{slog.LevelWarn, "\x1b[34m"},
	{slog.LevelInfo, "\x1b[32m"},
	{slog.LevelDebug, "\x1b[90m"},
}

const (
	dim   = "\x1b[2m"
	reset = "\x1b[0m"
)

// pretty is a human-oriented handler: a short time, the message colored by
// level, and the attributes dimmed. It drops the level and source columns
// that --log-format text keeps.
type pretty struct {
	mu     *sync.Mutex
	w      io.Writer
	level  slog.Leveler
	color  bool
	now    func() time.Time
	prefix string // group path for later attributes, e.g. "http."
	attrs  []byte // pre-rendered attributes from WithAttrs
}

func newPretty(w io.Writer, level slog.Leveler, color bool) *pretty {
	return &pretty{mu: &sync.Mutex{}, w: w, level: level, color: color, now: time.Now}
}

func (h *pretty) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level.Level() }

func (h *pretty) Handle(_ context.Context, r slog.Record) error {
	var b bytes.Buffer
	h.paint(&b, dim, h.now().Format("15:04:05"))
	b.WriteByte(' ')
	h.paint(&b, colorFor(r.Level), r.Message)
	attrs := bytes.Clone(h.attrs)
	r.Attrs(func(a slog.Attr) bool {
		attrs = appendAttr(attrs, h.prefix, a)
		return true
	})
	if len(attrs) > 0 {
		b.WriteString("  ")
		h.paint(&b, dim, string(bytes.TrimPrefix(attrs, []byte(" "))))
	}
	b.WriteByte('\n')
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.w.Write(b.Bytes())
	return err
}

func (h *pretty) WithAttrs(as []slog.Attr) slog.Handler {
	c := *h
	c.attrs = bytes.Clone(h.attrs)
	for _, a := range as {
		c.attrs = appendAttr(c.attrs, h.prefix, a)
	}
	return &c
}

func (h *pretty) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	c := *h
	c.prefix = h.prefix + name + "."
	return &c
}

func (h *pretty) paint(b *bytes.Buffer, color, s string) {
	if !h.color {
		b.WriteString(s)
		return
	}
	b.WriteString(color)
	b.WriteString(s)
	b.WriteString(reset)
}

func colorFor(l slog.Level) string {
	for _, c := range levelColors {
		if l >= c.min {
			return c.color
		}
	}
	return levelColors[len(levelColors)-1].color
}

// appendAttr renders " key=value", flattening groups into dotted keys.
func appendAttr(b []byte, prefix string, a slog.Attr) []byte {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return b
	}
	if a.Value.Kind() == slog.KindGroup {
		p := prefix
		if a.Key != "" {
			p += a.Key + "."
		}
		for _, g := range a.Value.Group() {
			b = appendAttr(b, p, g)
		}
		return b
	}
	b = append(b, ' ')
	b = append(b, prefix...)
	b = append(b, a.Key...)
	b = append(b, '=')
	return append(b, quote(value(a.Value))...)
}

func value(v slog.Value) string {
	switch v.Kind() {
	case slog.KindTime:
		return v.Time().Format(time.RFC3339)
	case slog.KindDuration:
		return v.Duration().String()
	}
	return v.String()
}

// quote leaves simple values bare and quotes empty ones or those with spaces,
// quotes, or control characters, so each line stays one unambiguous record.
func quote(s string) string {
	if s == "" || strings.ContainsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || r == '"' || r == '=' || !unicode.IsPrint(r) }) {
		return strconv.Quote(s)
	}
	return s
}
