package daemon

import (
	"encoding/json"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gandalfthegui/grove/internal/proto"
)

// loadPersistedInstances reads instance JSON files written by previous daemon
// runs and re-registers them with the correct state.  Instances that were
// RUNNING/WAITING/ATTACHED when the daemon was killed are marked as CRASHED.
// EXITED, CRASHED, and FINISHED states are preserved as-is.
func (d *Daemon) loadPersistedInstances() error {
	instancesDir := filepath.Join(d.rootDir, "instances")
	entries, err := os.ReadDir(instancesDir)
	if err != nil {
		return err
	}

	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(instancesDir, e.Name()))
		if err != nil {
			continue
		}
		var info proto.InstanceInfo
		if err := json.Unmarshal(data, &info); err != nil {
			continue
		}

		// Determine the correct state on reload.
		state := info.State
		endedAt := time.Time{}
		if info.EndedAt > 0 {
			endedAt = time.Unix(info.EndedAt, 0)
		}

		// If the daemon was killed mid-run, the process is gone → CRASHED.
		if state == proto.StateRunning || state == proto.StateWaiting || state == proto.StateAttached {
			state = proto.StateCrashed
			endedAt = time.Now()
		}

		inst := &Instance{
			ID:             info.ID,
			Project:        info.Project,
			Branch:         info.Branch,
			WorktreeDir:    info.WorktreeDir,
			CreatedAt:      time.Unix(info.CreatedAt, 0),
			LogFile:        filepath.Join(d.rootDir, "logs", info.ID+".log"),
			state:          state,
			endedAt:        endedAt,
			InstancesDir:   instancesDir,
			ContainerID:    info.ContainerID,
			ComposeProject: info.ComposeProject,
		}
		d.instances[info.ID] = inst

		// Persist the corrected state if it changed (e.g., RUNNING → CRASHED).
		if state != info.State {
			inst.persistMeta(instancesDir)
		}
	}

	return nil
}

// logAgentCredentials logs which credential keys are present in agentEnv so
// auth problems can be diagnosed from the daemon log without exposing values.
func logAgentCredentials(instanceID string, agentEnv map[string]string) {
	var found []string
	for _, k := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		if agentEnv[k] != "" {
			found = append(found, k)
		}
	}
	if len(found) > 0 {
		log.Printf("instance %s: claude credentials present: %s", instanceID, strings.Join(found, ", "))
	} else {
		log.Printf("instance %s: WARNING no claude credentials found — agent will show login screen", instanceID)
	}
}

// ─── resilientWriter ──────────────────────────────────────────────────────────

// resilientWriter fans output to a log file (always) and a network connection
// (best-effort).  If the connection breaks, writes continue to the log and the
// caller (exec.Command) never sees an error, so the child process keeps running
// even if the client disconnects.
type resilientWriter struct {
	mu     sync.Mutex
	conn   net.Conn
	log    *os.File
	connOK bool
}

func newResilientWriter(conn net.Conn, log *os.File) *resilientWriter {
	return &resilientWriter{conn: conn, log: log, connOK: true}
}

func (rw *resilientWriter) Write(p []byte) (int, error) {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	if rw.connOK {
		if _, err := rw.conn.Write(p); err != nil {
			rw.connOK = false
		}
	}
	if rw.log != nil {
		rw.log.Write(p) // best-effort; ignore log errors
	}
	return len(p), nil // always succeed so child processes never get SIGPIPE
}
