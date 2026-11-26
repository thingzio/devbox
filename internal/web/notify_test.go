package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/thingzio/devbox/internal/notify"
)

func TestNotificationSettingsWithoutDesktopSupport(t *testing.T) {
	ts := newTestServer(t)
	_, body := ts.get("/settings")
	assertContains(t, "settings", body, `href="#notifications"`, "Desktop notifications are available on macOS")
	if strings.Contains(body, `action="/settings/notifications"`) {
		t.Error("toggle offered where notifications cannot be sent")
	}
	if code, _, _ := ts.post("/settings/notifications", url.Values{"enabled": {"on"}}); code != http.StatusNotFound {
		t.Fatalf("enable without support = %d", code)
	}
}

func TestNotificationSettings(t *testing.T) {
	var tests int
	var fail error
	ts := newTestServerWith(t, func(o *Options) {
		o.TestNotification = func(context.Context) error { tests++; return fail }
	})
	_, body := ts.get("/settings")
	assertContains(t, "off by default", body, `action="/settings/notifications"`, `<option value="off" selected>`)

	if code, _, _ := ts.post("/settings/notifications", url.Values{"enabled": {"on"}}); code != http.StatusSeeOther {
		t.Fatalf("enable = %d", code)
	}
	if v, _, _ := ts.db.Setting(ts.ctx, notify.SettingKey); v != "on" {
		t.Fatalf("setting = %q", v)
	}
	_, body = ts.get("/settings")
	assertContains(t, "enabled", body, `<option value="on" selected>`)

	if code, _, _ := ts.post("/settings/notifications", url.Values{"enabled": {"off"}}); code != http.StatusSeeOther {
		t.Fatalf("disable = %d", code)
	}
	if _, ok, _ := ts.db.Setting(ts.ctx, notify.SettingKey); ok {
		t.Fatal("off left a saved setting")
	}
	if code, _, _ := ts.post("/settings/notifications", url.Values{"enabled": {"loud"}}); code != http.StatusUnprocessableEntity {
		t.Fatalf("bad value = %d", code)
	}

	if code, _, _ := ts.post("/settings/notifications/test", nil); code != http.StatusSeeOther || tests != 1 {
		t.Fatalf("test = %d after %d sends", code, tests)
	}
	fail = errors.New("exit status 1")
	if code, body, _ := ts.post("/settings/notifications/test", nil); code != http.StatusUnprocessableEntity || !strings.Contains(body, "test notification failed") {
		t.Fatalf("failed test = %d\n%s", code, body)
	}
}
