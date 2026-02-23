package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// validateDocker checks that Docker is available by running "docker info".
func validateDocker() error {
	cmd := exec.Command("docker", "info")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker is not available (%w)\nInstall Docker: https://docs.docker.com/get-docker/", err)
	}
	return nil
}

// startContainer dispatches to the single-container or compose variant.
// Returns the exec target container name.
func startContainer(p *Project, instanceID, worktreeDir string, w io.Writer) (string, error) {
	if p.Container.Compose != "" {
		return startComposeContainer(p, instanceID, worktreeDir, w)
	}
	if p.Container.Image == "" {
		groveYAML := filepath.Join(p.MainDir(), "grove.yaml")
		return "", fmt.Errorf("no container configured in %s\nadd a 'container:' section, e.g.:\n\n  container:\n    image: ubuntu:24.04\n", groveYAML)
	}
	return startSingleContainer(p, instanceID, worktreeDir, w)
}

// startSingleContainer runs:
//
//	docker run -d --name grove-<id> -v <worktreeDir>:<workdir> -w <workdir> [mounts...] <image> sleep infinity
func startSingleContainer(p *Project, instanceID, worktreeDir string, w io.Writer) (string, error) {
	name := "grove-" + instanceID
	workdir := p.containerWorkdir()
	image := p.Container.Image

	args := []string{"run", "-d",
		"--name", name,
		"-v", worktreeDir + ":" + workdir,
		"-w", workdir,
	}
	for _, m := range buildMounts(p, w) {
		args = append(args, "-v", m[0]+":"+m[1])
	}
	args = append(args, image, "sleep", "infinity")

	fmt.Fprintf(w, "Starting container %s (image: %s) …\n", name, image)
	cmd := exec.Command("docker", args...)
	out, err := cmd.CombinedOutput()
	if len(out) > 0 {
		w.Write(out)
	}
	if err != nil {
		return "", fmt.Errorf("docker run: %w", err)
	}
	return name, nil
}

// startComposeContainer writes a temporary override YAML that bind-mounts the
// worktree (and any extra mounts) into the app service, then runs:
//
//	docker compose -p grove-<id> -f <composefile> -f <overridefile> up -d
//
// Returns "grove-<id>-<service>-1" as the exec target.
func startComposeContainer(p *Project, instanceID, worktreeDir string, w io.Writer) (string, error) {
	project := "grove-" + instanceID
	service := p.containerService()
	workdir := p.containerWorkdir()
	composeFile := p.Container.Compose

	// Build the volumes block: worktree first, then any extra mounts.
	volumes := fmt.Sprintf("      - type: bind\n        source: %s\n        target: %s\n", worktreeDir, workdir)
	for _, m := range buildMounts(p, w) {
		volumes += fmt.Sprintf("      - type: bind\n        source: %s\n        target: %s\n", m[0], m[1])
	}
	overrideContent := fmt.Sprintf("services:\n  %s:\n    volumes:\n%s", service, volumes)

	overrideFile, err := os.CreateTemp("", "grove-compose-override-*.yml")
	if err != nil {
		return "", fmt.Errorf("create compose override: %w", err)
	}
	overridePath := overrideFile.Name()
	if _, err := overrideFile.WriteString(overrideContent); err != nil {
		overrideFile.Close()
		os.Remove(overridePath)
		return "", fmt.Errorf("write compose override: %w", err)
	}
	overrideFile.Close()
	defer os.Remove(overridePath)

	fmt.Fprintf(w, "Starting compose stack %s (compose: %s, service: %s) …\n", project, composeFile, service)
	cmd := exec.Command("docker", "compose",
		"-p", project,
		"-f", composeFile,
		"-f", overridePath,
		"up", "-d",
	)
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("docker compose up: %w", err)
	}

	// Exec target: "grove-<id>-<service>-1"
	return project + "-" + service + "-1", nil
}

// stopContainer tears down the container or compose stack for an instance.
// If composeProject is non-empty, tears down the compose stack; otherwise
// stops and removes the single container.
func stopContainer(containerName, composeProject string) {
	if composeProject != "" {
		exec.Command("docker", "compose", "-p", composeProject, "down", "-v").Run()
		return
	}
	exec.Command("docker", "stop", containerName).Run()
	exec.Command("docker", "rm", containerName).Run()
}

// execInContainer runs cmd inside the named container using "docker exec".
func execInContainer(containerName, cmd string, w io.Writer) error {
	c := exec.Command("docker", "exec", containerName, "sh", "-c", cmd)
	c.Stdout = w
	c.Stderr = w
	if err := c.Run(); err != nil {
		return fmt.Errorf("exec in container %s: %w", containerName, err)
	}
	return nil
}

