package daemon

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNextInstanceID(t *testing.T) {
	d := &Daemon{instances: make(map[string]*Instance)}

	d.mu.Lock()

	// First 9 IDs should be digits 1–9.
	for i, want := range []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"} {
		got := d.nextInstanceID()
		assert.Equal(t, want, got, "id #%d", i+1)
		d.instances[got] = &Instance{}
	}

	// Next 26 should be a–z.
	for _, want := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j",
		"k", "l", "m", "n", "o", "p", "q", "r", "s", "t", "u", "v", "w", "x", "y", "z"} {
		got := d.nextInstanceID()
		assert.Equal(t, want, got)
		d.instances[got] = &Instance{}
	}

	// After all 35 single-char slots are taken, IDs become two characters.
	got := d.nextInstanceID()
	assert.Equal(t, 2, len(got), "expected two-char ID after single-char exhaustion, got %q", got)

	d.mu.Unlock()
}

func TestRepoURLHintSuffix(t *testing.T) {
	cases := []struct {
		repo string
		hint bool
	}{
		{"github.com/org/repo", true},
		{"gitlab.com/org/repo", true},
		{"bitbucket.org/org/repo", true},
		{"git@github.com:org/repo.git", false},
		{"https://github.com/org/repo.git", false},
		{"", false},
	}

	for _, tc := range cases {
		suffix := repoURLHintSuffix(tc.repo)
		if tc.hint {
			assert.NotEmpty(t, suffix, "expected hint for %q", tc.repo)
		} else {
			assert.Empty(t, suffix, "expected no hint for %q", tc.repo)
		}
	}
}
