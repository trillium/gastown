package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/witness"
	"github.com/steveyegge/gastown/internal/workspace"
)

var (
	patrolScanJSON    bool
	patrolScanNotify  bool
	patrolScanRig     string
	patrolScanVerbose bool
)

var patrolScanCmd = &cobra.Command{
	Use:   "scan",
	Short: "Scan polecats for zombies, stalls, and completions",
	Long: `Run proactive detection across all polecats in a rig.

This command bridges the witness library detection functions to the CLI,
providing a single command for the survey-workers patrol step.

Detections:
  - Zombies: Dead sessions with active agent state, dead agent processes,
    stuck done-intent, closed beads with live sessions
  - Stalls: Agents stuck at startup prompts
  - Completions: Agent bead metadata indicating gt done was called

Actions taken automatically:
  - Zombie restart: Sessions are restarted (not nuked) to preserve worktrees
  - Cleanup wisps: Created for dirty state tracking
  - Completion routing: MR cleanup wisps created, refinery nudged

Use --notify to send mail when zombies with active work are detected.

Examples:
  gt patrol scan                    # Scan current rig
  gt patrol scan --rig gastown      # Scan specific rig
  gt patrol scan --json             # Machine-readable output
  gt patrol scan --notify           # Send mail on zombie detection`,
	RunE: runPatrolScan,
}

func init() {
	patrolScanCmd.Flags().BoolVar(&patrolScanJSON, "json", false, "Output as JSON")
	patrolScanCmd.Flags().BoolVar(&patrolScanNotify, "notify", false, "Send mail to witness/mayor when active-work zombies are detected")
	patrolScanCmd.Flags().StringVar(&patrolScanRig, "rig", "", "Rig to scan (default: infer from cwd or GT_RIG)")
	patrolScanCmd.Flags().BoolVarP(&patrolScanVerbose, "verbose", "v", false, "Verbose output")

	patrolCmd.AddCommand(patrolScanCmd)
}

// PatrolScanOutput is the JSON output format for patrol scan results.
type PatrolScanOutput struct {
	Rig         string                    `json:"rig"`
	Timestamp   string                    `json:"timestamp"`
	Zombies     *PatrolScanZombieOutput   `json:"zombies"`
	Stalls      *PatrolScanStallOutput    `json:"stalls,omitempty"`
	Completions *PatrolScanCompleteOutput `json:"completions,omitempty"`
	Receipts    []witness.PatrolReceipt   `json:"receipts,omitempty"`
}

// PatrolScanZombieOutput holds zombie detection results.
type PatrolScanZombieOutput struct {
	Checked int                    `json:"checked"`
	Found   int                    `json:"found"`
	Zombies []PatrolScanZombieItem `json:"zombies,omitempty"`
	Errors  []string               `json:"errors,omitempty"`
}

// PatrolScanZombieItem is a single zombie detection in scan output.
type PatrolScanZombieItem struct {
	Polecat        string `json:"polecat"`
	Classification string `json:"classification"`
	AgentState     string `json:"agent_state"`
	HookBead       string `json:"hook_bead,omitempty"`
	CleanupStatus  string `json:"cleanup_status,omitempty"`
	Action         string `json:"action"`
	WasActive      bool   `json:"was_active"`
	Error          string `json:"error,omitempty"`
}

// PatrolScanStallOutput holds stall detection results.
type PatrolScanStallOutput struct {
	Checked int                   `json:"checked"`
	Found   int                   `json:"found"`
	Stalls  []PatrolScanStallItem `json:"stalls,omitempty"`
}

// PatrolScanStallItem is a single stall detection in scan output.
type PatrolScanStallItem struct {
	Polecat   string `json:"polecat"`
	StallType string `json:"stall_type"`
	Action    string `json:"action"`
	Error     string `json:"error,omitempty"`
}

// PatrolScanCompleteOutput holds completion discovery results.
type PatrolScanCompleteOutput struct {
	Checked   int                       `json:"checked"`
	Found     int                       `json:"found"`
	Completed []PatrolScanCompleteItem  `json:"completed,omitempty"`
}

// PatrolScanCompleteItem is a single completion discovery in scan output.
type PatrolScanCompleteItem struct {
	Polecat        string `json:"polecat"`
	ExitType       string `json:"exit_type"`
	IssueID        string `json:"issue_id,omitempty"`
	MRID           string `json:"mr_id,omitempty"`
	Branch         string `json:"branch,omitempty"`
	Action         string `json:"action"`
	WispCreated    string `json:"wisp_created,omitempty"`
	CompletionTime string `json:"completion_time,omitempty"`
}

