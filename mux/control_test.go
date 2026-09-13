package mux

import (
	"bytes"
	"testing"
)

func TestHelloRoundTrip(t *testing.T) {
	var id [16]byte
	for i := range id {
		id[i] = byte(i + 1)
	}
	b := EncodeHello(id)
	if len(b) != HelloLen {
		t.Fatalf("len %d", len(b))
	}
	if !bytes.Equal(b[:4], []byte{0xff, 'G', 'R', 'P'}) {
		t.Fatalf("magic %x", b[:4])
	}
	got, ok := ParseHello(b)
	if !ok || got != id {
		t.Fatalf("parse: ok=%v got=%x", ok, got)
	}
	if _, ok := ParseHello(b[:19]); ok {
		t.Fatal("short hello accepted")
	}
	if !IsControl(b) {
		t.Fatal("hello not control")
	}
}

func TestProbeRoundTrip(t *testing.T) {
	b := EncodeProbe(0x0102030405060708)
	if len(b) != ProbeLen {
		t.Fatalf("len %d", len(b))
	}
	if !bytes.Equal(b, []byte{0xff, 'P', 'N', 'G', 1, 2, 3, 4, 5, 6, 7, 8}) {
		t.Fatalf("wire %x", b)
	}
	seq, ok := ParseProbe(b)
	if !ok || seq != 0x0102030405060708 {
		t.Fatalf("parse: ok=%v seq=%x", ok, seq)
	}
	if IsControl([]byte{1, 0, 0, 0}) {
		t.Fatal("wireguard handshake init treated as control")
	}
	if IsControl([]byte{0xff, 'X', 'Y', 'Z'}) {
		t.Fatal("unknown 0xff frame treated as control")
	}
}
