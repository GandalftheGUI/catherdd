package envfile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gandalfthegui/grove/internal/envfile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "env")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestLoad(t *testing.T) {
	path := write(t, "FOO=bar\nBAZ=qux\n")
	env := envfile.Load(path)
	assert.Equal(t, "bar", env["FOO"])
	assert.Equal(t, "qux", env["BAZ"])
}

func TestLoadStripsWhitespace(t *testing.T) {
	path := write(t, "  KEY = value  \n")
	env := envfile.Load(path)
	assert.Equal(t, "value", env["KEY"])
}

func TestLoadSkipsCommentsAndBlanks(t *testing.T) {
	path := write(t, "# comment\n\nA=1\n")
	env := envfile.Load(path)
	assert.Equal(t, map[string]string{"A": "1"}, env)
}

func TestLoadMissingFile(t *testing.T) {
	env := envfile.Load("/nonexistent/path/env")
	assert.Empty(t, env)
}

func TestLoadSkipsLinesWithoutEquals(t *testing.T) {
	path := write(t, "NOEQUALS\nA=1\n")
	env := envfile.Load(path)
	assert.Equal(t, map[string]string{"A": "1"}, env)
}
