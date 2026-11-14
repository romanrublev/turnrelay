package obfs

import (
	"testing"

	"github.com/romanrublev/turnrelay/passwords"
)

func TestMatchWrapCodecPicksTheRightKey(t *testing.T) {
	var keys [][]byte
	for _, pw := range []string{"one", "two", "three"} {
		k, err := DeriveWrapKey(pw)
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
	}
	client, err := NewWrapCodec(keys[1], false)
	if err != nil {
		t.Fatal(err)
	}
	pkt, err := client.Wrap(nil, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if matchWrapCodec(keys, false, append([]byte(nil), pkt...)) == nil {
		t.Fatal("packet under a listed key was not matched")
	}
	if matchWrapCodec(keys[:1], false, append([]byte(nil), pkt...)) != nil {
		t.Fatal("packet matched a key it was not sealed with")
	}
	if matchWrapCodec(keys, false, []byte("not rtp at all, just noise.....")) != nil {
		t.Fatal("garbage matched")
	}
}

type swapSource struct {
	list []string
	ch   chan struct{}
}

func (s *swapSource) List() []string           { return s.list }
func (s *swapSource) Changed() <-chan struct{} { return s.ch }

func TestKeyringFollowsSource(t *testing.T) {
	src := &swapSource{list: []string{"a"}, ch: make(chan struct{})}
	var _ passwords.Source = src
	k := newKeyring(src)
	if n := len(k.Keys()); n != 1 {
		t.Fatalf("keys = %d, want 1", n)
	}
	old := src.ch
	src.list, src.ch = []string{"a", "b"}, make(chan struct{})
	close(old)
	if n := len(k.Keys()); n != 2 {
		t.Fatalf("keys after change = %d, want 2", n)
	}
}
