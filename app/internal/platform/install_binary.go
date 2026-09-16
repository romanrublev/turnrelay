package platform

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// installBinary copies src to the root-owned InstalledBinaryPath and returns
// that path. The service unit must point at the copy, never at the binary the
// admin ran (which typically lives in a user-writable directory such as a home
// checkout or a Homebrew prefix): a root daemon whose program file any
// unprivileged user can overwrite is a local root escalation. The destination
// directory and file are forced to root ownership and 0755, and the result is
// verified before use.
func installBinary(src string) (string, error) {
	dst := InstalledBinaryPath()
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.Chown(dir, 0, 0); err != nil {
		return "", fmt.Errorf("chown %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		return "", err
	}
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()

	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return "", err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Chown(tmp, 0, 0); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("chown %s: %w", tmp, err)
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := verifyRootOwned(dst); err != nil {
		return "", err
	}
	return dst, nil
}

// verifyRootOwned returns an error unless path is owned by root and not writable
// by group or other. It is the invariant the installed daemon binary must hold.
func verifyRootOwned(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot read ownership of %s", path)
	}
	if st.Uid != 0 {
		return fmt.Errorf("%s is not owned by root (uid %d); refusing to install a root daemon from a non-root-owned file", path, st.Uid)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is group/other-writable (%#o); refusing", path, fi.Mode().Perm())
	}
	return nil
}
