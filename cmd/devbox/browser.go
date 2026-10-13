package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os/exec"
)

// browserURL is the UI address for a local browser; an unspecified listen
// address (0.0.0.0 or ::) is reached through loopback.
func browserURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr + "/"
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	return (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, port), Path: "/"}).String()
}

// browserCommand is the command that opens u in the default browser on goos.
func browserCommand(goos, u string) (string, []string, error) {
	switch goos {
	case "darwin":
		return "open", []string{u}, nil
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", u}, nil
	case "linux", "freebsd", "openbsd", "netbsd", "dragonfly", "solaris", "illumos":
		return "xdg-open", []string{u}, nil
	}
	return "", nil, fmt.Errorf("no default-browser command for %s", goos)
}

// openBrowser opens the UI at addr; failure only logs, since the server is
// already up and the URL can be opened by hand.
func openBrowser(log *slog.Logger, goos, addr string, launch func(name string, args ...string) error) {
	u := browserURL(addr)
	name, args, err := browserCommand(goos, u)
	if err == nil {
		err = launch(name, args...)
	}
	if err != nil {
		log.Warn("could not open the browser; open the URL yourself or pass --no-browser", "url", u, "err", err)
		return
	}
	log.Debug("opened the browser", "url", u)
}

// launchDetached starts a command without waiting for it to finish. It is not
// tied to the server's context: the opener may outlive a quick shutdown.
func launchDetached(name string, args ...string) error {
	cmd := exec.CommandContext(context.Background(), name, args...) //nolint:gosec // fixed per-OS opener; the only argument is the server's own URL
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
