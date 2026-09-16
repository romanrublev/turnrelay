package obfs

import (
	"context"
	"testing"
	"time"
)

func TestTrySendDropsWhenFull(t *testing.T) {
	ch := make(chan []byte, 1)
	trySend(ch, []byte("a")) // fits
	done := make(chan struct{})
	go func() {
		trySend(ch, []byte("b")) // full: must not block
		trySend(ch, []byte("c"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("trySend blocked on a full channel")
	}
	if got := <-ch; string(got) != "a" {
		t.Fatalf("first = %q, want a", got)
	}
	select {
	case <-ch:
		t.Fatal("more than one packet delivered to a cap-1 channel")
	default:
	}
}

func TestPerSourceCreatesOnceAndReaps(t *testing.T) {
	ps := newPerSource(50 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ps.reap(ctx)

	creates := 0
	mk := func([]byte) (func([]byte), bool) {
		creates++
		return func([]byte) {}, true
	}
	for i := 0; i < 3; i++ {
		ps.dispatch(time.Now(), "1.2.3.4:5", mk, []byte{byte(i)})
	}
	if creates != 1 {
		t.Fatalf("creates=%d, want 1 (one source seen thrice)", creates)
	}
	if ps.len() != 1 {
		t.Fatalf("len=%d, want 1", ps.len())
	}

	deadline := time.Now().Add(2 * time.Second)
	for ps.len() != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if ps.len() != 0 {
		t.Fatalf("idle session not reaped, len=%d", ps.len())
	}

	ps.dispatch(time.Now(), "1.2.3.4:5", mk, []byte{9})
	if creates != 2 {
		t.Fatalf("creates=%d after re-dispatch of a reaped source, want 2", creates)
	}
}

func TestPerSourceDispatchNeverBlocks(t *testing.T) {
	ps := newPerSource(time.Minute)
	full := make(chan []byte, 1)
	full <- []byte("x") // pre-filled so trySend always drops
	mk := func([]byte) (func([]byte), bool) {
		return func(p []byte) { trySend(full, p) }, true
	}
	done := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			ps.dispatch(time.Now(), "a", mk, []byte{byte(i)})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatch blocked while the per-source channel was full")
	}
}

// dispatch must not create an entry when newEntry rejects the first packet
// (e.g. serveSRTP refusing a non-DTLS first byte), so a scan cannot populate
// the map or spawn goroutines.
func TestPerSourceRejectsWhenNewEntryDeclines(t *testing.T) {
	ps := newPerSource(time.Minute)
	calls := 0
	mk := func(pkt []byte) (func([]byte), bool) {
		calls++
		return nil, false // decline: not a session-starting packet
	}
	ps.dispatch(time.Now(), "9.9.9.9:1", mk, []byte{0x00})
	ps.dispatch(time.Now(), "9.9.9.9:1", mk, []byte{0x00})
	if ps.len() != 0 {
		t.Fatalf("declined source created an entry, len=%d", ps.len())
	}
	if calls != 2 {
		t.Fatalf("newEntry called %d times, want 2 (retried each packet, never cached)", calls)
	}
}
