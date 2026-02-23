package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatUptime(t *testing.T) {
	cases := []struct {
		secs int64
		want string
	}{
		{0, "0s"},
		{1, "1s"},
		{59, "59s"},
		{60, "1m00s"},
		{90, "1m30s"},
		{3599, "59m59s"},
		{3600, "1h00m"},
		{3661, "1h01m"},
		{7322, "2h02m"},
		{-5, "0s"}, // negative clamped to zero
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, formatUptime(tc.secs), "secs=%d", tc.secs)
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		s    string
		n    int
		want string
	}{
		{"hello", 0, ""},
		{"hi", 5, "hi"},
		{"hello", 5, "hello"},
		{"hello world", 5, "he..."},
		{"hello world", 3, "hel"}, // n<=3: no ellipsis
		{"hello world", 8, "hello..."},
		{"", 5, ""},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, truncate(tc.s, tc.n), "truncate(%q, %d)", tc.s, tc.n)
	}
}

func TestColorState(t *testing.T) {
	// Each known state returns a non-empty ANSI escape.
	for _, state := range []string{"RUNNING", "WAITING", "ATTACHED", "EXITED", "CRASHED", "KILLED", "FINISHED"} {
		assert.NotEmpty(t, colorState(state), "expected color for state %q", state)
	}
	// Unknown state returns empty string (no color).
	assert.Empty(t, colorState("UNKNOWN"))
}

func TestLoadProjectEntries(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GROVE_ROOT", dir)

	for _, name := range []string{"alpha", "beta", "gamma"} {
		projectDir := filepath.Join(dir, "projects", name)
		require.NoError(t, os.MkdirAll(projectDir, 0o755))
		content := "name: " + name + "\nrepo: git@github.com:org/" + name + ".git\n"
		require.NoError(t, os.WriteFile(filepath.Join(projectDir, "project.yaml"), []byte(content), 0o644))
	}

	entries := loadProjectEntries()
	require.Len(t, entries, 3)
	assert.Equal(t, "alpha", entries[0].name)
	assert.Equal(t, "beta", entries[1].name)
	assert.Equal(t, "gamma", entries[2].name)
}

func TestLoadProjectEntriesEmpty(t *testing.T) {
	t.Setenv("GROVE_ROOT", t.TempDir())
	assert.Empty(t, loadProjectEntries())
}

func TestLoadProjectEntriesSkipsNonYAML(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GROVE_ROOT", dir)

	// Valid project.
	projectDir := filepath.Join(dir, "projects", "real")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "project.yaml"), []byte("name: real\n"), 0o644))

	// Directory with no project.yaml — should be silently skipped.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "projects", "empty"), 0o755))

	entries := loadProjectEntries()
	require.Len(t, entries, 1)
	assert.Equal(t, "real", entries[0].name)
}

func TestResolveProjectByName(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GROVE_ROOT", dir)

	projectDir := filepath.Join(dir, "projects", "my-app")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "project.yaml"), []byte("name: my-app\n"), 0o644))

	assert.Equal(t, "my-app", resolveProject("my-app"))
}

func TestResolveProjectByNumber(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GROVE_ROOT", dir)

	for _, name := range []string{"alpha", "beta"} {
		projectDir := filepath.Join(dir, "projects", name)
		require.NoError(t, os.MkdirAll(projectDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(projectDir, "project.yaml"), []byte("name: "+name+"\n"), 0o644))
	}

	assert.Equal(t, "alpha", resolveProject("1"))
	assert.Equal(t, "beta", resolveProject("2"))
}
