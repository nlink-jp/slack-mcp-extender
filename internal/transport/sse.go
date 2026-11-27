package transport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/nlink-jp/mcp/httpwire"
)

// Bounds on what the upstream sends (ADR-0005 §6).
const (
	// maxJSONBytes bounds a JSON answer body.
	maxJSONBytes = 8 << 20 // 8 MiB
	// maxSSEBytes bounds one SSE line and one event's joined data lines.
	maxSSEBytes = 1 << 20 // 1 MiB
)

const (
	codeInvalidParams = -32602
	codeInternalError = -32603
)

// sseClientTransport implements Transport for MCP servers speaking the
// Streamable HTTP transport (formerly SSE):
//
//   - the client POSTs JSON-RPC messages to the server endpoint;
//   - the server answers with application/json (a single message) or
//     text/event-stream (an SSE stream of "message" events);
//   - session affinity, when a legacy server wants it, rides on the
//     Mcp-Session-Id header.
//
// The era is the agent's (ADR-0005 §2). What the specification requires of an
// HTTP client is added here, through nlink-jp/mcp/httpwire: the headers
// derived from each body (Tracker.Outgoing), the version and x-mcp-header
// marks learned from the answers (Tracker.Incoming), and the judgement of
// non-2xx answers (Classify).
type sseClientTransport struct {
	endpoint string
	client   *http.Client
	auth     TokenProvider // nil if unauthenticated
	logf     func(format string, args ...any)

	tracker *httpwire.Tracker

	incoming chan []byte
	closed   chan struct{}

	mu        sync.Mutex
	isClosed  bool
	sessionID string

	// refreshMu serializes the tools/list refreshes a HeaderMismatch starts;
	// refreshSeq numbers their ids (ADR-0005 §7).
	refreshMu  sync.Mutex
	refreshSeq atomic.Uint64
}

// SSEOption configures optional behavior of the SSE transport.
type SSEOption func(*sseClientTransport)

// WithTokenProvider sets a TokenProvider for Bearer authentication. On a
// 401 the token is invalidated and the request retried once.
func WithTokenProvider(auth TokenProvider) SSEOption {
	return func(t *sseClientTransport) { t.auth = auth }
}

// WithHTTPClient overrides the HTTP client (tests).
func WithHTTPClient(c *http.Client) SSEOption {
	return func(t *sseClientTransport) { t.client = c }
}

// WithLogf sends the transport's diagnostics — a removed tool, a batch sent
// without protocol headers, a synthesized answer — to logf (stderr in
// production). Nothing here logs argument or result content.
func WithLogf(logf func(format string, args ...any)) SSEOption {
	return func(t *sseClientTransport) {
		if logf != nil {
			t.logf = logf
		}
	}
}

// NewSSEClientTransport creates a Streamable HTTP client transport for the
// given MCP endpoint URL.
func NewSSEClientTransport(endpoint string, opts ...SSEOption) (Transport, error) {
	if endpoint == "" {
		return nil, fmt.Errorf("SSE endpoint URL is required")
	}
	t := &sseClientTransport{
		endpoint: endpoint,
		client:   &http.Client{},
		logf:     func(string, ...any) {},
		tracker:  httpwire.NewTracker(),
		incoming: make(chan []byte, 64),
		closed:   make(chan struct{}),
	}
	for _, opt := range opts {
		opt(t)
	}
	return t, nil
}

func (t *sseClientTransport) log(format string, args ...any) {
	t.logf("slack-mcp-extender: "+format+"\n", args...)
}

// outgoing is one message on its way upstream.
type outgoing struct {
	body []byte
	// id is the request's id; nil for a notification, a response and a batch.
	id json.RawMessage
	// method is the request's method; empty unless id is set.
	method string
	// header holds the MCP protocol headers derived from the body.
	header http.Header
	// retried marks the one resend after a HeaderMismatch (ADR-0005 §7).
	retried bool
	// ctx bounds the request; nil means none. Only refreshes set one.
	ctx context.Context
}

// Send posts a JSON-RPC message; responses (JSON or SSE) are queued into
// the incoming channel.
func (t *sseClientTransport) Send(data []byte) error {
	out, err := t.prepare(data)
	if err != nil || out == nil {
		return err
	}
	resp, err := t.postRenewing(out)
	if err != nil {
		return err
	}
	return t.handleResponse(resp, out)
}

