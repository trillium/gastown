package satellite

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/session"
)

// MachinesConfig represents the machine registry (mayor/machines.json).
type MachinesConfig struct {
	Machines map[string]*MachineEntry `json:"machines"`
}

// MachineEntry describes a single machine in the fleet.
type MachineEntry struct {
	Host     string `json:"host"`
	SSHAlias string `json:"ssh_alias,omitempty"`
	User     string `json:"user,omitempty"`
	Enabled  bool   `json:"enabled"`
}

// SSHTarget returns the SSH target string for this machine.
func (m *MachineEntry) SSHTarget() string {
	if m.SSHAlias != "" {
		return m.SSHAlias
	}
	if m.User != "" {
		return m.User + "@" + m.Host
	}
	return m.Host
}

// RemoteSession represents a parsed tmux session from a remote machine.
type RemoteSession struct {
	Machine  string                 // Machine name from machines.json
	Identity *session.AgentIdentity // Parsed session identity
	RawName  string                 // Original tmux session name
}

// EnumerateRemoteSessions SSHes to each machine in parallel and returns
// all parsed tmux sessions. Machines that fail SSH are silently skipped.
func EnumerateRemoteSessions(townRoot string) ([]RemoteSession, error) {
	machinesPath := filepath.Join(townRoot, "mayor", "machines.json")
	machines, err := loadMachinesConfig(machinesPath)
	if err != nil {
		return nil, fmt.Errorf("loading machines config: %w", err)
	}

	return enumerateFromMachines(machines)
}

func loadMachinesConfig(path string) (*MachinesConfig, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is constructed internally
	if err != nil {
		return nil, err
	}
	var cfg MachinesConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing machines config: %w", err)
	}
	if cfg.Machines == nil {
		cfg.Machines = make(map[string]*MachineEntry)
	}
	return &cfg, nil
}

func enumerateFromMachines(machines *MachinesConfig) ([]RemoteSession, error) {
	type machineResult struct {
		sessions []RemoteSession
	}

	var wg sync.WaitGroup
	results := make(chan machineResult, len(machines.Machines))

	for name, entry := range machines.Machines {
		if !entry.Enabled {
			continue
		}
		wg.Add(1)
		go func(name string, entry *MachineEntry) {
			defer wg.Done()
			sessions := listRemoteSessions(name, entry)
			results <- machineResult{sessions: sessions}
		}(name, entry)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var all []RemoteSession
	for mr := range results {
		all = append(all, mr.sessions...)
	}

	return all, nil
}

func listRemoteSessions(machineName string, entry *MachineEntry) []RemoteSession {
	target := entry.SSHTarget()
	// Use the gt tmux socket on the remote machine
	out, err := RunSSH(target, "tmux -L gt list-sessions -F '#{session_name}' 2>/dev/null || true", 10*time.Second)
	if err != nil {
		return nil
	}

	var sessions []RemoteSession
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		identity, err := session.ParseSessionName(line)
		if err != nil {
			continue
		}
		sessions = append(sessions, RemoteSession{
			Machine:  machineName,
			Identity: identity,
			RawName:  line,
		})
	}
	return sessions
}