func runPatrolScan(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Determine rig name
	rigName := patrolScanRig
	if rigName == "" {
		// Try GT_RIG env, then infer from cwd
		rigName = os.Getenv("GT_RIG")
		if rigName == "" {
			rigName, err = inferRigFromCwd(townRoot)
			if err != nil {
				return fmt.Errorf("could not determine rig: %w\nUse --rig to specify", err)
			}
		}
	}

	bd := witness.DefaultBdCli()
	router := mail.NewRouter(townRoot)
	workDir := townRoot

	timestamp := time.Now().UTC().Format(time.RFC3339)

	// Run all three detection passes locally.
	zombieResult := witness.DetectZombiePolecats(bd, workDir, rigName, router)
	stallResult := witness.DetectStalledPolecats(workDir, rigName)
	completionResult := witness.DiscoverCompletions(bd, workDir, rigName, router)
	receipts := witness.BuildPatrolReceipts(rigName, zombieResult)

	// Automatically scan satellites if machines.json exists.
	remoteResults := scanSatellites(townRoot)

	if patrolScanNotify && zombieResult != nil {
		activeZombies := countActiveWorkZombies(zombieResult)
		if activeZombies > 0 {
			sendZombieNotification(router, rigName, zombieResult, activeZombies)
		}
	}

	if patrolScanJSON {
		return outputPatrolScanJSONWithSatellites(rigName, timestamp, zombieResult, stallResult, completionResult, receipts, remoteResults)
	}

	return outputPatrolScanHumanWithSatellites(rigName, zombieResult, stallResult, completionResult, receipts, remoteResults)
}

func countActiveWorkZombies(result *witness.DetectZombiePolecatsResult) int {
	count := 0
	for _, z := range result.Zombies {
		if z.WasActive {
			count++
		}
	}
	return count
}

func sendZombieNotification(router *mail.Router, rigName string, result *witness.DetectZombiePolecatsResult, activeCount int) {
	var lines []string
	lines = append(lines, fmt.Sprintf("Patrol scan detected %d zombie(s) with active work in rig %s:", activeCount, rigName))
	lines = append(lines, "")
	for _, z := range result.Zombies {
		if !z.WasActive {
			continue
		}
		line := fmt.Sprintf("- %s: %s (hook=%s, action=%s)",
			z.PolecatName, string(z.Classification), z.HookBead, z.Action)
		if z.Error != nil {
			line += fmt.Sprintf(" [error: %v]", z.Error)
		}
		lines = append(lines, line)
	}

	body := strings.Join(lines, "\n")
	subject := fmt.Sprintf("ZOMBIE_DETECTED: %d active-work zombie(s) in %s", activeCount, rigName)

	// Send to witness (best-effort)
	msg := &mail.Message{
		From:    fmt.Sprintf("%s/witness", rigName),
		To:      fmt.Sprintf("%s/witness", rigName),
		Subject: subject,
		Body:    body,
	}
	_ = router.Send(msg)
}

// SatelliteScanResult holds scan results from one satellite machine.
type SatelliteScanResult struct {
	Machine string            `json:"machine"`
	Scan    *PatrolScanOutput `json:"scan,omitempty"`
	Error   string            `json:"error,omitempty"`
}

// scanSatellites runs `gt patrol scan --json` on all enabled satellites via SSH.
// Returns nil if no machines.json exists (single-machine setup). Each satellite
// detects AND restarts zombies locally — this just collects the results.
func scanSatellites(townRoot string) []SatelliteScanResult {
	machinesPath := constants.MayorMachinesPath(townRoot)
	machines, err := config.LoadMachinesConfig(machinesPath)
	if err != nil {
		return nil // No machines config = single-machine setup
	}

	results := runOnSatellites(machines, func(gtBin string) string {
		remoteCmd := gtBin + " patrol scan --json"
		if patrolScanRig != "" {
			remoteCmd += " --rig " + config.ShellQuote(patrolScanRig)
		}
		if patrolScanNotify {
			remoteCmd += " --notify"
		}
		return remoteCmd
	}, 30*time.Second)

	var satellite []SatelliteScanResult
	for _, r := range results {
		sr := SatelliteScanResult{Machine: r.Machine}
		if r.Err != nil {
			sr.Error = r.Err.Error()
		} else {
			var scan PatrolScanOutput
			if err := json.Unmarshal([]byte(r.Output), &scan); err != nil {
				sr.Error = fmt.Sprintf("parsing scan JSON: %v", err)
			} else {
				sr.Scan = &scan
			}
		}
		satellite = append(satellite, sr)
	}
	return satellite
}

