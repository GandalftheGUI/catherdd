//go:build !darwin

package main

import (
	"fmt"
	"os"
)

func cmdDaemonInstall() {
	fmt.Fprintln(os.Stderr, "grove: daemon install is macOS-only (uses LaunchAgent)")
	fmt.Fprintln(os.Stderr, "  On Linux, manage groved with systemd — see docs/TECHNICAL.md")
	os.Exit(1)
}

func cmdDaemonUninstall() {
	fmt.Fprintln(os.Stderr, "grove: daemon uninstall is macOS-only (uses LaunchAgent)")
	fmt.Fprintln(os.Stderr, "  On Linux, manage groved with systemd — see docs/TECHNICAL.md")
	os.Exit(1)
}

func cmdDaemonStatus() {
	fmt.Fprintln(os.Stderr, "grove: daemon status is macOS-only (uses LaunchAgent)")
	fmt.Fprintln(os.Stderr, "  On Linux, manage groved with systemd — see docs/TECHNICAL.md")
	os.Exit(1)
}
