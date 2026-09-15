package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestStatusWhenDaemonAbsent(t *testing.T) {
	var out, errb bytes.Buffer
	// no daemon listening -> non-zero and a readable error
	if code := cmdStatus(&out, &errb); code == 0 {
		t.Fatal("want non-zero when daemon is not running")
	}
	if !strings.Contains(strings.ToLower(errb.String()), "daemon") {
		t.Fatalf("stderr should mention daemon: %q", errb.String())
	}
}
