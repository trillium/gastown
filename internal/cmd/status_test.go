package cmd

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
)

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	os.Stderr = w

	fn()

	_ = w.Close()
	os.Stderr = old

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	_ = r.Close()

	return buf.String()
}

func TestDiscoverRigAgents_UsesRigPrefix(t *testing.T) {
	townRoot := t.TempDir()
	writeTestRoutes(t, townRoot, []beads.Route{
		{Prefix: "bd-", Path: "beads/mayor/rig"},
	})

	r := &rig.Rig{
		Name:       "beads",
		Path:       filepath.Join(townRoot, "beads"),
		HasWitness: true,
	}

	allAgentBeads := map[string]*beads.Issue{
		"bd-beads-witness": {
			ID:         "bd-beads-witness",
			AgentState: "running",
			HookBead:   "bd-hook",
		},
	}
	allHookBeads := map[string]*beads.Issue{
		"bd-hook": {ID: "bd-hook", Title: "Pinned"},
	}

	agents := discoverRigAgents(map[string]bool{}, r, nil, allAgentBeads, allHookBeads, nil, true)
	if len(agents) != 1 {
		t.Fatalf("discoverRigAgents() returned %d agents, want 1", len(agents))
	}

	if agents[0].State != "running" {
		t.Fatalf("agent state = %q, want %q", agents[0].State, "running")
	}
	if !agents[0].HasWork {
		t.Fatalf("agent HasWork = false, want true")
	}
	if agents[0].WorkTitle != "Pinned" {
		t.Fatalf("agent WorkTitle = %q, want %q", agents[0].WorkTitle, "Pinned")
	}
}

func TestRenderAgentDetails_UsesRigPrefix(t *testing.T) {
	townRoot := t.TempDir()
	writeTestRoutes(t, townRoot, []beads.Route{
		{Prefix: "bd-", Path: "beads/mayor/rig"},
	})

	agent := AgentRuntime{
		Name:    "witness",
		Address: "beads/witness",
		Role:    "witness",
		Running: true,
	}

	var buf bytes.Buffer
	renderAgentDetails(&buf, agent, "", nil, townRoot)
	output := buf.String()

	if !strings.Contains(output, "bd-beads-witness") {
		t.Fatalf("output %q does not contain rig-prefixed bead ID", output)
	}
}

func TestDiscoverRigAgents_ZombieSessionNotRunning(t *testing.T) {
	// Verify that a session in allSessions with value=false (zombie: tmux alive,
	// agent dead) results in agent.Running=false. This is the core fix for gt-bd6i3.
	townRoot := t.TempDir()
	writeTestRoutes(t, townRoot, []beads.Route{
		{Prefix: "gt-", Path: "gastown/mayor/rig"},
	})

	r := &rig.Rig{
		Name:       "gastown",
		Path:       filepath.Join(townRoot, "gastown"),
		HasWitness: true,
	}

	// allSessions has the witness session but marked as zombie (false).
	// This simulates a tmux session that exists but whose agent process has died.
	allSessions := map[string]bool{
		"gt-gastown-witness": false, // zombie: tmux exists, agent dead
	}

	agents := discoverRigAgents(allSessions, r, nil, nil, nil, nil, true)
	for _, a := range agents {
		if a.Role == "witness" {
			if a.Running {
				t.Fatal("zombie witness session (allSessions=false) should show as not running")
			}
			return
		}
	}
	t.Fatal("witness agent not found in results")
}

func TestDiscoverRigAgents_MissingSessionNotRunning(t *testing.T) {
	// Verify that a session not in allSessions at all results in agent.Running=false.
	townRoot := t.TempDir()
	writeTestRoutes(t, townRoot, []beads.Route{
		{Prefix: "gt-", Path: "gastown/mayor/rig"},
	})

	r := &rig.Rig{
		Name:       "gastown",
		Path:       filepath.Join(townRoot, "gastown"),
		HasWitness: true,
	}

	// Empty sessions map - no tmux sessions exist at all
	allSessions := map[string]bool{}

	agents := discoverRigAgents(allSessions, r, nil, nil, nil, nil, true)
	for _, a := range agents {
		if a.Role == "witness" {
			if a.Running {
				t.Fatal("witness with no tmux session should show as not running")
			}
			return
		}
	}
	t.Fatal("witness agent not found in results")
}

