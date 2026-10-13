// Package logging configures devbox's structured logger.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
)

// New returns a logger writing to w. format is auto, text, or json; auto picks
// text on a terminal and JSON otherwise. debug enables debug level and source locations.
func New(w io.Writer, format string, debug bool) (*slog.Logger, error) {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	if debug {
		opts.Level, opts.AddSource = slog.LevelDebug, true
	}
	if format == "auto" {
		format = "json"
		if isTerminal(w) {
			format = "text"
		}
	}
	switch format {
	case "text":
		return slog.New(slog.NewTextHandler(w, opts)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(w, opts)), nil
	}
	return nil, fmt.Errorf("unknown log format %q (want auto, text, or json)", format)
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
