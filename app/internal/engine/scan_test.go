package engine

import "testing"

func TestScanCountsWorkers(t *testing.T) {
	var c Counters
	scanLine("outbound/turnrelay[relay]: mux: worker 3 up via 193.203.43.18:19302 relayed x", &c)
	scanLine("outbound/turnrelay[relay]: mux: worker 4 up via 193.203.43.30:19302 relayed y", &c)
	if c.Workers.Load() != 2 {
		t.Fatalf("workers=%d, want 2", c.Workers.Load())
	}
}

func TestScanHandshake(t *testing.T) {
	var c Counters
	scanLine("endpoint/wireguard[wg]: peer(x) - sending handshake initiation", &c)
	if c.HandshakeOK.Load() {
		t.Fatal("initiation must not set handshake ok")
	}
	scanLine("endpoint/wireguard[wg]: peer(x) - received handshake response", &c)
	if !c.HandshakeOK.Load() {
		t.Fatal("handshake response should set ok")
	}
}
