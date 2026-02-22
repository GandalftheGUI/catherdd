package daemon

import (
	"fmt"
	"io"
	"os"
	"os/exec"
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
		return "", fmt.Errorf("no container image or compose file configured in .grove/project.yaml (add a 'container:' section)")
	}
	return startSingleContainer(p, instanceID, worktreeDir, w)
}

// startSingleContainer runs:
//
//	docker run -d --name grove-<id> -v <worktreeDir>:<workdir> -w <workdir> <image> sleep infinity
func startSingleContainer(p *Project, instanceID, worktreeDir string, w io.Writer) (string, error) {
	name := "grove-" + instanceID
	workdir := p.containerWorkdir()
	image := p.Container.Image

	fmt.Fprintf(w, "Starting container %s (image: %s) …\n", name, image)
	cmd := exec.Command("docker", "run", "-d",
		"--name", name,
		"-v", worktreeDir+":"+workdir,
		"-w", workdir,
		image,
		"sleep", "infinity",
	)
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
// worktree into the app service, then runs:
//
//	docker compose -p grove-<id> -f <composefile> -f <overridefile> up -d
//
// Returns "grove-<id>-<service>-1" as the exec target.
func startComposeContainer(p *Project, instanceID, worktreeDir string, w io.Writer) (string, error) {
	project := "grove-" + instanceID
	service := p.containerService()
	workdir := p.containerWorkdir()
	composeFile := p.Container.Compose

	// Write a temporary override that shadow-mounts the worktree over the
	// service's default volume.
	overrideContent := fmt.Sprintf("services:\n  %s:\n    volumes:\n      - type: bind\n        source: %s\n        target: %s\n",
		service, worktreeDir, workdir)

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
