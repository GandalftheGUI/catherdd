package daemon

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gandalfthegui/grove/internal/envfile"
	"github.com/gandalfthegui/grove/internal/proto"
)

func (d *Daemon) handleStart(conn net.Conn, req proto.Request) {
	if req.Project == "" {
		respond(conn, proto.Response{OK: false, Error: "project name required"})
		return
	}
	if req.Branch == "" {
		respond(conn, proto.Response{OK: false, Error: "branch name required"})
		return
	}

	p, err := loadProject(d.rootDir, req.Project)
	if err != nil {
		respond(conn, proto.Response{OK: false, Error: err.Error()})
		return
	}

	// Allocate instance ID early so the log file can be named after it.
	d.mu.Lock()
	instanceID := d.nextInstanceID()
	d.mu.Unlock()
	startedAt := time.Now()

	logFile := filepath.Join(d.rootDir, "logs", instanceID+".log")
	logFd, _ := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if logFd != nil {
		defer logFd.Close()
	}

	// setupW captures all clone/pull/bootstrap output in memory and also
	// writes it to the log file so it's preserved after the connection closes.
	var outputBuf bytes.Buffer
	var setupW io.Writer = &outputBuf
	if logFd != nil {
		setupW = io.MultiWriter(&outputBuf, logFd)
	}
	log.Printf("start requested: project=%s branch=%s instance=%s repo=%q main_dir=%s", req.Project, req.Branch, instanceID, p.Repo, p.MainDir())

	// Deferred rollback: if setup fails at any point after resources are
	// allocated, the accumulated cleanup functions run in reverse order.
	var setupErr error
	var rollbacks []func()
	defer func() {
		if setupErr != nil {
			for i := len(rollbacks) - 1; i >= 0; i-- {
				rollbacks[i]()
			}
		}
	}()

	// Ensure the canonical checkout exists (clone if needed).
	if err := ensureMainCheckout(p, setupW); err != nil {
		setupErr = err
		log.Printf("start failed: stage=clone project=%s branch=%s instance=%s repo=%q elapsed=%s err=%v%s",
			req.Project, req.Branch, instanceID, p.Repo, time.Since(startedAt).Round(time.Millisecond), err, repoURLHintSuffix(p.Repo))
		respond(conn, proto.Response{OK: false, Error: err.Error()})
		return
	}

	// Pull latest changes so the new worktree branches from current remote HEAD.
	// Non-fatal: log the warning and continue so offline use still works.
	if err := pullMain(p, setupW); err != nil {
		log.Printf("warning: git pull failed for %s: %v", req.Project, err)
	}

	// Overlay grove.yaml from the repo root if it exists.
	inRepoFound, err := loadInRepoConfig(p)
	if err != nil {
		log.Printf("warning: could not read grove.yaml for %s: %v", req.Project, err)
	}

	// If there is no grove.yaml the project is not configured enough to start.
	// Tell the client so it can prompt the user to create one.
	if !inRepoFound {
		setupErr = fmt.Errorf("no grove.yaml")
		respond(conn, proto.Response{
			OK:       false,
			Error:    "no grove.yaml found in " + req.Project,
			InitPath: p.MainDir(),
		})
		return
	}

	// Create the git worktree on the user-specified branch.
	worktreeDir, err := createWorktree(p, instanceID, req.Branch, setupW)
	if err != nil {
		setupErr = err
		log.Printf("start failed: stage=worktree project=%s branch=%s instance=%s main_dir=%s elapsed=%s err=%v",
			req.Project, req.Branch, instanceID, p.MainDir(), time.Since(startedAt).Round(time.Millisecond), err)
		respond(conn, proto.Response{OK: false, Error: err.Error()})
		return
	}
	rollbacks = append(rollbacks, func() { removeWorktree(p, instanceID, req.Branch) })

	// Start the container with the worktree bind-mounted inside it.
	containerName, err := startContainer(p, instanceID, worktreeDir, setupW)
	if err != nil {
		setupErr = err
		log.Printf("start failed: stage=container project=%s branch=%s instance=%s worktree=%s elapsed=%s err=%v",
			req.Project, req.Branch, instanceID, worktreeDir, time.Since(startedAt).Round(time.Millisecond), err)
		respond(conn, proto.Response{OK: false, Error: err.Error()})
		return
	}
	composeProject := ""
	if p.Container.Compose != "" {
		composeProject = "grove-" + instanceID
	}
	rollbacks = append(rollbacks, func() { stopContainer(containerName, composeProject) })

	// Copy host's ~/.claude.json into the container so Claude starts with
	// existing preferences/auth. This is a copy, not a bind mount, to avoid
	// file corruption from concurrent writes by host and container Claude.
	if p.Agent.Command == "claude" || p.Agent.Command == "" {
		seedClaudeConfig(containerName)
	}

	// Run start commands inside the container.
	if err := runStart(p, containerName, setupW); err != nil {
		setupErr = err
		log.Printf("start failed: stage=start project=%s branch=%s instance=%s worktree=%s elapsed=%s err=%v",
			req.Project, req.Branch, instanceID, worktreeDir, time.Since(startedAt).Round(time.Millisecond), err)
		respond(conn, proto.Response{OK: false, Error: err.Error()})
		return
	}

	// Ensure the agent binary is available inside the container.
	agentCmd := p.Agent.Command
	if agentCmd == "" {
		agentCmd = "sh"
	}
	if err := ensureAgentInstalled(agentCmd, containerName, setupW); err != nil {
		setupErr = err
		log.Printf("start failed: stage=agent-install project=%s branch=%s instance=%s worktree=%s elapsed=%s err=%v",
			req.Project, req.Branch, instanceID, worktreeDir, time.Since(startedAt).Round(time.Millisecond), err)
		respond(conn, proto.Response{OK: false, Error: err.Error()})
		return
	}

	inst := &Instance{
		ID:             instanceID,
		Project:        req.Project,
		Branch:         req.Branch,
		WorktreeDir:    worktreeDir,
		CreatedAt:      time.Now(),
		LogFile:        logFile,
		state:          proto.StateRunning,
		InstancesDir:   filepath.Join(d.rootDir, "instances"),
		ContainerID:    containerName,
		ComposeProject: composeProject,
	}

	// Build the agent environment: env file is the base, request-level
	// values (from the CLI prompt or host env) override.
	agentEnv := envfile.Load(filepath.Join(d.rootDir, "env"))
	for k, v := range req.AgentEnv {
		agentEnv[k] = v
	}
	logAgentCredentials(instanceID, agentEnv)

	if err := inst.startAgent(agentCmd, p.Agent.Args, agentEnv); err != nil {
		setupErr = err
		log.Printf("start failed: stage=agent-launch project=%s branch=%s instance=%s worktree=%s elapsed=%s err=%v",
			req.Project, req.Branch, instanceID, worktreeDir, time.Since(startedAt).Round(time.Millisecond), err)
		respond(conn, proto.Response{OK: false, Error: err.Error()})
		return
	}

	// All steps succeeded — register the instance and respond.
	d.mu.Lock()
	d.instances[instanceID] = inst
	d.mu.Unlock()

	inst.persistMeta(filepath.Join(d.rootDir, "instances"))

	// Send the JSON ACK first, then stream any captured setup output.
	respond(conn, proto.Response{OK: true, InstanceID: instanceID})
	if outputBuf.Len() > 0 {
		conn.Write(outputBuf.Bytes())
	}
	log.Printf("start succeeded: project=%s branch=%s instance=%s worktree=%s elapsed=%s", req.Project, req.Branch, instanceID, worktreeDir, time.Since(startedAt).Round(time.Millisecond))
}

