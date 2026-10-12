package hub

import "testing"

func TestPublishSubscribe(t *testing.T) {
	h := New()
	ch, unsubscribe := h.Subscribe()
	h.Publish(Event{RepoID: 1, Number: 7})
	if ev := <-ch; ev.RepoID != 1 || ev.Number != 7 {
		t.Fatalf("event = %+v", ev)
	}
	if h.Subscribers() != 1 {
		t.Fatalf("subscribers = %d", h.Subscribers())
	}
	unsubscribe()
	if _, ok := <-ch; ok {
		t.Fatal("channel open after unsubscribe")
	}
	unsubscribe()
}

func TestPublishNeverBlocks(t *testing.T) {
	h := New()
	_, unsubscribe := h.Subscribe()
	defer unsubscribe()
	for i := 0; i < 100; i++ {
		h.Publish(Event{Number: i})
	}
}

func TestCloseEndsSubscriptions(t *testing.T) {
	h := New()
	ch, _ := h.Subscribe()
	h.Close()
	if _, ok := <-ch; ok {
		t.Fatal("channel open after Close")
	}
	late, _ := h.Subscribe()
	if _, ok := <-late; ok {
		t.Fatal("subscription after Close is open")
	}
	h.Close()
	h.Publish(Event{})
}
