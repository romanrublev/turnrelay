package obfs

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/pion/dtls/v3"
	"github.com/pion/dtls/v3/pkg/crypto/selfsign"
)

// GenerateServerCert makes a fresh self-signed certificate for the exit server.
// Persist it (and reload it via tls.X509KeyPair) so its CertFingerprint is
// stable across restarts and can be pinned by clients.
func GenerateServerCert() (tls.Certificate, error) {
	return selfsign.GenerateSelfSigned()
}

// LoadOrCreateCertFile returns the server certificate stored at path, creating
// and persisting a fresh self-signed one there the first time. Persisting it
// keeps CertFingerprint stable across restarts, which is what lets clients pin
// it. The file holds the certificate and its private key as PEM and is written
// 0600 because it contains the key.
func LoadOrCreateCertFile(path string) (tls.Certificate, error) {
	if b, err := os.ReadFile(path); err == nil {
		cert, err := tls.X509KeyPair(b, b) // one bundle; each scan finds its block
		if err != nil {
			return tls.Certificate{}, fmt.Errorf("obfs: parse cert %s: %w", path, err)
		}
		return cert, nil
	} else if !os.IsNotExist(err) {
		return tls.Certificate{}, err
	}
	cert, err := GenerateServerCert()
	if err != nil {
		return tls.Certificate{}, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	var buf []byte
	buf = append(buf, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})...)
	buf = append(buf, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})...)
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		return tls.Certificate{}, err
	}
	return cert, nil
}

// CertFingerprint is the SHA-256 of a certificate's leaf DER. The exit server
// publishes this for its (stable) certificate and the client pins it, which is
// how WebRTC authenticates DTLS - so pinning keeps the call-media disguise
// intact, unlike switching to a PSK handshake.
func CertFingerprint(cert tls.Certificate) []byte {
	if len(cert.Certificate) == 0 {
		return nil
	}
	sum := sha256.Sum256(cert.Certificate[0])
	return sum[:]
}

// verifyPinnedCert checks the server's presented leaf certificate against a
// pinned SHA-256. Without it the client trusts any certificate (the TURN relay
// or any on-path party could terminate DTLS and read all proxied traffic), so
// the fingerprint is what authenticates the exit server.
func verifyPinnedCert(s *dtls.State, pin []byte) error {
	if len(s.PeerCertificates) == 0 {
		return errors.New("obfs: server presented no certificate to pin")
	}
	sum := sha256.Sum256(s.PeerCertificates[0])
	if subtle.ConstantTimeCompare(sum[:], pin) != 1 {
		return errors.New("obfs: server certificate fingerprint mismatch")
	}
	return nil
}

// dtlsClientOptions is the cipher/extension set the whole server family
// accepts (cacggghp server: ECDHE-ECDSA-AES128-GCM, extended master secret,
// connection ids). When pin is set, the server's certificate is verified
// against it; otherwise any certificate is accepted (unauthenticated).
func dtlsClientOptions(cert tls.Certificate, pin []byte) []dtls.ClientOption {
	opts := []dtls.ClientOption{
		dtls.WithCertificates(cert),
		dtls.WithInsecureSkipVerify(true),
		dtls.WithExtendedMasterSecret(dtls.RequireExtendedMasterSecret),
		dtls.WithCipherSuites(dtls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256),
		dtls.WithConnectionIDGenerator(dtls.OnlySendCIDGenerator()),
	}
	if len(pin) > 0 {
		p := append([]byte(nil), pin...)
		opts = append(opts, dtls.WithVerifyConnection(func(s *dtls.State) error {
			return verifyPinnedCert(s, p)
		}))
	}
	return opts
}

func dtlsHandshake(ctx context.Context, conn net.PacketConn, peer net.Addr, timeout time.Duration, pin []byte, extra ...dtls.ClientOption) (*dtls.Conn, error) {
	cert, err := selfsign.GenerateSelfSigned()
	if err != nil {
		return nil, fmt.Errorf("obfs: self-signed cert: %w", err)
	}
	opts := append(dtlsClientOptions(cert, pin), extra...)
	dc, err := dtls.ClientWithOptions(conn, peer, opts...)
	if err != nil {
		return nil, fmt.Errorf("obfs: dtls client: %w", err)
	}
	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := dc.HandshakeContext(hctx); err != nil {
		_ = dc.Close()
		return nil, fmt.Errorf("obfs: dtls handshake: %w", err)
	}
	return dc, nil
}

type dtlsWrapper struct {
	timeout time.Duration
	pin     []byte
}

func (w *dtlsWrapper) Client(ctx context.Context, underlay net.PacketConn, peer net.Addr) (net.Conn, error) {
	return dtlsHandshake(ctx, &peerPacketConn{underlay, peer}, peer, w.timeout, w.pin)
}