func repoURLHintSuffix(repo string) string {
	if strings.HasPrefix(repo, "github.com/") || strings.HasPrefix(repo, "gitlab.com/") || strings.HasPrefix(repo, "bitbucket.org/") {
		return " hint=\"repo URL may be missing scheme; try https://host/org/repo.git or git@host:org/repo.git\""
	}
	return ""
}

func (d *Daemon) handleList(conn net.Conn) {
	d.mu.Lock()
	infos := make([]proto.InstanceInfo, 0, len(d.instances))
	for _, inst := range d.instances {
		infos = append(infos, inst.Info())
	}
	d.mu.Unlock()

	sort.Slice(infos, func(i, j int) bool {
		return infos[i].CreatedAt < infos[j].CreatedAt
	})

	respond(conn, proto.Response{OK: true, Instances: infos})
}

func (d *Daemon) handleAttach(conn net.Conn, req proto.Request) {
	inst := d.getInstance(req.InstanceID)
	if inst == nil {
		respond(conn, proto.Response{OK: false, Error: "instance not found: " + req.InstanceID})
		return
	}

	inst.mu.Lock()
	state := inst.state
	inst.mu.Unlock()

	if proto.IsTerminal(state) {
		respond(conn, proto.Response{OK: false, Error: "instance has " + strings.ToLower(state)})
		return
	}

	// Send the handshake ACK before entering streaming mode.
	respond(conn, proto.Response{OK: true})

	// Attach blocks until the client detaches or the agent exits.
	inst.Attach(conn)
}

