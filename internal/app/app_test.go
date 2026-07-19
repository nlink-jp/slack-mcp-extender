package app

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunDispatch(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantExit   int
		wantStdout string // substring expected on stdout ("" = none checked)
		wantStderr string // substring expected on stderr ("" = none checked)
	}{
		{"no args shows usage on stderr", nil, exitError, "", "Usage:"},
		{"version", []string{"version"}, exitOK, "slack-mcp-extender v1.2.3", ""},
		{"--version alias", []string{"--version"}, exitOK, "slack-mcp-extender v1.2.3", ""},
		{"help", []string{"help"}, exitOK, "Usage:", ""},
		{"-h alias", []string{"-h"}, exitOK, "Usage:", ""},
		{"unknown command", []string{"bogus"}, exitError, "", `unknown command "bogus"`},
		{"mcp stub", []string{"mcp"}, exitError, "", "not implemented"},
		{"init stub", []string{"init"}, exitError, "", "not implemented"},
		{"login stub", []string{"login"}, exitError, "", "not implemented"},
		{"config stub", []string{"config"}, exitError, "", "not implemented"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			got := Run(tt.args, "v1.2.3", &stdout, &stderr)
			if got != tt.wantExit {
				t.Errorf("Run(%v) exit = %d, want %d", tt.args, got, tt.wantExit)
			}
			if tt.wantStdout != "" && !strings.Contains(stdout.String(), tt.wantStdout) {
				t.Errorf("stdout = %q, want substring %q", stdout.String(), tt.wantStdout)
			}
			if tt.wantStderr != "" && !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want substring %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

func TestUsageListsAllCommands(t *testing.T) {
	var buf bytes.Buffer
	usage(&buf)
	for _, cmd := range []string{"mcp", "init", "login", "config", "version"} {
		if !strings.Contains(buf.String(), cmd) {
			t.Errorf("usage output missing command %q", cmd)
		}
	}
}
