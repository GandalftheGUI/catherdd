//go:build darwin

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestXmlEscape(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"plain", "plain"},
		{"a&b", "a&amp;b"},
		{"<tag>", "&lt;tag&gt;"},
		{"a<b&c>d", "a&lt;b&amp;c&gt;d"},
		{"", ""},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, xmlEscape(tc.in))
	}
}

func TestBuildPlistContainsFields(t *testing.T) {
	plist := buildPlist("/usr/local/bin/groved", "/home/user/.grove", "/home/user/.grove/daemon.log", "/usr/bin:/usr/local/bin")
	assert.Contains(t, plist, "com.grove.daemon")
	assert.Contains(t, plist, "/usr/local/bin/groved")
	assert.Contains(t, plist, "/home/user/.grove")
	assert.Contains(t, plist, "/home/user/.grove/daemon.log")
}

func TestBuildPlistEscapesSpecialChars(t *testing.T) {
	plist := buildPlist("/path/to/groved", "/root&dir", "/log<file>", "/usr/bin")
	assert.Contains(t, plist, "&amp;")
	assert.Contains(t, plist, "&lt;")
	assert.Contains(t, plist, "&gt;")
}
