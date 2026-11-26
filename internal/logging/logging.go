// Package logging configures devbox's structured logger.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
)

// New returns a logger writing to w. format is auto, pretty, text, or json:
// pretty is short colored lines for people, text the full key=value record,
// and auto picks pretty on a terminal and JSON otherwise. Color is used only
// on a terminal and never when NO_COLOR is set. debug enables debug level
// (and source locations in text and JSON).
func New(w io.Writer, format string, debug bool) (*slog.Logger, error) {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	if debug {
		opts.Level, opts.AddSource = slog.LevelDebug, true
	}
	if format == "auto" {
		format = "json"
		if isTerminal(w) {
			format = "pretty"
		}
	}
	switch format {
	case "pretty":
		return slog.New(newPretty(w, opts.Level, isTerminal(w) && os.Getenv("NO_COLOR") == "")), nil
	case "text":
		return slog.New(slog.NewTextHandler(w, opts)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(w, opts)), nil
	}
	return nil, fmt.Errorf("unknown log format %q (want auto, pretty, text, or json)", format)
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
