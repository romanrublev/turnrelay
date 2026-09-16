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

func TestScanHealthGauge(t *testing.T) {
	var c Counters
	// A gauge line as the mux supervisor emits it, with a sing-box log prefix.
	scanLine("outbound/turnrelay[relay]: mux: health active=59 evictions=3 max_loss_bp=1850 mean_rtt_ms=142", &c)
	if !c.GaugeSeen.Load() {
		t.Fatal("GaugeSeen not set after a gauge line")
	}
	if c.ActiveWorkers.Load() != 59 {
		t.Fatalf("active=%d, want 59", c.ActiveWorkers.Load())
	}
	if c.Evictions.Load() != 3 {
		t.Fatalf("evictions=%d, want 3", c.Evictions.Load())
	}
	if c.MaxLossBp.Load() != 1850 { // 18.5%
		t.Fatalf("max_loss_bp=%d, want 1850", c.MaxLossBp.Load())
	}
	if c.MeanRTTMs.Load() != 142 {
		t.Fatalf("mean_rtt_ms=%d, want 142", c.MeanRTTMs.Load())
	}
	// A later gauge replaces the values (gauge, not delta).
	scanLine("outbound/turnrelay[relay]: mux: health active=60 evictions=3 max_loss_bp=40 mean_rtt_ms=95", &c)
	if c.MaxLossBp.Load() != 40 {
		t.Fatalf("gauge not replaced: max_loss_bp=%d, want 40", c.MaxLossBp.Load())
	}
}