func TestBuildStatusIndicator_ZombieShowsStopped(t *testing.T) {
	// Verify that a zombie agent (Running=false) shows ○ (stopped), not ● (running)
	agent := AgentRuntime{Running: false}
	indicator := buildStatusIndicator(agent)
	if strings.Contains(indicator, "●") {
		t.Fatal("zombie agent (Running=false) should not show ● indicator")
	}
}

func TestBuildStatusIndicator_AliveShowsRunning(t *testing.T) {
	// Verify that an alive agent (Running=true) shows ● (running)
	agent := AgentRuntime{Running: true}
	indicator := buildStatusIndicator(agent)
	if strings.Contains(indicator, "○") {
		t.Fatal("alive agent (Running=true) should not show ○ indicator")
	}
}

func TestBuildStatusIndicator_DNDMutedShowsBadge(t *testing.T) {
	agent := AgentRuntime{Running: true, NotificationLevel: beads.NotifyMuted}
	indicator := buildStatusIndicator(agent)
	if !strings.Contains(indicator, "🔕") {
		t.Fatalf("expected muted indicator to include 🔕, got %q", indicator)
	}
}

func TestOutputStatusText_IncludesDNDSection(t *testing.T) {
	status := TownStatus{
		Name:     "gt",
		Location: "/tmp/gt",
		DND: &DNDInfo{
			Enabled: true,
			Level:   beads.NotifyMuted,
			Agent:   "hq-mayor",
		},
	}

	var buf bytes.Buffer
	if err := outputStatusText(&buf, status); err != nil {
		t.Fatalf("outputStatusText error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "DND:") {
		t.Fatalf("expected DND section in status output, got: %q", out)
	}
	if !strings.Contains(out, "on") {
		t.Fatalf("expected DND state 'on' in status output, got: %q", out)
	}
}

func TestRunStatusWatch_RejectsZeroInterval(t *testing.T) {
	oldInterval := statusInterval
	oldWatch := statusWatch
	defer func() {
		statusInterval = oldInterval
		statusWatch = oldWatch
	}()

	statusInterval = 0
	statusWatch = true

	err := runStatusWatch(nil, nil)
	if err == nil {
		t.Fatal("expected error for zero interval, got nil")
	}
	if !strings.Contains(err.Error(), "positive") {
		t.Errorf("error %q should mention 'positive'", err.Error())
	}
}

func TestRunStatusWatch_RejectsNegativeInterval(t *testing.T) {
	oldInterval := statusInterval
	oldWatch := statusWatch
	defer func() {
		statusInterval = oldInterval
		statusWatch = oldWatch
	}()

	statusInterval = -5
	statusWatch = true

	err := runStatusWatch(nil, nil)
	if err == nil {
		t.Fatal("expected error for negative interval, got nil")
	}
	if !strings.Contains(err.Error(), "positive") {
		t.Errorf("error %q should mention 'positive'", err.Error())
	}
}

func TestRunStatusWatch_RejectsJSONCombo(t *testing.T) {
	oldJSON := statusJSON
	oldWatch := statusWatch
	oldInterval := statusInterval
	defer func() {
		statusJSON = oldJSON
		statusWatch = oldWatch
		statusInterval = oldInterval
	}()

	statusJSON = true
	statusWatch = true
	statusInterval = 2

	err := runStatusWatch(nil, nil)
	if err == nil {
		t.Fatal("expected error for --json + --watch, got nil")
	}
	if !strings.Contains(err.Error(), "cannot be used together") {
		t.Errorf("error %q should mention 'cannot be used together'", err.Error())
	}
}

func TestIsKnownAgent(t *testing.T) {
	t.Parallel()

	// All agent presets should be recognized
	for _, name := range config.ListAgentPresets() {
		t.Run(name+"_known", func(t *testing.T) {
			if !isKnownAgent(name) {
				t.Errorf("isKnownAgent(%q) = false, want true", name)
			}
		})
	}

	// Non-agents should not be recognized
	for _, name := range []string{"bash", "node", ""} {
		t.Run(name+"_unknown", func(t *testing.T) {
			if isKnownAgent(name) {
				t.Errorf("isKnownAgent(%q) = true, want false", name)
			}
		})
	}
}

func TestIsAgentWrapper(t *testing.T) {
	t.Parallel()
	tests := []struct {
		base string
		want bool
	}{
		{"node", true},
		{"bun", true},
		{"npx", true},
		{"bunx", true},
		{"claude", false},
		{"pi", false},
		{"bash", false},
	}

	for _, tt := range tests {
		t.Run(tt.base, func(t *testing.T) {
			if got := isAgentWrapper(tt.base); got != tt.want {
				t.Errorf("isAgentWrapper(%q) = %v, want %v", tt.base, got, tt.want)
			}
		})
	}
}

func TestParseRuntimeInfo(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cmdline string
		want    string
	}{
		{
			name:    "claude with model",
			cmdline: "claude\x00--model\x00opus\x00--dangerously-skip-permissions",
			want:    "claude/opus",
		},
		{
			name:    "pi with model",
			cmdline: "pi\x00-e\x00gastown-hooks.js\x00--model\x00google-antigravity/gemini-3-flash",
			want:    "pi/google-antigravity/gemini-3-flash",
		},
		{
			name:    "cgroup-wrap then claude",
			cmdline: "cgroup-wrap\x00claude\x00--model\x00opus\x00--dangerously-skip-permissions",
			want:    "claude/opus",
		},
		{
			name:    "opencode with -m flag",
			cmdline: "opencode\x00-m\x00kimi-for-coding/kimi-k2.5",
			want:    "opencode/kimi-for-coding/kimi-k2.5",
		},
		{
			name:    "empty cmdline",
			cmdline: "",
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseRuntimeInfo(tt.cmdline)
			if got != tt.want {
				t.Errorf("parseRuntimeInfo(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestParseRuntimeInfo_PiBare(t *testing.T) {
	t.Parallel()
	// Bare pi (no --model flag) calls readPiDefaults() which reads
	// ~/.pi/agent/settings.json. The result is either "pi" (if no settings)
	// or "pi/<default-model>" (if settings exist). Both are valid.
	cmdline := "pi\x00-e\x00gastown-hooks.js"
	got := parseRuntimeInfo(cmdline)
	if !strings.HasPrefix(got, "pi") {
		t.Errorf("parseRuntimeInfo(pi bare) = %q, want prefix 'pi'", got)
	}
}

func TestBuildInfoFromConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		rc   *config.RuntimeConfig
		want string
	}{
		{
			name: "claude with model",
			rc:   &config.RuntimeConfig{Command: "claude", Args: []string{"--model", "opus"}},
			want: "claude/opus",
		},
		{
			name: "cgroup-wrap claude",
			rc:   &config.RuntimeConfig{Command: "cgroup-wrap", Args: []string{"claude", "--model", "opus"}},
			want: "claude/opus",
		},
		{
			name: "pi bare",
			rc:   &config.RuntimeConfig{Command: "pi", Args: []string{"-e", "hooks.js"}},
			want: "pi",
		},
		{
			name: "opencode with -m",
			rc:   &config.RuntimeConfig{Command: "opencode", Args: []string{"-m", "gpt-5"}},
			want: "opencode/gpt-5",
		},
		{
			name: "empty command",
			rc:   &config.RuntimeConfig{Command: ""},
			want: "claude",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildInfoFromConfig(tt.rc)
			if got != tt.want {
				t.Errorf("buildInfoFromConfig(%s) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestIsAgentCmdline(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cmdline string
		want    bool
	}{
		{"claude direct", "claude\x00--model\x00opus", true},
		{"pi direct", "pi\x00-e\x00hooks.js", true},
		{"node wrapper with pi", "node\x00/path/to/pi\x00-e\x00hooks.js", true},
		{"bun wrapper with opencode", "bun\x00/path/to/opencode", true},
		{"bash not agent", "bash\x00-c\x00echo hi", false},
		{"node without agent", "node\x00/path/to/server.js", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isAgentCmdline(tt.cmdline)
			if got != tt.want {
				t.Errorf("isAgentCmdline(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestCountRunningAgents(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status TownStatus
		want   int
	}{
		{
			name:   "empty status",
			status: TownStatus{},
			want:   0,
		},
		{
			name: "global agents only",
			status: TownStatus{
				Agents: []AgentRuntime{
					{Name: "mayor", Running: true},
					{Name: "deacon", Running: false},
				},
			},
			want: 1,
		},
		{
			name: "rig agents only",
			status: TownStatus{
				Rigs: []RigStatus{
					{
						Agents: []AgentRuntime{
							{Name: "polecat-1", Running: true},
							{Name: "witness", Running: true},
						},
					},
				},
			},
			want: 2,
		},
		{
			name: "mixed global and rig agents",
			status: TownStatus{
				Agents: []AgentRuntime{
					{Name: "mayor", Running: true},
				},
				Rigs: []RigStatus{
					{
						Agents: []AgentRuntime{
							{Name: "polecat-1", Running: true},
							{Name: "witness", Running: false},
						},
					},
					{
						Agents: []AgentRuntime{
							{Name: "polecat-2", Running: true},
						},
					},
				},
			},
			want: 3,
		},
		{
			name: "all not running",
			status: TownStatus{
				Agents: []AgentRuntime{
					{Name: "mayor", Running: false},
				},
				Rigs: []RigStatus{
					{
						Agents: []AgentRuntime{
							{Name: "polecat-1", Running: false},
						},
					},
				},
			},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := countRunningAgents(tt.status)
			if got != tt.want {
				t.Errorf(
					"countRunningAgents() = %d, want %d",
					got, tt.want,
				)
			}
		})
	}
}

func TestExtractBaseName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		cmdline string
		want    string
	}{
		{"claude\x00--model\x00opus", "claude"},
		{"/usr/bin/node\x00/path/pi", "node"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := extractBaseName(tt.cmdline)
			if got != tt.want {
				t.Errorf("extractBaseName(%q) = %q, want %q", tt.cmdline, got, tt.want)
			}
		})
	}
}

// --- discoverLiveSessions tests (gt-t54) ---

func withDiscoverSeams(t *testing.T) {
	t.Helper()
	origList := tmuxListSessionsFn
	origAlive := tmuxIsAgentAliveFn
	t.Cleanup(func() {
		tmuxListSessionsFn = origList
		tmuxIsAgentAliveFn = origAlive
	})
}

func setupStatusTestRegistry(t *testing.T) {
	t.Helper()
	orig := session.DefaultRegistry()
	reg := session.NewPrefixRegistry()
	reg.Register("gt", "gastown")
	session.SetDefaultRegistry(reg)
	t.Cleanup(func() { session.SetDefaultRegistry(orig) })
}

func TestDiscoverLiveSessions_AllAlive(t *testing.T) {
	setupStatusTestRegistry(t)
	withDiscoverSeams(t)

	tmuxListSessionsFn = func(_ *tmux.Tmux) ([]string, error) {
		return []string{"hq-mayor", "gt-Toast", "gt-Butter"}, nil
	}
	tmuxIsAgentAliveFn = func(_ *tmux.Tmux, _ string) bool { return true }

	result := discoverLiveSessions(nil)
	if len(result) != 3 {
		t.Fatalf("expected 3 sessions, got %d: %v", len(result), result)
	}
	for name, alive := range result {
		if !alive {
			t.Errorf("session %q should be alive", name)
		}
	}
}

func TestDiscoverLiveSessions_MixedLiveness(t *testing.T) {
	setupStatusTestRegistry(t)
	withDiscoverSeams(t)

	tmuxListSessionsFn = func(_ *tmux.Tmux) ([]string, error) {
		return []string{"hq-mayor", "gt-Toast", "gt-Zombie"}, nil
	}
	tmuxIsAgentAliveFn = func(_ *tmux.Tmux, name string) bool {
		return name != "gt-Zombie"
	}

	result := discoverLiveSessions(nil)
	if len(result) != 3 {
		t.Fatalf("expected 3 sessions, got %d", len(result))
	}
	if !result["hq-mayor"] {
		t.Error("hq-mayor should be alive")
	}
	if !result["gt-Toast"] {
		t.Error("gt-Toast should be alive")
	}
	if result["gt-Zombie"] {
		t.Error("gt-Zombie should be dead")
	}
}

func TestDiscoverLiveSessions_UnknownSessionsAlwaysTrue(t *testing.T) {
	setupStatusTestRegistry(t)
	withDiscoverSeams(t)

	tmuxListSessionsFn = func(_ *tmux.Tmux) ([]string, error) {
		return []string{"random-user-session", "my-scratch"}, nil
	}
	// IsAgentAlive should NOT be called for unknown sessions
	tmuxIsAgentAliveFn = func(_ *tmux.Tmux, name string) bool {
		t.Errorf("IsAgentAlive called for unknown session %q", name)
		return false
	}

	result := discoverLiveSessions(nil)
	if len(result) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(result))
	}
	for name, alive := range result {
		if !alive {
			t.Errorf("unknown session %q should default to alive=true", name)
		}
	}
}

func TestDiscoverLiveSessions_ListError(t *testing.T) {
	withDiscoverSeams(t)

	tmuxListSessionsFn = func(_ *tmux.Tmux) ([]string, error) {
		return nil, fmt.Errorf("no tmux server")
	}

	result := discoverLiveSessions(nil)
	if len(result) != 0 {
		t.Errorf("expected empty map on list error, got %d entries", len(result))
	}
}

func TestDiscoverLiveSessions_Empty(t *testing.T) {
	withDiscoverSeams(t)

	tmuxListSessionsFn = func(_ *tmux.Tmux) ([]string, error) {
		return nil, nil
	}

	result := discoverLiveSessions(nil)
	if len(result) != 0 {
		t.Errorf("expected empty map for no sessions, got %d entries", len(result))
	}
}

// --- gatherServiceStatus tests ---

func withServiceSeams(t *testing.T) {
	t.Helper()
	origDaemon := daemonIsRunningFn
	origDolt := gatherDoltStatusFn
	origTmux := gatherTmuxStatusFn
	origACP := gatherACPStatusFn
	t.Cleanup(func() {
		daemonIsRunningFn = origDaemon
		gatherDoltStatusFn = origDolt
		gatherTmuxStatusFn = origTmux
		gatherACPStatusFn = origACP
	})
}

func TestGatherServiceStatus_AllRunning(t *testing.T) {
	withServiceSeams(t)

	daemonIsRunningFn = func(_ string) (bool, int, error) { return true, 1234, nil }
	gatherDoltStatusFn = func(_ string) *DoltInfo { return &DoltInfo{Running: true, PID: 5678, Port: 3307} }
	gatherTmuxStatusFn = func(sessions map[string]bool) *TmuxInfo {
		return &TmuxInfo{Socket: "gt", Running: true, SessionCount: len(sessions), PID: 9012}
	}
	gatherACPStatusFn = func(_ string) *ServiceInfo { return &ServiceInfo{Running: true, PID: 3456} }

	status := &TownStatus{}
	gatherServiceStatus(status, "/town", map[string]bool{"hq-mayor": true})

	if status.Daemon == nil || !status.Daemon.Running || status.Daemon.PID != 1234 {
		t.Errorf("daemon: got %+v, want running with PID 1234", status.Daemon)
	}
	if status.Dolt == nil || !status.Dolt.Running || status.Dolt.PID != 5678 {
		t.Errorf("dolt: got %+v, want running with PID 5678", status.Dolt)
	}
	if status.Tmux == nil || !status.Tmux.Running || status.Tmux.PID != 9012 {
		t.Errorf("tmux: got %+v, want running with PID 9012", status.Tmux)
	}
	if status.ACP == nil || !status.ACP.Running || status.ACP.PID != 3456 {
		t.Errorf("acp: got %+v, want running with PID 3456", status.ACP)
	}
}

func TestGatherServiceStatus_AllStopped(t *testing.T) {
	withServiceSeams(t)

	daemonIsRunningFn = func(_ string) (bool, int, error) { return false, 0, nil }
	gatherDoltStatusFn = func(_ string) *DoltInfo { return &DoltInfo{Running: false, Port: 3307} }
	gatherTmuxStatusFn = func(_ map[string]bool) *TmuxInfo { return &TmuxInfo{Socket: "gt", Running: false} }
	gatherACPStatusFn = func(_ string) *ServiceInfo { return nil }

	status := &TownStatus{}
	gatherServiceStatus(status, "/town", map[string]bool{})

	if status.Daemon == nil || status.Daemon.Running {
		t.Error("daemon should be not-running")
	}
	if status.Dolt == nil || status.Dolt.Running {
		t.Error("dolt should be not-running")
	}
	if status.ACP != nil {
		t.Error("acp should be nil when not active")
	}
}

func TestGatherServiceStatus_DaemonCheckError(t *testing.T) {
	withServiceSeams(t)

	daemonIsRunningFn = func(_ string) (bool, int, error) { return false, 0, fmt.Errorf("no pid file") }
	gatherDoltStatusFn = func(_ string) *DoltInfo { return &DoltInfo{Running: true, Port: 3307} }
	gatherTmuxStatusFn = func(_ map[string]bool) *TmuxInfo { return &TmuxInfo{Running: true} }
	gatherACPStatusFn = func(_ string) *ServiceInfo { return nil }

	status := &TownStatus{}
	gatherServiceStatus(status, "/town", map[string]bool{"s": true})

	// Daemon status should be nil when IsRunning returns an error
	if status.Daemon != nil {
		t.Errorf("daemon should be nil on error, got %+v", status.Daemon)
	}
	// Other services should still be populated
	if status.Dolt == nil {
		t.Error("dolt should still be populated despite daemon error")
	}
}

func TestGatherServiceStatus_DoltRemote(t *testing.T) {
	withServiceSeams(t)

	daemonIsRunningFn = func(_ string) (bool, int, error) { return true, 100, nil }
	gatherDoltStatusFn = func(_ string) *DoltInfo { return &DoltInfo{Remote: true, Port: 3307} }
	gatherTmuxStatusFn = func(_ map[string]bool) *TmuxInfo { return &TmuxInfo{Running: true} }
	gatherACPStatusFn = func(_ string) *ServiceInfo { return nil }

	status := &TownStatus{}
	gatherServiceStatus(status, "/town", map[string]bool{})

	if status.Dolt == nil || !status.Dolt.Remote {
		t.Error("dolt should be marked as remote")
	}
}

// --- prefetchAgentBeads tests ---

// stubBeadsLoader is a test stub implementing the beadsLoader interface.
type stubBeadsLoader struct {
	agentBeads map[string]*beads.Issue
	hookBeads  map[string]*beads.Issue
}

func (s *stubBeadsLoader) ListAgentBeads() (map[string]*beads.Issue, error) {
	return s.agentBeads, nil
}

func (s *stubBeadsLoader) ShowMultiple(ids []string) (map[string]*beads.Issue, error) {
	result := make(map[string]*beads.Issue)
	for _, id := range ids {
		if issue, ok := s.hookBeads[id]; ok {
			result[id] = issue
		}
	}
	return result, nil
}

func withBeadsLoaderSeam(t *testing.T, loaders map[string]*stubBeadsLoader) {
	t.Helper()
	orig := newBeadsLoaderFn
	newBeadsLoaderFn = func(path string) beadsLoader {
		if loader, ok := loaders[path]; ok {
			return loader
		}
		return &stubBeadsLoader{} // empty stub for unknown paths
	}
	t.Cleanup(func() { newBeadsLoaderFn = orig })
}

func TestPrefetchAgentBeads_TownLevel(t *testing.T) {
	townRoot := "/test/town"
	townBeadsPath := beads.GetTownBeadsPath(townRoot)

	withBeadsLoaderSeam(t, map[string]*stubBeadsLoader{
		townBeadsPath: {
			agentBeads: map[string]*beads.Issue{
				"agent-1": {ID: "agent-1", Title: "Mayor", HookBead: "hook-1"},
			},
			hookBeads: map[string]*beads.Issue{
				"hook-1": {ID: "hook-1", Title: "Fix auth bug"},
			},
		},
	})

	agents, hooks := prefetchAgentBeads(townRoot, nil)
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent bead, got %d", len(agents))
	}
	if _, ok := agents["agent-1"]; !ok {
		t.Error("missing agent-1")
	}
	if len(hooks) != 1 {
		t.Fatalf("expected 1 hook bead, got %d", len(hooks))
	}
	if _, ok := hooks["hook-1"]; !ok {
		t.Error("missing hook-1")
	}
}

func TestPrefetchAgentBeads_RigLevel(t *testing.T) {
	townRoot := "/test/town"
	townBeadsPath := beads.GetTownBeadsPath(townRoot)
	rigBeadsPath := "/test/town/rigs/gastown/mayor/rig"

	withBeadsLoaderSeam(t, map[string]*stubBeadsLoader{
		townBeadsPath: {agentBeads: map[string]*beads.Issue{}},
		rigBeadsPath: {
			agentBeads: map[string]*beads.Issue{
				"rig-agent": {ID: "rig-agent", Title: "Witness", HookBead: "rig-hook"},
			},
			hookBeads: map[string]*beads.Issue{
				"rig-hook": {ID: "rig-hook", Title: "Patrol task"},
			},
		},
	})

	rigs := []*rig.Rig{{Path: "/test/town/rigs/gastown"}}
	agents, hooks := prefetchAgentBeads(townRoot, rigs)
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent bead, got %d", len(agents))
	}
	if _, ok := agents["rig-agent"]; !ok {
		t.Error("missing rig-agent")
	}
	if len(hooks) != 1 {
		t.Fatalf("expected 1 hook bead, got %d", len(hooks))
	}
}

