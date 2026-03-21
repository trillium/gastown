package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/witness"
)

func TestPatrolScanOutputJSON(t *testing.T) {
	output := PatrolScanOutput{
		Rig:       "gastown",
		Timestamp: "2026-03-17T12:00:00Z",
		Zombies: &PatrolScanZombieOutput{
			Checked: 3,
			Found:   1,
			Zombies: []PatrolScanZombieItem{
				{
					Polecat:        "alpha",
					Classification: "session-dead-active",
					AgentState:     "working",
					HookBead:       "gas-abc",
					Action:         "restarted",
					WasActive:      true,
				},
			},
		},
		Receipts: []witness.PatrolReceipt{
			{
				Rig:               "gastown",
				Polecat:           "alpha",
				Verdict:           witness.PatrolVerdictStale,
				RecommendedAction: "restarted",
				Evidence: witness.PatrolReceiptEvidence{
					AgentState:     "working",
					Classification: witness.ZombieSessionDeadActive,
					HookBead:       "gas-abc",
				},
			},
		},
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("failed to marshal output: %v", err)
	}

	var parsed PatrolScanOutput
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal output: %v", err)
	}

	if parsed.Rig != "gastown" {
		t.Errorf("Rig = %q, want %q", parsed.Rig, "gastown")
	}
	if parsed.Zombies.Found != 1 {
		t.Errorf("Zombies.Found = %d, want 1", parsed.Zombies.Found)
	}
	if parsed.Zombies.Checked != 3 {
		t.Errorf("Zombies.Checked = %d, want 3", parsed.Zombies.Checked)
	}
	if len(parsed.Zombies.Zombies) != 1 {
		t.Fatalf("len(Zombies) = %d, want 1", len(parsed.Zombies.Zombies))
	}
	z := parsed.Zombies.Zombies[0]
	if z.Polecat != "alpha" {
		t.Errorf("zombie Polecat = %q, want %q", z.Polecat, "alpha")
	}
	if z.Classification != "session-dead-active" {
		t.Errorf("zombie Classification = %q, want %q", z.Classification, "session-dead-active")
	}
	if !z.WasActive {
		t.Error("zombie WasActive = false, want true")
	}
	if len(parsed.Receipts) != 1 {
		t.Fatalf("len(Receipts) = %d, want 1", len(parsed.Receipts))
	}
	if parsed.Receipts[0].Verdict != witness.PatrolVerdictStale {
		t.Errorf("receipt Verdict = %q, want %q", parsed.Receipts[0].Verdict, witness.PatrolVerdictStale)
	}
}

func TestCountActiveWorkZombies(t *testing.T) {
	result := &witness.DetectZombiePolecatsResult{
		Zombies: []witness.ZombieResult{
			{PolecatName: "alpha", WasActive: true},
			{PolecatName: "beta", WasActive: false},
			{PolecatName: "gamma", WasActive: true},
		},
	}

	got := countActiveWorkZombies(result)
	if got != 2 {
		t.Errorf("countActiveWorkZombies() = %d, want 2", got)
	}
}

func TestCountActiveWorkZombies_Empty(t *testing.T) {
	result := &witness.DetectZombiePolecatsResult{}
	got := countActiveWorkZombies(result)
	if got != 0 {
		t.Errorf("countActiveWorkZombies() = %d, want 0", got)
	}
}

func TestPatrolScanZombieItemSerialization(t *testing.T) {
	item := PatrolScanZombieItem{
		Polecat:        "obsidian",
		Classification: "agent-dead-in-session",
		AgentState:     "working",
		HookBead:       "gas-xyz",
		CleanupStatus:  "has_uncommitted",
		Action:         "restarted-dirty (cleanup_status=has_uncommitted, wisp=gas-wisp-123)",
		WasActive:      true,
		Error:          "restart failed: tmux error",
	}

	data, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("failed to marshal item: %v", err)
	}

	var parsed PatrolScanZombieItem
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal item: %v", err)
	}

	if parsed.Polecat != "obsidian" {
		t.Errorf("Polecat = %q, want %q", parsed.Polecat, "obsidian")
	}
	if parsed.CleanupStatus != "has_uncommitted" {
		t.Errorf("CleanupStatus = %q, want %q", parsed.CleanupStatus, "has_uncommitted")
	}
	if parsed.Error != "restart failed: tmux error" {
		t.Errorf("Error = %q, want %q", parsed.Error, "restart failed: tmux error")
	}
}

