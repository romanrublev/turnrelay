package obfs

// WDTT-WRAP-v1 envelope, wire compatible with amurcanov/proxy-turn-vk-android
// (android-client/obfs.go, GPL-3.0). Each datagram becomes one RTP-looking
// packet: 12-byte header, ChaCha20-Poly1305 ciphertext, random padding and a
// trailing padding-length byte (RTP P bit set). The nonce is derived from
// the header, so the receiver needs only the key.

import (
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

const (
	WrapKeyLen     = 32
	wrapHdrLen     = 12
	wrapTSStep     = 960 // 20 ms of 48 kHz Opus
	wrapPTAudio    = 111
	wrapPTVideo    = 96
	wrapPadAudio   = 24
	wrapPadVideo   = 60
	wrapMinWireLen = wrapHdrLen + chacha20poly1305.Overhead + 1
)

func DeriveWrapKey(password string) ([]byte, error) {
	if password == "" {
		return nil, errors.New("obfs: empty wrap password")
	}
	key := make([]byte, WrapKeyLen)
	r := hkdf.New(sha256.New, []byte(password), []byte("WDTT-WRAP-v1"), []byte("rtp-obfs/chacha20poly1305"))
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, fmt.Errorf("obfs: derive wrap key: %w", err)
	}
	return key, nil
}

type WrapCodec struct {
	aead   cipher.AEAD
	ssrc   uint32
	pt     uint8
	padMax int

	mu      sync.Mutex
	initSeq uint16
	initTS  uint32
	count   uint64
}

func NewWrapCodec(key []byte, video bool) (*WrapCodec, error) {
	if len(key) != WrapKeyLen {
		return nil, fmt.Errorf("obfs: wrap key must be %d bytes, got %d", WrapKeyLen, len(key))
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	var seed [10]byte
	if _, err := rand.Read(seed[:]); err != nil {
		return nil, err
	}
	c := &WrapCodec{
		aead:    aead,
		ssrc:    binary.BigEndian.Uint32(seed[0:4]),
		pt:      wrapPTAudio,
		padMax:  wrapPadAudio,
		initSeq: binary.BigEndian.Uint16(seed[4:6]),
		initTS:  binary.BigEndian.Uint32(seed[6:10]),
	}
	if video {
		c.pt, c.padMax = wrapPTVideo, wrapPadVideo
	}
	return c, nil
}

func wrapNonce(ssrc uint32, seq uint16, ts uint32) [12]byte {
	var n [12]byte
	binary.BigEndian.PutUint32(n[0:4], ssrc)
	binary.BigEndian.PutUint16(n[4:6], seq)
	binary.BigEndian.PutUint32(n[8:12], ts)
	return n
}

func (c *WrapCodec) Wrap(dst, payload []byte) ([]byte, error) {
	if len(payload) == 0 {
		return nil, errors.New("obfs: empty payload")
	}
	c.mu.Lock()
	n := c.count
	c.count++
	c.mu.Unlock()
	seq := c.initSeq + uint16(n)
	ts := c.initTS + uint32(n)*wrapTSStep + uint32(n>>16)

	var rnd [1]byte
	_, _ = rand.Read(rnd[:])
	padRand := int(rnd[0]) % c.padMax
	padTotal := padRand + 1

	total := wrapHdrLen + len(payload) + chacha20poly1305.Overhead + padTotal
	out := append(dst[:0], make([]byte, total)...)
	out[0] = 0x80 | 0x20
	out[1] = c.pt & 0x7f
	binary.BigEndian.PutUint16(out[2:4], seq)
	binary.BigEndian.PutUint32(out[4:8], ts)
	binary.BigEndian.PutUint32(out[8:12], c.ssrc)
	nonce := wrapNonce(c.ssrc, seq, ts)
	sealed := c.aead.Seal(out[wrapHdrLen:wrapHdrLen], nonce[:], payload, out[:wrapHdrLen])
	padStart := wrapHdrLen + len(sealed)
	if padRand > 0 {
		_, _ = rand.Read(out[padStart : padStart+padRand])
	}
	out[total-1] = byte(padTotal)
	return out, nil
}

func (c *WrapCodec) Unwrap(dst, wire []byte) ([]byte, error) {
	if len(wire) < wrapMinWireLen {
		return nil, errors.New("obfs: wrap packet too short")
	}
	if wire[0]>>6 != 2 {
		return nil, errors.New("obfs: not RTP v2")
	}
	end := len(wire)
	if wire[0]&0x20 != 0 {
		pad := int(wire[end-1])
		if pad == 0 || pad > end-wrapHdrLen {
			return nil, fmt.Errorf("obfs: invalid padding %d", pad)
		}
		end -= pad
	}
	if end-wrapHdrLen <= chacha20poly1305.Overhead {
		return nil, errors.New("obfs: no ciphertext")
	}
	seq := binary.BigEndian.Uint16(wire[2:4])
	ts := binary.BigEndian.Uint32(wire[4:8])
	ssrc := binary.BigEndian.Uint32(wire[8:12])
	nonce := wrapNonce(ssrc, seq, ts)
	plain, err := c.aead.Open(dst[:0], nonce[:], wire[wrapHdrLen:end], wire[:wrapHdrLen])
	if err != nil {
		return nil, fmt.Errorf("obfs: wrap auth: %w", err)
	}
	return plain, nil
}

func IsWrapRTP(wire []byte) bool {
	if len(wire) < wrapMinWireLen || wire[0]>>6 != 2 {
		return false
	}
	pt := wire[1] & 0x7f
	return pt == wrapPTAudio || pt == wrapPTVideo
}
