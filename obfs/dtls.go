package obfs

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"time"

	"github.com/pion/dtls/v3"
	"github.com/pion/dtls/v3/pkg/crypto/selfsign"
)

// dtlsClientOptions is the cipher/extension set the whole server family
// accepts (cacggghp server: ECDHE-ECDSA-AES128-GCM, extended master secret,
// connection ids).
func dtlsClientOptions(cert tls.Certificate) []dtls.ClientOption {
	return []dtls.ClientOption{
		dtls.WithCertificates(cert),
		dtls.WithInsecureSkipVerify(true),
		dtls.WithExtendedMasterSecret(dtls.RequireExtendedMasterSecret),
		dtls.WithCipherSuites(dtls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256),
		dtls.WithConnectionIDGenerator(dtls.OnlySendCIDGenerator()),
	}
}

func dtlsHandshake(ctx context.Context, conn net.PacketConn, peer net.Addr, timeout time.Duration, extra ...dtls.ClientOption) (*dtls.Conn, error) {
	cert, err := selfsign.GenerateSelfSigned()
	if err != nil {
		return nil, fmt.Errorf("obfs: self-signed cert: %w", err)
	}
	opts := append(dtlsClientOptions(cert), extra...)
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

type dtlsWrapper struct{ timeout time.Duration }

func (w *dtlsWrapper) Client(ctx context.Context, underlay net.PacketConn, peer net.Addr) (net.Conn, error) {
	return dtlsHandshake(ctx, &peerPacketConn{underlay, peer}, peer, w.timeout)
}
