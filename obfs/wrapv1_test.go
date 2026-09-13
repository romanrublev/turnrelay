package obfs

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestDeriveWrapKey(t *testing.T) {
	k1, err := DeriveWrapKey("secret")
	if err != nil || len(k1) != WrapKeyLen {
		t.Fatalf("derive: %v len=%d", err, len(k1))
	}
	k2, _ := DeriveWrapKey("secret")
	k3, _ := DeriveWrapKey("other")
	if !bytes.Equal(k1, k2) || bytes.Equal(k1, k3) {
		t.Fatal("key derivation not deterministic per password")
	}
	if _, err := DeriveWrapKey(""); err == nil {
		t.Fatal("empty password accepted")
	}
}

func TestWrapRoundTripAndHeader(t *testing.T) {
	key, _ := DeriveWrapKey("pw")
	c, err := NewWrapCodec(key, false)
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte{0xAB}, 1200)
	w1, err := c.Wrap(nil, payload)
	if err != nil {
		t.Fatal(err)
	}
	if !IsWrapRTP(w1) {
		t.Fatal("not classified as RTP")
	}
	if w1[0] != 0x80|0x20 || w1[1]&0x7f != 111 {
		t.Fatalf("header %x", w1[:2])
	}
	pad := int(w1[len(w1)-1])
	if pad < 1 || pad > 24 {
		t.Fatalf("padding %d", pad)
	}
	if len(w1) != 12+len(payload)+16+pad {
		t.Fatalf("len %d", len(w1))
	}
	w2, _ := c.Wrap(nil, payload)
	seq1 := binary.BigEndian.Uint16(w1[2:4])
	seq2 := binary.BigEndian.Uint16(w2[2:4])
	ts1 := binary.BigEndian.Uint32(w1[4:8])
	ts2 := binary.BigEndian.Uint32(w2[4:8])
	if seq2 != seq1+1 || ts2 != ts1+960 {
		t.Fatalf("seq %d->%d ts %d->%d", seq1, seq2, ts1, ts2)
	}
	if binary.BigEndian.Uint32(w1[8:12]) != binary.BigEndian.Uint32(w2[8:12]) {
		t.Fatal("ssrc changed between packets")
	}
	d := &WrapCodec{}
	_ = d
	out, err := c.Unwrap(nil, w1)
	if err != nil || !bytes.Equal(out, payload) {
		t.Fatalf("unwrap: %v", err)
	}
	// Unwrap is keyed only: a second codec with the same key decodes too.
	c2, _ := NewWrapCodec(key, true)
	out, err = c2.Unwrap(nil, w2)
	if err != nil || !bytes.Equal(out, payload) {
		t.Fatalf("cross unwrap: %v", err)
	}
}

func TestWrapRejects(t *testing.T) {
	key, _ := DeriveWrapKey("pw")
	c, _ := NewWrapCodec(key, false)
	w, _ := c.Wrap(nil, []byte("hello"))
	w[15] ^= 1
	if _, err := c.Unwrap(nil, w); err == nil {
		t.Fatal("tampered packet accepted")
	}
	if _, err := c.Unwrap(nil, []byte{0x80, 111, 0}); err == nil {
		t.Fatal("short packet accepted")
	}
	if _, err := c.Unwrap(nil, append([]byte{0x00}, w[1:]...)); err == nil {
		t.Fatal("non-RTP accepted")
	}
	if _, err := c.Wrap(nil, nil); err == nil {
		t.Fatal("empty payload accepted")
	}
	if _, err := NewWrapCodec([]byte("short"), false); err == nil {
		t.Fatal("short key accepted")
	}
}

func TestUnwrapInPlace(t *testing.T) {
	key, _ := DeriveWrapKey("pw")
	c, _ := NewWrapCodec(key, false)
	payload := []byte("in-place test payload")
	buf := make([]byte, 0, 2048)
	w, err := c.Wrap(buf, payload)
	if err != nil {
		t.Fatal(err)
	}
	// In-place decode: reuse the same buffer for both wire and dst
	out, err := c.Unwrap(w[:cap(w)], w)
	if err != nil {
		t.Fatalf("in-place unwrap failed: %v", err)
	}
	if !bytes.Equal(out, payload) {
		t.Fatalf("in-place unwrap payload mismatch: got %v, want %v", out, payload)
	}
}
