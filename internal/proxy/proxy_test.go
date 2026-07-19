package proxy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nlink-jp/slack-mcp-extender/internal/containment"
	"github.com/nlink-jp/slack-mcp-extender/internal/upload"
)

// mockUpstream is an in-memory transport.Transport.
type mockUpstream struct {
	mu   sync.Mutex
	sent [][]byte

	incoming chan []byte
	// respond, when set, is invoked on every Send; returned messages are
	// queued as upstream output.
	respond func(sent []byte) [][]byte

	closeOnce sync.Once
	sendErr   error
}

func newMockUpstream() *mockUpstream {
	return &mockUpstream{incoming: make(chan []byte, 16)}
}

func (m *mockUpstream) Send(data []byte) error {
	if m.sendErr != nil {
		return m.sendErr
	}
	cp := append([]byte(nil), data...)
	m.mu.Lock()
	m.sent = append(m.sent, cp)
	respond := m.respond
	m.mu.Unlock()
	if respond != nil {
		for _, msg := range respond(cp) {
			m.incoming <- msg
		}
	}
	return nil
}

func (m *mockUpstream) ReadLine() ([]byte, bool) {
	data, ok := <-m.incoming
	return data, ok
}

func (m *mockUpstream) Close() error {
	m.closeOnce.Do(func() { close(m.incoming) })
	return nil
}

func (m *mockUpstream) sentLines() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.sent))
	for i, b := range m.sent {
		out[i] = string(b)
	}
	return out
}

// stubUploader records the upload request and returns a canned result.
type stubUploader struct {
	mu  sync.Mutex
	req *upload.Request
	res *upload.Result
	err error
}

func (s *stubUploader) Upload(r upload.Request) (*upload.Result, error) {
	s.mu.Lock()
	s.req = &r
	s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	res := *s.res
	res.ChannelID = r.ChannelID
	res.ThreadTS = r.ThreadTS
	return &res, nil
}

// harness is the dummy MCP client: it writes agent lines into the proxy
// and reads the proxy's agent-facing output.
type harness struct {
	t       *testing.T
	up      *mockUpstream
	agentW  io.WriteCloser
	outLine chan string
	done    chan error
}

func newHarness(t *testing.T, injected *InjectedTools, timeoutMs int) *harness {
	t.Helper()
	up := newMockUpstream()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()

	p := &Proxy{
		Upstream:  up,
		Injected:  injected,
		In:        inR,
		Out:       outW,
		TimeoutMs: timeoutMs,
		Logf:      func(format string, args ...any) { t.Logf("proxy: "+strings.TrimSuffix(format, "\n"), args...) },
	}

	h := &harness{t: t, up: up, agentW: inW, outLine: make(chan string, 16), done: make(chan error, 1)}
	go func() { h.done <- p.Run() }()
	go func() {
		scanner := bufio.NewScanner(outR)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		for scanner.Scan() {
			h.outLine <- scanner.Text()
		}
		close(h.outLine)
	}()
	t.Cleanup(func() {
		inW.Close()
		up.Close()
		outW.Close()
	})
	return h
}

func (h *harness) send(line string) {
	h.t.Helper()
	if _, err := io.WriteString(h.agentW, line+"\n"); err != nil {
		h.t.Fatalf("send: %v", err)
	}
}

func (h *harness) expect() string {
	h.t.Helper()
	select {
	case line, ok := <-h.outLine:
		if !ok {
			h.t.Fatal("agent output closed")
		}
		return line
	case <-time.After(2 * time.Second):
		h.t.Fatal("timed out waiting for agent output")
		return ""
	}
}

func testInjected(t *testing.T, uploader FileUploader) (*InjectedTools, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	policy, err := containment.NewPolicy([]string{root}, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	return &InjectedTools{
		Policy:   policy,
		Uploader: uploader,
		Audit:    &upload.AuditLog{Path: filepath.Join(root, "..", "audit.jsonl")},
	}, root
}

// --- transparency ---

func TestRequestForwardedVerbatimAndResultPreserved(t *testing.T) {
	h := newHarness(t, nil, 1000)
	h.up.respond = func(sent []byte) [][]byte {
		return [][]byte{[]byte(`{"jsonrpc":"2.0","id":1,"result":{"exotic":{"nested":[1,2,3]},"unknownField":"kept"}}`)}
	}

	request := `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"slack://x","futureParam":true}}`
	h.send(request)
	got := h.expect()

	// The request reached upstream byte-for-byte.
	sent := h.up.sentLines()
	if len(sent) != 1 || sent[0] != request {
		t.Errorf("upstream got %q", sent)
	}
	// The result content survived intact.
	var resp struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(got), &resp); err != nil {
		t.Fatalf("agent got non-JSON: %v", err)
	}
	if want := `{"exotic":{"nested":[1,2,3]},"unknownField":"kept"}`; string(resp.Result) != want {
		t.Errorf("result = %s, want %s", resp.Result, want)
	}
}

