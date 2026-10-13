package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/store"
	"github.com/thingzio/devbox/internal/syncer"
)

func TestPollingSetting(t *testing.T) {
	ts := newTestServer(t)
	_, body := ts.get("/settings")
	for _, want := range []string{`action="/settings/polling"`, `<option value="default" selected>Default (2m)</option>`,
		`<option value="manual">`, `<option value="1m">`, `<option value="15m">`} {
		if !strings.Contains(body, want) {
			t.Errorf("polling control missing %q", want)
		}
	}
	if code, _, _ := ts.post("/settings/polling", url.Values{"polling": {"manual"}}); code != http.StatusSeeOther {
		t.Fatalf("save manual = %d", code)
	}
	if v, ok, _ := ts.db.Setting(ts.ctx, store.SettingPolling); !ok || v != "manual" {
		t.Fatalf("saved setting = %q, %v", v, ok)
	}
	if _, body := ts.get("/settings"); !strings.Contains(body, `<option value="manual" selected>`) {
		t.Fatal("saved choice not selected")
	}
	if code, _, _ := ts.post("/settings/polling", url.Values{"polling": {"5m"}}); code != http.StatusSeeOther {
		t.Fatalf("save 5m = %d", code)
	}
	if code, _, _ := ts.post("/settings/polling", url.Values{"polling": {"default"}}); code != http.StatusSeeOther {
		t.Fatalf("restore default = %d", code)
	}
	if _, ok, _ := ts.db.Setting(ts.ctx, store.SettingPolling); ok {
		t.Fatal("default did not clear the saved choice")
	}
	ts.engine.mu.Lock()
	calls := append([]syncer.Polling(nil), ts.engine.setPolling...)
	ts.engine.mu.Unlock()
	want := []syncer.Polling{{Manual: true}, {Interval: 5 * time.Minute}, syncer.DefaultPolling()}
	if len(calls) != len(want) || calls[0] != want[0] || calls[1] != want[1] || calls[2] != want[2] {
		t.Fatalf("engine polling changes = %+v, want %+v", calls, want)
	}
	code, body, _ := ts.post("/settings/polling", url.Values{"polling": {"20m"}})
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "between 1m and 15m") {
		t.Fatalf("out-of-range = %d", code)
	}
}

func TestRefreshButton(t *testing.T) {
	ts := newTestServer(t)
	r := ts.repo(ts.account("me"), "o", "r")
	if _, body := ts.get("/"); !strings.Contains(body, `action="/refresh"`) || !strings.Contains(body, "Last checked never") ||
		!strings.Contains(body, "checking every 2m") {
		t.Fatalf("refresh control missing:\n%s", body)
	}
	if _, err := ts.db.RecordPoll(ts.ctx, r.ID, ts.clock().Add(-3*time.Minute), nil); err != nil {
		t.Fatal(err)
	}
	if _, body := ts.get("/"); !strings.Contains(body, "Last checked 3 min ago") {
		t.Fatal("last checked time missing")
	}
	code, _, hdr := ts.post("/refresh", nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/" {
		t.Fatalf("refresh = %d %v", code, hdr)
	}
	ts.engine.mu.Lock()
	n := ts.engine.refreshes
	ts.engine.mu.Unlock()
	if n != 1 {
		t.Fatalf("engine refreshes = %d", n)
	}
}

func TestManualModeSuppressesStaleness(t *testing.T) {
	ts := newTestServer(t)
	ts.repo(ts.account("me"), "o", "r")
	ts.engine.SetPolling(syncer.Polling{Manual: true})
	ts.setNow(t0.Add(10 * time.Minute))
	_, body := ts.get("/")
	if strings.Contains(body, "no successful poll yet") || !strings.Contains(body, "manual: press Refresh") {
		t.Fatalf("manual mode should not warn about staleness:\n%s", body)
	}
}
