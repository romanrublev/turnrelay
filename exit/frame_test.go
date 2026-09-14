package exit

import (
	"bytes"
	"net/netip"
	"testing"

	M "github.com/sagernet/sing/common/metadata"
)

func TestUDPFrameRoundTrip(t *testing.T) {
	for _, addr := range []M.Socksaddr{
		M.SocksaddrFrom(netip.MustParseAddr("192.0.2.1"), 53),
		M.SocksaddrFrom(netip.MustParseAddr("2001:db8::1"), 443),
		M.ParseSocksaddrHostPort("example.com", 8080),
	} {
		frame, err := EncodeUDPFrame(0xbeef, addr, []byte("hello"))
		if err != nil {
			t.Fatal(err)
		}
		assoc, got, payload, err := DecodeUDPFrame(frame)
		if err != nil || assoc != 0xbeef || got.String() != addr.String() || string(payload) != "hello" {
			t.Fatalf("%s: assoc=%x got=%s payload=%q err=%v", addr, assoc, got, payload, err)
		}
	}
	if _, _, _, err := DecodeUDPFrame([]byte{0, 1}); err == nil {
		t.Fatal("short frame accepted")
	}
	if _, _, _, err := DecodeUDPFrame([]byte{0, 1, 0x09, 1, 2}); err == nil {
		t.Fatal("bad atyp accepted")
	}
}

func TestStreamHeaderRoundTrip(t *testing.T) {
	var b bytes.Buffer
	want := M.ParseSocksaddrHostPort("example.org", 443)
	if err := WriteStreamHeader(&b, want); err != nil {
		t.Fatal(err)
	}
	if b.Bytes()[0] != CmdConnect {
		t.Fatalf("cmd %x", b.Bytes()[0])
	}
	got, err := ReadStreamHeader(&b)
	if err != nil || got.String() != want.String() {
		t.Fatalf("got %s err %v", got, err)
	}
	if _, err := ReadStreamHeader(bytes.NewReader([]byte{0x02, 0x01, 1, 2, 3, 4, 0, 80})); err == nil {
		t.Fatal("unknown cmd accepted")
	}
}
