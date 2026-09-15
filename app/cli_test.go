package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestStatusWhenDaemonAbsent(t *testing.T) {
	var out, errb bytes.Buffer
	// no daemon listening -> non-zero and a readable error
	if code := cmdStatus(nil, &out, &errb); code == 0 {
		t.Fatal("want non-zero when daemon is not running")
	}
	if !strings.Contains(strings.ToLower(errb.String()), "daemon") {
		t.Fatalf("stderr should mention daemon: %q", errb.String())
	}
}

// TestListenControlSocketOwnership proves that listenControlSocket chowns
// the socket to the owner uid (fixing the bug where the socket was left
// root:wheel and the human owner got EACCES on connect) and keeps the 0o660
// permission bits. Chowning to our own uid needs no root, so this runs
// unprivileged.
func TestListenControlSocketOwnership(t *testing.T) {
	// Use a short-prefixed temp dir rather than t.TempDir(): unix socket
	// paths are capped at ~104 bytes on macOS (sun_path), and t.TempDir()
	// embeds the (long) test name in the path.
	dir, err := os.MkdirTemp("", "ctl")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "c.sock")
	owner := uint32(os.Getuid())

	ln, err := listenControlSocket(path, owner)
	if err != nil {
		t.Fatalf("listenControlSocket: %v", err)
	}
	defer ln.Close()

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	sys, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("unexpected Sys() type: %T", fi.Sys())
	}
	if sys.Uid != owner {
		t.Fatalf("socket owned by uid %d, want %d", sys.Uid, owner)
	}
	if fi.Mode().Perm()&0o660 != 0o660 {
		t.Fatalf("socket mode %v missing 0o660 bits", fi.Mode().Perm())
	}
}
