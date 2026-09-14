package mux

import (
	"bytes"
	"testing"
)

func TestAuthFrameRoundTrip(t *testing.T) {
	var session [16]byte
	copy(session[:], []byte("0123456789abcdef"))
	tag := AuthTag("secret", session)
	if len(tag) != 16 {
		t.Fatalf("tag len %d", len(tag))
	}
	frame := EncodeAuth(tag)
	if len(frame) != AuthLen || !IsControl(frame) {
		t.Fatalf("frame len %d control %v", len(frame), IsControl(frame))
	}
	got, ok := ParseAuth(frame)
	if !ok || !bytes.Equal(got, tag) {
		t.Fatalf("parse ok=%v tag=%x", ok, got)
	}
	if !VerifyAuth("secret", session, got) {
		t.Fatal("valid tag rejected")
	}
	if VerifyAuth("wrong", session, got) {
		t.Fatal("wrong password accepted")
	}
	if _, ok := ParseAuth(frame[:AuthLen-1]); ok {
		t.Fatal("short frame parsed")
	}
	if _, ok := ParseAuth(EncodeHello(session)); ok {
		t.Fatal("hello parsed as auth")
	}
}

func TestPoolDerivesAuthTagFromPassword(t *testing.T) {
	p := New(Options{Workers: 1, Password: "pw"})
	if p.authTag == nil || !bytes.Equal(p.authTag, AuthTag("pw", p.session)) {
		t.Fatalf("authTag %x", p.authTag)
	}
	if New(Options{Workers: 1}).authTag != nil {
		t.Fatal("authTag set without password")
	}
}
