package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gandalfthegui/grove/internal/proto"
	"gopkg.in/yaml.v3"
)

func cmdProject() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: grove project <create|list|delete|dir>")
		os.Exit(1)
	}
	switch os.Args[2] {
	case "create":
		cmdProjectCreate()
	case "list":
		cmdProjectList()
	case "delete":
		cmdProjectDelete()
	case "dir":
		cmdProjectDir()
	default:
		fmt.Fprintf(os.Stderr, "grove: unknown project subcommand %q\n", os.Args[2])
		os.Exit(1)
	}
}

// cmdProjectCreate handles: grove project create <name> [--repo <url>]
//
// Writes a minimal registration (name + repo URL) to
// ~/.grove/projects/<name>/project.yaml. All other config (container, agent,
// start, finish, check) belongs in grove.yaml in the project repo.
func cmdProjectCreate() {
	if len(os.Args) < 4 || os.Args[3] == "" || os.Args[3][0] == '-' {
		fmt.Fprintln(os.Stderr, "usage: grove project create <name> [--repo <url>]")
		os.Exit(1)
	}
	name := os.Args[3]

	fs := flag.NewFlagSet("project create", flag.ExitOnError)
	repo := fs.String("repo", "", "git remote URL (can be added later)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: grove project create <name> [--repo <url>]")
		fs.PrintDefaults()
	}
	fs.Parse(os.Args[4:])

	projectDir := filepath.Join(rootDir(), "projects", name)
	if _, err := os.Stat(filepath.Join(projectDir, "project.yaml")); err == nil {
		fmt.Fprintf(os.Stderr, "grove: project %q already exists at %s\n", name, projectDir)
		os.Exit(1)
	}
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "grove: %v\n", err)
		os.Exit(1)
	}

	yamlPath := filepath.Join(projectDir, "project.yaml")
	content := fmt.Sprintf("name: %s\nrepo: %s\n", name, *repo)
	if err := os.WriteFile(yamlPath, []byte(content), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "grove: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n%s✓  Created project%s %s%q%s\n\n", colorGreen+colorBold, colorReset, colorCyan, name, colorReset)
	fmt.Printf("%sConfig:%s %s%s%s\n\n", colorBold, colorReset, colorCyan, yamlPath, colorReset)
	fmt.Printf("%sNext step:%s\n\n", colorBold, colorReset)
	if *repo == "" {
		fmt.Printf("  %s1.%s Edit the file to set your repo URL\n", colorBold, colorReset)
		fmt.Printf("  %s2.%s Start an instance\n", colorBold, colorReset)
	} else {
		fmt.Printf("  %s1.%s Start an instance\n", colorBold, colorReset)
	}
	fmt.Printf("     %sgrove start %s <branch>%s\n\n", colorDim, name, colorReset)
}

// projectEntry holds the parsed fields grove cares about from a registration.
type projectEntry struct {
	name string
	repo string
}

// loadProjectEntries scans ~/.grove/projects/ and returns all registered
// projects in directory order (alphabetical by folder name).
func loadProjectEntries() []projectEntry {
	projectsDir := filepath.Join(rootDir(), "projects")
	dirEntries, err := os.ReadDir(projectsDir)
	if err != nil {
		return nil
	}

	var entries []projectEntry
	for _, e := range dirEntries {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(projectsDir, e.Name(), "project.yaml"))
		if err != nil {
			continue
		}
		var p struct {
			Name string `yaml:"name"`
			Repo string `yaml:"repo"`
		}
		if err := yaml.Unmarshal(data, &p); err != nil {
			continue
		}
		name := p.Name
		if name == "" {
			name = e.Name()
		}
		repo := p.Repo
		if repo == "" {
			repo = "(no repo)"
		}
		entries = append(entries, projectEntry{name, repo})
	}
	return entries
}

