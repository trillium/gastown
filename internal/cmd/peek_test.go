package cmd

import (
	"fmt"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/session"
)

// --- peekRemote tests (gt-tgw) ---

func setupPeekTestRegistry(t *testing.T) {
	t.Helper()
	orig := session.DefaultRegistry()
	reg := session.NewPrefixRegistry()
	reg.Register("gt", "gastown")
	session.SetDefaultRegistry(reg)
	t.Cleanup(func() { session.SetDefaultRegistry(orig) })
}

func TestPeekRemote_Found(t *testing.T) {
	setupPeekTestRegistry(t)
	withMockSSH(t, func(target, cmd string, _ time.Duration) (string, error) {
		// listAllRemoteSessions call
		if target == "u@10.0.0.2" && !isCapturePaneCmd(cmd) {
			return "gt-gastown-p-Toast\n", nil
		}
		// capture-pane call
		if isCapturePaneCmd(cmd) {
			return "line 1\nline 2\nline 3\n", nil
		}
		return "", nil
	})

	townRoot := writeTempMachinesJSON(t, map[string]*config.MachineEntry{
		"m1": {Host: "10.0.0.2", User: "u", Roles: []string{"worker"}, Enabled: true},
	})

	output, err := peekRemote(townRoot, "gt-gastown-p-Toast", 50)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if output == "" {
		t.Error("expected non-empty output")
	}
}

func TestPeekRemote_NotFound(t *testing.T) {
	setupPeekTestRegistry(t)
	withMockSSH(t, func(_, _ string, _ time.Duration) (string, error) {
		return "gt-gastown-p-Other\n", nil
	})

	townRoot := writeTempMachinesJSON(t, map[string]*config.MachineEntry{
		"m1": {Host: "10.0.0.2", User: "u", Roles: []string{"worker"}, Enabled: true},
	})

	_, err := peekRemote(townRoot, "gt-gastown-p-Missing", 50)
	if err == nil {
		t.Error("expected error for missing session")
	}
}

func TestPeekRemote_SSHFailure(t *testing.T) {
	setupPeekTestRegistry(t)
	withMockSSH(t, func(_, _ string, _ time.Duration) (string, error) {
		return "", fmt.Errorf("ssh timeout")
	})

	townRoot := writeTempMachinesJSON(t, map[string]*config.MachineEntry{
		"m1": {Host: "10.0.0.2", User: "u", Roles: []string{"worker"}, Enabled: true},
	})

	_, err := peekRemote(townRoot, "gt-gastown-p-Toast", 50)
	if err == nil {
		t.Error("expected error when SSH fails")
	}
}

func TestPeekRemote_NoMachinesConfig(t *testing.T) {
	dir := t.TempDir()
	_, err := peekRemote(dir, "gt-gastown-p-Toast", 50)
	if err == nil {
		t.Error("expected error for missing machines config")
	}
}

// isCapturePaneCmd checks if a command string is a tmux capture-pane command.
func isCapturePaneCmd(cmd string) bool {
	return len(cmd) > 20 && cmd[:20] == "tmux -L gt capture-p"
}
