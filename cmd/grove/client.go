package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gandalfthegui/grove/internal/proto"
)

// rootDir returns the groved data directory.
// Precedence: GROVE_ROOT env var > ~/.grove
func rootDir() string {
	if env := os.Getenv("GROVE_ROOT"); env != "" {
		abs, err := filepath.Abs(env)
		if err == nil {
			return abs
		}
		return env
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".grove")
}

// daemonSocket returns the Unix socket path and ensures the daemon is running.
func daemonSocket() string {
	root := rootDir()
	sock := filepath.Join(root, "groved.sock")
	ensureDaemon(root, sock)
	return sock
}

// ensureDaemon starts groved in the background if the socket doesn't exist
// or is not responding to pings.  root is passed via --root so the daemon
// uses the same data directory that grove is targeting.
func ensureDaemon(root, socketPath string) {
	if pingDaemon(socketPath) {
		return
	}

	exe, _ := os.Executable()
	daemonBin := filepath.Join(filepath.Dir(exe), "groved")
	if _, err := os.Stat(daemonBin); err != nil {
		daemonBin = "groved"
	}

	cmd := exec.Command(daemonBin, "--root", root)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "grove: could not start daemon: %v\n", err)
		os.Exit(1)
	}

	// Wait up to 3 seconds for it to become ready.
	for i := 0; i < 30; i++ {
		time.Sleep(100 * time.Millisecond)
		if pingDaemon(socketPath) {
			return
		}
	}

	fmt.Fprintln(os.Stderr, "grove: daemon did not start in time")
	warnIfDockerUnavailable()
	os.Exit(1)
}

// pingDaemon returns true if the daemon is alive and responding.
func pingDaemon(socketPath string) bool {
	conn, err := net.DialTimeout("unix", socketPath, 500*time.Millisecond)
	if err != nil {
		return false
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
	if err := writeRequest(conn, proto.Request{Type: proto.ReqPing}); err != nil {
		return false
	}
	resp, err := readResponse(conn)
	return err == nil && resp.OK
}

// tryRequest sends a request to the daemon and returns the response.
// Unlike mustRequest it returns an error instead of exiting, so callers
// can tolerate a daemon that isn't running.
func tryRequest(req proto.Request) (proto.Response, error) {
	root := rootDir()
	sock := filepath.Join(root, "groved.sock")
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return proto.Response{}, err
	}
	defer conn.Close()

	if err := writeRequest(conn, req); err != nil {
		return proto.Response{}, err
	}
	resp, err := readResponse(conn)
	if err != nil {
		return proto.Response{}, err
	}
	if !resp.OK {
		return resp, fmt.Errorf("%s", resp.Error)
	}
	return resp, nil
}

// mustRequest sends a request to the daemon and returns the response, exiting
// on any error.
func mustRequest(req proto.Request) proto.Response {
	socketPath := daemonSocket()
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "grove: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	if err := writeRequest(conn, req); err != nil {
		fmt.Fprintf(os.Stderr, "grove: %v\n", err)
		os.Exit(1)
	}

	resp, err := readResponse(conn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "grove: %v\n", err)
		os.Exit(1)
	}
	if !resp.OK {
		fmt.Fprintf(os.Stderr, "grove: %s\n", resp.Error)
		os.Exit(1)
	}
	return resp
}

// streamCommand sends a request to the daemon and streams its output to
// stdout until the connection closes. Used by cmdFinish and cmdCheck.
func streamCommand(reqType string, instanceID string) {
	socketPath := daemonSocket()
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "grove: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	if err := writeRequest(conn, proto.Request{Type: reqType, InstanceID: instanceID}); err != nil {
		fmt.Fprintf(os.Stderr, "grove: %v\n", err)
		os.Exit(1)
	}

	resp, err := readResponse(conn)
	if err != nil || !resp.OK {
		msg := resp.Error
		if msg == "" && err != nil {
			msg = err.Error()
		}
		fmt.Fprintf(os.Stderr, "grove: %s\n", msg)
		os.Exit(1)
	}

	io.Copy(os.Stdout, conn)
}

// findInstance looks up a single instance by ID from a live daemon list.
// Returns nil and prints an error if the instance is not found.
func findInstance(instanceID string) *proto.InstanceInfo {
	resp := mustRequest(proto.Request{Type: proto.ReqList})
	for i := range resp.Instances {
		if resp.Instances[i].ID == instanceID {
			return &resp.Instances[i]
		}
	}
	return nil
}

func writeRequest(conn net.Conn, req proto.Request) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = conn.Write(data)
	return err
}

func readResponse(conn net.Conn) (proto.Response, error) {
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return proto.Response{}, err
		}
		return proto.Response{}, io.EOF
	}
	var resp proto.Response
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		return proto.Response{}, fmt.Errorf("bad response: %w", err)
	}
	return resp, nil
}

// warnIfDockerUnavailable prints a human-readable error to stderr when Docker
// is not running or not installed.
func warnIfDockerUnavailable() {
	cmd := exec.Command("docker", "info")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if cmd.Run() != nil {
		fmt.Fprintf(os.Stderr, "%sgrove requires Docker.%s Docker does not appear to be running.\n", colorRed+colorBold, colorReset)
		fmt.Fprintf(os.Stderr, "  Start Docker Desktop or install it: https://docs.docker.com/get-docker/\n")
	}
}