// resolveProject resolves a project argument that may be a 1-based index
// (e.g. "1", "2") or a literal project name. Exits with an error message
// if a numeric index is out of range.
func resolveProject(arg string) string {
	n, err := strconv.Atoi(arg)
	if err != nil {
		return arg // not a number — use as-is
	}
	entries := loadProjectEntries()
	if n < 1 || n > len(entries) {
		fmt.Fprintf(os.Stderr, "grove: project index %d out of range (have %d project(s))\n", n, len(entries))
		os.Exit(1)
	}
	return entries[n-1].name
}

// cmdProjectList handles: grove project list
//
// Scans ~/.grove/projects/ and prints a numbered summary table.
// This is a pure filesystem operation — no daemon required.
func cmdProjectList() {
	entries := loadProjectEntries()
	if len(entries) == 0 {
		fmt.Printf("%sno projects defined%s\n", colorDim, colorReset)
		return
	}

	fmt.Printf("%s%-4s  %-20s  %s%s\n", colorBold, "#", "NAME", "REPO", colorReset)
	fmt.Printf("%s%-4s  %-20s  %s%s\n", colorDim, "----", "--------------------", "----", colorReset)
	for i, e := range entries {
		fmt.Printf("%-4d  %-20s  %s\n", i+1, e.name, e.repo)
	}
}

// cmdProjectDelete handles: grove project delete <name>
//
// Prompts for confirmation (project and all worktrees are removed), then
// deletes the entire project directory under ~/.grove/projects/<name>/.
func cmdProjectDelete() {
	if len(os.Args) < 4 || os.Args[3] == "" {
		fmt.Fprintln(os.Stderr, "usage: grove project delete <name|#>")
		os.Exit(1)
	}
	name := resolveProject(os.Args[3])

	projectDir := filepath.Join(rootDir(), "projects", name)
	yamlPath := filepath.Join(projectDir, "project.yaml")
	if _, err := os.Stat(yamlPath); err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "grove: project %q not found\n", name)
		} else {
			fmt.Fprintf(os.Stderr, "grove: %v\n", err)
		}
		os.Exit(1)
	}

	// Count live instances so the warning can be specific.
	var instanceCount int
	if resp, err := tryRequest(proto.Request{Type: proto.ReqList}); err == nil {
		for _, inst := range resp.Instances {
			if inst.Project == name {
				instanceCount++
			}
		}
	}

	fmt.Printf("\n%s⚠  Remove project%s %s%q%s\n\n", colorYellow+colorBold, colorReset, colorCyan, name, colorReset)
	if instanceCount > 0 {
		fmt.Printf("  This will %sstop and remove %d instance(s)%s, delete all worktrees,\n", colorBold, instanceCount, colorReset)
		fmt.Printf("  and remove the project.\n\n")
	} else {
		fmt.Printf("  This will delete the project and %sall its worktrees%s.\n\n", colorBold, colorReset)
	}
	fmt.Printf("%sContinue?%s [y/N] ", colorBold, colorReset)

	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(answer)
	if answer != "y" && answer != "Y" {
		fmt.Printf("%saborted%s\n", colorDim, colorReset)
		return
	}

	// Drop all instances belonging to this project before removing the
	// project directory, so they don't linger in watch/list.
	if resp, err := tryRequest(proto.Request{Type: proto.ReqList}); err == nil {
		for _, inst := range resp.Instances {
			if inst.Project == name {
				tryRequest(proto.Request{Type: proto.ReqDrop, InstanceID: inst.ID})
			}
		}
	}

	if err := os.RemoveAll(projectDir); err != nil {
		fmt.Fprintf(os.Stderr, "grove: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("\n%s✓  Deleted project%s %s%q%s\n\n", colorGreen+colorBold, colorReset, colorCyan, name, colorReset)
}

func cmdProjectDir() {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: grove project dir <project|#>")
		os.Exit(1)
	}
	project := resolveProject(os.Args[3])
	fmt.Println(filepath.Join(rootDir(), "projects", project, "main"))
}