func (d *Daemon) handleLogs(conn net.Conn, req proto.Request) {
	inst := d.getInstance(req.InstanceID)
	if inst == nil {
		respond(conn, proto.Response{OK: false, Error: "instance not found: " + req.InstanceID})
		return
	}

	inst.mu.Lock()
	logs := make([]byte, len(inst.logBuf))
	copy(logs, inst.logBuf)
	inst.mu.Unlock()

	respond(conn, proto.Response{OK: true, InstanceID: req.InstanceID})
	conn.Write(logs)
}

func (d *Daemon) handleLogsFollow(conn net.Conn, req proto.Request) {
	inst := d.getInstance(req.InstanceID)
	if inst == nil {
		respond(conn, proto.Response{OK: false, Error: "instance not found: " + req.InstanceID})
		return
	}
	respond(conn, proto.Response{OK: true})

	// Snapshot current logBuf; track how many bytes we've sent.
	inst.mu.Lock()
	initial := make([]byte, len(inst.logBuf))
	copy(initial, inst.logBuf)
	offset := len(inst.logBuf)
	inst.mu.Unlock()

	if len(initial) > 0 {
		if _, err := conn.Write(initial); err != nil {
			return
		}
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		inst.mu.Lock()
		state := inst.state
		// Clamp offset if logBuf was trimmed (rolled over 1 MiB cap).
		if offset > len(inst.logBuf) {
			offset = 0
		}
		newData := make([]byte, len(inst.logBuf)-offset)
		copy(newData, inst.logBuf[offset:])
		offset += len(newData)
		inst.mu.Unlock()

		if len(newData) > 0 {
			if _, err := conn.Write(newData); err != nil {
				return // client disconnected
			}
		}

		// Exit when instance is done AND no more new bytes remain.
		if proto.IsTerminal(state) && len(newData) == 0 {
			return
		}
	}
}

func (d *Daemon) handleStop(conn net.Conn, req proto.Request) {
	inst := d.getInstance(req.InstanceID)
	if inst == nil {
		respond(conn, proto.Response{OK: false, Error: "instance not found: " + req.InstanceID})
		return
	}

	// Kill the agent process if it is running; ptyReader will transition
	// the state to CRASHED and persist it.  For already-dead instances
	// (EXITED/CRASHED/FINISHED) this is a no-op.
	inst.destroy()

	respond(conn, proto.Response{OK: true})
}