// prepare derives the protocol headers. A nil outgoing with a nil error
// means the request was answered here and is not to be sent (ADR-0005 §5).
func (t *sseClientTransport) prepare(data []byte) (*outgoing, error) {
	out := &outgoing{body: data}
	batch := isBatch(data)
	if !batch {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(data, &m) == nil && m.Method != "" && len(m.ID) > 0 && string(m.ID) != "null" {
			out.id, out.method = m.ID, m.Method
		}
	}
	header, err := t.tracker.Outgoing(data)
	if err != nil {
		if batch {
			t.log("forwarding a batch without MCP protocol headers")
			return out, nil
		}
		if !isModern(data) {
			// A legacy message fails only when the negotiated version cannot
			// go into a header. It goes without protocol headers, as before
			// the migration (ADR-0005 §10.2).
			t.log("sending a message without MCP protocol headers: %v", err)
			return out, nil
		}
		if out.id != nil {
			// httpwire's error names the argument and the tool, never a value.
			t.enqueue(errorBytes(out.id, codeInvalidParams, err.Error()))
			return nil, nil
		}
		return nil, fmt.Errorf("not sent: %w", err)
	}
	out.header = header
	return out, nil
}

// postRenewing posts the message, renewing the token once on a 401.
func (t *sseClientTransport) postRenewing(out *outgoing) (*http.Response, error) {
	resp, err := t.doPost(out)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusUnauthorized && t.auth != nil {
		_ = resp.Body.Close()
		t.auth.Invalidate()
		resp, err = t.doPost(out)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("HTTP 401: authentication failed after token refresh (run `slack-mcp-extender login` again)")
		}
	}
	return resp, nil
}

func (t *sseClientTransport) doPost(out *outgoing) (*http.Response, error) {
	t.mu.Lock()
	if t.isClosed {
		t.mu.Unlock()
		return nil, fmt.Errorf("transport is closed")
	}
	endpoint, sessionID := t.endpoint, t.sessionID
	t.mu.Unlock()

	ctx := out.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(out.body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	if t.auth != nil {
		token, err := t.auth.Token()
		if err != nil {
			return nil, fmt.Errorf("obtain auth token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range out.header {
		req.Header[k] = v
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP POST: %w", err)
	}
	return resp, nil
}

// handleResponse dispatches a response. httpwire.Classify decides what a
// non-2xx answer means; a 2xx JSON body and every SSE message pass through
// the Tracker before they are queued.
func (t *sseClientTransport) handleResponse(resp *http.Response, out *outgoing) error {
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.mu.Lock()
		t.sessionID = sid
		t.mu.Unlock()
	}

	status := resp.StatusCode
	contentType := resp.Header.Get("Content-Type")
	ok2xx := status >= 200 && status < 300

	if ok2xx && mediaType(contentType) == "text/event-stream" {
		go t.consumeSSEStream(resp.Body, out.id)
		return nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONBytes+1))
	_ = resp.Body.Close()
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}
	if len(body) > maxJSONBytes {
		if ok2xx {
			t.answerOwn(out.id, fmt.Sprintf("upstream response larger than %d bytes", maxJSONBytes))
			return nil
		}
		body = body[:maxJSONBytes]
	}

	outcome := httpwire.Classify(out.id, status, contentType, body)
	switch outcome.Kind {
	case httpwire.Accepted:
		if out.id != nil {
			// A request needs an answer (ADR-0005 §10.1).
			t.answerOwn(out.id, fmt.Sprintf("upstream answered HTTP %d with no body", status))
		}
		return nil
	case httpwire.Drop:
		if !ok2xx {
			t.log("upstream answered HTTP %d to a message without an id", status)
		}
		return nil
	case httpwire.Synthesized:
		if out.method == "server/discover" {
			// The usual answer of a legacy server to the era probe.
			t.log("upstream did not answer server/discover (HTTP %d): a legacy server; the agent falls back to initialize", status)
		} else {
			t.log("upstream answered HTTP %d without a JSON-RPC message; answering the request with -32603", status)
		}
		t.deliverMessage(outcome.Body)
		return nil
	}

	// Deliver.
	if status == http.StatusBadRequest && t.isHeaderMismatch(out, body) {
		return t.retryAfterMismatch(out, outcome.Body)
	}
	if ok2xx && !json.Valid(bytes.TrimSpace(body)) {
		// %q escapes control characters: the text is the upstream's.
		t.log("upstream sent a body that is not JSON: %q", truncateBytes(body, 200))
		t.answerOwn(out.id, fmt.Sprintf("upstream answered HTTP %d with a body that is not JSON", status))
		return nil
	}
	if ok2xx && out.id != nil {
		// The rule Classify applies to non-2xx answers: a response that does
		// not name the request would leave it waiting (ADR-0005 §10.5).
		t.deliverMessage(underID(outcome.Body, out.id))
		return nil
	}
	t.deliverMessage(outcome.Body)
	return nil
}

