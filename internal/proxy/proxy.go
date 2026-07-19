// Package proxy is the core of slack-mcp-extender: a transparent MCP pipe
// between the agent (stdio) and the upstream Slack MCP (Streamable HTTP),
// with exactly two seams — tools/list results gain the injected upload
// tool definitions, and tools/call requests naming an injected tool are
// handled locally. Everything else passes through unmodified, byte for
// byte, in both directions.
package proxy

import (
	"bufio"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/nlink-jp/slack-mcp-extender/internal/jsonrpc"
	"github.com/nlink-jp/slack-mcp-extender/internal/transport"
)

// Proxy pipes JSON-RPC between the agent and the upstream.
type Proxy struct {
	Upstream transport.Transport
	Injected *InjectedTools

	// In/Out are the agent-facing pipe (stdin/stdout in production).
	In  io.Reader
	Out io.Writer

	// TimeoutMs bounds each forwarded request's wait for its upstream
	// response.
	TimeoutMs int

	// Logf receives diagnostics (stderr in production; never Out — that
	// would corrupt the JSON-RPC stream).
	Logf func(format string, args ...any)

	outMu   sync.Mutex
	mu      sync.Mutex
	pending map[string]chan *jsonrpc.Message
}

// Run starts the pipe and blocks until the agent closes its side.
func (p *Proxy) Run() error {
	if p.Logf == nil {
		p.Logf = func(string, ...any) {}
	}
	if p.TimeoutMs <= 0 {
		p.TimeoutMs = 120000
	}
	p.pending = make(map[string]chan *jsonrpc.Message)

	go p.readUpstream()
	return p.readAgent()
}

// readAgent consumes the agent's messages until EOF.
func (p *Proxy) readAgent() error {
	scanner := bufio.NewScanner(p.In)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		msg, err := jsonrpc.Parse(line)
		if err != nil {
			p.Logf("slack-mcp-extender: invalid JSON from agent: %v\n", err)
			continue
		}
		if err := p.routeAgentMessage(msg, line); err != nil {
			p.Logf("slack-mcp-extender: route error: %v\n", err)
			// A request whose routing fails always gets a JSON-RPC error
			// response so the agent surfaces the reason instead of
			// hanging until its own timeout.
			if msg.IsRequest() {
				_ = p.writeMessage(jsonrpc.NewErrorResponse(msg.ID, -32603, err.Error()))
			}
		}
	}
	return scanner.Err()
}

// readUpstream dispatches upstream messages: responses resolve pending
// forwarded requests; everything else (notifications, server-initiated
// requests) is relayed to the agent verbatim.
func (p *Proxy) readUpstream() {
	for {
		line, ok := p.Upstream.ReadLine()
		if !ok {
			return
		}
		if len(line) == 0 {
			continue
		}
		msg, err := jsonrpc.Parse(line)
		if err != nil {
			p.Logf("slack-mcp-extender: invalid JSON from upstream: %v\n", err)
			continue
		}

		if msg.IsResponse() {
			id := msg.IDString()
			p.mu.Lock()
			ch, exists := p.pending[id]
			if exists {
				delete(p.pending, id)
			}
			p.mu.Unlock()
			if exists {
				ch <- msg
				continue
			}
		}
		// Unsolicited response, notification, or server-initiated
		// request — transparent relay.
		_ = p.writeRaw(line)
	}
}

func (p *Proxy) routeAgentMessage(msg *jsonrpc.Message, raw []byte) error {
	switch {
	case msg.IsRequest():
		return p.handleRequest(msg, raw)
	default:
		// Notifications and responses (to server-initiated requests)
		// pass through verbatim.
		return p.Upstream.Send(raw)
	}
}

func (p *Proxy) handleRequest(msg *jsonrpc.Message, raw []byte) error {
	switch msg.Method {
	case "tools/list":
		return p.handleToolsList(msg, raw)
	case "tools/call":
		return p.handleToolsCall(msg, raw)
	default:
		return p.forwardAndRelay(msg, raw)
	}
}

// handleToolsList forwards the request and merges the injected tool
// definitions into the raw result, preserving all upstream content.
func (p *Proxy) handleToolsList(msg *jsonrpc.Message, raw []byte) error {
	resp, err := p.forwardRequest(msg, raw)
	if err != nil {
		return err
	}
	if resp.Error == nil && resp.Result != nil && p.Injected != nil {
		merged, collisions, mergeErr := jsonrpc.MergeToolsListResult(resp.Result, p.Injected.Definitions())
		if mergeErr != nil {
			// An unmergeable result is forwarded untouched — degraded
			// (no injected tools this round) beats broken.
			p.Logf("slack-mcp-extender: tools/list merge failed, forwarding unmodified: %v\n", mergeErr)
		} else {
			for _, name := range collisions {
				p.Logf("slack-mcp-extender: upstream tool %q collides with an injected tool; local wins\n", name)
			}
			resp.Result = merged
		}
	}
	return p.writeMessage(resp)
}

// handleToolsCall routes injected tool names to the local handler and
// forwards everything else verbatim.
func (p *Proxy) handleToolsCall(msg *jsonrpc.Message, raw []byte) error {
	params, err := jsonrpc.ParseToolCallParams(msg.Params)
	if err != nil || p.Injected == nil || !p.Injected.Handles(params.Name) {
		return p.forwardAndRelay(msg, raw)
	}

	result := p.Injected.Handle(params.Name, params.Arguments)
	resp, err := jsonrpc.NewResultResponse(msg.ID, result)
	if err != nil {
		return p.writeMessage(jsonrpc.NewErrorResponse(msg.ID, -32603, err.Error()))
	}
	return p.writeMessage(resp)
}

func (p *Proxy) forwardAndRelay(msg *jsonrpc.Message, raw []byte) error {
	resp, err := p.forwardRequest(msg, raw)
	if err != nil {
		return err
	}
	return p.writeMessage(resp)
}

// forwardRequest sends raw upstream and waits for the matching response.
func (p *Proxy) forwardRequest(msg *jsonrpc.Message, raw []byte) (*jsonrpc.Message, error) {
	id := msg.IDString()
	ch := make(chan *jsonrpc.Message, 1)

	p.mu.Lock()
	p.pending[id] = ch
	p.mu.Unlock()

	if err := p.Upstream.Send(raw); err != nil {
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
		return nil, fmt.Errorf("send to upstream: %w", err)
	}

	timer := time.NewTimer(time.Duration(p.TimeoutMs) * time.Millisecond)
	defer timer.Stop()
	select {
	case resp := <-ch:
		return resp, nil
	case <-timer.C:
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
		return jsonrpc.NewErrorResponse(msg.ID, -32603, "upstream timeout"), nil
	}
}

func (p *Proxy) writeMessage(msg *jsonrpc.Message) error {
	data, err := jsonrpc.Marshal(msg)
	if err != nil {
		return err
	}
	return p.writeRaw(data)
}

func (p *Proxy) writeRaw(data []byte) error {
	p.outMu.Lock()
	defer p.outMu.Unlock()
	_, err := p.Out.Write(append(data, '\n'))
	return err
}
