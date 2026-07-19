//go:build e2e

// Live end-to-end tests against the real Slack MCP endpoint, driving the
// built binary over stdio exactly as Claude Desktop would. Network and a
// logged-in workspace config are required. Run with: make e2e.
package e2e

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// binary returns the built binary path, skipping if make build hasn't run.
func binary(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "dist", "slack-mcp-extender"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("binary not built (%v) — run: make build", err)
	}
	return path
}

// configPath returns the live workspace config, skipping when unset.
func configPath(t *testing.T) string {
	t.Helper()
	path := os.Getenv("SLACK_MCP_EXTENDER_E2E_CONFIG")
	if path == "" {
		t.Skip("SLACK_MCP_EXTENDER_E2E_CONFIG not set — skipping live E2E")
	}
	return path
}

// allowedRoot reads the first allowed root out of the live config so tests
// can construct contained and non-contained paths.
func allowedRoot(t *testing.T, cfgPath string) string {
	t.Helper()
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg struct {
		AllowedRoots []string `json:"allowed_roots"`
		StateDir     string   `json:"state_dir"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if len(cfg.AllowedRoots) == 0 {
		t.Skip("live config has no allowed_roots — containment/upload tests need one")
	}
	return cfg.AllowedRoots[0]
}

// stateDir mirrors config.applyDefaults for the audit-log check.
func stateDir(t *testing.T, cfgPath string) string {
	t.Helper()
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		StateDir string `json:"state_dir"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.StateDir != "" {
		return cfg.StateDir
	}
	abs, err := filepath.Abs(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs))
	return filepath.Join(filepath.Dir(abs), base+".state")
}

// mcpProc drives one `mcp` process as an MCP stdio client.
type mcpProc struct {
	t      *testing.T
	cmd    *exec.Cmd
	stdin  *json.Encoder
	lines  chan []byte
	nextID int
}

func startMCP(t *testing.T) *mcpProc {
	t.Helper()
	cmd := exec.Command(binary(t), "mcp", "--config", configPath(t))
	cmd.Stderr = os.Stderr
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start proxy: %v", err)
	}

	p := &mcpProc{t: t, cmd: cmd, stdin: json.NewEncoder(stdinPipe), lines: make(chan []byte, 64)}
	go func() {
		scanner := bufio.NewScanner(stdoutPipe)
		scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			p.lines <- line
		}
		close(p.lines)
	}()
	t.Cleanup(func() {
		stdinPipe.Close()
		cmd.Wait()
	})

	// MCP handshake.
	resp := p.request("initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "slack-mcp-extender-e2e", "version": "0"},
	})
	if resp["error"] != nil {
		t.Fatalf("initialize failed: %v", resp["error"])
	}
	if err := p.stdin.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		t.Fatalf("initialized notification: %v", err)
	}
	return p
}

// request sends one JSON-RPC request and waits for its response.
func (p *mcpProc) request(method string, params any) map[string]any {
	p.t.Helper()
	p.nextID++
	id := p.nextID
	if err := p.stdin.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		p.t.Fatalf("send %s: %v", method, err)
	}
	deadline := time.After(90 * time.Second)
	for {
		select {
		case line, ok := <-p.lines:
			if !ok {
				p.t.Fatalf("proxy closed while waiting for %s response", method)
			}
			var msg map[string]any
			if json.Unmarshal(line, &msg) != nil {
				continue
			}
			if idv, ok := msg["id"].(float64); ok && int(idv) == id {
				return msg
			}
		case <-deadline:
			p.t.Fatalf("timeout waiting for %s response", method)
		}
	}
}

// callTool invokes tools/call and returns the concatenated text payload
// plus the isError flag.
func (p *mcpProc) callTool(name string, args map[string]any) (string, bool) {
	p.t.Helper()
	resp := p.request("tools/call", map[string]any{"name": name, "arguments": args})
	if resp["error"] != nil {
		p.t.Fatalf("tools/call %s protocol error: %v", name, resp["error"])
	}
	result, _ := resp["result"].(map[string]any)
	isError, _ := result["isError"].(bool)
	var texts []string
	if content, ok := result["content"].([]any); ok {
		for _, c := range content {
			if m, ok := c.(map[string]any); ok && m["type"] == "text" {
				if s, ok := m["text"].(string); ok {
					texts = append(texts, s)
				}
			}
		}
	}
	return strings.Join(texts, "\n"), isError
}

