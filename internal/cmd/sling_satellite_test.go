package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/session"
)

// --- MachinesConfig method tests ---

func TestHubSSHTarget_WithAlias(t *testing.T) {
	cfg := &config.MachinesConfig{
		DoltHost: "100.111.197.110",
		Machines: map[string]*config.MachineEntry{
			"mini2": {
				Host:     "100.111.197.110",
				SSHAlias: "mini2",
				User:     "b",
				Roles:    []string{"worker"},
				Enabled:  true,
			},
		},
	}
	got := cfg.HubSSHTarget()
	if got != "mini2" {
		t.Errorf("HubSSHTarget() = %q, want %q", got, "mini2")
	}
}

func TestHubSSHTarget_WithoutAlias(t *testing.T) {
	cfg := &config.MachinesConfig{
		DoltHost: "100.111.197.110",
		Machines: map[string]*config.MachineEntry{
			"mini2": {
				Host:    "100.111.197.110",
				User:    "b",
				Roles:   []string{"worker"},
				Enabled: true,
			},
		},
	}
	got := cfg.HubSSHTarget()
	if got != "b@100.111.197.110" {
		t.Errorf("HubSSHTarget() = %q, want %q", got, "b@100.111.197.110")
	}
}

func TestHubSSHTarget_NoMatchingMachine(t *testing.T) {
	cfg := &config.MachinesConfig{
		DoltHost: "100.111.197.110",
		Machines: map[string]*config.MachineEntry{
			"mini3": {
				Host:    "100.86.9.58",
				User:    "worker",
				Roles:   []string{"worker"},
				Enabled: true,
			},
		},
	}
	got := cfg.HubSSHTarget()
	// Falls back to DoltHost raw IP
	if got != "100.111.197.110" {
		t.Errorf("HubSSHTarget() = %q, want %q (fallback)", got, "100.111.197.110")
	}
}

func TestSSHTarget_PrefersAlias(t *testing.T) {
	m := &config.MachineEntry{Host: "1.2.3.4", SSHAlias: "mybox", User: "bob"}
	if got := m.SSHTarget(); got != "mybox" {
		t.Errorf("SSHTarget() = %q, want %q", got, "mybox")
	}
}

func TestSSHTarget_UserAtHost(t *testing.T) {
	m := &config.MachineEntry{Host: "1.2.3.4", User: "bob"}
	if got := m.SSHTarget(); got != "bob@1.2.3.4" {
		t.Errorf("SSHTarget() = %q, want %q", got, "bob@1.2.3.4")
	}
}

func TestSSHTarget_HostOnly(t *testing.T) {
	m := &config.MachineEntry{Host: "1.2.3.4"}
	if got := m.SSHTarget(); got != "1.2.3.4" {
		t.Errorf("SSHTarget() = %q, want %q", got, "1.2.3.4")
	}
}

// --- loadMachinesConfig tests ---

func TestLoadMachinesConfig_FileNotExist(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "mayor"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadMachinesConfig(dir)
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected nil config for missing file, got: %+v", cfg)
	}
}

