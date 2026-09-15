package engine

import (
	"testing"
	"time"
)

// loopbackConfig is a minimal sing-box that needs no VK or root: a mixed
// inbound on an ephemeral port and a direct outbound. It proves Start/Stop.
const loopbackConfig = `{
  "log": {"level":"error"},
  "inbounds":[{"type":"mixed","tag":"in","listen":"127.0.0.1","listen_port":0}],
  "outbounds":[{"type":"direct","tag":"direct"}],
  "route":{"final":"direct"}
}`

func TestEngineStartStop(t *testing.T) {
	e := New()
	if e.Running() {
		t.Fatal("should not be running before start")
	}
	if err := e.Start([]byte(loopbackConfig)); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !e.Running() {
		t.Fatal("should be running after start")
	}
	time.Sleep(50 * time.Millisecond)
	e.Stop()
	if e.Running() {
		t.Fatal("should be stopped after Stop")
	}
	// second Stop is a no-op
	e.Stop()
}
