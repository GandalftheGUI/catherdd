package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/gandalfthegui/grove/internal/proto"
)

// stripBoolFlag removes every occurrence of the given short/long flag from
// args and returns (filtered, found). This lets the flag appear anywhere —
// before or after positional arguments — regardless of flag.Parse stopping at
// the first non-flag argument.
func stripBoolFlag(args []string, short, long string) ([]string, bool) {
	out := make([]string, 0, len(args))
	found := false
	for _, a := range args {
		if a == "-"+short || a == "--"+short || a == "-"+long || a == "--"+long {
			found = true
		} else {
			out = append(out, a)
		}
	}
	return out, found
}

func cmdStart() {
	rawArgs, detach := stripBoolFlag(os.Args[2:], "d", "detach")
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: grove start <project|#> <branch> [-d]")
	}
	fs.Parse(rawArgs)
	args := fs.Args()
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: grove start <project|#> <branch> [-d]")
		os.Exit(1)
	}
	project := resolveProject(args[0])
	branch := args[1]

	agentEnv := ensureAgentCredentials(project)

	socketPath := daemonSocket()
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "grove: %v\n", err)
		os.Exit(1)
	}

	if err := writeRequest(conn, proto.Request{
		Type:     proto.ReqStart,
		Project:  project,
		Branch:   branch,
		AgentEnv: agentEnv,
	}); err != nil {
		conn.Close()
		fmt.Fprintf(os.Stderr, "grove: %v\n", err)
		os.Exit(1)
	}

	// Show a throbber while the daemon starts the container and shell (clone, container, start commands, agent install).
	stopThrobber := make(chan struct{})
	throbberDone := make(chan struct{})
	go func() {
		defer close(throbberDone)
		frames := []rune(`|/-\`)
		i := 0
		for {
			select {
			case <-stopThrobber:
				// Clear the throbber line so setup output or success message starts clean.
				fmt.Fprint(os.Stderr, "\r  \033[K")
				return
			default:
				fmt.Fprintf(os.Stderr, "\r  Starting instance %c  ", frames[i])
				i = (i + 1) % len(frames)
				time.Sleep(120 * time.Millisecond)
			}
		}
	}()

	resp, err := readResponse(conn)
	close(stopThrobber)
	<-throbberDone
	if err != nil {
		conn.Close()
		fmt.Fprintf(os.Stderr, "grove: %v\n", err)
		os.Exit(1)
	}
	if !resp.OK {
		conn.Close()
		if resp.InitPath != "" {
			// Project exists but has no grove.yaml — prompt the user to create one.
			promptCreateProjectConfig(resp.InitPath, project)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "grove: %s\n", resp.Error)
		fmt.Fprintf(os.Stderr, "grove: check daemon logs with: grove daemon logs -n 100\n")
		os.Exit(1)
	}

	// Stream any setup output (clone, pull, bootstrap) the daemon buffered.
	io.Copy(os.Stdout, conn)
	conn.Close()

	fmt.Printf("\n%s✓  Started instance%s %s%s%s\n\n", colorGreen+colorBold, colorReset, colorCyan, resp.InstanceID, colorReset)

	if !detach {
		doAttach(resp.InstanceID)
	}
}

func cmdList() {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	activeOnly := fs.Bool("active", false, "show only active instances (exclude FINISHED)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: grove list [--active]")
	}
	fs.Parse(os.Args[2:])

	resp := mustRequest(proto.Request{Type: proto.ReqList})

	var instances []proto.InstanceInfo
	for _, inst := range resp.Instances {
		if *activeOnly && inst.State == proto.StateFinished {
			continue
		}
		instances = append(instances, inst)
	}

	if len(instances) == 0 {
		fmt.Printf("%sno instances%s\n", colorDim, colorReset)
		return
	}

	fmt.Printf("%s%-10s  %-12s  %-10s  %s%s\n", colorBold, "ID", "PROJECT", "STATE", "BRANCH", colorReset)
	fmt.Printf("%s%-10s  %-12s  %-10s  %s%s\n", colorDim, "----------", "------------", "----------", "------", colorReset)
	for _, inst := range instances {
		color := colorState(inst.State)
		reset := ""
		if color != "" {
			reset = "\033[0m"
		}
		fmt.Printf("%-10s  %-12s  %s%-10s%s  %s\n", inst.ID, inst.Project, color, inst.State, reset, inst.Branch)
	}
}

func cmdStop() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: grove stop <instance-id>")
		os.Exit(1)
	}
	instanceID := os.Args[2]

	mustRequest(proto.Request{
		Type:       proto.ReqStop,
		InstanceID: instanceID,
	})

	fmt.Printf("\n%s✓  Stopped%s %s%s%s\n\n", colorGreen+colorBold, colorReset, colorCyan, instanceID, colorReset)
}

func cmdRestart() {
	rawArgs, detach := stripBoolFlag(os.Args[2:], "d", "detach")
	fs := flag.NewFlagSet("restart", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: grove restart <instance-id> [-d]")
	}
	fs.Parse(rawArgs)
	args := fs.Args()
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: grove restart <instance-id> [-d]")
		os.Exit(1)
	}
	instanceID := args[0]

	var agentEnv map[string]string
	if inst := findInstance(instanceID); inst != nil {
		agentEnv = ensureAgentCredentials(inst.Project)
	}

	mustRequest(proto.Request{
		Type:       proto.ReqRestart,
		InstanceID: instanceID,
		AgentEnv:   agentEnv,
	})

	fmt.Printf("\n%s✓  Restarted%s %s%s%s\n\n", colorGreen+colorBold, colorReset, colorCyan, instanceID, colorReset)

	if !detach {
		doAttach(instanceID)
	}
}

func cmdDrop() {
	rawArgs, force := stripBoolFlag(os.Args[2:], "f", "force")
	fs := flag.NewFlagSet("drop", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, "usage: grove drop <instance-id> [-f]") }
	fs.Parse(rawArgs)
	args := fs.Args()
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: grove drop <instance-id> [-f]")
		os.Exit(1)
	}
	instanceID := args[0]

	found := findInstance(instanceID)
	if found == nil {
		fmt.Fprintf(os.Stderr, "grove: instance not found: %s\n", instanceID)
		os.Exit(1)
	}

	if !force {
		fmt.Printf("\n%sInstance%s %s%s%s\n\n", colorBold, colorReset, colorCyan, instanceID, colorReset)
		fmt.Printf("  %sProject:%s  %s%s%s\n", colorDim, colorReset, colorCyan, found.Project, colorReset)
		fmt.Printf("  %sWorktree:%s %s%s%s\n", colorDim, colorReset, colorCyan, found.WorktreeDir, colorReset)
		fmt.Printf("  %sBranch:%s   %s%s%s\n\n", colorDim, colorReset, colorCyan, found.Branch, colorReset)
		fmt.Printf("%sDelete instance %q and worktree?%s [y/N] ", colorBold, found.Project, colorReset)

		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(answer)
		if answer != "y" && answer != "Y" {
			fmt.Printf("%saborted%s\n", colorDim, colorReset)
			return
		}
	}

	mustRequest(proto.Request{
		Type:       proto.ReqDrop,
		InstanceID: instanceID,
	})
	fmt.Printf("\n%s✓  Dropped%s %s%s%s\n\n", colorGreen+colorBold, colorReset, colorCyan, instanceID, colorReset)
}

func cmdFinish() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: grove finish <instance-id>")
		os.Exit(1)
	}
	streamCommand(proto.ReqFinish, os.Args[2])
}

func cmdCheck() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: grove check <instance-id>")
		os.Exit(1)
	}
	streamCommand(proto.ReqCheck, os.Args[2])
}

func cmdDir() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: grove dir <instance-id>")
		os.Exit(1)
	}
	id := os.Args[2]

	inst := findInstance(id)
	if inst == nil {
		fmt.Fprintf(os.Stderr, "grove: instance not found: %s\n", id)
		os.Exit(1)
	}
	fmt.Println(inst.WorktreeDir)
}

func cmdShell() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: grove shell <instance-id> [shell]")
		os.Exit(1)
	}
	instanceID := os.Args[2]
	shell := "sh"
	if len(os.Args) >= 4 {
		shell = os.Args[3]
	}

	inst := findInstance(instanceID)
	if inst == nil {
		fmt.Fprintf(os.Stderr, "grove: instance not found: %s\n", instanceID)
		os.Exit(1)
	}
	if inst.ContainerID == "" {
		fmt.Fprintf(os.Stderr, "grove: instance not found: %s\n", instanceID)
		os.Exit(1)
	}

	cmd := exec.Command("docker", "exec", "-it", "-u", "root", "-e", "HOME=/root", inst.ContainerID, shell)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "grove: %v\n", err)
		os.Exit(1)
	}
}

func cmdLogs() {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	follow := fs.Bool("f", false, "follow log output")
	fs.BoolVar(follow, "follow", false, "follow log output")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: grove logs <instance-id> [-f]")
	}
	fs.Parse(os.Args[2:])
	remaining := fs.Args()
	if len(remaining) < 1 {
		fmt.Fprintln(os.Stderr, "usage: grove logs <instance-id> [-f]")
		os.Exit(1)
	}
	instanceID := remaining[0]

	reqType := proto.ReqLogs
	if *follow {
		reqType = proto.ReqLogsFollow
	}

	socketPath := daemonSocket()
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "grove: cannot connect to daemon: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	if err := writeRequest(conn, proto.Request{Type: reqType, InstanceID: instanceID}); err != nil {
		fmt.Fprintf(os.Stderr, "grove: %v\n", err)
		os.Exit(1)
	}
	resp, err := readResponse(conn)
	if err != nil || !resp.OK {
		msg := "logs failed"
		if resp.Error != "" {
			msg = resp.Error
		}
		fmt.Fprintf(os.Stderr, "grove: %s\n", msg)
		os.Exit(1)
	}
	io.Copy(os.Stdout, conn)
}

func cmdPrune() {
	fs := flag.NewFlagSet("prune", flag.ExitOnError)
	includeFinished := fs.Bool("finished", false, "also drop FINISHED instances")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: grove prune [--finished]")
	}
	fs.Parse(os.Args[2:])

	resp := mustRequest(proto.Request{Type: proto.ReqList})

	var dead []proto.InstanceInfo
	for _, inst := range resp.Instances {
		switch inst.State {
		case proto.StateExited, proto.StateCrashed, proto.StateKilled:
			dead = append(dead, inst)
		case proto.StateFinished:
			if *includeFinished {
				dead = append(dead, inst)
			}
		}
	}

	if len(dead) == 0 {
		fmt.Printf("%snothing to prune%s\n", colorDim, colorReset)
		return
	}

	fmt.Printf("\n%s⚠  Prune%s — the following instance(s) and their worktrees will be removed:\n\n", colorYellow+colorBold, colorReset)
	for _, inst := range dead {
		fmt.Printf("  %s%s%s\n", colorBold, inst.ID, colorReset)
		fmt.Printf("    %sProject:%s   %s%s%s\n", colorDim, colorReset, colorCyan, inst.Project, colorReset)
		fmt.Printf("    %sWorktree:%s  %s%s%s\n", colorDim, colorReset, colorCyan, inst.WorktreeDir, colorReset)
		fmt.Printf("    %sBranch:%s    %s%s%s\n", colorDim, colorReset, colorCyan, inst.Branch, colorReset)
		fmt.Printf("    %sState:%s     %s\n\n", colorDim, colorReset, inst.State)
	}
	fmt.Printf("  This will drop %d instance(s) and their worktrees.\n\n", len(dead))
	fmt.Printf("%sContinue?%s [y/N] ", colorBold, colorReset)

	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(answer)
	if answer != "y" && answer != "Y" {
		fmt.Printf("%saborted%s\n", colorDim, colorReset)
		return
	}

	for _, inst := range dead {
		mustRequest(proto.Request{Type: proto.ReqDrop, InstanceID: inst.ID})
		fmt.Printf("%s✓  Dropped%s %s%s%s\n", colorGreen+colorBold, colorReset, colorCyan, inst.ID, colorReset)
	}
	fmt.Println()
}