func TestNotificationsForwardedVerbatimBothWays(t *testing.T) {
	h := newHarness(t, nil, 1000)

	// Agent → upstream.
	note := `{"jsonrpc":"2.0","method":"notifications/initialized","params":{"odd":1}}`
	h.send(note)
	deadline := time.Now().Add(time.Second)
	for len(h.up.sentLines()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if sent := h.up.sentLines(); len(sent) != 1 || sent[0] != note {
		t.Errorf("upstream got %q", sent)
	}

	// Upstream → agent (verbatim raw relay).
	upNote := `{"jsonrpc":"2.0","method":"notifications/tools/list_changed","params":{"x":[true]}}`
	h.up.incoming <- []byte(upNote)
	if got := h.expect(); got != upNote {
		t.Errorf("agent got %q, want %q", got, upNote)
	}
}

func TestServerInitiatedRequestRoundtrip(t *testing.T) {
	h := newHarness(t, nil, 1000)

	// Upstream asks the agent something (e.g. sampling); relayed verbatim.
	serverReq := `{"jsonrpc":"2.0","id":"srv-1","method":"sampling/createMessage","params":{}}`
	h.up.incoming <- []byte(serverReq)
	if got := h.expect(); got != serverReq {
		t.Errorf("agent got %q", got)
	}

	// The agent's response flows back verbatim.
	agentResp := `{"jsonrpc":"2.0","id":"srv-1","result":{"role":"assistant"}}`
	h.send(agentResp)
	deadline := time.Now().Add(time.Second)
	for len(h.up.sentLines()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if sent := h.up.sentLines(); len(sent) != 1 || sent[0] != agentResp {
		t.Errorf("upstream got %q", sent)
	}
}

// --- tools/list merge ---

func TestToolsListMergeInjectsAndPreserves(t *testing.T) {
	injected, _ := testInjected(t, &stubUploader{res: &upload.Result{}})
	h := newHarness(t, injected, 1000)
	h.up.respond = func([]byte) [][]byte {
		return [][]byte{[]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"slack_search","title":"Search","annotations":{"readOnlyHint":true}}],"nextCursor":"c1"}}`)}
	}

	h.send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	got := h.expect()

	var resp struct {
		Result struct {
			Tools      []map[string]json.RawMessage `json:"tools"`
			NextCursor string                       `json:"nextCursor"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(got), &resp); err != nil {
		t.Fatalf("agent got: %v (%s)", err, got)
	}
	if resp.Result.NextCursor != "c1" {
		t.Error("nextCursor lost")
	}
	if len(resp.Result.Tools) != 3 {
		t.Fatalf("tools = %d, want 3 (1 upstream + 2 injected)", len(resp.Result.Tools))
	}
	if _, ok := resp.Result.Tools[0]["annotations"]; !ok {
		t.Error("upstream tool annotations lost")
	}
	names := map[string]bool{}
	for _, tool := range resp.Result.Tools {
		var n string
		_ = json.Unmarshal(tool["name"], &n)
		names[n] = true
	}
	if !names[ToolUploadFile] || !names[ToolUploadFileToThread] {
		t.Errorf("injected tools missing: %v", names)
	}
}

func TestToolsListErrorPassesThrough(t *testing.T) {
	injected, _ := testInjected(t, &stubUploader{res: &upload.Result{}})
	h := newHarness(t, injected, 1000)
	h.up.respond = func([]byte) [][]byte {
		return [][]byte{[]byte(`{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":"upstream sad"}}`)}
	}
	h.send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	got := h.expect()
	if !strings.Contains(got, "upstream sad") {
		t.Errorf("agent got %q", got)
	}
}

// --- tools/call routing ---

