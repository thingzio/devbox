package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func snoozeForm(repoID int64, number, choice, next string) url.Values {
	return url.Values{"repo_id": {strconv.FormatInt(repoID, 10)}, "number": {number}, "for": {choice}, "next": {next}}
}

func TestSnoozeHidesAPRFromTheQueue(t *testing.T) {
	ts := newTestServer(t)
	r := ts.repo(ts.account("me"), "o", "r")
	ts.seed(r, 1, "Snooze me", "https://github.com/o/r/pull/1", "needs_initial_review", `{}`)
	ts.seed(r, 2, "Keep me", "https://github.com/o/r/pull/2", "waiting_on_me", `{}`)
	_, body := ts.get("/")
	assertContains(t, "queue before", body, `action="/snooze"`, `name="for" value="change"`, `name="for" value="7d"`)

	events, unsubscribe := ts.hub.Subscribe()
	defer unsubscribe()
	code, _, h := ts.post("/snooze", snoozeForm(r.ID, "1", "change", "/?account=me"))
	if code != http.StatusSeeOther || h.Get("Location") != "/?account=me" {
		t.Fatalf("snooze = %d → %q", code, h.Get("Location"))
	}
	select {
	case ev := <-events:
		if ev.RepoID != r.ID || ev.Number != 1 {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no change event; other open pages would not refresh")
	}

	_, body = ts.get("/")
	assertContains(t, "queue after", body, "Snoozed (1)", "until it changes", `action="/unsnooze"`,
		`<span class="stat-n">1</span><span class="stat-l">All actions</span>`)
	if strings.Contains(body, "Needs initial review (1)") {
		t.Error("snoozed PR still listed in its bucket")
	}

	if code, _, _ := ts.post("/unsnooze", snoozeForm(r.ID, "1", "", "/")); code != http.StatusSeeOther {
		t.Fatalf("unsnooze = %d", code)
	}
	_, body = ts.get("/")
	assertContains(t, "queue after unsnooze", body, "Needs initial review (1)")
	if strings.Contains(body, "Snoozed (") {
		t.Error("Snoozed section shown with nothing snoozed")
	}
}

func TestTimedSnoozeEnds(t *testing.T) {
	ts := newTestServer(t)
	r := ts.repo(ts.account("me"), "o", "r")
	ts.seed(r, 1, "Later", "https://github.com/o/r/pull/1", "needs_initial_review", `{}`)
	if code, _, _ := ts.post("/snooze", snoozeForm(r.ID, "1", "1d", "/")); code != http.StatusSeeOther {
		t.Fatalf("snooze = %d", code)
	}
	_, body := ts.get("/")
	assertContains(t, "snoozed", body, "until Oct 2 12:00 or it changes")
	ts.setNow(t0.Add(25 * time.Hour))
	_, body = ts.get("/")
	assertContains(t, "after a day", body, "Needs initial review (1)")
}

func TestSnoozeRejectsBadInput(t *testing.T) {
	ts := newTestServer(t)
	r := ts.repo(ts.account("me"), "o", "r")
	ts.seed(r, 1, "T", "https://github.com/o/r/pull/1", "needs_initial_review", `{}`)
	cases := []struct {
		name string
		form url.Values
		want int
	}{
		{"unknown duration", snoozeForm(r.ID, "1", "forever", "/"), http.StatusBadRequest},
		{"bad number", snoozeForm(r.ID, "one", "1d", "/"), http.StatusBadRequest},
		{"unknown PR", snoozeForm(r.ID, "99", "1d", "/"), http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if code, _, _ := ts.post("/snooze", tc.form); code != tc.want {
				t.Fatalf("snooze = %d, want %d", code, tc.want)
			}
		})
	}
	if code, _, h := ts.post("/snooze", snoozeForm(r.ID, "1", "3d", "https://evil.example/")); code != http.StatusSeeOther || h.Get("Location") != "/" {
		t.Fatalf("foreign next = %d → %q", code, h.Get("Location"))
	}
}

func TestUnsnoozeRejectsBadInput(t *testing.T) {
	ts := newTestServer(t)
	for _, form := range []url.Values{{"repo_id": {"x"}, "number": {"1"}}, {"repo_id": {"1"}, "number": {"0"}}} {
		if code, _, _ := ts.post("/unsnooze", form); code != http.StatusBadRequest {
			t.Errorf("unsnooze %v = %d, want 400", form, code)
		}
	}
}
