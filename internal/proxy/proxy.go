// Package proxy is the core of slack-mcp-extender: a transparent MCP pipe
// between the agent (stdio) and the upstream Slack MCP (Streamable HTTP),
// with exactly two seams — tools/list results gain the injected upload
// tool definitions, and tools/call requests naming an injected tool are
// handled locally, in the shape of the agent's MCP era. Everything else
// passes through with its content unchanged, in both directions; the era is
// the agent's (ADR-0005 §2). What the specification requires of an HTTP
// client happens in the transport.
package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/nlink-jp/mcp"
	"github.com/nlink-jp/mcp/server"
	"github.com/nlink-jp/mcp/wire"

	"github.com/nlink-jp/slack-mcp-extender/internal/jsonrpc"
	"github.com/nlink-jp/slack-mcp-extender/internal/transport"
)

// maxAgentLine bounds one message from the agent (ADR-0005 §8).
const maxAgentLine = 1 << 20 // 1 MiB

// nullID is the id of an answer to a message whose id could not be read.
// JSON-RPC requires it to be present and null; jsonrpc.Message omits an
// empty id.
var nullID = json.RawMessage("null")

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

	// Version is slack-mcp-extender's version, named in the serverInfo of
	// the injected tools' modern answers (ADR-0005 §4).
	Version string

	out     *wire.Writer
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
	p.out = wire.NewWriter(p.Out)

	go p.readUpstream()
	return p.readAgent()
}

// readAgent consumes the agent's messages until EOF.
func (p *Proxy) readAgent() error {
	reader := wire.NewReader(p.In, maxAgentLine)

	for {
		read, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if errors.Is(err, wire.ErrLineTooLong) {
			// The rest of the line was skipped, so its id is unknown. Answer
			// with a null id and keep reading (ADR-0005 §8).
			p.Logf("slack-mcp-extender: skipping an agent message longer than %d bytes\n", maxAgentLine)
			_ = p.writeMessage(jsonrpc.NewErrorResponse(nullID, -32700, fmt.Sprintf("message longer than %d bytes", maxAgentLine)))
			continue
		}
		if err != nil {
			return err
		}
		// The reader reuses its buffer; the line outlives this iteration.
		line := append([]byte(nil), read...)
		msg, err := jsonrpc.Parse(line)
		if err != nil {
			// A request among these would otherwise wait for ever; its id
			// cannot be read, so the answer carries a null one (ADR-0005 §8).
			p.Logf("slack-mcp-extender: invalid JSON from agent: %v\n", err)
			_ = p.writeMessage(jsonrpc.NewErrorResponse(nullID, -32700, "invalid JSON-RPC message"))
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

	result := p.Injected.Handle(params.Name, params.Arguments, params.Meta)
	resp, err := p.shapeLocalResult(raw, msg.ID, result)
	if err != nil {
		return p.writeMessage(jsonrpc.NewErrorResponse(msg.ID, -32603, err.Error()))
	}
	return p.writeMessage(resp)
}

// shapeLocalResult answers a locally handled request in the shape of the
// agent's era: a modern request's result gains resultType and the
// extender's serverInfo; a legacy one is unchanged (ADR-0005 §4). The
// version is not checked here — that check is Frontend's, which needs every
// line, initialize included, and the upstream judges the session's version.
func (p *Proxy) shapeLocalResult(raw []byte, id json.RawMessage, result any) (*jsonrpc.Message, error) {
	data, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	info := server.Info{Server: mcp.Implementation{Name: "slack-mcp-extender", Version: p.Version}}
	shaped, err := server.ShapeResult(server.Inspect(raw), info, data)
	if err != nil {
		return nil, err
	}
	return &jsonrpc.Message{JSONRPC: "2.0", ID: id, Result: json.RawMessage(shaped)}, nil
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

// writeRaw writes one message as one line. wire.Writer compacts it and
// refuses what is not JSON; such a message is logged by its error alone.
func (p *Proxy) writeRaw(data []byte) error {
	err := p.out.Write(data)
	if err != nil {
		p.Logf("slack-mcp-extender: write to the agent failed: %v\n", err)
	}
	return err
}
