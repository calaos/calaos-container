package events

import (
	"sync"
	"testing"
	"time"
)

func TestSubscribeUnsubscribe(t *testing.T) {
	h := NewHub()

	s := h.Subscribe()
	if s == nil || s.Ch == nil {
		t.Fatalf("Subscribe returned invalid subscriber")
	}

	h.mu.RLock()
	_, ok := h.subs[s]
	h.mu.RUnlock()
	if !ok {
		t.Fatalf("subscriber not registered in hub")
	}

	h.Unsubscribe(s)

	h.mu.RLock()
	_, ok = h.subs[s]
	h.mu.RUnlock()
	if ok {
		t.Fatalf("subscriber still registered after Unsubscribe")
	}

	// Channel must be closed.
	select {
	case _, open := <-s.Ch:
		if open {
			t.Fatalf("expected channel to be closed and empty")
		}
	default:
		t.Fatalf("expected channel to be closed (read should not block)")
	}

	// Idempotent: unsubscribing again (or with an unknown subscriber) must
	// not panic.
	h.Unsubscribe(s)
	h.Unsubscribe(nil)
}

func TestPublishMultiSubscribers(t *testing.T) {
	h := NewHub()

	const n = 5
	subs := make([]*Subscriber, n)
	for i := range subs {
		subs[i] = h.Subscribe()
	}

	h.Publish(EventUpgradeProgress, map[string]int{"pct": 42})

	for i, s := range subs {
		select {
		case ev := <-s.Ch:
			if ev.Type != EventUpgradeProgress {
				t.Fatalf("subscriber %d: got type %q, want %q", i, ev.Type, EventUpgradeProgress)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d: did not receive published event", i)
		}
	}
}

func TestPublishDropsOnFullBuffer(t *testing.T) {
	h := NewHub()
	s := h.Subscribe()

	// Fill the buffer completely without ever draining it.
	for i := 0; i < subscriberBufferSize; i++ {
		h.Publish(EventUpgradeProgress, i)
	}
	if len(s.Ch) != subscriberBufferSize {
		t.Fatalf("expected buffer full (%d), got %d", subscriberBufferSize, len(s.Ch))
	}

	// This publish must not block, and must be dropped.
	done := make(chan struct{})
	go func() {
		h.Publish(EventUpgradeProgress, "overflow")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("Publish blocked on a full subscriber buffer")
	}

	if len(s.Ch) != subscriberBufferSize {
		t.Fatalf("expected buffer to remain full (event should be dropped), got len=%d", len(s.Ch))
	}
}

func TestClose(t *testing.T) {
	h := NewHub()

	s1 := h.Subscribe()
	s2 := h.Subscribe()

	h.Close()

	for i, s := range []*Subscriber{s1, s2} {
		select {
		case _, open := <-s.Ch:
			if open {
				t.Fatalf("subscriber %d: channel not closed after Close", i)
			}
		default:
			t.Fatalf("subscriber %d: expected closed channel to be readable without blocking", i)
		}
	}

	// Close must be idempotent.
	h.Close()
}

func TestPublishAfterCloseIsNoop(t *testing.T) {
	h := NewHub()
	s := h.Subscribe()

	h.Close()

	// Publish after close must not panic and must not send anything (the
	// channel is closed so sending on it would panic if attempted).
	h.Publish(EventUpgradeProgress, "should not be delivered")

	select {
	case _, open := <-s.Ch:
		if open {
			t.Fatalf("unexpected open channel receive after close")
		}
	default:
		t.Fatalf("expected closed channel to be readable without blocking")
	}
}

func TestSubscribeAfterCloseReturnsClosedChannel(t *testing.T) {
	h := NewHub()
	h.Close()

	s := h.Subscribe()
	select {
	case _, open := <-s.Ch:
		if open {
			t.Fatalf("expected channel from Subscribe on closed hub to be closed")
		}
	default:
		t.Fatalf("expected closed channel to be readable without blocking")
	}
}

// TestConcurrentUse exercises Subscribe/Unsubscribe/Publish/Close
// concurrently to be run under -race.
func TestConcurrentUse(t *testing.T) {
	h := NewHub()

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Publishers.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					h.Publish(EventUpgradeProgress, "x")
				}
			}
		}()
	}

	// Subscribers churn: subscribe, read a bit, unsubscribe.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				s := h.Subscribe()
				select {
				case <-s.Ch:
				case <-time.After(time.Millisecond):
				}
				h.Unsubscribe(s)
			}
		}()
	}

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()

	h.Close()
}

func TestDefaultHubConvenienceFuncs(t *testing.T) {
	// Use a fresh Default so this test doesn't interfere with others via
	// shared global state ordering (Go runs tests sequentially by default
	// within a package unless t.Parallel is used, but be defensive anyway).
	orig := Default
	defer func() { Default = orig }()
	Default = NewHub()

	s := Subscribe()
	Publish(EventUpgradeProgress, "hello")

	select {
	case ev := <-s.Ch:
		if ev.Type != EventUpgradeProgress {
			t.Fatalf("got type %q, want %q", ev.Type, EventUpgradeProgress)
		}
	case <-time.After(time.Second):
		t.Fatalf("did not receive event via package-level Publish/Subscribe")
	}

	Unsubscribe(s)
	Close()
}
