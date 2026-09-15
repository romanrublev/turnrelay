package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestDispatchUnknownReturns2(t *testing.T) {
	var out, errb bytes.Buffer
	if code := dispatch([]string{"wat"}, &out, &errb); code != 2 {
		t.Fatalf("code=%d, want 2", code)
	}
	if !strings.Contains(errb.String(), "usage") {
		t.Fatalf("stderr missing usage: %q", errb.String())
	}
}

func TestDispatchEmptyReturns2(t *testing.T) {
	var out, errb bytes.Buffer
	if code := dispatch(nil, &out, &errb); code != 2 {
		t.Fatalf("code=%d, want 2", code)
	}
}
