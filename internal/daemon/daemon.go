// Package daemon implements the groved background daemon.
//
// The daemon listens on a Unix domain socket and handles requests from grove
// clients.  Each request is a single newline-terminated JSON object; the daemon
// writes a single newline-terminated JSON response and then closes the
// connection — except for attach requests, which enter a bidirectional
// streaming mode (see instance.go and proto/messages.go for the wire format).
package daemon

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/gandalfthegui/grove/internal/proto"
)

// Daemon is the central supervisor.  It owns a map of live instances and
// handles all IPC requests from grove.
type Daemon struct {
	rootDir string // ~/.grove  (data root: projects, instances, logs)

	mu        sync.Mutex
	instances map[string]*Instance // keyed by instance ID
}

// New creates a Daemon that uses rootDir (~/.grove) as its data directory.
// Project registrations are read from rootDir/projects/<name>/project.yaml.
// Returns an error if Docker is not available.
func New(rootDir string) (*Daemon, error) {
	if err := validateDocker(); err != nil {
		return nil, err
	}

	for _, sub := range []string{
		"projects",
		"instances",
		"logs",
	} {
		if err := os.MkdirAll(filepath.Join(rootDir, sub), 0o755); err != nil {
			return nil, err
		}
	}

	d := &Daemon{
		rootDir:   rootDir,
		instances: make(map[string]*Instance),
	}

	if err := d.loadPersistedInstances(); err != nil {
		log.Printf("warning: could not reload persisted instances: %v", err)
	}

	return d, nil
}

// Run starts the Unix socket listener and blocks until it is closed.
func (d *Daemon) Run(socketPath string) error {
	// Remove stale socket.
	os.Remove(socketPath)

	l, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", socketPath, err)
	}
	defer l.Close()

	log.Printf("groved listening on %s", socketPath)

	for {
		conn, err := l.Accept()
		if err != nil {
			// Listener was closed (shutdown).
			return nil
		}
		go d.handleConn(conn)
	}
}

// ─── Connection handling ──────────────────────────────────────────────────────

func (d *Daemon) handleConn(conn net.Conn) {
	// Non-attach requests are handled quickly; attach blocks for its duration.
	defer func() {
		// conn may already be closed by Attach(); that's fine.
		conn.Close()
	}()

	var req proto.Request
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		return
	}
	if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
		respond(conn, proto.Response{OK: false, Error: "bad request: " + err.Error()})
		return
	}

	switch req.Type {
	case proto.ReqPing:
		respond(conn, proto.Response{OK: true})

	case proto.ReqStart:
		d.handleStart(conn, req)

	case proto.ReqList:
		d.handleList(conn)

	case proto.ReqAttach:
		d.handleAttach(conn, req)

	case proto.ReqLogs:
		d.handleLogs(conn, req)

	case proto.ReqLogsFollow:
		d.handleLogsFollow(conn, req)

	case proto.ReqStop:
		d.handleStop(conn, req)

	case proto.ReqDrop:
		d.handleDrop(conn, req)

	case proto.ReqFinish:
		d.handleFinish(conn, req)

	case proto.ReqCheck:
		d.handleCheck(conn, req)

	case proto.ReqRestart:
		d.handleRestart(conn, req)

	default:
		respond(conn, proto.Response{OK: false, Error: "unknown request type: " + req.Type})
	}
}

func respond(conn net.Conn, r proto.Response) {
	data, _ := json.Marshal(r)
	data = append(data, '\n')
	conn.Write(data)
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func (d *Daemon) getInstance(id string) *Instance {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.instances[id]
}

// idAlphabet is the ordered set of characters used to build instance IDs.
// Single-character IDs are assigned first (digits 1-9, then a-z), giving 35
// slots before falling back to two-character combinations.
var idAlphabet = []string{
	"1", "2", "3", "4", "5", "6", "7", "8", "9",
	"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m",
	"n", "o", "p", "q", "r", "s", "t", "u", "v", "w", "x", "y", "z",
}

// nextInstanceID returns the lowest unused instance ID.
// Must be called with d.mu held.
func (d *Daemon) nextInstanceID() string {
	for _, id := range idAlphabet {
		if _, taken := d.instances[id]; !taken {
			return id
		}
	}
	for _, a := range idAlphabet {
		for _, b := range idAlphabet {
			id := a + b
			if _, taken := d.instances[id]; !taken {
				return id
			}
		}
	}
	// Extremely unlikely: fall back to random hex.
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}
