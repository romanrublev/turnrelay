package exit

import (
	"sync"

	"github.com/klauspost/reedsolomon"
)

// fecCodec is a systematic Reed-Solomon codec whose parity count can vary per
// block, which is what makes adaptive FEC possible without tearing down the KCP
// session: the pipe groups data datagrams into a block and appends parity
// shards, and the parity count is chosen per block from the current loss tier.
// reedsolomon.New fixes (data, parity) at construction, so encoders are cached
// per (data, parity) pair and reused. All shards in a block must be equal
// length (the pipe pads to the block's max and records original lengths).
type fecCodec struct {
	mu  sync.Mutex
	enc map[[2]int]reedsolomon.Encoder
}

func newFECCodec() *fecCodec {
	return &fecCodec{enc: map[[2]int]reedsolomon.Encoder{}}
}

func (c *fecCodec) encoder(data, parity int) (reedsolomon.Encoder, error) {
	key := [2]int{data, parity}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.enc[key]; ok {
		return e, nil
	}
	e, err := reedsolomon.New(data, parity)
	if err != nil {
		return nil, err
	}
	c.enc[key] = e
	return e, nil
}

// parityFor returns parity shards for the given equal-length data shards. A
// parity of 0 means no FEC for this block (the clean tier), so it returns nil.
func (c *fecCodec) parityFor(data [][]byte, parity int) ([][]byte, error) {
	if parity == 0 {
		return nil, nil
	}
	e, err := c.encoder(len(data), parity)
	if err != nil {
		return nil, err
	}
	shardLen := len(data[0])
	shards := make([][]byte, len(data)+parity)
	copy(shards, data)
	for i := len(data); i < len(shards); i++ {
		shards[i] = make([]byte, shardLen)
	}
	if err := e.Encode(shards); err != nil {
		return nil, err
	}
	return shards[len(data):], nil
}

// recoverData reconstructs the missing data shards of a block in place. shards
// has dataCount+parityCount entries in order, with nil for any shard that did
// not arrive; on success the first dataCount entries are all present. It fails
// if fewer than dataCount shards survived.
func (c *fecCodec) recoverData(shards [][]byte, dataCount, parityCount int) error {
	e, err := c.encoder(dataCount, parityCount)
	if err != nil {
		return err
	}
	return e.ReconstructData(shards)
}
