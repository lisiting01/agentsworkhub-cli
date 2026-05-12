package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractOpenClawText_TopLevelPayloads(t *testing.T) {
	resp := &openclawJSONResponse{}
	resp.Payloads = []struct {
		Text     string `json:"text"`
		MediaURL string `json:"mediaUrl"`
	}{
		{Text: "hello"},
		{Text: ""},
		{Text: "world"},
	}
	got := extractOpenClawText(resp)
	if got != "hello\nworld" {
		t.Errorf("extractOpenClawText = %q, want %q", got, "hello\nworld")
	}
}

func TestExtractOpenClawText_ResultPayloadsFallback(t *testing.T) {
	// Gateway-backed responses put the same shape under .result so we must
	// fall back when top-level is empty.
	resp := &openclawJSONResponse{}
	resp.Result.Payloads = []struct {
		Text     string `json:"text"`
		MediaURL string `json:"mediaUrl"`
	}{
		{Text: "from-gateway"},
	}
	got := extractOpenClawText(resp)
	if got != "from-gateway" {
		t.Errorf("extractOpenClawText fallback = %q, want %q", got, "from-gateway")
	}
}

func TestExtractOpenClawText_Empty(t *testing.T) {
	if got := extractOpenClawText(&openclawJSONResponse{}); got != "" {
		t.Errorf("expected empty string for no payloads, got %q", got)
	}
}

func TestOpenClawEngine_PrepareMessage_SmallStaysInline(t *testing.T) {
	dir := t.TempDir()
	e := &OpenClawEngine{workerDir: dir}
	msg, cleanup := e.prepareMessage("sys appendix", "user message")
	defer cleanup()

	if !strings.Contains(msg, "sys appendix") || !strings.Contains(msg, "user message") {
		t.Errorf("expected inline message to contain both parts, got %q", msg)
	}
	// No spill files should appear in the worker dir.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "openclaw-message-") {
			t.Errorf("unexpected spill file %s for small message", e.Name())
		}
	}
}

func TestOpenClawEngine_PrepareMessage_LargeSpillsToFile(t *testing.T) {
	dir := t.TempDir()
	e := &OpenClawEngine{workerDir: dir}

	huge := strings.Repeat("X", openClawMessageInlineLimit+1)
	msg, cleanup := e.prepareMessage("", huge)

	// We expect a pointer message that references a real on-disk file.
	if !strings.Contains(msg, "Use your file-reading tool") {
		t.Errorf("expected pointer-style message for oversized payload, got %q", msg)
	}

	// Find the spill file and verify it actually contains the original payload.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var spillPath string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "openclaw-message-") {
			spillPath = filepath.Join(dir, e.Name())
			break
		}
	}
	if spillPath == "" {
		t.Fatalf("expected a spill file in %s, found %d entries", dir, len(entries))
	}
	data, err := os.ReadFile(spillPath)
	if err != nil {
		t.Fatalf("read spill: %v", err)
	}
	if string(data) != huge {
		t.Errorf("spill contents differ from original payload (len got=%d want=%d)", len(data), len(huge))
	}

	// Cleanup must remove the spill file so workers don't accumulate junk
	// across turns.
	cleanup()
	if _, err := os.Stat(spillPath); !os.IsNotExist(err) {
		t.Errorf("expected spill file removed after cleanup, got err=%v", err)
	}
}

func TestOpenClawEngine_BuildArgs_GatewayMode(t *testing.T) {
	e := &OpenClawEngine{
		agentID:   "main",
		sessionID: "awh-worker-w12345678",
	}
	args := e.buildArgs("hello")

	want := []string{"agent", "--json", "--agent", "main", "--session-id", "awh-worker-w12345678", "--message", "hello"}
	if len(args) != len(want) {
		t.Fatalf("got args %v, want %v", args, want)
	}
	for i, a := range want {
		if args[i] != a {
			t.Errorf("arg[%d] = %q, want %q", i, args[i], a)
		}
	}
}

func TestOpenClawEngine_BuildArgs_LocalMode(t *testing.T) {
	e := &OpenClawEngine{
		agentID:   "main",
		sessionID: "awh-worker-w1",
		useLocal:  true,
	}
	args := e.buildArgs("m")

	// `--local` must be present and must come before --message so it
	// applies to the agent invocation, not be parsed as a positional.
	foundLocal := false
	for _, a := range args {
		if a == "--local" {
			foundLocal = true
		}
	}
	if !foundLocal {
		t.Errorf("expected --local in args, got %v", args)
	}
}

func TestOpenClawEngine_BuildArgs_AppendsExtraArgs(t *testing.T) {
	e := &OpenClawEngine{
		agentID:   "main",
		sessionID: "s",
		extraArgs: []string{"--thinking", "high"},
	}
	args := e.buildArgs("m")
	last := args[len(args)-2:]
	if last[0] != "--thinking" || last[1] != "high" {
		t.Errorf("expected extraArgs at the end, got %v", args)
	}
}

func TestNewEngine_OpenClaw_ReturnsCorrectType(t *testing.T) {
	eng := NewEngine("openclaw", "/usr/local/bin/openclaw", "ignored", []string{"--thinking", "high"}, map[string]string{"FOO": "bar"}, EngineOptions{
		OpenClawAgentID:   "ops",
		OpenClawSessionID: "sess-1",
		OpenClawLocal:     true,
		WorkerDir:         "/tmp/awh-worker",
	})
	oc, ok := eng.(*OpenClawEngine)
	if !ok {
		t.Fatalf("NewEngine(openclaw) = %T, want *OpenClawEngine", eng)
	}
	if oc.path != "/usr/local/bin/openclaw" {
		t.Errorf("path = %q, want /usr/local/bin/openclaw", oc.path)
	}
	if oc.agentID != "ops" {
		t.Errorf("agentID = %q, want ops", oc.agentID)
	}
	if oc.sessionID != "sess-1" {
		t.Errorf("sessionID = %q, want sess-1", oc.sessionID)
	}
	if !oc.useLocal {
		t.Errorf("useLocal = false, want true")
	}
	if oc.workerDir != "/tmp/awh-worker" {
		t.Errorf("workerDir = %q, want /tmp/awh-worker", oc.workerDir)
	}
	if len(oc.extraArgs) != 2 || oc.extraArgs[0] != "--thinking" {
		t.Errorf("extraArgs = %v, want [--thinking high]", oc.extraArgs)
	}
	if oc.extraEnv["FOO"] != "bar" {
		t.Errorf("extraEnv[FOO] = %q, want bar", oc.extraEnv["FOO"])
	}
	if oc.Name() != "openclaw" {
		t.Errorf("Name() = %q, want openclaw", oc.Name())
	}
	// Smoke: streaming engine interface satisfied.
	if _, ok := eng.(StreamingEngine); !ok {
		t.Errorf("OpenClawEngine should implement StreamingEngine")
	}
}

func TestNewEngine_NonOpenClawIgnoresOptions(t *testing.T) {
	// Options must be a no-op for engines that do not consume them, so
	// callers can always pass the same struct regardless of engine.
	eng := NewEngine("claude", "claude", "model-x", nil, nil, EngineOptions{
		OpenClawAgentID: "ignored",
		OpenClawLocal:   true,
	})
	if _, ok := eng.(*ClaudeEngine); !ok {
		t.Fatalf("NewEngine(claude) = %T, want *ClaudeEngine", eng)
	}
}
