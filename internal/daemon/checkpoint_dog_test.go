package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
)

func TestCheckpointDogInterval_Default(t *testing.T) {
	interval := checkpointDogInterval(nil)
	if interval != defaultCheckpointDogInterval {
		t.Errorf("expected default interval %v, got %v", defaultCheckpointDogInterval, interval)
	}
}

func TestCheckpointDogInterval_NilPatrols(t *testing.T) {
	config := &DaemonPatrolConfig{}
	interval := checkpointDogInterval(config)
	if interval != defaultCheckpointDogInterval {
		t.Errorf("expected default interval %v, got %v", defaultCheckpointDogInterval, interval)
	}
}

func TestCheckpointDogInterval_NilCheckpointDog(t *testing.T) {
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{},
	}
	interval := checkpointDogInterval(config)
	if interval != defaultCheckpointDogInterval {
		t.Errorf("expected default interval %v, got %v", defaultCheckpointDogInterval, interval)
	}
}

func TestCheckpointDogInterval_Configured(t *testing.T) {
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			CheckpointDog: &CheckpointDogConfig{
				Enabled:     true,
				IntervalStr: "5m",
			},
		},
	}
	interval := checkpointDogInterval(config)
	if interval != 5*time.Minute {
		t.Errorf("expected 5m, got %v", interval)
	}
}

func TestCheckpointDogInterval_InvalidFallsBack(t *testing.T) {
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			CheckpointDog: &CheckpointDogConfig{
				Enabled:     true,
				IntervalStr: "not-a-duration",
			},
		},
	}
	interval := checkpointDogInterval(config)
	if interval != defaultCheckpointDogInterval {
		t.Errorf("expected default interval for invalid config, got %v", interval)
	}
}

func TestCheckpointDogInterval_ZeroFallsBack(t *testing.T) {
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			CheckpointDog: &CheckpointDogConfig{
				Enabled:     true,
				IntervalStr: "0s",
			},
		},
	}
	interval := checkpointDogInterval(config)
	if interval != defaultCheckpointDogInterval {
		t.Errorf("expected default interval for zero config, got %v", interval)
	}
}

func TestCheckpointDogEnabled(t *testing.T) {
	// Nil config → disabled (opt-in patrol)
	if IsPatrolEnabled(nil, "checkpoint_dog") {
		t.Error("expected checkpoint_dog disabled for nil config")
	}

	// Explicitly enabled
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			CheckpointDog: &CheckpointDogConfig{
				Enabled: true,
			},
		},
	}
	if !IsPatrolEnabled(config, "checkpoint_dog") {
		t.Error("expected checkpoint_dog enabled")
	}

	// Explicitly disabled
	config.Patrols.CheckpointDog.Enabled = false
	if IsPatrolEnabled(config, "checkpoint_dog") {
		t.Error("expected checkpoint_dog disabled when Enabled=false")
	}
}

// --- checkpointSatellites tests ---

func withDaemonSSHMock(t *testing.T, fn func(target, cmd string, timeout time.Duration) (string, error)) {
	t.Helper()
	orig := runSSHCmd
	runSSHCmd = fn
	t.Cleanup(func() { runSSHCmd = orig })
}

func writeDaemonMachinesJSON(t *testing.T, townRoot string, machines map[string]*config.MachineEntry) {
	t.Helper()
	mayorDir := filepath.Join(townRoot, "mayor")
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
}

func newTestDaemon(t *testing.T, townRoot string) *Daemon {
	t.Helper()
	return &Daemon{
		config: &Config{TownRoot: townRoot},
		logger: log.New(io.Discard, "", 0),
	}
}

func TestCheckpointSatellites_HappyPath(t *testing.T) {
	townRoot := t.TempDir()
	writeDaemonMachinesJSON(t, townRoot, map[string]*config.MachineEntry{
		"m1": {Host: "10.0.0.2", User: "u", Roles: []string{"worker"}, Enabled: true},
		"m2": {Host: "10.0.0.3", User: "u", Roles: []string{"worker"}, Enabled: true},
	})

	withDaemonSSHMock(t, func(_, _ string, _ time.Duration) (string, error) {
		return "", nil
	})

	d := newTestDaemon(t, townRoot)
	triggered := d.checkpointSatellites()
	if triggered != 2 {
		t.Errorf("triggered = %d, want 2", triggered)
	}
}

func TestCheckpointSatellites_PartialFailure(t *testing.T) {
	townRoot := t.TempDir()
	writeDaemonMachinesJSON(t, townRoot, map[string]*config.MachineEntry{
		"m1": {Host: "10.0.0.2", User: "u", Roles: []string{"worker"}, Enabled: true},
		"m2": {Host: "10.0.0.3", User: "u", Roles: []string{"worker"}, Enabled: true},
	})

	withDaemonSSHMock(t, func(target, _ string, _ time.Duration) (string, error) {
		if target == "u@10.0.0.3" {
			return "", fmt.Errorf("connection refused")
		}
		return "", nil
	})

	d := newTestDaemon(t, townRoot)
	triggered := d.checkpointSatellites()
	if triggered != 1 {
		t.Errorf("triggered = %d, want 1 (one success, one failure)", triggered)
	}
}

func TestCheckpointSatellites_SkipsDisabled(t *testing.T) {
	townRoot := t.TempDir()
	writeDaemonMachinesJSON(t, townRoot, map[string]*config.MachineEntry{
		"active":   {Host: "10.0.0.2", User: "u", Roles: []string{"worker"}, Enabled: true},
		"disabled": {Host: "10.0.0.3", User: "u", Roles: []string{"worker"}, Enabled: false},
	})

	callCount := 0
	withDaemonSSHMock(t, func(_, _ string, _ time.Duration) (string, error) {
		callCount++
		return "", nil
	})

	d := newTestDaemon(t, townRoot)
	triggered := d.checkpointSatellites()
	if triggered != 1 {
		t.Errorf("triggered = %d, want 1 (disabled skipped)", triggered)
	}
	if callCount != 1 {
		t.Errorf("SSH calls = %d, want 1", callCount)
	}
}

func TestCheckpointSatellites_NoMachinesConfig(t *testing.T) {
	townRoot := t.TempDir()
	// No machines.json — should return 0

	d := newTestDaemon(t, townRoot)
	triggered := d.checkpointSatellites()
	if triggered != 0 {
		t.Errorf("triggered = %d, want 0 (no machines config)", triggered)
	}
}

func TestCheckpointSatellites_UsesGtBinary(t *testing.T) {
	townRoot := t.TempDir()
	writeDaemonMachinesJSON(t, townRoot, map[string]*config.MachineEntry{
		"m1": {Host: "10.0.0.2", User: "u", Roles: []string{"worker"}, Enabled: true, GtBinary: "/opt/gt-custom"},
	})

	var capturedCmd string
	withDaemonSSHMock(t, func(_, cmd string, _ time.Duration) (string, error) {
		capturedCmd = cmd
		return "", nil
	})

	d := newTestDaemon(t, townRoot)
	d.checkpointSatellites()
	if capturedCmd == "" {
		t.Fatal("SSH was not called")
	}
	if !strings.Contains(capturedCmd, "/opt/gt-custom") {
		t.Errorf("expected custom gt binary in command, got %q", capturedCmd)
	}
}