func (d *Daemon) handleDrop(conn net.Conn, req proto.Request) {
	inst := d.getInstance(req.InstanceID)
	if inst == nil {
		respond(conn, proto.Response{OK: false, Error: "instance not found: " + req.InstanceID})
		return
	}

	worktreeDir := inst.WorktreeDir
	branch := inst.Branch
	containerID := inst.ContainerID
	composeProject := inst.ComposeProject
	projectName := inst.Project

	// Kill the docker exec session (container keeps running until stopContainer).
	inst.destroy()

	// Stop and remove the container (or compose stack).
	stopContainer(containerID, composeProject)

	// Derive mainDir from the project and daemon root — explicit and resilient.
	mainDir := filepath.Join(d.rootDir, "projects", projectName, "main")

	if out, err := exec.Command("git", "-C", mainDir, "worktree", "remove", "--force", worktreeDir).CombinedOutput(); err != nil {
		log.Printf("instance %s: git worktree remove failed: %v: %s", req.InstanceID, err, out)
	}
	if out, err := exec.Command("git", "-C", mainDir, "branch", "-D", branch).CombinedOutput(); err != nil {
		log.Printf("instance %s: git branch -D failed: %v: %s", req.InstanceID, err, out)
	}

	d.mu.Lock()
	delete(d.instances, req.InstanceID)
	d.mu.Unlock()

	os.Remove(filepath.Join(d.rootDir, "instances", req.InstanceID+".json"))

	respond(conn, proto.Response{OK: true})
}

