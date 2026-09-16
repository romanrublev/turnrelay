package exit

import (
	"net"
	"testing"
	"time"
)

func TestDemuxRoutesByKind(t *testing.T) {
	server, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	client, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	d := NewDemux(server)
	defer d.Close()

	// KCP-side frames carry a 4-byte sequence after the discriminator; UDP and
	// unknown kinds do not.
	for _, raw := range [][]byte{{KindKCP, 0, 0, 0, 0, 'k', '1'}, {KindUDP, 'u', '1'}, {0x07, 'x'}, {KindKCP, 0, 0, 0, 1, 'k', '2'}} {
		if _, err := client.WriteTo(raw, server.LocalAddr()); err != nil {
			t.Fatal(err)
		}
	}
	buf := make([]byte, 64)
	_ = d.KCP().SetReadDeadline(time.Now().Add(2 * time.Second))
	for _, want := range []string{"k1", "k2"} {
		n, from, err := d.KCP().ReadFrom(buf)
		if err != nil || string(buf[:n]) != want || from.String() != client.LocalAddr().String() {
			t.Fatalf("kcp side: n=%d %q from=%v err=%v", n, buf[:n], from, err)
		}
	}
	_ = d.UDP().SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := d.UDP().ReadFrom(buf)
	if err != nil || string(buf[:n]) != "u1" {
		t.Fatalf("udp side: %q err=%v", buf[:n], err)
	}
	// The unknown kind was dropped: nothing more on either side.
	_ = d.UDP().SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, _, err := d.UDP().ReadFrom(buf); err == nil {
		t.Fatal("unknown kind delivered")
	}

	// WriteTo prepends the kind byte.
	if _, err := d.UDP().WriteTo([]byte("reply"), client.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err = client.ReadFrom(buf)
	if err != nil || string(buf[:n]) != string(append([]byte{KindUDP}, "reply"...)) {
		t.Fatalf("wire %q err=%v", buf[:n], err)
	}
}
