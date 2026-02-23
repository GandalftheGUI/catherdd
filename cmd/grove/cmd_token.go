package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gandalfthegui/grove/internal/envfile"
	"gopkg.in/yaml.v3"
)

// cmdToken sets or replaces the CLAUDE_CODE_OAUTH_TOKEN in ~/.grove/env.
// It replaces any existing entry rather than appending, so repeated calls
// don't accumulate stale tokens.
func cmdToken() {
	root := rootDir()
	envPath := filepath.Join(root, "env")

	envFile := envfile.Load(filepath.Join(root, "env"))
	if envFile["CLAUDE_CODE_OAUTH_TOKEN"] != "" {
		fmt.Printf("\n%sCurrent token:%s CLAUDE_CODE_OAUTH_TOKEN is set\n\n", colorBold, colorReset)
	} else {
		fmt.Printf("\n%sNo token currently set.%s\n\n", colorDim, colorReset)
	}

	fmt.Printf("Generate a new token by running:\n\n")
	fmt.Printf("    %sclaude setup-token%s\n\n", colorCyan, colorReset)
	fmt.Printf("%sNew token%s (or Enter to cancel): ", colorBold, colorReset)

	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return
	}
	token := strings.TrimSpace(scanner.Text())
	if token == "" {
		fmt.Printf("%scancelled%s\n", colorDim, colorReset)
		return
	}

	// Re-write the env file, stripping any existing CLAUDE_CODE_OAUTH_TOKEN
	// lines so we don't accumulate duplicates.
	existing, _ := os.ReadFile(envPath)
	var kept []string
	for _, line := range strings.Split(string(existing), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "CLAUDE_CODE_OAUTH_TOKEN=") {
			continue
		}
		kept = append(kept, line)
	}
	// Drop trailing blank lines before appending the new entry.
	for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
		kept = kept[:len(kept)-1]
	}
	kept = append(kept, "CLAUDE_CODE_OAUTH_TOKEN="+token)
	content := strings.Join(kept, "\n") + "\n"

	if err := os.MkdirAll(root, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "grove: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(envPath, []byte(content), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "grove: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n%s✓  Token saved%s %s%s%s\n\n", colorGreen+colorBold, colorReset, colorDim, envPath, colorReset)
}

// ensureAgentCredentials checks whether the required credentials for the
// project's agent are available. If not, it prompts the user interactively
// and saves the token to ~/.grove/env. Returns env vars to pass through the
// request for this session.
//
// Tokens found only in the shell environment (os.Getenv) are explicitly
// forwarded via the return map because the daemon runs as a LaunchAgent and
// does not inherit the user's shell environment.
func ensureAgentCredentials(project string) map[string]string {
	agentCmd := detectAgentCommand(project)
	// Skip only when we know for certain it is not a claude agent.
	// If detectAgentCommand returns "" (grove.yaml unreadable, e.g. first run
	// before the repo is cloned), we still check — claude is the default and
	// skipping silently would leave the container without credentials.
	if agentCmd != "" && agentCmd != "claude" {
		return nil
	}

	root := rootDir()
	envFile := envfile.Load(filepath.Join(root, "env"))

	// If a token is already persisted in ~/.grove/env, the daemon will inject
	// it directly — no need to echo it back through the request.
	if envFile["CLAUDE_CODE_OAUTH_TOKEN"] != "" || envFile["ANTHROPIC_API_KEY"] != "" {
		return nil
	}

	// Token found only in the shell environment: forward it explicitly so the
	// daemon (which runs without the user's shell env) can inject it into the
	// container.
	agentEnv := map[string]string{}
	if v := os.Getenv("CLAUDE_CODE_OAUTH_TOKEN"); v != "" {
		agentEnv["CLAUDE_CODE_OAUTH_TOKEN"] = v
	}
	if v := os.Getenv("ANTHROPIC_API_KEY"); v != "" {
		agentEnv["ANTHROPIC_API_KEY"] = v
	}
	if len(agentEnv) > 0 {
		return agentEnv
	}

	// No token found anywhere — prompt the user.
	fmt.Printf("\n%sClaude authentication required.%s\n\n", colorYellow+colorBold, colorReset)
	fmt.Printf("Generate a long-lived token by running:\n\n")
	fmt.Printf("    %sclaude setup-token%s\n\n", colorCyan, colorReset)
	fmt.Printf("Then paste the token below.\n\n")
	fmt.Printf("%sToken%s (or Enter to skip): ", colorBold, colorReset)

	s := bufio.NewScanner(os.Stdin)
	if !s.Scan() {
		return nil
	}
	token := strings.TrimSpace(s.Text())
	if token == "" {
		return nil
	}

	// Save to ~/.grove/env so the user never has to do this again.
	envPath := filepath.Join(root, "env")
	f, err := os.OpenFile(envPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		fmt.Fprintf(f, "CLAUDE_CODE_OAUTH_TOKEN=%s\n", token)
		f.Close()
		fmt.Printf("\n%s✓  Saved to %s%s\n\n", colorGreen, envPath, colorReset)
	}

	return map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": token}
}

