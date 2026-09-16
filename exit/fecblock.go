package exit

import (
	"encoding/binary"
)

// Block FEC on the KCP side of the pipe. The sender groups data datagrams into
// a block, sends each immediately (no added latency for data), then appends
// parity datagrams for the block; the receiver delivers data as it arrives and,
// if any data datagram was lost, reconstructs it from the parity once enough
// shards of the block are in. Parity count is chosen per block from the current
// loss tier, so protection is adaptive with no KCP reconnect.
//
// Wire frame (inside one demux KCP-side datagram):
//   [blockID u32][shardIdx u8][nData u8][nParity u8][shard bytes...]
// All shards of a block are the same length. A data shard's bytes are
// [origLen u16][payload][zero pad]; keeping the length INSIDE the coded data is
// what lets a lost data shard be unpadded correctly after reconstruction.

const fecHeaderLen = 7 // blockID(4) + shardIdx(1) + nData(1) + nParity(1)

func putFECHeader(dst []byte, blockID uint32, shardIdx, nData, nParity int) {
	binary.BigEndian.PutUint32(dst[0:4], blockID)
	dst[4] = byte(shardIdx)
	dst[5] = byte(nData)
	dst[6] = byte(nParity)
}

// encodeBlock frames a block of payloads into wire datagrams (data shards first,
// then parity). shardLen is derived from the longest payload (+2 for the length
// prefix). parity may be 0 (no FEC for this block).
func encodeBlock(codec *fecCodec, blockID uint32, payloads [][]byte, parity int) ([][]byte, error) {
	nData := len(payloads)
	shardLen := 0
	for _, p := range payloads {
		if len(p)+2 > shardLen {
			shardLen = len(p) + 2
		}
	}
	// data shards: [origLen u16][payload][pad]
	data := make([][]byte, nData)
	for i, p := range payloads {
		s := make([]byte, shardLen)
		binary.BigEndian.PutUint16(s[0:2], uint16(len(p)))
		copy(s[2:], p)
		data[i] = s
	}
	parityShards, err := codec.parityFor(data, parity)
	if err != nil {
		return nil, err
	}
	out := make([][]byte, 0, nData+parity)
	for i := 0; i < nData; i++ {
		f := make([]byte, fecHeaderLen+shardLen)
		putFECHeader(f, blockID, i, nData, parity)
		copy(f[fecHeaderLen:], data[i])
		out = append(out, f)
	}
	for i := 0; i < parity; i++ {
		f := make([]byte, fecHeaderLen+shardLen)
		putFECHeader(f, blockID, nData+i, nData, parity)
		copy(f[fecHeaderLen:], parityShards[i])
		out = append(out, f)
	}
	return out, nil
}

type blockState struct {
	shards    [][]byte // nData+nParity, nil until received
	shardLen  int
	nData     int
	nParity   int
	got       int
	delivered []bool // per data shard, whether already handed up
}

// blockReassembler decodes block-FEC frames back into the original payloads,
// recovering lost data shards from parity. It keeps a bounded window of recent
// blocks so out-of-order and late frames still land.
type blockReassembler struct {
	codec  *fecCodec
	blocks map[uint32]*blockState
	order  []uint32 // insertion order for eviction
	max    int      // max concurrent blocks retained
}

func newBlockReassembler(codec *fecCodec, window int) *blockReassembler {
	if window <= 0 {
		window = 64
	}
	return &blockReassembler{codec: codec, blocks: map[uint32]*blockState{}, max: window}
}

func unpadDataShard(s []byte) []byte {
	if len(s) < 2 {
		return nil
	}
	n := int(binary.BigEndian.Uint16(s[0:2]))
	if 2+n > len(s) {
		return nil
	}
	out := make([]byte, n)
	copy(out, s[2:2+n])
	return out
}

// decode ingests one wire frame and returns any newly available payloads (a
// freshly received data shard, plus any data shards a completed block could
// reconstruct). Order across calls is not guaranteed; KCP above reorders.
func (r *blockReassembler) decode(frame []byte) [][]byte {
	if len(frame) <= fecHeaderLen {
		return nil
	}
	blockID := binary.BigEndian.Uint32(frame[0:4])
	shardIdx := int(frame[4])
	nData := int(frame[5])
	nParity := int(frame[6])
	shard := frame[fecHeaderLen:]
	total := nData + nParity
	if nData == 0 || shardIdx >= total || total > 256 {
		return nil
	}

	b := r.blocks[blockID]
	if b == nil {
		b = &blockState{
			shards:    make([][]byte, total),
			shardLen:  len(shard),
			nData:     nData,
			nParity:   nParity,
			delivered: make([]bool, nData),
		}
		r.blocks[blockID] = b
		r.order = append(r.order, blockID)
		for len(r.order) > r.max {
			delete(r.blocks, r.order[0])
			r.order = r.order[1:]
		}
	}
	if shardIdx >= len(b.shards) || b.shards[shardIdx] != nil {
		return nil // duplicate or malformed
	}
	sc := make([]byte, len(shard))
	copy(sc, shard)
	b.shards[shardIdx] = sc
	b.got++

	var out [][]byte
	// Deliver a freshly arrived data shard immediately (low latency).
	if shardIdx < b.nData && !b.delivered[shardIdx] {
		if p := unpadDataShard(sc); p != nil {
			b.delivered[shardIdx] = true
			out = append(out, p)
		}
	}
	// If the block has enough shards and some data is still missing, recover.
	if b.nParity > 0 && b.got >= b.nData && r.missingData(b) {
		if err := r.codec.recoverData(b.shards, b.nData, b.nParity); err == nil {
			for i := 0; i < b.nData; i++ {
				if !b.delivered[i] && b.shards[i] != nil {
					if p := unpadDataShard(b.shards[i]); p != nil {
						b.delivered[i] = true
						out = append(out, p)
					}
				}
			}
		}
	}
	return out
}

func (r *blockReassembler) missingData(b *blockState) bool {
	for i := 0; i < b.nData; i++ {
		if !b.delivered[i] {
			return true
		}
	}
	return false
}