// --- scanSatellites tests (gt-0li) ---

// writeTempMachinesJSON creates a temp town root with machines.json for testing.
func writeTempMachinesJSON(t *testing.T, machines map[string]*config.MachineEntry) string {
	t.Helper()
	dir := t.TempDir()
	mayorDir := filepath.Join(dir, "mayor")
	if err := os.MkdirAll(mayorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.MachinesConfig{
		Type:     "machines",
		Version:  1,
		DoltHost: "10.0.0.1",
		DoltPort: 3307,
		Machines: machines,
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "machines.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestScanSatellites_HappyPath(t *testing.T) {
	scanJSON := `{"rig":"gastown","timestamp":"2026-03-20T12:00:00Z","zombies":{"checked":2,"found":0,"zombies":[]}}`
	withMockSSH(t, func(_, _ string, _ time.Duration) (string, error) {
		return scanJSON, nil
	})

	townRoot := writeTempMachinesJSON(t, map[string]*config.MachineEntry{
		"m1": {Host: "10.0.0.2", User: "u", Roles: []string{"worker"}, Enabled: true},
	})

	results := scanSatellites(townRoot)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Machine != "m1" {
		t.Errorf("machine = %q, want %q", results[0].Machine, "m1")
	}
	if results[0].Error != "" {
		t.Errorf("unexpected error: %s", results[0].Error)
	}
	if results[0].Scan == nil {
		t.Fatal("expected non-nil scan result")
	}
	if results[0].Scan.Zombies.Checked != 2 {
		t.Errorf("zombies checked = %d, want 2", results[0].Scan.Zombies.Checked)
	}
}

func TestScanSatellites_PartialFailure(t *testing.T) {
	scanJSON := `{"rig":"gastown","timestamp":"2026-03-20T12:00:00Z","zombies":{"checked":1,"found":0,"zombies":[]}}`
	withMockSSH(t, func(target, _ string, _ time.Duration) (string, error) {
		if target == "u@10.0.0.3" {
			return "", fmt.Errorf("connection refused")
		}
		return scanJSON, nil
	})

	townRoot := writeTempMachinesJSON(t, map[string]*config.MachineEntry{
		"m1": {Host: "10.0.0.2", User: "u", Roles: []string{"worker"}, Enabled: true},
		"m2": {Host: "10.0.0.3", User: "u", Roles: []string{"worker"}, Enabled: true},
	})

	results := scanSatellites(townRoot)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	var succeeded, errored int
	for _, r := range results {
		if r.Error != "" {
			errored++
		} else if r.Scan != nil {
			succeeded++
		}
	}
	if succeeded != 1 || errored != 1 {
		t.Errorf("expected 1 success + 1 error, got %d success + %d error", succeeded, errored)
	}
}

func TestScanSatellites_InvalidJSON(t *testing.T) {
	withMockSSH(t, func(_, _ string, _ time.Duration) (string, error) {
		return "not json", nil
	})

	townRoot := writeTempMachinesJSON(t, map[string]*config.MachineEntry{
		"m1": {Host: "10.0.0.2", User: "u", Roles: []string{"worker"}, Enabled: true},
	})

	results := scanSatellites(townRoot)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Error == "" {
		t.Error("expected error for invalid JSON")
	}
	if results[0].Scan != nil {
		t.Error("scan should be nil for invalid JSON")
	}
}

func TestScanSatellites_NoMachinesConfig(t *testing.T) {
	// No machines.json → single-machine setup → returns nil
	dir := t.TempDir()
	results := scanSatellites(dir)
	if results != nil {
		t.Errorf("expected nil for missing machines config, got %d results", len(results))
	}
}
