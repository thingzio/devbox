package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type deviceServer struct {
	mu        sync.Mutex
	responses []string
	polls     int
	server    *httptest.Server
}

func newDeviceServer(t *testing.T, responses ...string) *deviceServer {
	t.Helper()
	ds := &deviceServer{responses: responses}
	ds.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("form: %v", err)
		}
		switch r.URL.Path {
		case "/login/device/code":
			if r.PostForm.Get("client_id") != "cid" || r.PostForm.Get("scope") != Scopes {
				t.Errorf("device code form = %v", r.PostForm)
			}
			fmt.Fprint(w, `{"device_code":"dc","user_code":"ABCD-1234","verification_uri":"https://github.com/login/device","expires_in":900,"interval":5}`)
		case "/login/oauth/access_token":
			grant := r.PostForm.Get("grant_type")
			deviceGrant := grant == "urn:ietf:params:oauth:grant-type:device_code" && r.PostForm.Get("device_code") == "dc"
			refreshGrant := grant == "refresh_token" && r.PostForm.Get("refresh_token") != ""
			if (!deviceGrant && !refreshGrant) || r.PostForm.Get("client_id") != "cid" || r.PostForm.Has("client_secret") {
				t.Errorf("token form = %v", r.PostForm)
			}
			ds.mu.Lock()
			resp := ds.responses[min(ds.polls, len(ds.responses)-1)]
			ds.polls++
			ds.mu.Unlock()
			fmt.Fprint(w, resp)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ds.server.Close)
	return ds
}

func newDeviceClient(base string, sleeps *[]time.Duration) *DeviceClient {
	return &DeviceClient{BaseURL: base, ClientID: "cid", Sleep: func(ctx context.Context, d time.Duration) error {
		*sleeps = append(*sleeps, d)
		return ctx.Err()
	}}
}

func TestDeviceFlowSucceedsAfterPendingAndSlowDown(t *testing.T) {
	ds := newDeviceServer(t, `{"error":"authorization_pending"}`, `{"error":"slow_down"}`, `{"access_token":"gho_x","token_type":"bearer","scope":"repo,read:org"}`)
	var sleeps []time.Duration
	dc := newDeviceClient(ds.server.URL, &sleeps)
	code, err := dc.Start(context.Background())
	if err != nil || code.UserCode != "ABCD-1234" || code.VerificationURI != "https://github.com/login/device" {
		t.Fatalf("Start = %+v, %v", code, err)
	}
	cred, err := dc.Wait(context.Background(), code)
	if err != nil || cred != (Credential{AccessToken: "gho_x"}) {
		t.Fatalf("Wait = %+v, %v", cred, err)
	}
	want := []time.Duration{5 * time.Second, 5 * time.Second, 10 * time.Second}
	if fmt.Sprint(sleeps) != fmt.Sprint(want) {
		t.Fatalf("sleeps = %v, want %v", sleeps, want)
	}
}

func TestDeviceFlowFailures(t *testing.T) {
	cases := map[string]error{
		`{"error":"expired_token"}`: ErrExpired,
		`{"error":"access_denied"}`: ErrDenied,
	}
	for resp, want := range cases {
		ds := newDeviceServer(t, resp)
		var sleeps []time.Duration
		dc := newDeviceClient(ds.server.URL, &sleeps)
		code, _ := dc.Start(context.Background())
		if _, err := dc.Wait(context.Background(), code); !errors.Is(err, want) {
			t.Errorf("Wait(%s) err = %v, want %v", resp, err, want)
		}
	}
	ds := newDeviceServer(t, `{"error":"incorrect_client_credentials","error_description":"bad"}`)
	var sleeps []time.Duration
	dc := newDeviceClient(ds.server.URL, &sleeps)
	code, _ := dc.Start(context.Background())
	if _, err := dc.Wait(context.Background(), code); err == nil {
		t.Fatal("unknown error accepted")
	}
}

func TestDeviceFlowDeadline(t *testing.T) {
	ds := newDeviceServer(t, `{"error":"authorization_pending"}`)
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	dc := &DeviceClient{BaseURL: ds.server.URL, ClientID: "cid", Now: func() time.Time { return now },
		Sleep: func(_ context.Context, d time.Duration) error { now = now.Add(d); return nil }}
	code, _ := dc.Start(context.Background())
	if _, err := dc.Wait(context.Background(), code); !errors.Is(err, ErrExpired) {
		t.Fatalf("err = %v, want ErrExpired", err)
	}
}

func TestDeviceStartRequiresClientID(t *testing.T) {
	if _, err := (&DeviceClient{}).Start(context.Background()); err == nil {
		t.Fatal("started without a client id")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"error":"device_flow_disabled","error_description":"Device flow is not enabled"}`)
	}))
	defer srv.Close()
	if _, err := (&DeviceClient{BaseURL: srv.URL, ClientID: "cid"}).Start(context.Background()); err == nil {
		t.Fatal("disabled device flow accepted")
	}
}

var authNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func TestDeviceFlowReturnsExpiringGrant(t *testing.T) {
	ds := newDeviceServer(t, `{"access_token":"ghu_a","expires_in":28800,"refresh_token":"ghr_a","refresh_token_expires_in":15897600,"token_type":"bearer","scope":""}`)
	dc := &DeviceClient{BaseURL: ds.server.URL, ClientID: "cid", Now: func() time.Time { return authNow },
		Sleep: func(context.Context, time.Duration) error { return nil }}
	code, _ := dc.Start(context.Background())
	cred, err := dc.Wait(context.Background(), code)
	want := Credential{AccessToken: "ghu_a", RefreshToken: "ghr_a", ExpiresAt: authNow.Add(8 * time.Hour), RefreshExpiresAt: authNow.Add(15897600 * time.Second)}
	if err != nil || cred != want {
		t.Fatalf("Wait = %+v, %v", cred, err)
	}
}

func TestRefresh(t *testing.T) {
	cases := []struct {
		name     string
		resp     string
		want     Credential
		rejected bool
	}{
		{name: "rotates", resp: `{"access_token":"ghu_b","expires_in":28800,"refresh_token":"ghr_b","refresh_token_expires_in":15897600}`,
			want: Credential{AccessToken: "ghu_b", RefreshToken: "ghr_b", ExpiresAt: authNow.Add(8 * time.Hour), RefreshExpiresAt: authNow.Add(15897600 * time.Second)}},
		{name: "bad refresh token", resp: `{"error":"bad_refresh_token","error_description":"The refresh token passed is incorrect or expired."}`, rejected: true},
		{name: "unknown error is rejected", resp: `{"error":"unauthorized_client"}`, rejected: true},
		{name: "empty token", resp: `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ds := newDeviceServer(t, tc.resp)
			dc := &DeviceClient{BaseURL: ds.server.URL, ClientID: "cid", Now: func() time.Time { return authNow }}
			cred, err := dc.Refresh(context.Background(), "ghr_a")
			if errors.Is(err, ErrRefreshRejected) != tc.rejected {
				t.Fatalf("err = %v, rejected want %v", err, tc.rejected)
			}
			if tc.want.AccessToken != "" && (err != nil || cred != tc.want) {
				t.Fatalf("Refresh = %+v, %v", cred, err)
			}
			if tc.want.AccessToken == "" && err == nil {
				t.Fatal("refresh without a token succeeded")
			}
		})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer srv.Close()
	if _, err := (&DeviceClient{BaseURL: srv.URL, ClientID: "cid"}).Refresh(context.Background(), "ghr_a"); err == nil || errors.Is(err, ErrRefreshRejected) {
		t.Fatalf("gateway error = %v, want a transient error", err)
	}
}
