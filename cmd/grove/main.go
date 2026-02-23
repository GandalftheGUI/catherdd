// grove – the CLI client for the groved daemon.
//
// Usage:
//
//	grove project create <name>      – define a new project
//	grove project list               – list defined projects
//	grove start <project> "<task>"   – create and start a new agent instance
//	grove list                       – list all instances
//	grove attach <instance-id>       – attach your terminal to an instance PTY
//	grove logs <instance-id>         – print buffered logs for an instance
//	grove destroy <instance-id>      – stop and remove an instance
//
// grove will start the daemon automatically if it is not already running.
// Detach from an attached session with Ctrl-] (0x1D).
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "project":
		cmdProject()
	case "start":
		cmdStart()
	case "list":
		cmdList()
	case "attach":
		cmdAttach()
	case "watch":
		cmdWatch()
	case "logs":
		cmdLogs()
	case "stop":
		cmdStop()
	case "restart":
		cmdRestart()
	case "drop":
		cmdDrop()
	case "finish":
		cmdFinish()
	case "check":
		cmdCheck()
	case "prune":
		cmdPrune()
	case "dir":
		cmdDir()
	case "daemon":
		cmdDaemon()
	case "token":
		cmdToken()
	case "shell":
		cmdShell()
	default:
		fmt.Fprintf(os.Stderr, "grove: unknown command %q\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `grove – supervise AI coding agent instances

Project commands:
  project create <name> [--repo <url>]
                           Register a new project (name + repo URL)
  project list             List registered projects (numbered)
  project delete <name|#>  Remove a project and all its worktrees
  project dir <name|#>     Print the main checkout path for a project

Instance commands:
  start <project|#> <branch> [-d]
                                 Start a new agent instance on <branch> (attaches immediately; -d to skip)
                                 <project> may be a name or the number from 'project list'
  attach <instance-id>           Attach terminal to an instance (detach: Ctrl-])
  stop <instance-id>             Kill the agent; instance stays in list as KILLED
  restart <instance-id> [-d]     Restart agent in existing worktree (attaches immediately; -d to skip)
  check <instance-id>            Run check commands concurrently; instance returns to WAITING
  finish <instance-id>           Run finish steps; instance stays as FINISHED
  shell <instance-id> [shell]    Open an interactive shell in the instance container (default: sh)
  drop <instance-id>             Delete the worktree and branch permanently
  list [--active]                List all instances (--active: exclude FINISHED)
  logs <instance-id> [-f]        Print buffered output for an instance
  watch                          Live dashboard (refreshes every second, Ctrl-C to exit)
  prune [--finished]             Drop all exited/crashed instances (--finished: also FINISHED)
  dir <instance-id>              Print the worktree path for an instance

Daemon commands:
  daemon install           Register groved as a login LaunchAgent
  daemon uninstall         Remove the LaunchAgent
  daemon status            Show whether the LaunchAgent is installed and running
  daemon logs [-f] [-n N]  Print daemon log (-f follow, -n tail lines)

Credential commands:
  token                    Set or replace the CLAUDE_CODE_OAUTH_TOKEN in ~/.grove/env`)
}
