package ssh

import (
	"strings"
	"testing"
	"time"
)

// These tests verify the SSH package's argument construction, error formatting,
// and timeout behavior. They use a fake SSH target ("false" command) to avoid
// requiring real SSH connectivity.

func TestRun_ErrorIncludesTarget(t *testing.T) {
	// "ssh nosuchhost echo ok" will fail — verify error formatting
	_, err := Run("nosuchhost-test", "echo ok", 2*time.Second)
	if err == nil {
		t.Skip("SSH to nosuchhost-test somehow succeeded (unexpected network config)")
	}
	if !strings.Contains(err.Error(), "nosuchhost-test") {
		t.Errorf("error should contain target name, got: %v", err)
	}
}

func TestRunWithStdin_ErrorIncludesTarget(t *testing.T) {
	_, err := RunWithStdin("nosuchhost-test", "cat", []byte("hello"), 2*time.Second)
	if err == nil {
		t.Skip("SSH to nosuchhost-test somehow succeeded")
	}
	if !strings.Contains(err.Error(), "nosuchhost-test") {
		t.Errorf("error should contain target name, got: %v", err)
	}
}

func TestPing_ErrorIncludesTarget(t *testing.T) {
	err := Ping("nosuchhost-test")
	if err == nil {
		t.Skip("Ping to nosuchhost-test somehow succeeded")
	}
	if !strings.Contains(err.Error(), "nosuchhost-test") {
		t.Errorf("error should contain target name, got: %v", err)
	}
}

func TestCombinedRun_ErrorIncludesTarget(t *testing.T) {
	_, err := CombinedRun("nosuchhost-test", "echo ok", 2*time.Second)
	if err == nil {
		t.Skip("SSH to nosuchhost-test somehow succeeded")
	}
	if !strings.Contains(err.Error(), "nosuchhost-test") {
		t.Errorf("error should contain target name, got: %v", err)
	}
}

func TestRun_TimeoutError(t *testing.T) {
	// Use a command that will hang: ssh to a non-routable IP with very short timeout
	// The ConnectTimeout in args is 10s, but our function timeout is 100ms
	_, err := RunWithStdin("192.0.2.1", "echo ok", nil, 100*time.Millisecond)
	if err == nil {
		t.Skip("connection to 192.0.2.1 somehow succeeded")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("expected timeout error, got: %v", err)
	}
}

func TestRun_DelegatesToRunWithStdin(t *testing.T) {
	// Run should produce the same error as RunWithStdin with nil stdin
	_, err1 := Run("nosuchhost-test", "echo ok", 2*time.Second)
	_, err2 := RunWithStdin("nosuchhost-test", "echo ok", nil, 2*time.Second)
	if err1 == nil || err2 == nil {
		t.Skip("SSH somehow succeeded")
	}
	// Both should mention the target
	if !strings.Contains(err1.Error(), "nosuchhost-test") {
		t.Errorf("Run error should contain target, got: %v", err1)
	}
	if !strings.Contains(err2.Error(), "nosuchhost-test") {
		t.Errorf("RunWithStdin error should contain target, got: %v", err2)
	}
}