// toolError decodes the structured {code, details} error payload.
func toolError(t *testing.T, text string) (code, reason string) {
	t.Helper()
	var payload struct {
		Code    string `json:"code"`
		Details struct {
			Reason string `json:"reason"`
		} `json:"details"`
	}
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("tool error payload not JSON: %v (%s)", err, text)
	}
	return payload.Code, payload.Details.Reason
}

// TestLiveTransparency verifies the proxy relays the upstream tool set and
// injects exactly the two upload tools, preserving upstream-only fields.
func TestLiveTransparency(t *testing.T) {
	p := startMCP(t)
	resp := p.request("tools/list", map[string]any{})
	result, _ := resp["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) < 3 {
		t.Fatalf("tools = %d, want upstream set + 3 injected", len(tools))
	}

	injected := 0
	extraFields := map[string]bool{}
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		name, _ := tool["name"].(string)
		if name == "ext_file_upload" || name == "ext_file_upload_to_thread" || name == "ext_file_download" {
			injected++
			continue
		}
		for key := range tool {
			switch key {
			case "name", "description", "inputSchema":
			default:
				extraFields[key] = true
			}
		}
	}
	if injected != 3 {
		t.Errorf("injected tools = %d, want 3", injected)
	}
	if len(extraFields) == 0 {
		t.Error("no upstream-only tool fields survived the merge — transparency regression")
	}
	t.Logf("tools=%d injected=%d preserved-extra-fields=%v", len(tools), injected, extraFields)
}

// TestLiveContainmentDenials exercises the deny paths through the live
// proxy. Nothing is ever sent to Slack: every case fails before upload.
func TestLiveContainmentDenials(t *testing.T) {
	cfgPath := configPath(t)
	root := allowedRoot(t, cfgPath)
	p := startMCP(t)

	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	hiddenDir := filepath.Join(root, ".e2e-hidden")
	if err := os.MkdirAll(hiddenDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hidden := filepath.Join(hiddenDir, "secret.txt")
	if err := os.WriteFile(hidden, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(hiddenDir) })

	cases := []struct {
		label      string
		tool       string
		args       map[string]any
		wantCode   string
		wantReason string
	}{
		{"outside roots", "ext_file_upload", map[string]any{"channel_id": "C000TEST", "file": outside}, "path_denied", "outside_allowed_roots"},
		{"traversal", "ext_file_upload", map[string]any{"channel_id": "C000TEST", "file": filepath.Join(root, "..", filepath.Base(t.TempDir()))}, "path_denied", ""},
		{"hidden below root", "ext_file_upload", map[string]any{"channel_id": "C000TEST", "file": hidden}, "path_denied", "hidden_component"},
		{"missing file", "ext_file_upload", map[string]any{"channel_id": "C000TEST", "file": filepath.Join(root, "no-such-file-e2e.bin")}, "path_denied", "not_found"},
		{"thread without ts", "ext_file_upload_to_thread", map[string]any{"channel_id": "C000TEST", "file": hidden}, "invalid_arguments", ""},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			text, isError := p.callTool(tc.tool, tc.args)
			if !isError {
				t.Fatalf("call unexpectedly succeeded: %s", text)
			}
			code, reason := toolError(t, text)
			if code != tc.wantCode {
				t.Errorf("code = %q, want %q (%s)", code, tc.wantCode, text)
			}
			if tc.wantReason != "" && reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", reason, tc.wantReason)
			}
		})
	}
}

