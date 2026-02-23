//go:build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const launchAgentLabel = "com.grove.daemon"

func launchAgentPlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist")
}

func cmdDaemonInstall() {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "grove: cannot resolve executable path: %v\n", err)
		os.Exit(1)
	}
	daemonBin := filepath.Join(filepath.Dir(exe), "groved")
	if _, err := os.Stat(daemonBin); err != nil {
		fmt.Fprintf(os.Stderr, "grove: groved binary not found at %s\n", daemonBin)
		os.Exit(1)
	}

	root := rootDir()
	logFile := filepath.Join(root, "daemon.log")
	socketPath := filepath.Join(root, "groved.sock")

	plist := buildPlist(daemonBin, root, logFile, os.Getenv("PATH"))

	plistPath := launchAgentPlistPath()
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "grove: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "grove: %v\n", err)
		os.Exit(1)
	}

	uid := fmt.Sprintf("%d", os.Getuid())
	// Unload existing instance silently (ignore errors).
	exec.Command("launchctl", "bootout", "gui/"+uid+"/"+launchAgentLabel).Run()

	// Load the new plist.
	out, err := exec.Command("launchctl", "bootstrap", "gui/"+uid, plistPath).CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "grove: launchctl bootstrap failed: %v\n%s", err, out)
		os.Exit(1)
	}

	fmt.Printf("\n%s✓  groved LaunchAgent installed%s\n\n", colorGreen+colorBold, colorReset)
	fmt.Printf("  %sPlist:%s %s%s%s\n", colorDim, colorReset, colorCyan, plistPath, colorReset)
	fmt.Printf("  %sLog:%s   %s%s%s\n\n", colorDim, colorReset, colorCyan, logFile, colorReset)

	// Verify the daemon actually started — the LaunchAgent is registered but
	// the process may have exited immediately (e.g. Docker not running).
	for i := 0; i < 20; i++ {
		time.Sleep(150 * time.Millisecond)
		if pingDaemon(socketPath) {
			fmt.Printf("%s✓  daemon is running%s\n\n", colorGreen+colorBold, colorReset)
			return
		}
	}
	fmt.Fprintf(os.Stderr, "%s✗  daemon did not start%s\n\n", colorRed+colorBold, colorReset)
	warnIfDockerUnavailable()
	fmt.Fprintf(os.Stderr, "  Check the log for details: %s%s%s\n\n", colorCyan, logFile, colorReset)
	os.Exit(1)
}

func cmdDaemonUninstall() {
	uid := fmt.Sprintf("%d", os.Getuid())
	exec.Command("launchctl", "bootout", "gui/"+uid+"/"+launchAgentLabel).Run()

	plistPath := launchAgentPlistPath()
	os.Remove(plistPath)

	fmt.Printf("\n%s✓  groved LaunchAgent removed%s\n\n", colorGreen+colorBold, colorReset)
}

func cmdDaemonStatus() {
	plistPath := launchAgentPlistPath()
	if _, err := os.Stat(plistPath); os.IsNotExist(err) {
		fmt.Printf("%snot installed%s\n", colorDim, colorReset)
		return
	}

	root := rootDir()
	sock := filepath.Join(root, "groved.sock")
	if pingDaemon(sock) {
		fmt.Printf("%s✓  running%s\n\n  %splist:%s %s%s%s\n", colorGreen+colorBold, colorReset, colorDim, colorReset, colorCyan, plistPath, colorReset)
	} else {
		fmt.Printf("%s⚠  installed but not running%s\n\n  %splist:%s %s%s%s\n", colorYellow+colorBold, colorReset, colorDim, colorReset, colorCyan, plistPath, colorReset)
	}
}

// buildPlist generates the LaunchAgent plist XML.
// envPath is embedded as EnvironmentVariables.PATH so the daemon inherits the
// user's full shell PATH (launchd provides only a minimal default PATH).
func buildPlist(daemonBin, rootDir, logFile string, envPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>--root</string>
		<string>%s</string>
	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>%s</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, xmlEscape(launchAgentLabel), xmlEscape(daemonBin), xmlEscape(rootDir),
		xmlEscape(envPath), xmlEscape(logFile), xmlEscape(logFile))
}

func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
