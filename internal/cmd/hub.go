package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/daemon"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

// HubLock records which machine currently owns the Gas Town hub.
// Stored as a JSON file at <townRoot>/daemon/hub.lock.
type HubLock struct {
	Hostname  string    `json:"hostname"`
	ClaimedAt time.Time `json:"claimed_at"`
	PID       int       `json:"pid"`
}

// hubLockPath returns the path to the hub lock file.
func hubLockPath(townRoot string) string {
	return filepath.Join(townRoot, "daemon", "hub.lock")
}

// readHubLock reads the current hub lock, or returns nil if none exists.
func readHubLock(townRoot string) (*HubLock, error) {
	data, err := os.ReadFile(hubLockPath(townRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading hub lock: %w", err)
	}
	var lock HubLock
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, fmt.Errorf("parsing hub lock: %w", err)
	}
	return &lock, nil
}

// writeHubLock writes a hub lock file atomically.
func writeHubLock(townRoot string) error {
	hostname, _ := os.Hostname()
	lock := HubLock{
		Hostname:  hostname,
		ClaimedAt: time.Now(),
		PID:       os.Getpid(),
	}
	data, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling hub lock: %w", err)
	}

	dir := filepath.Dir(hubLockPath(townRoot))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating daemon directory: %w", err)
	}

	tmpPath := hubLockPath(townRoot) + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("writing hub lock: %w", err)
	}
	if err := os.Rename(tmpPath, hubLockPath(townRoot)); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("renaming hub lock: %w", err)
	}
	return nil
}

// removeHubLock removes the hub lock file.
func removeHubLock(townRoot string) error {
	if err := os.Remove(hubLockPath(townRoot)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing hub lock: %w", err)
	}
	return nil
}

var hubCmd = &cobra.Command{
	Use:     "hub",
	GroupID: GroupServices,
	Short:   "Manage the portable Gas Town hub",
	RunE:    requireSubcommand,
	Long: `Manage the portable Gas Town hub across machines.

The hub is the active instance of Gas Town — only one machine can own it
at a time. Use 'gt hub claim' to pull state and start services on this
machine, and 'gt hub release' to push state and stop services before
moving to another machine.

Typical workflow:
  # On machine A (done working):
  gt hub release

  # On machine B (starting work):
  gt hub claim

The hub lock prevents two machines from running simultaneously.`,
}

var hubClaimCmd = &cobra.Command{
	Use:   "claim",
	Short: "Claim the hub on this machine",
	Long: `Claim the Gas Town hub on this machine.

This command:
  1. Checks that no other machine holds the hub lock
  2. Pulls latest Dolt state from DoltHub remotes
  3. Starts the Dolt server
  4. Starts the Gas Town daemon
  5. Sets the hub lock for this machine

Use --force to override a stale lock from a crashed machine.`,
	RunE: runHubClaim,
}

var hubReleaseCmd = &cobra.Command{
	Use:   "release",
	Short: "Release the hub from this machine",
	Long: `Release the Gas Town hub from this machine.

This command:
  1. Parks all rigs (stops agents gracefully)
  2. Syncs Dolt state to DoltHub remotes
  3. Stops the Gas Town daemon
  4. Stops the Dolt server
  5. Removes the hub lock

After release, another machine can claim the hub.`,
	RunE: runHubRelease,
}

var hubStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show hub lock status",
	Long:  `Show which machine currently owns the Gas Town hub.`,
	RunE:  runHubStatus,
}

var hubClaimForce bool

func init() {
	hubCmd.AddCommand(hubClaimCmd)
	hubCmd.AddCommand(hubReleaseCmd)
	hubCmd.AddCommand(hubStatusCmd)

	hubClaimCmd.Flags().BoolVar(&hubClaimForce, "force", false, "Override a stale hub lock")

	rootCmd.AddCommand(hubCmd)
}

