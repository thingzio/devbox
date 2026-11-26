package web

import (
	"net/http"
	"strings"
	"testing"
)

func TestRemoteAccessRequiresTheStartupToken(t *testing.T) {
	ts := newTestServerWith(t, func(o *Options) { o.AllowRemote, o.AccessToken = true, "s3cret" })
	for _, path := range []string{"/", "/settings", "/metrics", "/events", "/static/app.js", "/?token=wrong"} {
		if code, body := ts.get(path); code != http.StatusUnauthorized || !strings.Contains(body, "printed in the terminal") {
			t.Errorf("GET %s without the token = %d %q", path, code, body)
		}
	}
	if code, _ := ts.get("/healthz"); code != http.StatusOK {
		t.Errorf("healthz = %d", code)
	}
	if code, _, _ := ts.post("/refresh", nil); code != http.StatusUnauthorized {
		t.Errorf("POST without the token = %d", code)
	}

	resp, err := ts.client.Get(ts.http.URL + "/settings?token=s3cret&x=1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/settings?x=1" {
		t.Fatalf("token login = %d → %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			session = c
		}
	}
	if session == nil || !session.HttpOnly || session.SameSite != http.SameSiteStrictMode || session.Value == "s3cret" || session.Path != "/" {
		t.Fatalf("session cookie = %+v", session)
	}

	req, _ := http.NewRequestWithContext(ts.ctx, http.MethodGet, ts.http.URL+"/settings", nil)
	req.AddCookie(session)
	resp, err = ts.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET with the session cookie = %d", resp.StatusCode)
	}

	req, _ = http.NewRequestWithContext(ts.ctx, http.MethodGet, ts.http.URL+"/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "forged"})
	resp, err = ts.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET with a forged cookie = %d", resp.StatusCode)
	}
}

func TestLoopbackNeedsNoToken(t *testing.T) {
	ts := newTestServer(t)
	if code, _ := ts.get("/"); code != http.StatusOK {
		t.Fatalf("loopback GET / = %d", code)
	}
}

func TestRemoteWithoutTokenIsRefused(t *testing.T) {
	_, err := New(Options{AllowRemote: true, Addr: "0.0.0.0:8484"})
	if err == nil || !strings.Contains(err.Error(), "access token") {
		t.Fatalf("New = %v", err)
	}
}