func (d *Daemon) handleFinish(conn net.Conn, req proto.Request) {
	inst := d.getInstance(req.InstanceID)
	if inst == nil {
		respond(conn, proto.Response{OK: false, Error: "instance not found: " + req.InstanceID})
		return
	}

	worktreeDir := inst.WorktreeDir
	branch := inst.Branch
	projectName := inst.Project

	inst.mu.Lock()
	state := inst.state
	switch state {
	case proto.StateExited, proto.StateCrashed, proto.StateKilled:
		// Process already dead; transition to FINISHED directly.
		inst.state = proto.StateFinished
		inst.mu.Unlock()
	case proto.StateFinished:
		// Already finished; respond and skip finish commands.
		inst.mu.Unlock()
		respond(conn, proto.Response{OK: true, WorktreeDir: worktreeDir, Branch: branch})
		return
	default:
		// Agent is alive; request finish and wait for ptyReader to exit.
		inst.finishRequest = true
		processDone := inst.processDone
		inst.mu.Unlock()
		inst.destroy()
		if processDone != nil {
			<-processDone
		}
	}

	// Persist FINISHED state. (ptyReader may have already done this if it ran,
	// but an extra write is harmless.)
	inst.persistMeta(filepath.Join(d.rootDir, "instances"))

	// Send ACK — instance is now FINISHED regardless of what complete commands do.
	respond(conn, proto.Response{OK: true, WorktreeDir: worktreeDir, Branch: branch})

	p, err := loadProject(d.rootDir, projectName)
	if err != nil {
		fmt.Fprintf(conn, "warning: could not load project to run finish commands: %v\n", err)
		return
	}
	if _, err := loadInRepoConfig(p); err != nil {
		log.Printf("warning: could not read grove.yaml for %s: %v", projectName, err)
	}
	if len(p.Finish) == 0 {
		return
	}

	// Open the instance log file for appending so finish command output is
	// preserved even if the client disconnects mid-way.
	logFd, _ := os.OpenFile(inst.LogFile, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if logFd != nil {
		defer logFd.Close()
	}

	// w writes to both the connection and the log file.  If the client
	// disconnects, writes to conn are silently dropped but the log keeps
	// receiving output and commands run to completion.
	w := newResilientWriter(conn, logFd)

	containerID := inst.ContainerID

	for _, cmdStr := range p.Finish {
		expanded := strings.ReplaceAll(cmdStr, "{{branch}}", branch)
		fmt.Fprintf(w, "$ %s\n", expanded)
		if err := execInContainer(containerID, expanded, w); err != nil {
			fmt.Fprintf(w, "error: command failed: %v\n", err)
			log.Printf("instance %s: finish command failed: %v", inst.ID, err)
			return
		}
	}
}

func (d *Daemon) handleCheck(conn net.Conn, req proto.Request) {
	inst := d.getInstance(req.InstanceID)
	if inst == nil {
		respond(conn, proto.Response{OK: false, Error: "instance not found: " + req.InstanceID})
		return
	}

	projectName := inst.Project

	inst.mu.Lock()
	state := inst.state
	if proto.IsTerminal(state) || state == proto.StateChecking {
		inst.mu.Unlock()
		respond(conn, proto.Response{OK: false, Error: "cannot check: instance is " + state})
		return
	}
	inst.state = proto.StateChecking
	inst.mu.Unlock()

	defer func() {
		inst.mu.Lock()
		if inst.state == proto.StateChecking {
			inst.state = proto.StateWaiting
		}
		inst.mu.Unlock()
	}()

	p, err := loadProject(d.rootDir, projectName)
	if err != nil {
		respond(conn, proto.Response{OK: false, Error: err.Error()})
		return
	}
	if _, err := loadInRepoConfig(p); err != nil {
		log.Printf("warning: could not read grove.yaml for %s: %v", projectName, err)
	}
	if len(p.Check) == 0 {
		respond(conn, proto.Response{OK: false, Error: "no check commands defined in grove.yaml"})
		return
	}

	respond(conn, proto.Response{OK: true})

	logFd, _ := os.OpenFile(inst.LogFile, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if logFd != nil {
		defer logFd.Close()
	}

	w := newResilientWriter(conn, logFd)

	containerID := inst.ContainerID

	var wg sync.WaitGroup
	for _, cmdStr := range p.Check {
		wg.Add(1)
		go func(cmd string) {
			defer wg.Done()
			fmt.Fprintf(w, "$ %s\n", cmd)
			if err := execInContainer(containerID, cmd, w); err != nil {
				fmt.Fprintf(w, "error: check command failed: %v\n", err)
				log.Printf("instance %s: check command %q failed: %v", inst.ID, cmd, err)
			}
		}(cmdStr)
	}
	wg.Wait()
}

func (d *Daemon) handleRestart(conn net.Conn, req proto.Request) {
	inst := d.getInstance(req.InstanceID)
	if inst == nil {
		respond(conn, proto.Response{OK: false, Error: "instance not found: " + req.InstanceID})
		return
	}

	inst.mu.Lock()
	state := inst.state
	inst.mu.Unlock()

	if !proto.IsTerminal(state) {
		respond(conn, proto.Response{OK: false, Error: "cannot restart: instance is " + state})
		return
	}

	p, err := loadProject(d.rootDir, inst.Project)
	if err != nil {
		respond(conn, proto.Response{OK: false, Error: err.Error()})
		return
	}

	if _, err := loadInRepoConfig(p); err != nil {
		log.Printf("warning: could not read grove.yaml for %s: %v", inst.Project, err)
	}

	agentCmd := p.Agent.Command
	if agentCmd == "" {
		agentCmd = "sh"
	}

	// Reset mutable state before restarting.
	inst.mu.Lock()
	inst.endedAt = time.Time{}
	inst.finishRequest = false
	inst.killed = false
	inst.mu.Unlock()

	agentEnv := envfile.Load(filepath.Join(d.rootDir, "env"))
	for k, v := range req.AgentEnv {
		agentEnv[k] = v
	}
	logAgentCredentials(inst.ID, agentEnv)

	if err := inst.startAgent(agentCmd, p.Agent.Args, agentEnv); err != nil {
		respond(conn, proto.Response{OK: false, Error: err.Error()})
		return
	}

	inst.persistMeta(filepath.Join(d.rootDir, "instances"))

	respond(conn, proto.Response{OK: true})
}