func TestToolsCallUpstreamToolForwarded(t *testing.T) {
	injected, _ := testInjected(t, &stubUploader{res: &upload.Result{}})
	h := newHarness(t, injected, 1000)
	h.up.respond = func([]byte) [][]byte {
		return [][]byte{[]byte(`{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"hits"}]}}`)}
	}

	call := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"slack_search","arguments":{"query":"q"}}}`
	h.send(call)
	got := h.expect()
	if sent := h.up.sentLines(); len(sent) != 1 || sent[0] != call {
		t.Errorf("upstream got %q", sent)
	}
	if !strings.Contains(got, "hits") {
		t.Errorf("agent got %q", got)
	}
}

func TestToolsCallInjectedHandledLocally(t *testing.T) {
	stub := &stubUploader{res: &upload.Result{FileID: "F42", Filename: "r.txt", Size: 2}}
	injected, root := testInjected(t, stub)
	h := newHarness(t, injected, 1000)

	file := filepath.Join(root, "r.txt")
	if err := os.WriteFile(file, []byte("ab"), 0o644); err != nil {
		t.Fatal(err)
	}

	h.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"upload_file","arguments":{"channel_id":"C1","file":%q,"comment":"here"}}}`, file))
	got := h.expect()

	if len(h.up.sentLines()) != 0 {
		t.Errorf("injected call leaked upstream: %q", h.up.sentLines())
	}
	if !strings.Contains(got, `\"ok\":true`) || !strings.Contains(got, "F42") {
		t.Errorf("agent got %q", got)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.req == nil || stub.req.Path != file || stub.req.ChannelID != "C1" || stub.req.Comment != "here" {
		t.Errorf("upload request = %+v", stub.req)
	}
	if stub.req.ThreadTS != "" {
		t.Errorf("root upload got thread_ts %q", stub.req.ThreadTS)
	}
}

func TestToolsCallInjectedThread(t *testing.T) {
	stub := &stubUploader{res: &upload.Result{FileID: "F1"}}
	injected, root := testInjected(t, stub)
	h := newHarness(t, injected, 1000)

	file := filepath.Join(root, "x.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	h.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"upload_file_to_thread","arguments":{"channel_id":"C1","file":%q,"thread_ts":"171.001"}}}`, file))
	got := h.expect()
	if !strings.Contains(got, `\"ok\":true`) {
		t.Errorf("agent got %q", got)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.req.ThreadTS != "171.001" {
		t.Errorf("thread_ts = %q", stub.req.ThreadTS)
	}
}

func TestToolsCallInjectedPathDenied(t *testing.T) {
	stub := &stubUploader{res: &upload.Result{}}
	injected, root := testInjected(t, stub)
	h := newHarness(t, injected, 1000)

	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	if err := os.WriteFile(outside, []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}

	h.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"upload_file","arguments":{"channel_id":"C1","file":%q}}}`, outside))
	got := h.expect()

	if len(h.up.sentLines()) != 0 {
		t.Error("denied call leaked upstream")
	}
	if !strings.Contains(got, "path_denied") || !strings.Contains(got, `\"isError\":true`) &&
		!strings.Contains(got, `"isError":true`) {
		t.Errorf("agent got %q", got)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.req != nil {
		t.Error("uploader invoked despite denial")
	}

	// The denial landed in the audit log.
	data, err := os.ReadFile(injected.Audit.Path)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if !strings.Contains(string(data), `"outcome":"denied"`) {
		t.Errorf("audit = %s", data)
	}
}

// --- failure paths ---

func TestUpstreamTimeoutSurfacesError(t *testing.T) {
	h := newHarness(t, nil, 50) // upstream never responds
	h.send(`{"jsonrpc":"2.0","id":9,"method":"ping"}`)
	got := h.expect()
	if !strings.Contains(got, "upstream timeout") {
		t.Errorf("agent got %q", got)
	}
}

func TestUpstreamSendErrorSurfaces(t *testing.T) {
	h := newHarness(t, nil, 1000)
	h.up.sendErr = fmt.Errorf("token refresh failed: run login")
	h.send(`{"jsonrpc":"2.0","id":10,"method":"ping"}`)
	got := h.expect()
	if !strings.Contains(got, "run login") || !strings.Contains(got, "-32603") {
		t.Errorf("agent got %q", got)
	}
}

func TestMalformedAgentLineSkipped(t *testing.T) {
	h := newHarness(t, nil, 1000)
	h.up.respond = func([]byte) [][]byte {
		return [][]byte{[]byte(`{"jsonrpc":"2.0","id":11,"result":{}}`)}
	}
	h.send(`{this is not json`)
	h.send(`{"jsonrpc":"2.0","id":11,"method":"ping"}`)
	got := h.expect()
	if !strings.Contains(got, `"id":11`) {
		t.Errorf("agent got %q", got)
	}
}
