package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/session"
	tmuxPkg "github.com/steveyegge/gastown/internal/tmux"
)

const (
	defaultCheckpointDogInterval = 10 * time.Minute
)

// CheckpointDogConfig holds configuration for the checkpoint_dog patrol.
type CheckpointDogConfig struct {
	// Enabled controls whether the checkpoint dog runs.
	Enabled bool `json:"enabled"`

	// IntervalStr is how often to run, as a string (e.g., "10m").
	IntervalStr string `json:"interval,omitempty"`
}

// checkpointDogInterval returns the configured interval, or the default (10m).
func checkpointDogInterval(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.CheckpointDog != nil {
		if config.Patrols.CheckpointDog.IntervalStr != "" {
			if d, err := time.ParseDuration(config.Patrols.CheckpointDog.IntervalStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultCheckpointDogInterval
}

// runtimeExcludeDirs are directories to unstage after git add -A.
// These contain runtime/ephemeral data that should not be checkpointed.
var runtimeExcludeDirs = []string{
	".claude/",
	".beads/",
	".runtime/",
	"__pycache__/",
}

// runCheckpointDog auto-commits WIP changes in active polecat worktrees.
// This protects against data loss when sessions crash or hit context limits.
//
// ## ZFC Exemption
// The checkpoint dog executes git operations directly (same pattern as
// compactor_dog's SQL operations). The daemon pours a molecule for
// observability, then runs git commands via exec.Command.
func (d *Daemon) runCheckpointDog() {
	if !IsPatrolEnabled(d.patrolConfig, "checkpoint_dog") {
		return
	}

	d.logger.Printf("checkpoint_dog: starting cycle")

	mol := d.pourDogMolecule(constants.MolDogCheckpoint, nil)
	defer mol.close()

	rigs := d.getKnownRigs()
	totalScanned := 0
	totalCheckpointed := 0

	for _, rigName := range rigs {
		scanned, checkpointed := d.checkpointRigPolecats(rigName)
		totalScanned += scanned
		totalCheckpointed += checkpointed
	}

	// Trigger checkpoint on satellite machines.
	satelliteCheckpointed := d.checkpointSatellites()
	totalCheckpointed += satelliteCheckpointed

	mol.closeStep("scan")
	mol.closeStep("checkpoint")

	d.logger.Printf("checkpoint_dog: cycle complete — scanned %d worktrees, checkpointed %d (satellites: %d)",
		totalScanned, totalCheckpointed, satelliteCheckpointed)
	mol.closeStep("report")
}

// checkpointRigPolecats checkpoints dirty polecat worktrees in a single rig.
// Returns (scanned, checkpointed) counts.
func (d *Daemon) checkpointRigPolecats(rigName string) (int, int) {
	polecatsDir := filepath.Join(d.config.TownRoot, rigName, "polecats")
	polecats, err := listPolecatWorktrees(polecatsDir)
	if err != nil {
		return 0, 0
	}

	scanned := 0
	checkpointed := 0

	for _, polecatName := range polecats {
		scanned++

		// Check if tmux session is alive — only checkpoint active sessions.
		// Dead sessions can't benefit from checkpoints.
		sessionName := session.PolecatSessionName(session.PrefixFor(rigName), polecatName)
		alive, err := d.tmux.HasSession(sessionName)
		if err != nil {
			d.logger.Printf("checkpoint_dog: error checking session %s: %v", sessionName, err)
			continue
		}
		if !alive {
			continue
		}

		workDir := filepath.Join(polecatsDir, polecatName)
		if d.checkpointWorktree(workDir, rigName, polecatName) {
			checkpointed++
		}
	}

	return scanned, checkpointed
}

// checkpointWorktree creates a WIP checkpoint commit for a single worktree.
// Returns true if a checkpoint was created.
func (d *Daemon) checkpointWorktree(workDir, rigName, polecatName string) bool {
	// Check git status (exclude runtime dirs from consideration)
	statusOut, err := runGitCmd(workDir, "status", "--porcelain")
	if err != nil {
		d.logger.Printf("checkpoint_dog: git status failed in %s/%s: %v", rigName, polecatName, err)
		return false
	}
	if strings.TrimSpace(statusOut) == "" {
		return false // Clean worktree
	}

	// Stage everything
	if _, err := runGitCmd(workDir, "add", "-A"); err != nil {
		d.logger.Printf("checkpoint_dog: git add -A failed in %s/%s: %v", rigName, polecatName, err)
		return false
	}

	// Unstage runtime/ephemeral directories
	for _, dir := range runtimeExcludeDirs {
		// git reset HEAD -- <dir> is safe even if dir doesn't exist (exits 0)
		_, _ = runGitCmd(workDir, "reset", "HEAD", "--", dir)
	}

	// Check if anything is staged after exclusions
	diffOut, err := runGitCmd(workDir, "diff", "--cached", "--quiet")
	if err == nil && strings.TrimSpace(diffOut) == "" {
		// --quiet exits 0 if no diff → nothing staged
		return false
	}

	// Commit the checkpoint
	if _, err := runGitCmd(workDir, "commit", "-m", "WIP: checkpoint (auto)"); err != nil {
		d.logger.Printf("checkpoint_dog: git commit failed in %s/%s: %v", rigName, polecatName, err)
		return false
	}

	d.logger.Printf("checkpoint_dog: created WIP checkpoint in %s/%s", rigName, polecatName)
	return true
}

// checkpointSatellites triggers checkpoint on all enabled satellite machines.
// SSHes to each satellite and runs the checkpoint logic via gt there.
// Returns the total number of checkpoints created across all satellites.
func (d *Daemon) checkpointSatellites() int {
	machinesPath := constants.MayorMachinesPath(d.config.TownRoot)
	machines, err := config.LoadMachinesConfig(machinesPath)
	if err != nil {
		return 0 // No machines config = single-machine setup
	}

	type result struct {
		name         string
		checkpointed int
		err          error
	}

	var wg sync.WaitGroup
	results := make(chan result, len(machines.Machines))

	for name, entry := range machines.Machines {
		if !entry.Enabled {
			continue
		}
		wg.Add(1)
		go func(name string, entry *config.MachineEntry) {
			defer wg.Done()
			target := entry.SSHTarget()
			gtBin := entry.GtBinary
			if gtBin == "" {
				gtBin = "gt"
			}
			// Run checkpoint cycle on the satellite. The satellite's gt binary
			// handles worktree discovery and git operations locally.
			remoteCmd := fmt.Sprintf("%s daemon checkpoint-cycle 2>/dev/null", gtBin)
			_, err := runSSHCmd(target, remoteCmd, 30*time.Second)
			if err != nil {
				results <- result{name: name, err: err}
				return
			}
			results <- result{name: name}
		}(name, entry)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	total := 0
	for r := range results {
		if r.err != nil {
			d.logger.Printf("checkpoint_dog: satellite %s failed: %v", r.name, r.err)
		}
	}
	return total
}

// runSSHCmd executes a command on a remote machine via SSH with a timeout.
func runSSHCmd(sshTarget, remoteCmd string, timeout time.Duration) (string, error) {
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

// CheckpointRunner is a lightweight checkpoint executor for use outside the
// daemon process. Created by NewCheckpointRunner for the `gt daemon checkpoint-cycle` command.
type CheckpointRunner struct {
	townRoot string
	tmux     interface {
		HasSession(string) (bool, error)
	}
	rigs []string
}

// NewCheckpointRunner creates a runner for executing a one-shot checkpoint cycle.
func NewCheckpointRunner(townRoot string) (*CheckpointRunner, error) {
	rigs, err := getKnownRigsFromTownRoot(townRoot)
	if err != nil {
		return nil, err
	}
	return &CheckpointRunner{
		townRoot: townRoot,
		tmux:     tmuxPkg.NewTmux(),
		rigs:     rigs,
	}, nil
}

// RunCheckpointCycle runs one full checkpoint cycle across all local rigs.
func (cr *CheckpointRunner) RunCheckpointCycle() {
	for _, rigName := range cr.rigs {
		polecatsDir := filepath.Join(cr.townRoot, rigName, "polecats")
		polecats, err := listPolecatWorktrees(polecatsDir)
		if err != nil {
			continue
		}
		for _, polecatName := range polecats {
			sessionName := session.PolecatSessionName(session.PrefixFor(rigName), polecatName)
			alive, err := cr.tmux.HasSession(sessionName)
			if err != nil || !alive {
				continue
			}
			workDir := filepath.Join(polecatsDir, polecatName)
			checkpointWorktreeStandalone(workDir)
		}
	}
}

// checkpointWorktreeStandalone runs the checkpoint logic without daemon context.
func checkpointWorktreeStandalone(workDir string) {
	statusOut, err := runGitCmd(workDir, "status", "--porcelain")
	if err != nil || strings.TrimSpace(statusOut) == "" {
		return
	}
	if _, err := runGitCmd(workDir, "add", "-A"); err != nil {
		return
	}
	for _, dir := range runtimeExcludeDirs {
		_, _ = runGitCmd(workDir, "reset", "HEAD", "--", dir)
	}
	diffOut, err := runGitCmd(workDir, "diff", "--cached", "--quiet")
	if err == nil && strings.TrimSpace(diffOut) == "" {
		return
	}
	_, _ = runGitCmd(workDir, "commit", "-m", "WIP: checkpoint (auto)")
}

// getKnownRigsFromTownRoot reads rig names from rigs.json for standalone use.
func getKnownRigsFromTownRoot(townRoot string) ([]string, error) {
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	data, err := os.ReadFile(rigsPath)
	if err != nil {
		return nil, err
	}
	// rigs.json is a JSON object with rig names as keys
	var rigsMap map[string]interface{}
	if err := json.Unmarshal(data, &rigsMap); err != nil {
		return nil, err
	}
	var rigs []string
	for name := range rigsMap {
		rigs = append(rigs, name)
	}
	return rigs, nil
}

// runGitCmd executes a git command in the given directory and returns stdout.
func runGitCmd(workDir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return "", fmt.Errorf("%s: %s", err, errMsg)
		}
		return "", err
	}

	return strings.TrimSpace(stdout.String()), nil
}