// TestLiveUploadRootAndThread posts a real root attachment and a thread
// reply. Gated on SLACK_MCP_EXTENDER_E2E_CHANNEL so nothing is posted
// without an explicit opt-in.
func TestLiveUploadRootAndThread(t *testing.T) {
	channel := os.Getenv("SLACK_MCP_EXTENDER_E2E_CHANNEL")
	if channel == "" {
		t.Skip("SLACK_MCP_EXTENDER_E2E_CHANNEL not set — skipping posting test")
	}
	cfgPath := configPath(t)
	root := allowedRoot(t, cfgPath)

	testFile := filepath.Join(root, "slack-mcp-extender-e2e.txt")
	if err := os.WriteFile(testFile, []byte("slack-mcp-extender Go E2E\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(testFile) })

	p := startMCP(t)

	// Root attachment via the injected tool.
	text, isError := p.callTool("ext_file_upload", map[string]any{
		"channel_id": channel,
		"file":       testFile,
		"comment":    "slack-mcp-extender Go E2E: root attachment",
	})
	if isError {
		t.Fatalf("upload_file: %s", text)
	}
	var rootRes struct {
		OK     bool   `json:"ok"`
		FileID string `json:"file_id"`
	}
	if err := json.Unmarshal([]byte(text), &rootRes); err != nil || !rootRes.OK || rootRes.FileID == "" {
		t.Fatalf("upload_file result = %s (err %v)", text, err)
	}

	// Find the root message ts through the upstream read tool (transparent
	// path), then thread-reply via the injected tool.
	time.Sleep(3 * time.Second)
	readText, isError := p.callTool("slack_read_channel", map[string]any{"channel_id": channel})
	if isError {
		t.Fatalf("slack_read_channel: %s", readText)
	}
	tsCandidates := regexp.MustCompile(`\b\d{10}\.\d{6}\b`).FindAllString(readText, -1)
	if len(tsCandidates) == 0 {
		t.Fatalf("no ts in channel read: %.300s", readText)
	}
	threadTS := tsCandidates[0]
	for _, ts := range tsCandidates {
		if ts > threadTS {
			threadTS = ts
		}
	}

	text, isError = p.callTool("ext_file_upload_to_thread", map[string]any{
		"channel_id": channel,
		"file":       testFile,
		"filename":   "e2e-thread-reply.txt",
		"thread_ts":  threadTS,
		"comment":    "slack-mcp-extender Go E2E: thread attachment",
	})
	if isError {
		t.Fatalf("upload_file_to_thread: %s", text)
	}
	var threadRes struct {
		OK       bool   `json:"ok"`
		ThreadTS string `json:"thread_ts"`
	}
	if err := json.Unmarshal([]byte(text), &threadRes); err != nil || !threadRes.OK || threadRes.ThreadTS != threadTS {
		t.Fatalf("upload_file_to_thread result = %s (err %v)", text, err)
	}

	// Both egress events landed in the audit log.
	audit := filepath.Join(stateDir(t, cfgPath), "audit.jsonl")
	data, err := os.ReadFile(audit)
	if err != nil {
		t.Fatalf("audit log: %v", err)
	}
	if got := strings.Count(string(data), fmt.Sprintf(`"file_id":%q`, rootRes.FileID)); got != 1 {
		t.Errorf("root upload audit entries = %d, want 1", got)
	}
	t.Logf("root file=%s thread_ts=%s — verify visually in the channel", rootRes.FileID, threadTS)
}

// TestLiveDownloadRoundtrip uploads a file and downloads it back through
// ext_file_download, verifying byte-for-byte parity. Gated on
// SLACK_MCP_EXTENDER_E2E_CHANNEL like the posting test.
func TestLiveDownloadRoundtrip(t *testing.T) {
	channel := os.Getenv("SLACK_MCP_EXTENDER_E2E_CHANNEL")
	if channel == "" {
		t.Skip("SLACK_MCP_EXTENDER_E2E_CHANNEL not set — skipping roundtrip test")
	}
	cfgPath := configPath(t)
	root := allowedRoot(t, cfgPath)

	content := fmt.Sprintf("roundtrip payload %d\n", os.Getpid())
	source := filepath.Join(root, "smx-roundtrip-src.txt")
	if err := os.WriteFile(source, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(source) })

	p := startMCP(t)

	// Up: post the file.
	text, isError := p.callTool("ext_file_upload", map[string]any{
		"channel_id": channel,
		"file":       source,
		"comment":    "slack-mcp-extender Go E2E: download roundtrip source",
	})
	if isError {
		t.Fatalf("ext_file_upload: %s", text)
	}
	var up struct {
		FileID string `json:"file_id"`
	}
	if err := json.Unmarshal([]byte(text), &up); err != nil || up.FileID == "" {
		t.Fatalf("upload result = %s", text)
	}

	// Down: fetch it back under a different name.
	time.Sleep(2 * time.Second)
	text, isError = p.callTool("ext_file_download", map[string]any{
		"file_id":  up.FileID,
		"dest_dir": root,
		"filename": "smx-roundtrip-back.txt",
	})
	if isError {
		t.Fatalf("ext_file_download: %s", text)
	}
	var down struct {
		OK   bool   `json:"ok"`
		Path string `json:"path"`
		Size int64  `json:"size"`
	}
	if err := json.Unmarshal([]byte(text), &down); err != nil || !down.OK {
		t.Fatalf("download result = %s", text)
	}
	t.Cleanup(func() { os.Remove(down.Path) })

	got, err := os.ReadFile(down.Path)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(got) != content {
		t.Errorf("roundtrip mismatch: %q vs %q", got, content)
	}
	t.Logf("roundtrip OK: %s -> %s -> %s (%d bytes)", source, up.FileID, down.Path, down.Size)
}