// ensureAgentInstalled checks whether agentCmd is present in the container and,
// if not, attempts to install it automatically for known agents.
// All output (install progress, errors) is written to w so it appears in the
// instance log and in the user's terminal during "grove start".
func ensureAgentInstalled(agentCmd, containerName string, w io.Writer) error {
	// Fast path: agent already installed.
	check := exec.Command("docker", "exec", containerName,
		"sh", "-c", "command -v "+agentCmd+" >/dev/null 2>&1")
	if check.Run() == nil {
		return nil
	}

	// Auto-install for known agents.
	var installScript, startSnippet string
	switch agentCmd {
	case "claude":
		// Claude Code uses a native installer (npm install is deprecated).
		// The installer writes the binary to $HOME/.local/bin/claude; we also
		// symlink it into /usr/local/bin so that plain "docker exec ... claude"
		// finds it without needing a login shell or PATH override.
		// Alpine requires libgcc/libstdc++ for the native binary; all images
		// need curl (installed here if missing via apt-get).
		installScript = `set -e
export HOME=/root
export PATH=/root/.local/bin:$PATH
if command -v apk >/dev/null 2>&1; then
  apk add --no-cache libgcc libstdc++ ripgrep curl
elif ! command -v curl >/dev/null 2>&1; then
  if command -v apt-get >/dev/null 2>&1; then
    apt-get update -qq && apt-get install -y -qq curl
  else
    echo "Cannot install Claude: curl not found and no supported package manager." >&2
    exit 1
  fi
fi
curl -fsSL https://claude.ai/install.sh | bash
if [ -f /root/.local/bin/claude ] && [ ! -e /usr/local/bin/claude ]; then
  ln -sf /root/.local/bin/claude /usr/local/bin/claude
fi`
		startSnippet = `  start:
    - curl -fsSL https://claude.ai/install.sh | bash
    - ln -sf /root/.local/bin/claude /usr/local/bin/claude`
	case "aider":
		installScript = `set -e
if ! command -v pip >/dev/null 2>&1 && ! command -v pip3 >/dev/null 2>&1; then
  if command -v apt-get >/dev/null 2>&1; then
    apt-get update -qq && apt-get install -y -qq python3 python3-pip
  elif command -v apk >/dev/null 2>&1; then
    apk add --no-cache python3 py3-pip
  else
    echo "pip not found and no supported package manager available" >&2
    exit 1
  fi
fi
pip install aider-chat 2>/dev/null || pip3 install aider-chat`
		startSnippet = `  start:
    - pip install aider-chat`
	default:
		return fmt.Errorf("agent command %q not found in container %s\n"+
			"install it in your container image or add it to 'start:' in grove.yaml",
			agentCmd, containerName)
	}

	fmt.Fprintf(w, "Agent %q not found — auto-installing (this runs once per container)…\n", agentCmd)
	c := exec.Command("docker", "exec", containerName, "sh", "-c", installScript)
	c.Stdout = w
	c.Stderr = w
	if err := c.Run(); err != nil {
		return fmt.Errorf("auto-install of %q failed: %w\n"+
			"to install it yourself, add to grove.yaml:\n%s",
			agentCmd, err, startSnippet)
	}

	// Verify the install actually made the binary available.
	verify := exec.Command("docker", "exec", containerName,
		"sh", "-c", "command -v "+agentCmd+" >/dev/null 2>&1")
	if err := verify.Run(); err != nil {
		return fmt.Errorf("auto-install of %q appeared to succeed but the command is still not in PATH\n"+
			"check that the install placed the binary in a directory on $PATH inside the container",
			agentCmd)
	}

	fmt.Fprintf(w, "Agent %q installed successfully.\n", agentCmd)
	return nil
}

// buildMounts returns all (source, target) mount pairs for the container:
// auto-detected agent credentials followed by user-configured mounts.
// Each applied mount is logged to w. User-configured paths that don't exist
// on the host produce a warning; missing credential dirs are silently skipped
// (the agent may not be installed yet).
func buildMounts(p *Project, w io.Writer) [][2]string {
	home, _ := os.UserHomeDir()
	var mounts [][2]string

	// Auto-mount credentials for known agents.
	for _, pair := range agentCredentialMounts(p.Agent.Command, home) {
		if _, err := os.Stat(pair[0]); err == nil {
			fmt.Fprintf(w, "Mounting credentials: %s → %s\n", pair[0], pair[1])
			mounts = append(mounts, pair)
		}
	}

	// User-configured extra mounts from grove.yaml.
	for _, m := range p.Container.Mounts {
		src, tgt := resolveMountPath(m, home)
		if _, err := os.Stat(src); err == nil {
			fmt.Fprintf(w, "Mounting: %s → %s\n", src, tgt)
			mounts = append(mounts, [2]string{src, tgt})
		} else {
			fmt.Fprintf(w, "Warning: skipping mount %q — path not found on host\n", m)
		}
	}

	return mounts
}

// agentCredentialMounts returns (source, target) pairs for known agent CLIs.
//
// Note: ~/.claude.json is deliberately NOT bind-mounted for Claude because the
// host's Claude Code and the container's Claude Code both write to it
// frequently, causing file corruption. Instead, seedClaudeConfig copies a
// snapshot into the container after creation.
func agentCredentialMounts(agentCmd, home string) [][2]string {
	switch agentCmd {
	case "claude":
		return [][2]string{
			{filepath.Join(home, ".claude"), "/root/.claude"},
		}
	case "aider":
		return [][2]string{
			{filepath.Join(home, ".aider"), "/root/.aider"},
		}
	}
	return nil
}

// seedClaudeConfig copies the host's ~/.claude.json into the container so
// Claude Code starts with the user's existing preferences and auth state.
// Unlike a bind mount, this gives the container its own copy that won't
// corrupt the host file when both write concurrently.
func seedClaudeConfig(containerName string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	src := filepath.Join(home, ".claude.json")

	data, err := os.ReadFile(src)
	if err != nil {
		return
	}
	if !json.Valid(data) {
		log.Printf("seedClaudeConfig: %s is not valid JSON, skipping", src)
		return
	}

	cmd := exec.Command("docker", "cp", src, containerName+":/root/.claude.json")
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("seedClaudeConfig: docker cp failed: %v: %s", err, out)
	}
}

// resolveMountPath expands a user-specified mount path to (source, target).
// ~/foo  →  (/home/user/foo, /root/foo)
// /abs   →  (/abs, /abs)
func resolveMountPath(m, home string) (source, target string) {
	if m == "~" {
		return home, "/root"
	}
	if strings.HasPrefix(m, "~/") {
		rel := m[2:]
		return filepath.Join(home, rel), "/root/" + rel
	}
	return m, m
}