// answerOwn queues a synthesized -32603 for the request with this id, or
// nothing when the message was not a request.
func (t *sseClientTransport) answerOwn(id json.RawMessage, message string) {
	if id == nil {
		t.log("%s (no request to answer)", message)
		return
	}
	t.log("%s; answering the request with -32603", message)
	t.deliverMessage(errorBytes(id, codeInternalError, message))
}

// deliverMessage passes one message through the Tracker, which learns the
// negotiated version and the tools' marks and removes tools whose marks are
// invalid (ADR-0005 §3, §6), and queues the result.
func (t *sseClientTransport) deliverMessage(msg []byte) {
	out, rejected, err := t.tracker.Incoming(msg)
	if err != nil {
		out = msg
	}
	for _, r := range rejected {
		t.log("removed tool %q from tools/list: %s", r.Tool, r.Reason)
	}
	t.enqueue(out)
}

func (t *sseClientTransport) enqueue(data []byte) {
	select {
	case t.incoming <- data:
	case <-t.closed:
	}
}

// ReadLine returns the next JSON-RPC message from the upstream.
func (t *sseClientTransport) ReadLine() ([]byte, bool) {
	select {
	case data, ok := <-t.incoming:
		return data, ok
	case <-t.closed:
		return nil, false
	}
}

// Close shuts down the transport, terminating the server session if one
// was established.
func (t *sseClientTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.isClosed {
		return nil
	}
	t.isClosed = true
	close(t.closed)

	if t.sessionID != "" {
		req, err := http.NewRequest(http.MethodDelete, t.endpoint, nil)
		if err == nil {
			req.Header.Set("Mcp-Session-Id", t.sessionID)
			if t.auth != nil {
				if token, err := t.auth.Token(); err == nil {
					req.Header.Set("Authorization", "Bearer "+token)
				}
			}
			if legacy := t.tracker.LegacyVersion(); legacy != "" && validHeaderValue(legacy) {
				// A value the HTTP client would refuse would stop the DELETE
				// itself (ADR-0005 §10.7).
				req.Header.Set(httpwire.HeaderProtocolVersion, legacy)
			}
			if resp, err := t.client.Do(req); err == nil {
				_ = resp.Body.Close()
			}
		}
	}
	return nil
}

// consumeSSEStream parses an SSE stream and queues each "message" event.
// openID is the id of the request whose POST opened the stream, or nil.
//
// One line and one event's joined data lines are each bounded by
// maxSSEBytes. Past either bound the stream is abandoned, and if the request
// that opened it has not been answered, it is answered with -32603
// (ADR-0005 §6) rather than left to the proxy's timeout.
func (t *sseClientTransport) consumeSSEStream(body io.ReadCloser, openID json.RawMessage) {
	defer func() { _ = body.Close() }()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), maxSSEBytes)

	var (
		eventType string
		dataLines []string
		size      int
		answered  bool
		overflow  bool
	)
	flush := func() {
		if len(dataLines) > 0 && (eventType == "" || eventType == "message") {
			if t.dispatchSSEData(strings.Join(dataLines, "\n"), openID) {
				answered = true
			}
		}
		eventType, dataLines, size = "", nil, 0
	}

	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "event:"):
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			// The joined size: the lines and the newlines between them.
			if len(dataLines) > 0 {
				size++
			}
			size += len(data)
			if size > maxSSEBytes {
				overflow = true
			}
			dataLines = append(dataLines, data)
		}
		// id:, retry:, and ":" comment lines are ignored.
		if overflow {
			break
		}
	}
	if errors.Is(scanner.Err(), bufio.ErrTooLong) {
		overflow = true
	}
	if overflow {
		if openID != nil && !answered {
			t.answerOwn(openID, fmt.Sprintf("upstream response larger than %d bytes", maxSSEBytes))
		} else {
			t.log("abandoning an upstream stream with a message larger than %d bytes", maxSSEBytes)
		}
		return
	}
	flush()
	if openID != nil && !answered {
		// The stream ended without the answer to the request that opened it
		// (ADR-0005 §10.1).
		t.answerOwn(openID, "upstream stream ended without an answer")
	}
}

