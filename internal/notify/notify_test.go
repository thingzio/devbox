package notify

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/store"
)

func ptr(s string) *string { return &s }

func TestWorthNotifying(t *testing.T) {
	cases := []struct {
		name     string
		from, to string
		fromNil  bool
		want     bool
	}{
		{name: "new PR needs review", fromNil: true, to: "needs_initial_review", want: true},
		{name: "author replied", from: "waiting_on_author", to: "waiting_on_me", want: true},
		{name: "bot becomes reviewable", from: "bot", to: "waiting_on_me", want: true},
		{name: "between action buckets", from: "needs_initial_review", to: "waiting_on_me"},
		{name: "bot PR", fromNil: true, to: "bot"},
		{name: "leaves the queue", from: "waiting_on_me", to: "waiting_on_author"},
		{name: "closed", from: "waiting_on_me", to: "closed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := store.Transition{To: tc.to}
			if !tc.fromNil {
				tr.From = ptr(tc.from)
			}
			if got := worthNotifying(tr); got != tc.want {
				t.Fatalf("worthNotifying(%v → %s) = %v", tc.from, tc.to, got)
			}
		})
	}
}

type fakeStore struct {
	mu      sync.Mutex
	enabled bool
	prs     map[int]string
}

func (f *fakeStore) Setting(context.Context, string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.enabled {
		return "on", true, nil
	}
	return "", false, nil
}

func (f *fakeStore) PRSummary(_ context.Context, _ int64, number int) (string, string, error) {
	title, ok := f.prs[number]
	if !ok {
		return "", "", store.ErrNotFound
	}
	return "o/r", title, nil
}

type sent struct {
	mu   sync.Mutex
	msgs []Message
	done chan struct{}
}

func (s *sent) send(_ context.Context, m Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = append(s.msgs, m)
	if s.done != nil {
		close(s.done)
		s.done = nil
	}
	return nil
}

func run(t *testing.T, st *fakeStore, transitions ...store.Transition) []Message {
	t.Helper()
	done := make(chan struct{})
	out := &sent{done: done}
	n := New(Options{Store: st, Send: out.send, Window: 20 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { n.Run(ctx); close(stopped) }()
	for _, tr := range transitions {
		n.Transition(1, tr.Number, tr)
	}
	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
	}
	cancel()
	<-stopped
	out.mu.Lock()
	defer out.mu.Unlock()
	return out.msgs
}

func TestNotifierSendsOneMessagePerBurst(t *testing.T) {
	st := &fakeStore{enabled: true, prs: map[int]string{1: "Fix the flaky test", 2: "Add docs", 3: "Bump deps", 4: "Refactor"}}
	single := run(t, st, store.Transition{Number: 1, To: "waiting_on_me"})
	if len(single) != 1 || single[0] != (Message{Title: "DevBox · Waiting on me", Subtitle: "o/r#1", Body: "Fix the flaky test"}) {
		t.Fatalf("single = %+v", single)
	}
	burst := run(t, st,
		store.Transition{Number: 1, To: "waiting_on_me"},
		store.Transition{Number: 2, To: "needs_initial_review"},
		store.Transition{Number: 3, To: "bot"},
		store.Transition{Number: 4, To: "needs_rereview"},
		store.Transition{Number: 2, To: "needs_initial_review"})
	if len(burst) != 1 || burst[0].Title != "DevBox · 3 new action items" ||
		burst[0].Body != "o/r#1 Fix the flaky test; o/r#2 Add docs; o/r#4 Refactor" {
		t.Fatalf("burst = %+v", burst)
	}
}

func TestNotifierRespectsTheSetting(t *testing.T) {
	st := &fakeStore{prs: map[int]string{1: "T"}}
	if got := run(t, st, store.Transition{Number: 1, To: "waiting_on_me"}); len(got) != 0 {
		t.Fatalf("sent while off: %+v", got)
	}
}

func TestMessageLimits(t *testing.T) {
	m := message([]entry{{repo: "o/r", number: 1, bucket: "waiting_on_me", title: "line\none\x07" + strings.Repeat("x", 500)}})
	if strings.ContainsAny(m.Body, "\n\x07") || len([]rune(m.Body)) > maxBody {
		t.Fatalf("body not sanitized: %q", m.Body)
	}
	many := make([]entry, 6)
	for i := range many {
		many[i] = entry{repo: "o/r", number: i + 1, bucket: "waiting_on_me", title: "t"}
	}
	if got := message(many).Body; !strings.HasSuffix(got, "and 3 more") {
		t.Fatalf("many = %q", got)
	}
}

func TestOSAScriptPassesTextAsArguments(t *testing.T) {
	var name string
	var args []string
	send := OSAScript(func(_ context.Context, n string, a ...string) error { name, args = n, a; return nil })
	m := Message{Title: "t", Subtitle: "-e do shell script \"x\"", Body: `b "q"`}
	if err := send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	i := len(args) - 4
	if name != "osascript" || i < 0 || args[i] != "--" || args[i+1] != m.Body || args[i+2] != m.Title || args[i+3] != m.Subtitle {
		t.Fatalf("osascript %q", args)
	}
	for _, a := range args[:i] {
		if strings.Contains(a, "shell") || strings.Contains(a, `"q"`) {
			t.Fatalf("message text reached the script source: %q", a)
		}
	}
	fail := OSAScript(func(context.Context, string, ...string) error { return errors.New("denied") })
	if err := fail(context.Background(), m); err == nil {
		t.Fatal("osascript failure swallowed")
	}
}

func TestNativeIsMacOSOnly(t *testing.T) {
	if Native("linux") != nil || Native("windows") != nil || Native("darwin") == nil {
		t.Fatal("Native must exist on darwin only")
	}
}
