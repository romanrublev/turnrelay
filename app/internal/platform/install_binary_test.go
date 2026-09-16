package platform

import (
	"os"
	"path/filepath"
	"testing"
)

// A root daemon must never be installed from a file an unprivileged user owns
// or can write. verifyRootOwned enforces that; here we confirm it rejects a
// user-owned file (the positive, root-owned case needs root and is exercised in
// a real install).
func TestVerifyRootOwnedRejectsUserFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: a file we create would be root-owned")
	}
	f := filepath.Join(t.TempDir(), "turnrelayd")
	if err := os.WriteFile(f, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := verifyRootOwned(f); err == nil {
		t.Fatal("verifyRootOwned accepted a non-root-owned file")
	}
}

func TestVerifyRootOwnedRejectsWorldWritable(t *testing.T) {
	f := filepath.Join(t.TempDir(), "turnrelayd")
	if err := os.WriteFile(f, []byte("binary"), 0o757); err != nil {
		t.Fatal(err)
	}
	if err := verifyRootOwned(f); err == nil {
		t.Fatal("verifyRootOwned accepted a world-writable file")
	}
}
