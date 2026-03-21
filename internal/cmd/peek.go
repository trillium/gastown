package cmd

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

// Peek command flags
var peekLines int

func init() {
	rootCmd.AddCommand(peekCmd)
	peekCmd.Flags().IntVarP(&peekLines, "lines", "n", 100, "Number of lines to capture")
}

var peekCmd = &cobra.Command{
	Use:     "peek <rig/polecat> [count]",
	GroupID: GroupComm,
	Short:   "View recent output from a polecat or crew session",
	Long: `Capture and display recent terminal output from an agent session.

This is the ergonomic alias for 'gt session capture'. Use it to check
what an agent is currently doing or has recently output.

The nudge/peek pair provides the canonical interface for agent sessions:
  gt nudge - send messages TO a session (reliable delivery)
  gt peek  - read output FROM a session (capture-pane wrapper)

Supports polecats, crew workers, and town-level agents:
  - Polecats: rig/name format (e.g., greenplace/furiosa)
  - Crew: rig/crew/name format (e.g., beads/crew/dave)
  - Town-level: mayor, deacon, boot (or hq/mayor, hq/deacon, hq/boot)

Examples:
  gt peek greenplace/furiosa         # Polecat: last 100 lines (default)
  gt peek greenplace/furiosa 50      # Polecat: last 50 lines
  gt peek beads/crew/dave            # Crew: last 100 lines
  gt peek beads/crew/dave -n 200     # Crew: last 200 lines
  gt peek mayor                      # Mayor: last 100 lines
  gt peek deacon -n 50               # Deacon: last 50 lines`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runPeek,
}

func runPeek(cmd *cobra.Command, args []string) error {
	address := args[0]

	// Handle optional positional count argument
	lines := peekLines
	if len(args) > 1 {
		n, err := strconv.Atoi(args[1])
		if err != nil {
			return fmt.Errorf("invalid line count: %s", args[1])
		}
		lines = n
	}

	// Resolve address to tmux session name.
	sessionName, err := resolveToSessionName(address)
	if err != nil {
		return err
	}

	// Try local capture first.
	t := tmux.NewTmux()
	output, err := t.CapturePane(sessionName, lines)
	if err == nil {
		fmt.Print(output)
		return nil
	}

	// Local failed — try remote machines.
	townRoot, twErr := workspace.FindFromCwdOrError()
	if twErr != nil {
		return fmt.Errorf("capturing output: %w (not in a Gas Town workspace for remote fallback)", err)
	}
	output, remoteErr := peekRemote(townRoot, sessionName, lines)
	if remoteErr != nil {
		// Return the original local error — remote was a fallback
		return fmt.Errorf("capturing output: %w", err)
	}

	fmt.Print(output)
	return nil
}

// resolveToSessionName converts a peek address to a tmux session name.
func resolveToSessionName(address string) (string, error) {
	// Town-level agents: mayor, deacon, boot
	townAgentSessions := map[string]string{
		"mayor":     "hq-mayor",
		"hq/mayor":  "hq-mayor",
		"deacon":    "hq-deacon",
		"hq/deacon": "hq-deacon",
		"boot":      "hq-boot",
		"hq/boot":   "hq-boot",
	}
	if sessionName, ok := townAgentSessions[address]; ok {
		return sessionName, nil
	}

	rigName, polecatName, err := parseAddress(address)
	if err != nil {
		if !strings.Contains(address, "/") {
			return "", fmt.Errorf("not in a rig directory. Use full address format: gt peek <rig>/<polecat>")
		}
		return "", err
	}

	rigPrefix := session.PrefixFor(rigName)
	if strings.HasPrefix(polecatName, "crew/") {
		crewName := strings.TrimPrefix(polecatName, "crew/")
		return session.CrewSessionName(rigPrefix, crewName), nil
	}
	return session.PolecatSessionName(rigPrefix, polecatName), nil
}

// peekRemote captures pane output from a session on a satellite machine.
func peekRemote(townRoot, sessionName string, lines int) (string, error) {
	machinesPath := constants.MayorMachinesPath(townRoot)
	machines, err := config.LoadMachinesConfig(machinesPath)
	if err != nil {
		return "", err
	}

	// Find which machine has this session.
	for _, rs := range listAllRemoteSessions(machines) {
		if rs.RawName != sessionName {
			continue
		}
		entry := machines.Machines[rs.Machine]
		if entry == nil {
			continue
		}
		remoteCmd := fmt.Sprintf("tmux -L gt capture-pane -p -t %s -S -%d",
			config.ShellQuote(sessionName), lines)
		return runSSH(entry.SSHTarget(), remoteCmd, 15*time.Second)
	}

	return "", fmt.Errorf("session %q not found on any satellite machine", sessionName)
}
