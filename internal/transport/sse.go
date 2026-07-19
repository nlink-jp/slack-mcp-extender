package transport

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// sseClientTransport implements Transport for MCP servers speaking the
// Streamable HTTP transport (formerly SSE):
//
//   - the client POSTs JSON-RPC messages to the server endpoint;
//   - the server answers with application/json (a single message) or
//     text/event-stream (an SSE stream of "message" events);
//   - session affinity rides on the Mcp-Session-Id header.
type sseClientTransport struct {
	endpoint string
	client   *http.Client
	auth     TokenProvider // nil if unauthenticated

	incoming chan []byte
	closed   chan struct{}

	mu        sync.Mutex
	isClosed  bool
	sessionID string
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

// NewSSEClientTransport creates a Streamable HTTP client transport for the
// given MCP endpoint URL.
func NewSSEClientTransport(endpoint string, opts ...SSEOption) (Transport, error) {
	if endpoint == "" {
		return nil, fmt.Errorf("SSE endpoint URL is required")
	}
	t := &sseClientTransport{
		endpoint: endpoint,
		client:   &http.Client{},
		incoming: make(chan []byte, 64),
		closed:   make(chan struct{}),
	}
	for _, opt := range opts {
		opt(t)
	}
	return t, nil
}

// Send posts a JSON-RPC message; responses (JSON or SSE) are queued into
// the incoming channel.
func (t *sseClientTransport) Send(data []byte) error {
	resp, err := t.doPost(data)
	if err != nil {
		return err
	}

	if resp.StatusCode == http.StatusUnauthorized && t.auth != nil {
		resp.Body.Close()
		t.auth.Invalidate()
		resp, err = t.doPost(data)
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusUnauthorized {
			resp.Body.Close()
			return fmt.Errorf("HTTP 401: authentication failed after token refresh (run `slack-mcp-extender login` again)")
		}
	}

	return t.handleResponse(resp)
}

func (t *sseClientTransport) doPost(data []byte) (*http.Response, error) {
	t.mu.Lock()
	if t.isClosed {
		t.mu.Unlock()
		return nil, fmt.Errorf("transport is closed")
	}
	endpoint, sessionID := t.endpoint, t.sessionID
	t.mu.Unlock()

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(data))
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

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP POST: %w", err)
	}
	return resp, nil
}

func (t *sseClientTransport) handleResponse(resp *http.Response) error {
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.mu.Lock()
		t.sessionID = sid
		t.mu.Unlock()
	}

	contentType := resp.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(contentType, "application/json"):
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("read response body: %w", err)
		}
		t.enqueue(body)

	case strings.HasPrefix(contentType, "text/event-stream"):
		go t.consumeSSEStream(resp.Body)

	default:
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			return fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateBytes(body, 200))
		}
		if len(body) > 0 {
			t.enqueue(body)
		}
	}
	return nil
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
			if resp, err := t.client.Do(req); err == nil {
				resp.Body.Close()
			}
		}
	}
	return nil
}

// consumeSSEStream parses an SSE stream and queues each "message" event.
func (t *sseClientTransport) consumeSSEStream(body io.ReadCloser) {
	defer body.Close()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	var eventType string
	var dataLines []string
	flush := func() {
		if len(dataLines) > 0 && (eventType == "" || eventType == "message") {
			t.dispatchSSEData(strings.Join(dataLines, "\n"))
		}
		eventType = ""
		dataLines = nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "event:"):
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
		// id:, retry:, and ":" comment lines are ignored.
	}
	flush()
}

// dispatchSSEData queues SSE event data — a single JSON-RPC message or a
// batch array.
func (t *sseClientTransport) dispatchSSEData(data string) {
	trimmed := strings.TrimSpace(data)
	if trimmed == "" {
		return
	}
	if trimmed[0] == '[' {
		var batch []json.RawMessage
		if json.Unmarshal([]byte(trimmed), &batch) == nil {
			for _, msg := range batch {
				t.enqueue([]byte(msg))
			}
			return
		}
	}
	t.enqueue([]byte(trimmed))
}

func truncateBytes(b []byte, maxLen int) string {
	if len(b) <= maxLen {
		return string(b)
	}
	return string(b[:maxLen]) + "..."
}
