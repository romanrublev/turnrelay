package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// Status mirrors the JSON printed by `turnrelay status --json`. The tray keeps
// its own copy so it stays a separate module with no dependency on the daemon
// package.
type Status struct {
	Running     bool   `json:"running"`
	Workers     int    `json:"workers"`
	Egress      string `json:"egress"`
	HandshakeOK bool   `json:"handshake_ok"`
	Error       string `json:"error"`
}

// binaryPath is the turnrelay CLI the tray drives: $TURNRELAY_BIN if set,
// otherwise "turnrelay" resolved on PATH.
func binaryPath() string {
	if p := os.Getenv("TURNRELAY_BIN"); p != "" {
		return p
	}
	return "turnrelay"
}

func runCLI(timeout time.Duration, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return exec.CommandContext(ctx, binaryPath(), args...).Run()
}

func up() error   { return runCLI(25*time.Second, "up") }
func down() error { return runCLI(20*time.Second, "down") }

// fetchStatus runs `turnrelay status --json`. A non-zero exit (the daemon is
// unreachable) returns a zero Status and the error.
func fetchStatus() (Status, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binaryPath(), "status", "--json").Output()
	if err != nil {
		return Status{}, err
	}
	var s Status
	if err := json.Unmarshal(out, &s); err != nil {
		return Status{}, err
	}
	return s, nil
}

// profilePath mirrors the CLI's profile location so the tray can open it.
func profilePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "turnrelay", "profile.json")
}

// openInEditor opens a path with the platform's default handler.
func openInEditor(path string) {
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	_ = exec.Command(opener, path).Start()
}