func TestPrefetchAgentBeads_MergesMultipleRigs(t *testing.T) {
	townRoot := "/test/town"
	townBeadsPath := beads.GetTownBeadsPath(townRoot)

	withBeadsLoaderSeam(t, map[string]*stubBeadsLoader{
		townBeadsPath: {
			agentBeads: map[string]*beads.Issue{
				"town-agent": {ID: "town-agent", Title: "Mayor"},
			},
		},
		"/test/town/rigs/rig1/mayor/rig": {
			agentBeads: map[string]*beads.Issue{
				"rig1-agent": {ID: "rig1-agent", Title: "Witness 1"},
			},
		},
		"/test/town/rigs/rig2/mayor/rig": {
			agentBeads: map[string]*beads.Issue{
				"rig2-agent": {ID: "rig2-agent", Title: "Witness 2"},
			},
		},
	})

	rigs := []*rig.Rig{
		{Path: "/test/town/rigs/rig1"},
		{Path: "/test/town/rigs/rig2"},
	}
	agents, _ := prefetchAgentBeads(townRoot, rigs)
	if len(agents) != 3 {
		t.Fatalf("expected 3 agent beads (1 town + 2 rig), got %d", len(agents))
	}
	for _, id := range []string{"town-agent", "rig1-agent", "rig2-agent"} {
		if _, ok := agents[id]; !ok {
			t.Errorf("missing agent %q", id)
		}
	}
}

func TestPrefetchAgentBeads_NoAgentBeads(t *testing.T) {
	townRoot := "/test/town"
	townBeadsPath := beads.GetTownBeadsPath(townRoot)

	withBeadsLoaderSeam(t, map[string]*stubBeadsLoader{
		townBeadsPath: {agentBeads: nil},
	})

	agents, hooks := prefetchAgentBeads(townRoot, nil)
	if len(agents) != 0 {
		t.Errorf("expected 0 agents, got %d", len(agents))
	}
	if len(hooks) != 0 {
		t.Errorf("expected 0 hooks, got %d", len(hooks))
	}
}

func TestPrefetchAgentBeads_AgentWithNoHook(t *testing.T) {
	townRoot := "/test/town"
	townBeadsPath := beads.GetTownBeadsPath(townRoot)

	withBeadsLoaderSeam(t, map[string]*stubBeadsLoader{
		townBeadsPath: {
			agentBeads: map[string]*beads.Issue{
				"agent-idle": {ID: "agent-idle", Title: "Idle polecat"},
			},
		},
	})

	agents, hooks := prefetchAgentBeads(townRoot, nil)
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if len(hooks) != 0 {
		t.Errorf("expected 0 hooks (agent has no hook bead), got %d", len(hooks))
	}
}