// dispatchSSEData queues SSE event data — a single JSON-RPC message or a
// batch array — and reports whether one of the messages answered openID.
func (t *sseClientTransport) dispatchSSEData(data string, openID json.RawMessage) bool {
	trimmed := strings.TrimSpace(data)
	if trimmed == "" {
		return false
	}
	messages := []json.RawMessage{json.RawMessage(trimmed)}
	if trimmed[0] == '[' {
		var batch []json.RawMessage
		if json.Unmarshal([]byte(trimmed), &batch) == nil {
			messages = batch
		}
	}
	answered := false
	for _, msg := range messages {
		if !json.Valid(msg) {
			t.log("dropping an upstream event that is not JSON")
			continue
		}
		if openID != nil && answers(msg, openID) {
			answered = true
		}
		t.deliverMessage([]byte(msg))
	}
	return answered
}

// answers reports whether msg is a response to the request with this id.
func answers(msg, id json.RawMessage) bool {
	var m struct {
		ID     json.RawMessage `json:"id"`
		Method *string         `json:"method"`
	}
	if json.Unmarshal(msg, &m) != nil || m.Method != nil {
		return false
	}
	return sameID(m.ID, id)
}

// sameID compares two JSON-RPC ids by their compacted text.
func sameID(a, b json.RawMessage) bool {
	var x, y bytes.Buffer
	if json.Compact(&x, a) != nil || json.Compact(&y, b) != nil {
		return false
	}
	return x.String() == y.String()
}

// errorBytes builds one JSON-RPC error response.
func errorBytes(id json.RawMessage, code int, message string) []byte {
	b, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	})
	return b
}

// isBatch reports whether data is a JSON array (a batch).
func isBatch(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	return len(trimmed) > 0 && trimmed[0] == '['
}

// mediaType returns the lower-cased type of a Content-Type, without its
// parameters, or "" when it cannot be read.
func mediaType(contentType string) string {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return ""
	}
	return strings.ToLower(mt)
}

func truncateBytes(b []byte, maxLen int) string {
	if len(b) <= maxLen {
		return string(b)
	}
	return string(b[:maxLen]) + "..."
}

// isModern reports whether a message carries a protocol version in its
// params._meta — a message of the per-request era.
func isModern(body []byte) bool {
	var m struct {
		Params struct {
			Meta map[string]json.RawMessage `json:"_meta"`
		} `json:"params"`
	}
	if json.Unmarshal(body, &m) != nil {
		return false
	}
	var v string
	return json.Unmarshal(m.Params.Meta[metaProtocolVersion], &v) == nil
}

// underID returns body with its id set to id when body is a single response
// (result or error, no method) whose id is absent, null or another one;
// otherwise body unchanged.
func underID(body []byte, id json.RawMessage) []byte {
	var msg map[string]json.RawMessage
	if json.Unmarshal(bytes.TrimSpace(body), &msg) != nil {
		return body
	}
	if _, isMethod := msg["method"]; isMethod {
		return body
	}
	_, hasResult := msg["result"]
	_, hasError := msg["error"]
	if !hasResult && !hasError {
		return body
	}
	if raw, ok := msg["id"]; ok && sameID(raw, id) {
		return body
	}
	msg["id"] = id
	out, err := json.Marshal(msg)
	if err != nil {
		return body
	}
	return out
}

// validHeaderValue reports whether net/http will send s as a header value:
// no control character other than a horizontal tab.
func validHeaderValue(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < 0x20 && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}