func outputPatrolScanJSONWithSatellites(rigName, timestamp string, zombieResult *witness.DetectZombiePolecatsResult, stallResult *witness.DetectStalledPolecatsResult, completionResult *witness.DiscoverCompletionsResult, receipts []witness.PatrolReceipt, satellites []SatelliteScanResult) error {
	output := buildPatrolScanOutput(rigName, timestamp, zombieResult, stallResult, completionResult, receipts)
	if len(satellites) == 0 {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(output)
	}

	// Wrap in fleet structure when satellites are present
	type fleetOutput struct {
		Local      *PatrolScanOutput     `json:"local"`
		Satellites []SatelliteScanResult `json:"satellites"`
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(fleetOutput{Local: output, Satellites: satellites})
}

func outputPatrolScanHumanWithSatellites(rigName string, zombieResult *witness.DetectZombiePolecatsResult, stallResult *witness.DetectStalledPolecatsResult, completionResult *witness.DiscoverCompletionsResult, _ []witness.PatrolReceipt, satellites []SatelliteScanResult) error {
	// Print local results using existing format
	fmt.Printf("%s Patrol scan: %s\n\n", style.Bold.Render("🔍"), rigName)

	if zombieResult != nil {
		fmt.Printf("%s Zombie Detection: checked %d polecat(s)\n",
			style.Bold.Render("👻"), zombieResult.Checked)

		if len(zombieResult.Zombies) == 0 {
			fmt.Printf("  %s\n", style.Dim.Render("No zombies detected"))
		} else {
			for _, z := range zombieResult.Zombies {
				icon := "⚠"
				if z.WasActive {
					icon = "🚨"
				}
				fmt.Printf("  %s %s: %s\n", icon, z.PolecatName, z.Classification)
				fmt.Printf("    State: %s", z.AgentState)
				if z.HookBead != "" {
					fmt.Printf("  Hook: %s", z.HookBead)
				}
				if z.CleanupStatus != "" {
					fmt.Printf("  Cleanup: %s", z.CleanupStatus)
				}
				fmt.Println()
				fmt.Printf("    Action: %s\n", z.Action)
				if z.Error != nil {
					fmt.Printf("    %s\n", style.Dim.Render(fmt.Sprintf("Error: %v", z.Error)))
				}
			}
		}

		if len(zombieResult.Errors) > 0 && patrolScanVerbose {
			fmt.Printf("  Errors: %d\n", len(zombieResult.Errors))
			for _, e := range zombieResult.Errors {
				fmt.Printf("    - %v\n", e)
			}
		}

		if len(zombieResult.ConvoyFailures) > 0 {
			fmt.Printf("  Convoy failures: %d\n", len(zombieResult.ConvoyFailures))
		}
		fmt.Println()
	}

	if stallResult != nil && (len(stallResult.Stalled) > 0 || patrolScanVerbose) {
		fmt.Printf("%s Stall Detection: checked %d polecat(s)\n",
			style.Bold.Render("⏳"), stallResult.Checked)

		if len(stallResult.Stalled) == 0 {
			fmt.Printf("  %s\n", style.Dim.Render("No stalls detected"))
		} else {
			for _, s := range stallResult.Stalled {
				fmt.Printf("  ⚠ %s: %s → %s\n", s.PolecatName, s.StallType, s.Action)
				if s.Error != nil {
					fmt.Printf("    %s\n", style.Dim.Render(fmt.Sprintf("Error: %v", s.Error)))
				}
			}
		}
		fmt.Println()
	}

	if completionResult != nil && (len(completionResult.Discovered) > 0 || patrolScanVerbose) {
		fmt.Printf("%s Completion Discovery: checked %d polecat(s)\n",
			style.Bold.Render("✅"), completionResult.Checked)

		if len(completionResult.Discovered) == 0 {
			fmt.Printf("  %s\n", style.Dim.Render("No completions discovered"))
		} else {
			for _, d := range completionResult.Discovered {
				fmt.Printf("  ● %s: exit=%s", d.PolecatName, d.ExitType)
				if d.IssueID != "" {
					fmt.Printf("  issue=%s", d.IssueID)
				}
				if d.MRID != "" {
					fmt.Printf("  mr=%s", d.MRID)
				}
				fmt.Println()
				fmt.Printf("    Action: %s\n", d.Action)
			}
		}
		fmt.Println()
	}

	// Local summary
	zombieCount := 0
	activeCount := 0
	if zombieResult != nil {
		zombieCount = len(zombieResult.Zombies)
		activeCount = countActiveWorkZombies(zombieResult)
	}
	stallCount := 0
	if stallResult != nil {
		stallCount = len(stallResult.Stalled)
	}
	completionCount := 0
	if completionResult != nil {
		completionCount = len(completionResult.Discovered)
	}

	if zombieCount == 0 && stallCount == 0 && completionCount == 0 {
		fmt.Printf("%s All clear — no issues detected\n", style.Success.Render("✓"))
	} else {
		fmt.Printf("Summary: %d zombie(s) (%d active-work), %d stall(s), %d completion(s)\n",
			zombieCount, activeCount, stallCount, completionCount)
	}

	// Satellite results (printed automatically when present)
	if len(satellites) > 0 {
		fmt.Printf("\n%s\n", style.Bold.Render("Satellites"))
		for _, sr := range satellites {
			fmt.Printf("\n── %s ──\n", style.Bold.Render(sr.Machine))
			if sr.Error != "" {
				fmt.Printf("  %s %s\n", style.Error.Render("✗"), sr.Error)
				continue
			}
			printPatrolScanSummary(sr.Scan)
		}
	}

	return nil
}

// buildPatrolScanOutput constructs a PatrolScanOutput from detection results.
func buildPatrolScanOutput(rigName, timestamp string, zombieResult *witness.DetectZombiePolecatsResult, stallResult *witness.DetectStalledPolecatsResult, completionResult *witness.DiscoverCompletionsResult, receipts []witness.PatrolReceipt) *PatrolScanOutput {
	output := &PatrolScanOutput{
		Rig:       rigName,
		Timestamp: timestamp,
		Receipts:  receipts,
	}

	if zombieResult != nil {
		zo := &PatrolScanZombieOutput{
			Checked: zombieResult.Checked,
			Found:   len(zombieResult.Zombies),
		}
		for _, z := range zombieResult.Zombies {
			item := PatrolScanZombieItem{
				Polecat:        z.PolecatName,
				Classification: string(z.Classification),
				AgentState:     z.AgentState,
				HookBead:       z.HookBead,
				CleanupStatus:  z.CleanupStatus,
				Action:         z.Action,
				WasActive:      z.WasActive,
			}
			if z.Error != nil {
				item.Error = z.Error.Error()
			}
			zo.Zombies = append(zo.Zombies, item)
		}
		for _, e := range zombieResult.Errors {
			zo.Errors = append(zo.Errors, e.Error())
		}
		output.Zombies = zo
	}

	if stallResult != nil {
		so := &PatrolScanStallOutput{
			Checked: stallResult.Checked,
			Found:   len(stallResult.Stalled),
		}
		for _, s := range stallResult.Stalled {
			item := PatrolScanStallItem{
				Polecat:   s.PolecatName,
				StallType: s.StallType,
				Action:    s.Action,
			}
			if s.Error != nil {
				item.Error = s.Error.Error()
			}
			so.Stalls = append(so.Stalls, item)
		}
		output.Stalls = so
	}

	if completionResult != nil {
		co := &PatrolScanCompleteOutput{
			Checked: completionResult.Checked,
			Found:   len(completionResult.Discovered),
		}
		for _, d := range completionResult.Discovered {
			co.Completed = append(co.Completed, PatrolScanCompleteItem{
				Polecat:        d.PolecatName,
				ExitType:       d.ExitType,
				IssueID:        d.IssueID,
				MRID:           d.MRID,
				Branch:         d.Branch,
				Action:         d.Action,
				WispCreated:    d.WispCreated,
				CompletionTime: d.CompletionTime,
			})
		}
		output.Completions = co
	}

	return output
}

// printPatrolScanSummary displays a compact summary of a patrol scan result.
func printPatrolScanSummary(scan *PatrolScanOutput) {
	if scan == nil {
		fmt.Printf("  %s no data\n", style.Dim.Render("—"))
		return
	}

	zombieCount := 0
	activeCount := 0
	if scan.Zombies != nil {
		zombieCount = scan.Zombies.Found
		for _, z := range scan.Zombies.Zombies {
			if z.WasActive {
				activeCount++
			}
		}
	}

	stallCount := 0
	if scan.Stalls != nil {
		stallCount = scan.Stalls.Found
	}

	completionCount := 0
	if scan.Completions != nil {
		completionCount = scan.Completions.Found
	}

	if zombieCount == 0 && stallCount == 0 && completionCount == 0 {
		checked := 0
		if scan.Zombies != nil {
			checked = scan.Zombies.Checked
		}
		fmt.Printf("  %s all clear (checked %d)\n", style.Success.Render("✓"), checked)
		return
	}

	fmt.Printf("  rig: %s  zombies: %d (%d active)  stalls: %d  completions: %d\n",
		scan.Rig, zombieCount, activeCount, stallCount, completionCount)

	if scan.Zombies == nil {
		return
	}
	for _, z := range scan.Zombies.Zombies {
		icon := "⚠"
		if z.WasActive {
			icon = "🚨"
		}
		fmt.Printf("    %s %s: %s → %s\n", icon, z.Polecat, z.Classification, z.Action)
	}
}
