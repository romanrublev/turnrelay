package main

import (
	"fmt"
	"io"
	"os"
)

const usage = `usage: turnrelay <command>

commands:
  up          start the VPN
  down        stop the VPN
  status      show status
  daemon      run the privileged service (used by launchd)
  install     install the service (needs sudo)
  uninstall   remove the service (needs sudo)
`

func dispatch(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	// subcommands are wired in later tasks
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

func main() {
	os.Exit(dispatch(os.Args[1:], os.Stdout, os.Stderr))
}
