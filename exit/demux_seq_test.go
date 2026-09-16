package exit

import (
	"net"
	"testing"
	"time"
)

// TestDemuxSeqRoundTrip checks the KCP-side sequence wire change: a KCP frame
// and a UDP frame each survive the demux intact, and the receiver's loss
// estimate stays ~0 for a clean stream.
func TestDemuxSeqRoundTrip(t *testing.T) {
	a, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	da := NewDemux(a)
	db := NewDemux(b)
	t.Cleanup(func() { da.Close(); db.Close() })
	bAddr := b.LocalAddr()

	// KCP side: send several frames, read them back, payloads must match.
	for i := 0; i < 300; i++ {
		msg := []byte{'k', byte(i)}
		if _, err := da.KCP().WriteTo(msg, bAddr); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 64)
		_ = db.KCP().SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _, err := db.KCP().ReadFrom(buf)
		if err != nil {
			t.Fatalf("kcp read %d: %v", i, err)
		}
		if n != 2 || buf[0] != 'k' || buf[1] != byte(i) {
			t.Fatalf("kcp payload %d corrupted: %v", i, buf[:n])
		}
	}
	if r := db.LossRate(); r > 0.01 {
		t.Fatalf("clean KCP stream loss estimate = %v, want ~0", r)
	}

	// UDP side still works (no seq, unchanged framing).
	if _, err := da.UDP().WriteTo([]byte("dgram"), bAddr); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	_ = db.UDP().SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := db.UDP().ReadFrom(buf)
	if err != nil || string(buf[:n]) != "dgram" {
		t.Fatalf("udp payload corrupted: %q err %v", buf[:n], err)
	}
}
