package exit

import (
	"bytes"
	"testing"
)

func makeShards(n, size int) [][]byte {
	s := make([][]byte, n)
	for i := range s {
		s[i] = make([]byte, size)
		for j := range s[i] {
			s[i][j] = byte(i*31 + j)
		}
	}
	return s
}

func TestFECRecoversUpToParity(t *testing.T) {
	c := newFECCodec()
	const data, parity, size = 10, 3, 64
	orig := makeShards(data, size)
	parityShards, err := c.parityFor(orig, parity)
	if err != nil {
		t.Fatal(err)
	}
	if len(parityShards) != parity {
		t.Fatalf("got %d parity shards, want %d", len(parityShards), parity)
	}
	// Assemble the full block, then drop `parity` data shards (the max
	// recoverable) and confirm they are reconstructed exactly.
	shards := make([][]byte, data+parity)
	copy(shards, orig)
	copy(shards[data:], parityShards)
	for _, drop := range []int{0, 4, 9} { // 3 data shards lost
		shards[drop] = nil
	}
	if err := c.recoverData(shards, data, parity); err != nil {
		t.Fatalf("recover: %v", err)
	}
	for i := 0; i < data; i++ {
		if !bytes.Equal(shards[i], orig[i]) {
			t.Fatalf("data shard %d not recovered", i)
		}
	}
}

func TestFECFailsBeyondParity(t *testing.T) {
	c := newFECCodec()
	const data, parity, size = 10, 2, 32
	orig := makeShards(data, size)
	parityShards, _ := c.parityFor(orig, parity)
	shards := make([][]byte, data+parity)
	copy(shards, orig)
	copy(shards[data:], parityShards)
	// Drop 3 shards with only 2 parity -> unrecoverable.
	shards[0], shards[1], shards[2] = nil, nil, nil
	if err := c.recoverData(shards, data, parity); err == nil {
		t.Fatal("expected failure when losses exceed parity")
	}
}

func TestFECZeroParity(t *testing.T) {
	c := newFECCodec()
	p, err := c.parityFor(makeShards(10, 16), 0)
	if err != nil || p != nil {
		t.Fatalf("parity 0 should yield nil, got %v err %v", p, err)
	}
}
