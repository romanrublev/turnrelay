package exit

import (
	"bytes"
	"testing"
)

func distinctPayloads(n int) [][]byte {
	p := make([][]byte, n)
	for i := range p {
		// varying lengths, unique content
		size := 8 + i*3
		p[i] = make([]byte, size)
		for j := range p[i] {
			p[i][j] = byte(i*7 + j + 1)
		}
	}
	return p
}

func hasPayload(got [][]byte, want []byte) bool {
	for _, g := range got {
		if bytes.Equal(g, want) {
			return true
		}
	}
	return false
}

func TestBlockFECRecoversLostData(t *testing.T) {
	codec := newFECCodec()
	payloads := distinctPayloads(10)
	frames, err := encodeBlock(codec, 42, payloads, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 13 {
		t.Fatalf("got %d frames, want 13", len(frames))
	}
	// Drop 3 data shards (idx 1,5,8 -> the first three frames after those) by
	// skipping them; feed the rest (7 data + 3 parity = 10 >= nData).
	drop := map[int]bool{1: true, 5: true, 8: true}
	r := newBlockReassembler(codec, 16)
	var got [][]byte
	for i, f := range frames {
		if i < 10 && drop[i] {
			continue // simulate a lost data shard
		}
		got = append(got, r.decode(f)...)
	}
	for i, want := range payloads {
		if !hasPayload(got, want) {
			t.Fatalf("payload %d (len %d) not recovered", i, len(want))
		}
	}
}

func TestBlockFECDeliversWithoutLoss(t *testing.T) {
	codec := newFECCodec()
	payloads := distinctPayloads(6)
	frames, _ := encodeBlock(codec, 1, payloads, 2)
	r := newBlockReassembler(codec, 16)
	var got [][]byte
	for _, f := range frames {
		got = append(got, r.decode(f)...)
	}
	// Every data shard delivered on arrival; each payload exactly once.
	if len(got) != len(payloads) {
		t.Fatalf("delivered %d payloads, want %d (no dupes)", len(got), len(payloads))
	}
	for i, want := range payloads {
		if !hasPayload(got, want) {
			t.Fatalf("payload %d missing", i)
		}
	}
}

func TestBlockFECZeroParityNoRecovery(t *testing.T) {
	codec := newFECCodec()
	payloads := distinctPayloads(5)
	frames, err := encodeBlock(codec, 7, payloads, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 5 {
		t.Fatalf("parity 0: got %d frames, want 5", len(frames))
	}
	r := newBlockReassembler(codec, 16)
	var got [][]byte
	for i, f := range frames {
		if i == 2 {
			continue // a lost data shard, unrecoverable with no parity
		}
		got = append(got, r.decode(f)...)
	}
	if hasPayload(got, payloads[2]) {
		t.Fatal("payload 2 should be unrecoverable with zero parity")
	}
	if len(got) != 4 {
		t.Fatalf("delivered %d, want 4", len(got))
	}
}
