package ssh

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Run executes a command on a remote machine via SSH.
func Run(target, remoteCmd string, timeout time.Duration) (string, error) {
	return RunWithStdin(target, remoteCmd, nil, timeout)
}

// RunWithStdin executes a command on a remote machine via SSH, optionally piping stdin.
func RunWithStdin(target, remoteCmd string, stdin []byte, timeout time.Duration) (string, error) {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "StrictHostKeyChecking=accept-new",
		target,
		remoteCmd,
	}
	cmd := exec.Command("ssh", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()

	select {
	case err := <-done:
		if err != nil {
			return "", fmt.Errorf("ssh %s: %w\nstderr: %s", target, err, stderr.String())
		}
		return stdout.String(), nil
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		return "", fmt.Errorf("ssh %s: timed out after %s", target, timeout)
	}
}

// Ping checks SSH connectivity with a short timeout.
func Ping(target string) error {
	_, err := Run(target, "echo ok", 5*time.Second)
	if err != nil {
		return fmt.Errorf("ping %s: %w", target, err)
	}
	return nil
}

// CombinedRun executes a command on a remote machine via SSH, returning combined stdout+stderr.
// Uses a shorter ConnectTimeout (5s) suitable for doctor checks.
func CombinedRun(target, remoteCmd string, timeout time.Duration) (string, error) {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=5",
		"-o", "StrictHostKeyChecking=accept-new",
		target,
		remoteCmd,
	}
	cmd := exec.Command("ssh", args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()

	select {
	case err := <-done:
		if err != nil {
			return "", fmt.Errorf("ssh %s: %w (stderr: %s)", target, err, strings.TrimSpace(stderr.String()))
		}
		return stdout.String(), nil
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		return "", fmt.Errorf("ssh %s: timed out after %s", target, timeout)
	}
}
