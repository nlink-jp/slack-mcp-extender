// Package jsonrpc provides minimal JSON-RPC 2.0 message types for the MCP
// proxy. Messages keep every field as json.RawMessage wherever possible so
// that forwarding never re-serializes (and therefore never mutates) upstream
// content — transparency is a design invariant of slack-mcp-extender.
package jsonrpc

import "encoding/json"

// Message represents a raw JSON-RPC 2.0 message (request, response, or
// notification).
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError represents a JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// IsRequest reports whether m is a request (method and non-null id).
func (m *Message) IsRequest() bool {
	return m.Method != "" && len(m.ID) > 0 && string(m.ID) != "null"
}

// IsNotification reports whether m is a notification (method, no id).
func (m *Message) IsNotification() bool {
	return m.Method != "" && (len(m.ID) == 0 || string(m.ID) == "null")
}

// IsResponse reports whether m is a response (result or error, no method).
func (m *Message) IsResponse() bool {
	return m.Method == "" && (m.Result != nil || m.Error != nil)
}

// IDString returns the ID as a comparable string ("" for notifications).
func (m *Message) IDString() string {
	if len(m.ID) == 0 || string(m.ID) == "null" {
		return ""
	}
	return string(m.ID)
}

// Parse parses a raw JSON message.
func Parse(data []byte) (*Message, error) {
	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// Marshal serializes a message to JSON bytes.
func Marshal(msg *Message) ([]byte, error) {
	return json.Marshal(msg)
}

// NewErrorResponse creates a JSON-RPC error response.
func NewErrorResponse(id json.RawMessage, code int, message string) *Message {
	return &Message{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &RPCError{Code: code, Message: message},
	}
}

// NewResultResponse creates a JSON-RPC success response.
func NewResultResponse(id json.RawMessage, result any) (*Message, error) {
	data, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return &Message{JSONRPC: "2.0", ID: id, Result: json.RawMessage(data)}, nil
}

// ToolCallParams represents the params of a tools/call request.
type ToolCallParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// ParseToolCallParams extracts tool call parameters.
func ParseToolCallParams(params json.RawMessage) (*ToolCallParams, error) {
	var p ToolCallParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// ToolResult is the result payload of a tools/call response.
type ToolResult struct {
	Content []ToolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// ToolContent is a single content item in a tool result.
type ToolContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ToolInfo is a tool definition for tools we inject ourselves. Upstream tool
// definitions are never decoded into this type — see MergeToolsListResult —
// so upstream-only fields (title, annotations, outputSchema, …) survive.
type ToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// MergeToolsListResult appends the injected tool definitions to a raw
// tools/list result while preserving every byte of upstream content that is
// not part of the merge: unknown result fields (e.g. nextCursor) and unknown
// tool-entry fields pass through untouched because upstream entries are
// handled as json.RawMessage, never decoded into a struct.
//
// If an upstream tool has the same name as an injected tool, the upstream
// entry is dropped and the name is returned in collisions so the caller can
// log it — the injected (local) tool wins deterministically, matching the
// tools/call routing which checks injected names first.
func MergeToolsListResult(result json.RawMessage, injected []ToolInfo) (merged json.RawMessage, collisions []string, err error) {
	var resultObj map[string]json.RawMessage
	if err := json.Unmarshal(result, &resultObj); err != nil {
		return nil, nil, err
	}

	var tools []json.RawMessage
	if rawTools, ok := resultObj["tools"]; ok {
		if err := json.Unmarshal(rawTools, &tools); err != nil {
			return nil, nil, err
		}
	}

	injectedNames := make(map[string]bool, len(injected))
	for _, t := range injected {
		injectedNames[t.Name] = true
	}

	// Drop upstream entries that collide with an injected name.
	kept := tools[:0]
	for _, raw := range tools {
		var probe struct {
			Name string `json:"name"`
		}
		// An undecodable entry cannot collide; keep it untouched.
		if json.Unmarshal(raw, &probe) == nil && injectedNames[probe.Name] {
			collisions = append(collisions, probe.Name)
			continue
		}
		kept = append(kept, raw)
	}

	for _, t := range injected {
		data, err := json.Marshal(t)
		if err != nil {
			return nil, nil, err
		}
		kept = append(kept, json.RawMessage(data))
	}

	newTools, err := json.Marshal(kept)
	if err != nil {
		return nil, nil, err
	}
	resultObj["tools"] = json.RawMessage(newTools)

	merged, err = json.Marshal(resultObj)
	if err != nil {
		return nil, nil, err
	}
	return merged, collisions, nil
}
