package obfs_test

import (
	"testing"

	"github.com/romanrublev/turnrelay/obfs"
	"github.com/romanrublev/turnrelay/obfs/obfstest"
)

func TestSRTPRoundTrip(t *testing.T) {
	roundTrip(t, obfs.ModeSRTP, obfs.Options{}, obfstest.ListenSRTP(t))
}