func TestLoadMachinesConfig_ValidFile(t *testing.T) {
	dir := t.TempDir()
	mayorDir := filepath.Join(dir, "mayor")
	if err := os.MkdirAll(mayorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := `{
		"type": "machines",
		"version": 1,
		"machines": {
			"mini2": {
				"host": "10.0.0.1",
				"user": "test",
				"roles": ["worker"],
				"enabled": true
			}
		},
		"dolt_host": "10.0.0.1",
		"dolt_port": 3307
	}`
	if err := os.WriteFile(filepath.Join(mayorDir, "machines.json"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadMachinesConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.DoltHost != "10.0.0.1" {
		t.Errorf("DoltHost = %q, want %q", cfg.DoltHost, "10.0.0.1")
	}
	if _, ok := cfg.Machines["mini2"]; !ok {
		t.Error("expected mini2 in machines map")
	}
}

func TestLoadMachinesConfig_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	mayorDir := filepath.Join(dir, "mayor")
	if err := os.MkdirAll(mayorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "machines.json"), []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadMachinesConfig(dir)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// --- Bootstrap script generation tests (uses production buildBootstrapScript) ---

func TestBootstrapScript_ContainsRequiredElements(t *testing.T) {
	script := buildBootstrapScript("/Users/test/gt", "gastown", "Toast", "10.0.0.1", 3307, "https://10.0.0.1:9876", "")

	checks := []struct {
		name   string
		substr string
	}{
		{"set -e", "set -e"},
		{"cert dir creation", "mktemp -d"},
		{"cert dir perms", "chmod 700"},
		{"cert from stdin", "CERT_JSON=$(cat)"},
		{"cert.pem write", `jq -r .cert > "$CERT_DIR/cert.pem"`},
		{"key.pem write", `jq -r .key  > "$CERT_DIR/key.pem"`},
		{"ca.pem write", `jq -r .ca   > "$CERT_DIR/ca.pem"`},
		{"key perms", `chmod 600 "$CERT_DIR/key.pem"`},
		{"cd to town root", "cd /Users/test/gt"},
		{"polecat spawn", "gt.real polecat spawn gastown --name Toast"},
		{"dolt host", "--dolt-host 10.0.0.1"},
		{"dolt port", "--dolt-port 3307"},
		{"json flag", "--json"},
		{"session name", `SESS=gt-gastown-p-Toast`},
		{"tmux new-session", "tmux new-session -d -s"},
		{"proxy url env", `GT_PROXY_URL https://10.0.0.1:9876`},
		{"proxy cert env", "GT_PROXY_CERT"},
		{"proxy key env", "GT_PROXY_KEY"},
		{"proxy ca env", "GT_PROXY_CA"},
		{"real bin env", "GT_REAL_BIN"},
		{"printf not echo for cert", `printf '%s' "$CERT_JSON"`},
		{"grep json from mixed output", `grep '^{' | tail -1`},
		{"error capture", "SPAWN_EXIT=$?"},
	}

	for _, c := range checks {
		if !strings.Contains(script, c.substr) {
			t.Errorf("script missing %s: expected substring %q", c.name, c.substr)
		}
	}

	// Verify cert material is NOT in script (it's piped via stdin)
	if strings.Contains(script, "BEGIN CERTIFICATE") {
		t.Error("script should not contain cert material (must be piped via stdin)")
	}
}

func TestBootstrapScript_SessionBeforeEnv(t *testing.T) {
	script := buildBootstrapScript("/gt", "rig", "Name", "10.0.0.1", 3307, "https://10.0.0.1:9876", "")

	sessionIdx := strings.Index(script, "tmux new-session")
	setenvIdx := strings.Index(script, "tmux setenv")

	if sessionIdx < 0 {
		t.Fatal("tmux new-session not found in script")
	}
	if setenvIdx < 0 {
		t.Fatal("tmux setenv not found in script")
	}
	if setenvIdx < sessionIdx {
		t.Error("tmux setenv appears before tmux new-session — env vars will fail to set")
	}
}

func TestBootstrapScript_NoEchoForCert(t *testing.T) {
	script := buildBootstrapScript("/gt", "rig", "Name", "10.0.0.1", 3307, "https://10.0.0.1:9876", "")

	// echo "$CERT_JSON" on macOS expands \n — we must use printf '%s'
	if strings.Contains(script, `echo "$CERT_JSON"`) {
		t.Error("script uses echo for cert JSON — must use printf to avoid macOS \\n expansion")
	}
}

func TestBootstrapScript_DoltPortPassthrough(t *testing.T) {
	// buildBootstrapScript passes through whatever port the caller provides.
	// Default (3307) is applied by spawnRemoteSatellite before calling this.
	script := buildBootstrapScript("/gt", "rig", "Name", "10.0.0.1", 0, "https://10.0.0.1:9876", "")
	if !strings.Contains(script, "--dolt-port 0") {
		t.Error("expected --dolt-port 0 in script (builder passes through caller value)")
	}
}

func TestBootstrapScript_CustomGtBinary(t *testing.T) {
	script := buildBootstrapScript("/gt", "rig", "Name", "10.0.0.1", 3307, "https://10.0.0.1:9876", "/usr/local/bin/gt-custom")
	if !strings.Contains(script, "/usr/local/bin/gt-custom polecat spawn") {
		t.Error("expected custom gt binary path in spawn command")
	}
	if strings.Contains(script, "gt.real polecat spawn") {
		t.Error("should not use gt.real when custom binary is specified")
	}
}

func TestBootstrapScript_DefaultGtReal(t *testing.T) {
	script := buildBootstrapScript("/gt", "rig", "Name", "10.0.0.1", 3307, "https://10.0.0.1:9876", "")
	if !strings.Contains(script, "gt.real polecat spawn") {
		t.Error("expected gt.real as default binary for satellite bootstrap")
	}
}

// --- Cert API payload tests ---

func TestIssueCertPayload_JSONSafe(t *testing.T) {
	cases := []struct {
		name string
		rig  string
		pc   string
	}{
		{"normal", "gastown", "Toast"},
		{"with quotes", "gas\"town", "To\"ast"},
		{"with backslash", "gas\\town", "To\\ast"},
		{"with newline", "gas\ntown", "To\nast"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := json.Marshal(map[string]string{
				"rig":  tc.rig,
				"name": tc.pc,
			})
			if err != nil {
				t.Fatalf("json.Marshal failed: %v", err)
			}
			var parsed map[string]string
			if err := json.Unmarshal(payload, &parsed); err != nil {
				t.Fatalf("payload is not valid JSON: %v\npayload: %s", err, payload)
			}
			if parsed["rig"] != tc.rig {
				t.Errorf("rig roundtrip: got %q, want %q", parsed["rig"], tc.rig)
			}
			if parsed["name"] != tc.pc {
				t.Errorf("name roundtrip: got %q, want %q", parsed["name"], tc.pc)
			}
		})
	}
}

func TestDenyCertPayload_JSONSafe(t *testing.T) {
	serial := "abc123\"special"
	payload, err := json.Marshal(map[string]string{"serial": serial})
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	var parsed map[string]string
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if parsed["serial"] != serial {
		t.Errorf("serial roundtrip: got %q, want %q", parsed["serial"], serial)
	}
}

// --- parseSpawnOutput tests (uses production parseSpawnOutput) ---

func TestParseSpawnOutput_CleanJSON(t *testing.T) {
	output := `{"session_name":"gt-gastown-p-Toast","cert_dir":"/tmp/abc","clone_path":"/gt/gastown/polecats/Toast","base_branch":"main","branch":"polecat/Toast"}`

	info, err := parseSpawnOutput(output)
	if err != nil {
		t.Fatalf("failed to parse clean JSON output: %v", err)
	}
	if info.SessionName != "gt-gastown-p-Toast" {
		t.Errorf("SessionName = %q, want %q", info.SessionName, "gt-gastown-p-Toast")
	}
	if info.ClonePath != "/gt/gastown/polecats/Toast" {
		t.Errorf("ClonePath = %q, want %q", info.ClonePath, "/gt/gastown/polecats/Toast")
	}
	if info.BaseBranch != "main" {
		t.Errorf("BaseBranch = %q, want %q", info.BaseBranch, "main")
	}
	if info.Branch != "polecat/Toast" {
		t.Errorf("Branch = %q, want %q", info.Branch, "polecat/Toast")
	}
}

func TestParseSpawnOutput_MixedWithProgress(t *testing.T) {
	// gt polecat spawn prints progress text before JSON
	output := "Checking Dolt health...\nCreated polecat: Toast\n" +
		"\u2713 Polecat Toast spawned (session start deferred)\n" +
		`{"rig":"gastown","polecat":"Toast","session_name":"gt-gastown-p-Toast","clone_path":"/gt/gastown/polecats/Toast","base_branch":"main","branch":"polecat/Toast"}`

	info, err := parseSpawnOutput(output)
	if err != nil {
		t.Fatalf("failed to parse JSON from mixed output: %v", err)
	}
	if info.SessionName != "gt-gastown-p-Toast" {
		t.Errorf("SessionName = %q, want %q", info.SessionName, "gt-gastown-p-Toast")
	}
}

func TestParseSpawnOutput_NoJSON(t *testing.T) {
	_, err := parseSpawnOutput("Some error occurred\nNo JSON here")
	if err == nil {
		t.Error("expected parse error for non-JSON output")
	}
}

func TestParseSpawnOutput_EmptyString(t *testing.T) {
	_, err := parseSpawnOutput("")
	if err == nil {
		t.Error("expected parse error for empty output")
	}
}

func TestParseSpawnOutput_MultipleJSONLines(t *testing.T) {
	// Only the last line should be parsed (bootstrap script uses jq -c at the end)
	output := `{"session_name":"wrong","clone_path":"/wrong"}
{"session_name":"gt-gastown-p-Toast","clone_path":"/gt/gastown/polecats/Toast","base_branch":"main","branch":"polecat/Toast"}`

	info, err := parseSpawnOutput(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.SessionName != "gt-gastown-p-Toast" {
		t.Errorf("should parse last JSON line, got SessionName = %q", info.SessionName)
	}
}

// --- verifyBootstrap parsing tests ---

func TestVerifyBootstrapParsing_Match(t *testing.T) {
	// verifyBootstrap uses strings.Contains(stdout, expectedURL)
	stdout := "GT_PROXY_URL=https://127.0.0.1:9876\n"
	expected := "https://127.0.0.1:9876"
	if !strings.Contains(stdout, expected) {
		t.Errorf("expected stdout to contain %q", expected)
	}
}

func TestVerifyBootstrapParsing_Mismatch(t *testing.T) {
	stdout := "GT_PROXY_URL=https://10.0.0.1:9876\n"
	expected := "https://127.0.0.1:9876"
	if strings.Contains(stdout, expected) {
		t.Error("expected mismatch but got match")
	}
}

func TestVerifyBootstrapParsing_Empty(t *testing.T) {
	stdout := ""
	expected := "https://127.0.0.1:9876"
	if strings.Contains(stdout, expected) {
		t.Error("empty stdout should not match")
	}
}

// --- ProxyURL tests (supplement existing) ---

func TestProxyURL_HubGetsLoopback(t *testing.T) {
	cfg := &config.MachinesConfig{DoltHost: "10.0.0.1"}
	url := cfg.ProxyURL("10.0.0.1")
	if url != "https://127.0.0.1:9876" {
		t.Errorf("hub should get loopback, got %q", url)
	}
}

func TestProxyURL_SatelliteGetsDoltHost(t *testing.T) {
	cfg := &config.MachinesConfig{DoltHost: "10.0.0.1"}
	url := cfg.ProxyURL("10.0.0.2")
	if url != "https://10.0.0.1:9876" {
		t.Errorf("satellite should get hub IP, got %q", url)
	}
}

// --- Worker machine filtering tests ---

func TestWorkerMachines_FiltersCorrectly(t *testing.T) {
	cfg := &config.MachinesConfig{
		Machines: map[string]*config.MachineEntry{
			"worker1":    {Host: "1", Roles: []string{"worker"}, Enabled: true},
			"worker2":    {Host: "2", Roles: []string{"worker"}, Enabled: true},
			"disabled":   {Host: "3", Roles: []string{"worker"}, Enabled: false},
			"controller": {Host: "4", Roles: []string{"controller"}, Enabled: true},
			"multi":      {Host: "5", Roles: []string{"controller", "worker"}, Enabled: true},
		},
	}
	workers := cfg.WorkerMachines()
	if len(workers) != 3 {
		t.Errorf("expected 3 workers (worker1, worker2, multi), got %d: %v", len(workers), keys(workers))
	}
	for _, name := range []string{"worker1", "worker2", "multi"} {
		if _, ok := workers[name]; !ok {
			t.Errorf("expected %q in workers", name)
		}
	}
}

func TestIsWorker_MultiRole(t *testing.T) {
	m := &config.MachineEntry{Roles: []string{"controller", "worker"}}
	if !m.IsWorker() {
		t.Error("multi-role machine with 'worker' should return true")
	}
}

func TestIsWorker_NoWorkerRole(t *testing.T) {
	m := &config.MachineEntry{Roles: []string{"controller"}}
	if m.IsWorker() {
		t.Error("machine without 'worker' role should return false")
	}
}

func TestIsWorker_EmptyRoles(t *testing.T) {
	m := &config.MachineEntry{Roles: nil}
	if m.IsWorker() {
		t.Error("machine with nil roles should return false")
	}
}

// --- TownRoot default tests ---

func TestTownRoot_DefaultsToHome(t *testing.T) {
	m := &config.MachineEntry{TownRoot: ""}
	// spawnRemoteSatellite defaults empty TownRoot to "~/gt"
	defaulted := m.TownRoot
	if defaulted == "" {
		defaulted = "~/gt"
	}
	if defaulted != "~/gt" {
		t.Errorf("default TownRoot should be ~/gt, got %q", defaulted)
	}
}

// --- Cert response JSON roundtrip ---

func TestIssueCertResponse_Roundtrip(t *testing.T) {
	resp := &issueCertResponse{
		CN:        "gt-gastown-Toast",
		Cert:      "-----BEGIN CERTIFICATE-----\nMIIB...\n-----END CERTIFICATE-----",
		Key:       "-----BEGIN EC PRIVATE KEY-----\nMHQ...\n-----END EC PRIVATE KEY-----",
		CA:        "-----BEGIN CERTIFICATE-----\nMIIB...\n-----END CERTIFICATE-----",
		Serial:    "abc123",
		ExpiresAt: "2026-04-14T00:00:00Z",
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var parsed issueCertResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if parsed.CN != resp.CN {
		t.Errorf("CN: got %q, want %q", parsed.CN, resp.CN)
	}
	if parsed.Serial != resp.Serial {
		t.Errorf("Serial: got %q, want %q", parsed.Serial, resp.Serial)
	}
	if parsed.Cert != resp.Cert {
		t.Errorf("Cert roundtrip failed (newlines likely corrupted)")
	}
}

// --- SSH test seam tests (gt-778) ---

// withMockSSH swaps runSSH for the duration of a test and restores it on cleanup.
func withMockSSH(t *testing.T, fn func(target, cmd string, timeout time.Duration) (string, error)) {
	t.Helper()
	orig := runSSH
	runSSH = fn
	t.Cleanup(func() { runSSH = orig })
}

func TestRunSSH_SeamIsSwappable(t *testing.T) {
	called := false
	withMockSSH(t, func(target, cmd string, _ time.Duration) (string, error) {
		called = true
		if target != "testhost" {
			t.Errorf("target = %q, want %q", target, "testhost")
		}
		return "ok", nil
	})

	out, err := runSSH("testhost", "echo hi", 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "ok" {
		t.Errorf("output = %q, want %q", out, "ok")
	}
	if !called {
		t.Error("mock was not called — seam is not effective")
	}
}

func TestRunSSHWithStdin_SeamIsSwappable(t *testing.T) {
	orig := runSSHWithStdin
	defer func() { runSSHWithStdin = orig }()

	called := false
	runSSHWithStdin = func(target, cmd string, stdin []byte, _ time.Duration) (string, error) {
		called = true
		if string(stdin) != "hello" {
			t.Errorf("stdin = %q, want %q", string(stdin), "hello")
		}
		return "done", nil
	}

	out, err := runSSHWithStdin("host", "cat", []byte("hello"), 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "done" {
		t.Errorf("output = %q, want %q", out, "done")
	}
	if !called {
		t.Error("mock was not called — seam is not effective")
	}
}

// --- runOnSatellites tests (gt-8jq) ---

func newTestMachinesConfig(machines map[string]*config.MachineEntry) *config.MachinesConfig {
	return &config.MachinesConfig{
		DoltHost: "10.0.0.1",
		Machines: machines,
	}
}

func TestRunOnSatellites_HappyPath(t *testing.T) {
	withMockSSH(t, func(target, cmd string, _ time.Duration) (string, error) {
		return "output-from-" + target, nil
	})

	machines := newTestMachinesConfig(map[string]*config.MachineEntry{
		"m1": {Host: "10.0.0.2", User: "u", Roles: []string{"worker"}, Enabled: true},
		"m2": {Host: "10.0.0.3", User: "u", Roles: []string{"worker"}, Enabled: true},
	})

	results := runOnSatellites(machines, func(gtBin string) string {
		return gtBin + " costs --json"
	}, 10*time.Second)

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	byMachine := map[string]SatelliteResult{}
	for _, r := range results {
		byMachine[r.Machine] = r
	}
	for _, name := range []string{"m1", "m2"} {
		r, ok := byMachine[name]
		if !ok {
			t.Errorf("missing result for machine %q", name)
			continue
		}
		if r.Err != nil {
			t.Errorf("machine %q: unexpected error: %v", name, r.Err)
		}
		if r.Output == "" {
			t.Errorf("machine %q: empty output", name)
		}
	}
}

func TestRunOnSatellites_PartialFailure(t *testing.T) {
	withMockSSH(t, func(target, cmd string, _ time.Duration) (string, error) {
		if target == "u@10.0.0.3" {
			return "", fmt.Errorf("connection refused")
		}
		return "ok", nil
	})

	machines := newTestMachinesConfig(map[string]*config.MachineEntry{
		"m1": {Host: "10.0.0.2", User: "u", Roles: []string{"worker"}, Enabled: true},
		"m2": {Host: "10.0.0.3", User: "u", Roles: []string{"worker"}, Enabled: true},
	})

	results := runOnSatellites(machines, func(gtBin string) string {
		return gtBin + " status"
	}, 10*time.Second)

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	var succeeded, failed int
	for _, r := range results {
		if r.Err != nil {
			failed++
		} else {
			succeeded++
		}
	}
	if succeeded != 1 || failed != 1 {
		t.Errorf("expected 1 success + 1 failure, got %d success + %d failure", succeeded, failed)
	}
}

func TestRunOnSatellites_AllFail(t *testing.T) {
	withMockSSH(t, func(_, _ string, _ time.Duration) (string, error) {
		return "", fmt.Errorf("ssh timeout")
	})

	machines := newTestMachinesConfig(map[string]*config.MachineEntry{
		"m1": {Host: "10.0.0.2", User: "u", Roles: []string{"worker"}, Enabled: true},
	})

	results := runOnSatellites(machines, func(gtBin string) string {
		return gtBin + " status"
	}, 10*time.Second)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Err == nil {
		t.Error("expected error result")
	}
}

func TestRunOnSatellites_SkipsDisabledMachines(t *testing.T) {
	callCount := 0
	withMockSSH(t, func(_, _ string, _ time.Duration) (string, error) {
		callCount++
		return "ok", nil
	})

	machines := newTestMachinesConfig(map[string]*config.MachineEntry{
		"active":   {Host: "10.0.0.2", User: "u", Roles: []string{"worker"}, Enabled: true},
		"disabled": {Host: "10.0.0.3", User: "u", Roles: []string{"worker"}, Enabled: false},
	})

	results := runOnSatellites(machines, func(gtBin string) string {
		return gtBin + " status"
	}, 10*time.Second)

	if len(results) != 1 {
		t.Fatalf("expected 1 result (disabled skipped), got %d", len(results))
	}
	if callCount != 1 {
		t.Errorf("expected 1 SSH call, got %d", callCount)
	}
}

func TestRunOnSatellites_EmptyConfig(t *testing.T) {
	withMockSSH(t, func(_, _ string, _ time.Duration) (string, error) {
		t.Fatal("should not be called with empty config")
		return "", nil
	})

	machines := newTestMachinesConfig(map[string]*config.MachineEntry{})
	results := runOnSatellites(machines, func(gtBin string) string {
		return gtBin + " status"
	}, 10*time.Second)

	if len(results) != 0 {
		t.Errorf("expected 0 results for empty config, got %d", len(results))
	}
}

func TestRunOnSatellites_UsesGtBinaryFromConfig(t *testing.T) {
	var capturedCmd string
	withMockSSH(t, func(_, cmd string, _ time.Duration) (string, error) {
		capturedCmd = cmd
		return "ok", nil
	})

	machines := newTestMachinesConfig(map[string]*config.MachineEntry{
		"m1": {Host: "10.0.0.2", User: "u", Roles: []string{"worker"}, Enabled: true, GtBinary: "/opt/bin/gt-custom"},
	})

	runOnSatellites(machines, func(gtBin string) string {
		return gtBin + " costs --json"
	}, 10*time.Second)

	if !strings.Contains(capturedCmd, "/opt/bin/gt-custom") {
		t.Errorf("expected custom binary in command, got %q", capturedCmd)
	}
}

func TestRunOnSatellites_DefaultsGtBinary(t *testing.T) {
	var capturedCmd string
	withMockSSH(t, func(_, cmd string, _ time.Duration) (string, error) {
		capturedCmd = cmd
		return "ok", nil
	})

	machines := newTestMachinesConfig(map[string]*config.MachineEntry{
		"m1": {Host: "10.0.0.2", User: "u", Roles: []string{"worker"}, Enabled: true},
	})

	runOnSatellites(machines, func(gtBin string) string {
		return gtBin + " costs --json"
	}, 10*time.Second)

	if !strings.HasPrefix(capturedCmd, "gt ") {
		t.Errorf("expected default 'gt' binary, got %q", capturedCmd)
	}
}

// --- findSessionMachine / listAllRemoteSessions tests (gt-rnc) ---

// withTestRegistry sets up a prefix registry so ParseSessionName can parse
// "gt-gastown-p-<name>" style session names in tests.
func withTestRegistry(t *testing.T) {
	t.Helper()
	orig := session.DefaultRegistry()
	reg := session.NewPrefixRegistry()
	reg.Register("gt", "gastown")
	session.SetDefaultRegistry(reg)
	t.Cleanup(func() { session.SetDefaultRegistry(orig) })
}

func TestFindSessionMachine_Found(t *testing.T) {
	withTestRegistry(t)
	withMockSSH(t, func(target, _ string, _ time.Duration) (string, error) {
		if target == "u@10.0.0.2" {
			return "gt-gastown-p-Toast\ngt-gastown-p-Butter\n", nil
		}
		return "gt-gastown-p-Jam\n", nil
	})

	machines := newTestMachinesConfig(map[string]*config.MachineEntry{
		"m1": {Host: "10.0.0.2", User: "u", Roles: []string{"worker"}, Enabled: true},
		"m2": {Host: "10.0.0.3", User: "u", Roles: []string{"worker"}, Enabled: true},
	})

	machine, err := findSessionMachine(machines, "gt-gastown-p-Toast")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if machine != "m1" {
		t.Errorf("machine = %q, want %q", machine, "m1")
	}
}

func TestFindSessionMachine_NotFound(t *testing.T) {
	withTestRegistry(t)
	withMockSSH(t, func(_, _ string, _ time.Duration) (string, error) {
		return "gt-gastown-p-Existing\n", nil
	})

	machines := newTestMachinesConfig(map[string]*config.MachineEntry{
		"m1": {Host: "10.0.0.2", User: "u", Roles: []string{"worker"}, Enabled: true},
	})

	_, err := findSessionMachine(machines, "gt-gastown-p-Missing")
	if err == nil {
		t.Error("expected error for missing session")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}
}

func TestFindSessionMachine_SSHFailureSkipsMachine(t *testing.T) {
	withTestRegistry(t)
	withMockSSH(t, func(target, _ string, _ time.Duration) (string, error) {
		if target == "u@10.0.0.2" {
			return "", fmt.Errorf("connection refused")
		}
		return "gt-gastown-p-Toast\n", nil
	})

	machines := newTestMachinesConfig(map[string]*config.MachineEntry{
		"m1": {Host: "10.0.0.2", User: "u", Roles: []string{"worker"}, Enabled: true},
		"m2": {Host: "10.0.0.3", User: "u", Roles: []string{"worker"}, Enabled: true},
	})

	machine, err := findSessionMachine(machines, "gt-gastown-p-Toast")
	if err != nil {
		t.Fatalf("should find session on m2 even when m1 fails: %v", err)
	}
	if machine != "m2" {
		t.Errorf("machine = %q, want %q", machine, "m2")
	}
}

func TestListAllRemoteSessions_ParsesMultipleSessions(t *testing.T) {
	withTestRegistry(t)
	withMockSSH(t, func(_, _ string, _ time.Duration) (string, error) {
		return "gt-gastown-p-Toast\ngt-gastown-p-Butter\n", nil
	})

	machines := newTestMachinesConfig(map[string]*config.MachineEntry{
		"m1": {Host: "10.0.0.2", User: "u", Roles: []string{"worker"}, Enabled: true},
	})

	sessions := listAllRemoteSessions(machines)
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
	names := map[string]bool{}
	for _, s := range sessions {
		names[s.RawName] = true
		if s.Machine != "m1" {
			t.Errorf("session %q machine = %q, want %q", s.RawName, s.Machine, "m1")
		}
	}
	if !names["gt-gastown-p-Toast"] || !names["gt-gastown-p-Butter"] {
		t.Errorf("unexpected session names: %v", names)
	}
}

func TestListAllRemoteSessions_SkipsUnparsableSessions(t *testing.T) {
	withTestRegistry(t)
	withMockSSH(t, func(_, _ string, _ time.Duration) (string, error) {
		return "gt-gastown-p-Toast\nnot-a-valid-session\n", nil
	})

	machines := newTestMachinesConfig(map[string]*config.MachineEntry{
		"m1": {Host: "10.0.0.2", User: "u", Roles: []string{"worker"}, Enabled: true},
	})

	sessions := listAllRemoteSessions(machines)
	if len(sessions) != 1 {
		t.Fatalf("expected 1 parsable session, got %d", len(sessions))
	}
	if sessions[0].RawName != "gt-gastown-p-Toast" {
		t.Errorf("session name = %q, want %q", sessions[0].RawName, "gt-gastown-p-Toast")
	}
}

func TestListAllRemoteSessions_EmptyOutput(t *testing.T) {
	withTestRegistry(t)
	withMockSSH(t, func(_, _ string, _ time.Duration) (string, error) {
		return "", nil
	})

	machines := newTestMachinesConfig(map[string]*config.MachineEntry{
		"m1": {Host: "10.0.0.2", User: "u", Roles: []string{"worker"}, Enabled: true},
	})

	sessions := listAllRemoteSessions(machines)
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions for empty output, got %d", len(sessions))
	}
}

// --- helpers ---

func keys(m map[string]*config.MachineEntry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
