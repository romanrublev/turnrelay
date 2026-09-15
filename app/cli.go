package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"strconv"

	"github.com/romanrublev/turnrelay/app/internal/control"
	"github.com/romanrublev/turnrelay/app/internal/daemon"
	"github.com/romanrublev/turnrelay/app/internal/platform"
	"github.com/romanrublev/turnrelay/app/internal/profile"
)

func cmdUp(stdout, stderr io.Writer) int {
	p, err := profile.Load(platform.ProfilePath())
	if err != nil {
		fmt.Fprintf(stderr, "load profile (%s): %v\n", platform.ProfilePath(), err)
		return 1
	}
	if err := (control.Client{Path: platform.SocketPath()}).Up(p); err != nil {
		fmt.Fprintf(stderr, "up: %v (is the daemon installed and running?)\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "connecting")
	return 0
}

func cmdDown(stdout, stderr io.Writer) int {
	if err := (control.Client{Path: platform.SocketPath()}).Down(); err != nil {
		fmt.Fprintf(stderr, "down: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "disconnected")
	return 0
}

func cmdStatus(stdout, stderr io.Writer) int {
	st, err := (control.Client{Path: platform.SocketPath()}).Status()
	if err != nil {
		fmt.Fprintf(stderr, "status: cannot reach daemon: %v\n", err)
		return 1
	}
	state := "disconnected"
	if st.Running {
		state = "connected"
	}
	fmt.Fprintf(stdout, "%s  workers=%d  egress=%s  handshake=%v\n", state, st.Workers, st.Egress, st.HandshakeOK)
	return 0
}

func cmdDaemon(stdout, stderr io.Writer) int {
	owner, err := readOwnerUID()
	if err != nil {
		fmt.Fprintf(stderr, "daemon: owner uid: %v\n", err)
		return 1
	}
	path := platform.SocketPath()
	ln, err := listenControlSocket(path, owner)
	if err != nil {
		fmt.Fprintf(stderr, "daemon: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "turnrelay daemon listening on %s\n", path)
	if err := control.Serve(ln, owner, daemon.New()); err != nil {
		fmt.Fprintf(stderr, "daemon: %v\n", err)
		return 1
	}
	return 0
}

// listenControlSocket creates the control-plane unix socket at path, then
// chowns it to owner (the human user uid recorded at install time) and
// chmods it 0o660 so that owner can connect() to a listener created by root.
// A socket the owner cannot reach is a fatal misconfiguration, so any chown
// or chmod failure fails the whole call rather than being ignored.
func listenControlSocket(path string, owner uint32) (*net.UnixListener, error) {
	_ = os.Remove(path)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", path, err)
	}
	if err := os.Chown(path, int(owner), -1); err != nil {
		ln.Close()
		return nil, fmt.Errorf("chown %s to uid %d: %w", path, owner, err)
	}
	if err := os.Chmod(path, 0o660); err != nil {
		ln.Close()
		return nil, fmt.Errorf("chmod %s: %w", path, err)
	}
	return ln, nil
}

func readOwnerUID() (uint32, error) {
	b, err := os.ReadFile(platform.OwnerUIDPath())
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseUint(string(bytesTrim(b)), 10, 32)
	return uint32(n), err
}

func bytesTrim(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	return b
}

func cmdInstall(stderr io.Writer) int {
	if os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "install: must run as root (use sudo)")
		return 1
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "install: exec path: %v\n", err)
		return 1
	}
	owner := uint32(os.Getuid())
	if s := os.Getenv("SUDO_UID"); s != "" {
		if n, err := strconv.ParseUint(s, 10, 32); err == nil {
			owner = uint32(n)
		}
	}
	if err := platform.Install(exe, owner); err != nil {
		fmt.Fprintf(stderr, "install: %v\n", err)
		return 1
	}
	fmt.Fprintln(stderr, "installed; owner uid", owner)
	return 0
}

func cmdUninstall(stderr io.Writer) int {
	if os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "uninstall: must run as root (use sudo)")
		return 1
	}
	if err := platform.Uninstall(); err != nil {
		fmt.Fprintf(stderr, "uninstall: %v\n", err)
		return 1
	}
	return 0
}
