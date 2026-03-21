package satellite

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/session"
)

// RemoteSession represents a parsed tmux session from a remote machine.
type RemoteSession struct {
	Machine  string             // Machine name from machines.json
	Identity *session.AgentIdentity // Parsed session identity
	RawName  string             // Original tmux session name
}

// EnumerateRemoteSessions SSHes to each machine in parallel and returns
// all parsed tmux sessions. Machines that fail SSH are silently skipped.
func EnumerateRemoteSessions(townRoot string) ([]RemoteSession, error) {
	machinesPath := constants.MayorMachinesPath(townRoot)
	machines, err := config.LoadMachinesConfig(machinesPath)
	if err != nil {
		return nil, fmt.Errorf("loading machines config: %w", err)
	}

	return enumerateFromMachines(machines)
}

func enumerateFromMachines(machines *config.MachinesConfig) ([]RemoteSession, error) {
	type machineResult struct {
		name     string
		sessions []RemoteSession
	}

	var wg sync.WaitGroup
	results := make(chan machineResult, len(machines.Machines))

	for name, entry := range machines.Machines {
		if !entry.Enabled {
			continue
		}
		wg.Add(1)
		go func(name string, entry *config.MachineEntry) {
			defer wg.Done()
			sessions := listRemoteSessions(name, entry)
			results <- machineResult{name: name, sessions: sessions}
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

func listRemoteSessions(machineName string, entry *config.MachineEntry) []RemoteSession {
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
