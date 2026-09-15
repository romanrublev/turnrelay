package proto

import (
	"bufio"
	"bytes"
	"testing"

	"github.com/romanrublev/turnrelay/app/internal/profile"
)

func TestRequestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	p := profile.Defaults()
	p.Link = "L"
	if err := WriteMessage(&buf, Request{Cmd: CmdUp, Profile: &p}); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRequest(bufio.NewReader(&buf))
	if err != nil {
		t.Fatal(err)
	}
	if got.Cmd != CmdUp || got.Profile == nil || got.Profile.Link != "L" {
		t.Fatalf("bad: %+v", got)
	}
}

func TestResponseRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteMessage(&buf, Response{OK: true, Status: &Status{Running: true, Workers: 18, Egress: "1.2.3.4"}}); err != nil {
		t.Fatal(err)
	}
	got, err := ReadResponse(bufio.NewReader(&buf))
	if err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.Status == nil || got.Status.Workers != 18 {
		t.Fatalf("bad: %+v", got)
	}
}
