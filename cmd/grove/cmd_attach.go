package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/gandalfthegui/grove/internal/proto"
	"golang.org/x/term"
)

func cmdAttach() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: grove attach <instance-id>")
		os.Exit(1)
	}
	doAttach(os.Args[2])
}

// doAttach connects the terminal to the instance PTY and blocks until the
// user detaches (Ctrl-]) or the agent exits.
func doAttach(instanceID string) {
	socketPath := daemonSocket()
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "grove: cannot connect to daemon: %v\n", err)
		os.Exit(1)
	}
	// Note: conn is NOT deferred-closed here; the attach loop owns its lifetime.

	if err := writeRequest(conn, proto.Request{
		Type:       proto.ReqAttach,
		InstanceID: instanceID,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "grove: %v\n", err)
		os.Exit(1)
	}

	resp, err := readResponse(conn)
	if err != nil || !resp.OK {
		msg := "attach failed"
		if err != nil {
			msg = err.Error()
		} else if resp.Error != "" {
			msg = resp.Error
		}
		fmt.Fprintf(os.Stderr, "grove: %s\n", msg)
		conn.Close()
		os.Exit(1)
	}

	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "grove: cannot set raw mode: %v\n", err)
		conn.Close()
		os.Exit(1)
	}

	// sync.Once ensures the terminal is restored exactly once whether we
	// exit via defer or via the explicit call below before cleanup output.
	var restoreOnce sync.Once
	restore := func() {
		restoreOnce.Do(func() { term.Restore(fd, oldState) })
	}
	defer restore()

	fmt.Fprintf(os.Stdout, "\r\n[grove] attached to %s  (detach: Ctrl-])\r\n", instanceID)

	done := make(chan struct{}, 1)

	// Goroutine 1: copy PTY output (server → client) to stdout.
	go func() {
		io.Copy(os.Stdout, conn)
		select {
		case done <- struct{}{}:
		default:
		}
	}()

	// Goroutine 2: read stdin, watch for Ctrl-], frame and send to server.
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				for i := 0; i < n; i++ {
					if buf[i] == 0x1D {
						proto.WriteFrame(conn, proto.AttachFrameDetach, nil)
						select {
						case done <- struct{}{}:
						default:
						}
						return
					}
				}
				proto.WriteFrame(conn, proto.AttachFrameData, buf[:n])
			}
			if err != nil {
				select {
				case done <- struct{}{}:
				default:
				}
				return
			}
		}
	}()

	// Forward terminal resize events.
	winchCh := make(chan os.Signal, 1)
	signal.Notify(winchCh, syscall.SIGWINCH)
	go func() {
		for range winchCh {
			cols, rows, err := term.GetSize(fd)
			if err == nil {
				payload := make([]byte, 4)
				binary.BigEndian.PutUint16(payload[0:2], uint16(cols))
				binary.BigEndian.PutUint16(payload[2:4], uint16(rows))
				proto.WriteFrame(conn, proto.AttachFrameResize, payload)
			}
		}
	}()

	// Send initial window size.
	if cols, rows, err := term.GetSize(fd); err == nil {
		payload := make([]byte, 4)
		binary.BigEndian.PutUint16(payload[0:2], uint16(cols))
		binary.BigEndian.PutUint16(payload[2:4], uint16(rows))
		proto.WriteFrame(conn, proto.AttachFrameResize, payload)
	}

	<-done
	signal.Stop(winchCh)
	conn.Close()

	// Restore terminal before printing the detach message so the output
	// is not in raw mode.
	restore()
	// Reset terminal modes the agent may have left on (focus reporting, bracketed paste, etc.).
	fmt.Fprint(os.Stdout, "\033[?1004l\033[?2004l")
	fmt.Fprintf(os.Stdout, "\n[grove] detached from %s\n", instanceID)
}