func runHubClaim(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	hostname, _ := os.Hostname()

	// Check existing lock
	lock, err := readHubLock(townRoot)
	if err != nil {
		return err
	}
	if lock != nil {
		if lock.Hostname == hostname {
			fmt.Printf("%s Hub already claimed by this machine (%s)\n",
				style.Bold.Render("~"), hostname)
			return nil
		}
		if !hubClaimForce {
			return fmt.Errorf("hub is locked by %s (claimed %s)\nUse --force to override",
				lock.Hostname, lock.ClaimedAt.Format(time.RFC3339))
		}
		fmt.Printf("%s Overriding stale lock from %s\n",
			style.Warning.Render("!"), lock.Hostname)
	}

	config := doltserver.DefaultConfig(townRoot)
	if config.IsRemote() {
		return fmt.Errorf("hub claim requires a local Dolt server (current: remote at %s)", config.HostPort())
	}

	// Step 1: Pull latest Dolt state from DoltHub
	fmt.Printf("Pulling Dolt databases from remotes...\n")
	pullResults := pullAllDatabases(townRoot)
	var pullFailed int
	for _, r := range pullResults {
		switch {
		case r.Pulled:
			fmt.Printf("  %s %s ← %s\n", style.Bold.Render("✓"), r.Database, r.Remote)
		case r.Skipped:
			fmt.Printf("  %s %s — no remote configured\n", style.Dim.Render("○"), r.Database)
		case r.Error != nil:
			fmt.Printf("  %s %s: %v\n", style.Bold.Render("✗"), r.Database, r.Error)
			pullFailed++
		}
	}
	if pullFailed > 0 {
		return fmt.Errorf("%d database(s) failed to pull", pullFailed)
	}

	// Step 2: Start Dolt server
	fmt.Printf("\nStarting Dolt server...\n")
	databases, _ := doltserver.ListDatabases(townRoot)
	if len(databases) == 0 {
		fmt.Printf("  %s No databases found, skipping Dolt start\n", style.Warning.Render("!"))
	} else {
		running, _, _ := doltserver.IsRunning(townRoot)
		if running {
			fmt.Printf("  %s Dolt server already running\n", style.Bold.Render("~"))
		} else {
			if err := doltserver.Start(townRoot); err != nil {
				return fmt.Errorf("starting Dolt server: %w", err)
			}
			state, _ := doltserver.LoadState(townRoot)
			fmt.Printf("  %s Dolt server started (PID %d, port %d)\n",
				style.Bold.Render("✓"), state.PID, config.Port)
		}
	}

	// Step 3: Start daemon
	fmt.Printf("Starting daemon...\n")
	daemonRunning, daemonPid, _ := daemon.IsRunning(townRoot)
	if daemonRunning {
		fmt.Printf("  %s Daemon already running (PID %d)\n", style.Bold.Render("~"), daemonPid)
	} else {
		if err := startDaemonForHub(townRoot); err != nil {
			return fmt.Errorf("starting daemon: %w", err)
		}
		fmt.Printf("  %s Daemon started\n", style.Bold.Render("✓"))
	}

	// Step 4: Write hub lock
	if err := writeHubLock(townRoot); err != nil {
		return fmt.Errorf("writing hub lock: %w", err)
	}
	fmt.Printf("\n%s Hub claimed by %s\n", style.Bold.Render("✓"), hostname)

	return nil
}

func runHubRelease(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	hostname, _ := os.Hostname()

	// Check lock ownership
	lock, err := readHubLock(townRoot)
	if err != nil {
		return err
	}
	if lock == nil {
		fmt.Printf("%s Hub is not locked (nothing to release)\n", style.Bold.Render("~"))
		return nil
	}
	if lock.Hostname != hostname {
		return fmt.Errorf("hub is locked by %s, not this machine (%s)", lock.Hostname, hostname)
	}

	config := doltserver.DefaultConfig(townRoot)
	if config.IsRemote() {
		return fmt.Errorf("hub release requires a local Dolt server (current: remote at %s)", config.HostPort())
	}

	// Step 1: Park all rigs
	fmt.Printf("Parking rigs...\n")
	rigs, discoverErr := discoverAllRigs(townRoot)
	var parkedRigs []string
	if discoverErr != nil {
		fmt.Printf("  %s Could not discover rigs: %v\n", style.Warning.Render("!"), discoverErr)
	} else {
		for _, r := range rigs {
			name := r.Name
			if IsRigParked(townRoot, name) {
				continue
			}
			if err := parkOneRig(name); err != nil {
				fmt.Printf("  %s Failed to park %s: %v\n", style.Warning.Render("!"), name, err)
			} else {
				parkedRigs = append(parkedRigs, name)
				fmt.Printf("  %s Parked %s\n", style.Bold.Render("✓"), name)
			}
		}
	}

	// Step 2: Sync Dolt databases (push to remotes)
	fmt.Printf("\nSyncing Dolt databases to remotes...\n")
	wasRunning, pid, _ := doltserver.IsRunning(townRoot)
	if wasRunning {
		// Stop server for clean push (dolt push needs exclusive access)
		fmt.Printf("Stopping Dolt server (PID %d) for sync...\n", pid)
		if err := doltserver.Stop(townRoot); err != nil {
			fmt.Printf("  %s Could not stop Dolt server: %v\n", style.Warning.Render("!"), err)
		}
	}

	results := doltserver.SyncDatabases(townRoot, doltserver.SyncOptions{})
	var pushed, skipped, failed int
	for _, r := range results {
		switch {
		case r.Pushed:
			fmt.Printf("  %s %s → %s\n", style.Bold.Render("✓"), r.Database, r.Remote)
			pushed++
		case r.Skipped:
			fmt.Printf("  %s %s — no remote\n", style.Dim.Render("○"), r.Database)
			skipped++
		case r.Error != nil:
			fmt.Printf("  %s %s: %v\n", style.Bold.Render("✗"), r.Database, r.Error)
			failed++
		}
	}
	if failed > 0 {
		// Restart server if push failed — don't leave the hub in a broken state
		fmt.Printf("\n%s %d database(s) failed to sync, restarting services...\n",
			style.Bold.Render("✗"), failed)
		if wasRunning {
			_ = doltserver.Start(townRoot)
		}
		for _, name := range parkedRigs {
			_ = unparkOneRig(name)
		}
		return fmt.Errorf("%d database(s) failed to sync — hub NOT released", failed)
	}

	// Step 3: Stop daemon
	fmt.Printf("\nStopping daemon...\n")
	daemonRunning, daemonPid, _ := daemon.IsRunning(townRoot)
	if daemonRunning {
		if err := daemon.StopDaemon(townRoot); err != nil {
			fmt.Printf("  %s Could not stop daemon: %v\n", style.Warning.Render("!"), err)
		} else {
			fmt.Printf("  %s Daemon stopped (was PID %d)\n", style.Bold.Render("✓"), daemonPid)
		}
	} else {
		fmt.Printf("  %s Daemon not running\n", style.Dim.Render("○"))
	}

	// Step 4: Dolt server already stopped above, verify it's down
	if running, _, _ := doltserver.IsRunning(townRoot); running {
		fmt.Printf("Stopping Dolt server...\n")
		if err := doltserver.Stop(townRoot); err != nil {
			fmt.Printf("  %s Could not stop Dolt server: %v\n", style.Warning.Render("!"), err)
		}
	}
	fmt.Printf("  %s Dolt server stopped\n", style.Bold.Render("✓"))

	// Step 5: Remove hub lock
	if err := removeHubLock(townRoot); err != nil {
		return fmt.Errorf("removing hub lock: %w", err)
	}

	fmt.Printf("\n%s Hub released from %s\n", style.Bold.Render("✓"), hostname)
	fmt.Printf("  Pushed: %d, Skipped: %d\n", pushed, skipped)
	return nil
}

