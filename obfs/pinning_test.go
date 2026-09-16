package obfs_test

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"github.com/romanrublev/turnrelay/obfs"
	"github.com/romanrublev/turnrelay/obfs/obfstest"
)

// A client that pins the server's real certificate fingerprint completes the
// handshake and exchanges datagrams.
func TestSRTPPinnedFingerprintRoundTrip(t *testing.T) {
	cert, err := obfs.GenerateServerCert()
	if err != nil {
		t.Fatal(err)
	}
	fp := obfs.CertFingerprint(cert)
	if len(fp) != 32 {
		t.Fatalf("fingerprint len=%d, want 32", len(fp))
	}
	roundTrip(t, obfs.ModeSRTP, obfs.Options{ServerFingerprint: fp}, obfstest.ListenSRTPCert(t, &cert))
}

// A client that pins the WRONG fingerprint must refuse the handshake: this is
// the MITM defence, so a relay presenting its own certificate is rejected.
func TestSRTPWrongFingerprintRejected(t *testing.T) {
	cert, err := obfs.GenerateServerCert()
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := obfs.GenerateServerCert()
	if err != nil {
		t.Fatal(err)
	}
	srv := obfstest.ListenSRTPCert(t, &cert)
	w, err := obfs.New(obfs.ModeSRTP, obfs.Options{
		ServerFingerprint: obfs.CertFingerprint(wrong),
		HandshakeTimeout:  3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	underlay, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer underlay.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	go func() { _, _ = srv.Accept(ctx) }() // server attempts its side; will fail too

	if _, err := w.Client(ctx, underlay, srv.Addr()); err == nil {
		t.Fatal("client accepted a server whose certificate did not match the pinned fingerprint")
	}
}

func TestLoadOrCreateCertFileStableFingerprint(t *testing.T) {
	path := t.TempDir() + "/cert.pem"
	c1, err := obfs.LoadOrCreateCertFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Reload: same file, same fingerprint (this is what makes pinning work
	// across server restarts).
	c2, err := obfs.LoadOrCreateCertFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fp1, fp2 := obfs.CertFingerprint(c1), obfs.CertFingerprint(c2)
	if len(fp1) != 32 || string(fp1) != string(fp2) {
		t.Fatalf("fingerprint not stable across reload: %x vs %x", fp1, fp2)
	}
	if fi, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm() != 0o600 {
		t.Fatalf("cert file perms = %o, want 600 (holds the private key)", fi.Mode().Perm())
	}
}