// detectAgentCommand reads the project's grove.yaml to determine the agent
// command. Returns "" if the file doesn't exist or has no agent configured.
func detectAgentCommand(project string) string {
	root := rootDir()
	groveYAML := filepath.Join(root, "projects", project, "main", "grove.yaml")
	data, err := os.ReadFile(groveYAML)
	if err != nil {
		return ""
	}
	var cfg struct {
		Agent struct {
			Command string `yaml:"command"`
		} `yaml:"agent"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return ""
	}
	return cfg.Agent.Command
}

// promptCreateProjectConfig is called when the daemon reports that the project
// has no .grove/project.yaml in its repository. It asks the user whether to
// create a boilerplate file, writes it if they agree, then exits with
// instructions to edit, commit, and re-run.
func promptCreateProjectConfig(mainDir, projectName string) {
	configPath := filepath.Join(mainDir, "grove.yaml")

	fmt.Printf("\n%s⚠  No grove.yaml found in %s%s\n\n", colorYellow+colorBold, projectName, colorReset)
	fmt.Printf("  This file tells grove how to set up the container, run the agent,\n")
	fmt.Printf("  and finish the work. Commit it once and every grove user gets the\n")
	fmt.Printf("  same setup automatically — no per-machine configuration needed.\n\n")

	fmt.Printf("%sCreate a boilerplate now?%s [Y/n] ", colorBold, colorReset)

	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(answer)
	if answer != "" && answer != "y" && answer != "Y" {
		fmt.Printf("%saborted%s\n", colorDim, colorReset)
		return
	}

	if err := os.WriteFile(configPath, []byte(projectConfigBoilerplate), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "grove: %v\n", err)
		return
	}

	fmt.Printf("\n%s✓  Created%s %s%s%s\n\n", colorGreen+colorBold, colorReset, colorCyan, configPath, colorReset)
	fmt.Printf("%sNext steps:%s\n\n", colorBold, colorReset)
	fmt.Printf("  %s1.%s Edit the file to match your project\n", colorBold, colorReset)
	fmt.Printf("     %s%s%s\n\n", colorDim, configPath, colorReset)
	fmt.Printf("  %s2.%s Commit it\n", colorBold, colorReset)
	fmt.Printf("     %sgit -C %s add grove.yaml%s\n", colorDim, mainDir, colorReset)
	fmt.Printf("     %sgit -C %s commit -m 'Add grove.yaml'%s\n\n", colorDim, mainDir, colorReset)
	fmt.Printf("  %s3.%s Re-run\n", colorBold, colorReset)
	fmt.Printf("     %sgrove start %s <branch>%s\n\n", colorDim, projectName, colorReset)
}

// projectConfigBoilerplate is written to grove.yaml (repo root) when a project
// has none. It is designed to be self-explanatory with enough comments and
// examples that a developer can configure it without reading external docs.
const projectConfigBoilerplate = `# grove.yaml
# ─────────────────────────────────────────────────────────────────────────────
# Grove project configuration.
# Commit this file so everyone using Grove gets the same setup automatically.
# https://github.com/gandalfthegui/grove
# ─────────────────────────────────────────────────────────────────────────────

# ── Container ─────────────────────────────────────────────────────────────────
# Docker is required.  Each agent instance runs in its own container with the
# git worktree bind-mounted inside.
#
# Option A – single image (no external services):
#   container:
#     image: ruby:3.3      # any Docker image
#     workdir: /app        # working directory inside the container (default /app)
#
# Option B – docker-compose.yml (databases, caches, etc.):
#   container:
#     compose: docker-compose.yml   # path relative to repo root
#     service: app                  # service to exec into (default: app)
#     workdir: /app
#
container:
  image: ubuntu:24.04

# ── Start ─────────────────────────────────────────────────────────────────────
# Commands run once in each fresh worktree before the agent starts.
# The working directory is the worktree root.
#
# Best practice: delegate to an existing setup script so the logic lives in one
# place and can be run and tested independently of groved.
#
# Examples:
#   - ./scripts/bootstrap.sh        ← recommended if you have one
#   - make setup
#   - npm install
#   - pip install -r requirements.txt && pre-commit install
#   - bundle install
start:

# ── Agent ─────────────────────────────────────────────────────────────────────
# The AI coding agent to run inside each worktree PTY.
# 'grove attach' and 'grove start' connect your terminal directly to it.
#
# Common values:
#   claude   – Claude Code  (https://claude.ai/code)
#   aider    – Aider        (https://aider.chat)
#   sh       – plain shell  (useful for testing without an agent)
agent:
  command: claude
  args: []

# ── Check ─────────────────────────────────────────────────────────────────────
# Commands run concurrently by 'grove check <id>' inside the worktree directory.
# The daemon executes these while the agent stays alive; the instance returns to
# WAITING when all commands complete.
#
# Use these for verification steps: running tests, linting, type-checking, or
# starting a dev server to inspect the agent's work.
#
# Examples:
#   - npm test
#   - go test ./...
#   - make lint
check:

# ── Finish ────────────────────────────────────────────────────────────────────
# Commands run by 'grove finish <id>' inside the worktree directory.
# The daemon executes these — they complete even if you close your terminal.
# Use {{branch}} as a placeholder for the instance's branch name.
#
# The instance is marked FINISHED before these run, so a disconnection mid-way
# does not leave it in a broken state; output is preserved in the instance log.
#
# Tip: for anything beyond a simple push, delegate to a script so you can test
# the finish flow independently.
#
#   - ./scripts/finish.sh {{branch}}
#
finish:
  # Push the branch to the remote.
  - git push -u origin {{branch}}

  # Open a pull request (requires GitHub CLI: https://cli.github.com).
  # - gh pr create --title "{{branch}}" --fill

  # Or push, open a PR, squash-merge, and delete the branch in one step.
  # - git push -u origin {{branch}} && gh pr create --title "{{branch}}" --fill && gh pr merge --squash --delete-branch
`