func runHubStatus(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	lock, err := readHubLock(townRoot)
	if err != nil {
		return err
	}

	hostname, _ := os.Hostname()

	if lock == nil {
		fmt.Printf("%s Hub is unclaimed (no lock)\n", style.Dim.Render("○"))
	} else {
		owner := lock.Hostname
		if owner == hostname {
			owner += " (this machine)"
		}
		fmt.Printf("%s Hub claimed by: %s\n", style.Bold.Render("🔒"), owner)
		fmt.Printf("  Claimed at: %s\n", lock.ClaimedAt.Format(time.RFC3339))
	}

	// Show service status
	doltRunning, doltPid, _ := doltserver.IsRunning(townRoot)
	if doltRunning {
		fmt.Printf("  Dolt server: running (PID %d)\n", doltPid)
	} else {
		fmt.Printf("  Dolt server: stopped\n")
	}

	daemonRunning, daemonPid, _ := daemon.IsRunning(townRoot)
	if daemonRunning {
		fmt.Printf("  Daemon: running (PID %d)\n", daemonPid)
	} else {
		fmt.Printf("  Daemon: stopped\n")
	}

	return nil
}

// PullResult records the outcome of pulling a single database.
type PullResult struct {
	Database string
	Pulled   bool
	Skipped  bool
	Remote   string
	Error    error
}

// pullAllDatabases pulls all Dolt databases from their configured remotes.
func pullAllDatabases(townRoot string) []PullResult {
	databases, err := doltserver.ListDatabases(townRoot)
	if err != nil {
		return []PullResult{{Database: "(list)", Error: fmt.Errorf("listing databases: %w", err)}}
	}

	var results []PullResult
	for _, db := range databases {
		dbDir := doltserver.RigDatabaseDir(townRoot, db)
		result := PullResult{Database: db}

		remoteName, remoteURL, err := doltserver.FindRemote(dbDir)
		if err != nil {
			result.Error = fmt.Errorf("checking remote: %w", err)
			results = append(results, result)
			continue
		}
		result.Remote = remoteURL

		if remoteURL == "" {
			result.Skipped = true
			results = append(results, result)
			continue
		}

		// Pull from remote
		if err := pullDatabase(dbDir, remoteName); err != nil {
			// "up to date" is not an error
			errMsg := err.Error()
			if strings.Contains(strings.ToLower(errMsg), "up to date") ||
				strings.Contains(strings.ToLower(errMsg), "everything up-to-date") {
				result.Pulled = true
			} else {
				result.Error = err
			}
		} else {
			result.Pulled = true
		}
		results = append(results, result)
	}
	return results
}

// pullDatabase runs dolt pull on a database directory.
func pullDatabase(dbDir, remote string) error {
	cmd := exec.Command("dolt", "pull", remote, "main")
	cmd.Dir = dbDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("dolt pull: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// startDaemonForHub starts the Gas Town daemon in the background.
func startDaemonForHub(townRoot string) error {
	gtPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding executable: %w", err)
	}

	daemonCmd := exec.Command(gtPath, "daemon", "run")
	daemonCmd.Dir = townRoot
	daemonCmd.Stdin = nil
	daemonCmd.Stdout = nil
	daemonCmd.Stderr = nil

	if err := daemonCmd.Start(); err != nil {
		return err
	}

	// Detach — don't wait for the daemon to exit
	go func() { _ = daemonCmd.Wait() }()

	// Wait for it to be ready
	for i := 0; i < 20; i++ {
		time.Sleep(250 * time.Millisecond)
		if running, _, _ := daemon.IsRunning(townRoot); running {
			return nil
		}
	}
	return fmt.Errorf("daemon did not start within 5 seconds")
}
