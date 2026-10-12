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
			if r.PostForm.Get("device_code") != "dc" || r.PostForm.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" {
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
	token, err := dc.Wait(context.Background(), code)
	if err != nil || token != "gho_x" {
		t.Fatalf("Wait = %q, %v", token, err)
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
