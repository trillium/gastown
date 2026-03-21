// Package satellite provides utilities for cross-machine agent enumeration
// and SSH communication with satellite machines in a Gas Town fleet.
package satellite

import (
	"bytes"
	"fmt"
	"os/exec"
	"time"
)

// RunSSH executes a command on a remote machine via SSH with BatchMode and timeout.
func RunSSH(sshTarget, remoteCmd string, timeout time.Duration) (string, error) {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "StrictHostKeyChecking=accept-new",
		sshTarget,
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
			return "", fmt.Errorf("ssh %s: %w\nstderr: %s", sshTarget, err, stderr.String())
		}
		return stdout.String(), nil
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		return "", fmt.Errorf("ssh %s: timed out after %s", sshTarget, timeout)
	}
}
