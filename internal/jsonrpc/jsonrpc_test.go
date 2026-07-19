package jsonrpc

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMessageClassification(t *testing.T) {
	tests := []struct {
		name                                 string
		raw                                  string
		isRequest, isNotification, isResponse bool
		idString                             string
	}{
		{"request numeric id", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, true, false, false, "1"},
		{"request string id", `{"jsonrpc":"2.0","id":"a-1","method":"ping"}`, true, false, false, `"a-1"`},
		{"notification", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, false, true, false, ""},
		{"null id is notification", `{"jsonrpc":"2.0","id":null,"method":"x"}`, false, true, false, ""},
		{"result response", `{"jsonrpc":"2.0","id":1,"result":{}}`, false, false, true, "1"},
		{"error response", `{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"bad"}}`, false, false, true, "1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, err := Parse([]byte(tt.raw))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := msg.IsRequest(); got != tt.isRequest {
				t.Errorf("IsRequest = %v, want %v", got, tt.isRequest)
			}
			if got := msg.IsNotification(); got != tt.isNotification {
				t.Errorf("IsNotification = %v, want %v", got, tt.isNotification)
			}
			if got := msg.IsResponse(); got != tt.isResponse {
				t.Errorf("IsResponse = %v, want %v", got, tt.isResponse)
			}
			if got := msg.IDString(); got != tt.idString {
				t.Errorf("IDString = %q, want %q", got, tt.idString)
			}
		})
	}
}

func TestParseInvalidJSON(t *testing.T) {
	if _, err := Parse([]byte(`{not json`)); err == nil {
		t.Fatal("Parse accepted invalid JSON")
	}
}

func TestNewErrorResponse(t *testing.T) {
	msg := NewErrorResponse(json.RawMessage("7"), -32601, "nope")
	data, err := Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"jsonrpc":"2.0","id":7,"error":{"code":-32601,"message":"nope"}}`
	if string(data) != want {
		t.Errorf("got %s, want %s", data, want)
	}
}

func TestNewResultResponse(t *testing.T) {
	msg, err := NewResultResponse(json.RawMessage(`"x"`), map[string]int{"n": 1})
	if err != nil {
		t.Fatalf("NewResultResponse: %v", err)
	}
	data, _ := Marshal(msg)
	if want := `{"jsonrpc":"2.0","id":"x","result":{"n":1}}`; string(data) != want {
		t.Errorf("got %s, want %s", data, want)
	}
}

func TestParseToolCallParams(t *testing.T) {
	p, err := ParseToolCallParams(json.RawMessage(`{"name":"upload_file","arguments":{"channel_id":"C123","file":"a.txt"}}`))
	if err != nil {
		t.Fatalf("ParseToolCallParams: %v", err)
	}
	if p.Name != "upload_file" {
		t.Errorf("Name = %q", p.Name)
	}
	if p.Arguments["channel_id"] != "C123" {
		t.Errorf("Arguments = %v", p.Arguments)
	}
}

// TestMergeToolsListResult_PreservesUnknownFields is the transparency
// invariant test: upstream-only fields at both the result level (nextCursor)
// and the tool-entry level (title, annotations, outputSchema) must survive
// the merge byte-for-byte in content.
func TestMergeToolsListResult_PreservesUnknownFields(t *testing.T) {
	upstream := `{
		"tools": [
			{"name":"slack_search","description":"d","inputSchema":{"type":"object"},
			 "title":"Search","annotations":{"readOnlyHint":true},"outputSchema":{"type":"object","properties":{}}}
		],
		"nextCursor": "abc123"
	}`
	injected := []ToolInfo{{Name: "upload_file", Description: "u", InputSchema: json.RawMessage(`{"type":"object"}`)}}

	merged, collisions, err := MergeToolsListResult(json.RawMessage(upstream), injected)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if len(collisions) != 0 {
		t.Errorf("collisions = %v, want none", collisions)
	}

	var out struct {
		Tools      []map[string]json.RawMessage `json:"tools"`
		NextCursor string                       `json:"nextCursor"`
	}
	if err := json.Unmarshal(merged, &out); err != nil {
		t.Fatalf("unmarshal merged: %v", err)
	}
	if out.NextCursor != "abc123" {
		t.Errorf("nextCursor lost: %q", out.NextCursor)
	}
	if len(out.Tools) != 2 {
		t.Fatalf("tools count = %d, want 2", len(out.Tools))
	}
	first := out.Tools[0]
	for _, key := range []string{"title", "annotations", "outputSchema"} {
		if _, ok := first[key]; !ok {
			t.Errorf("upstream tool field %q lost in merge", key)
		}
	}
	if string(first["name"]) != `"slack_search"` {
		t.Errorf("first tool = %s", first["name"])
	}
	if string(out.Tools[1]["name"]) != `"upload_file"` {
		t.Errorf("injected tool missing: %s", out.Tools[1]["name"])
	}
}

func TestMergeToolsListResult_CollisionLocalWins(t *testing.T) {
	upstream := `{"tools":[{"name":"upload_file","description":"upstream version"},{"name":"other"}]}`
	injected := []ToolInfo{{Name: "upload_file", Description: "local version"}}

	merged, collisions, err := MergeToolsListResult(json.RawMessage(upstream), injected)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if len(collisions) != 1 || collisions[0] != "upload_file" {
		t.Errorf("collisions = %v, want [upload_file]", collisions)
	}
	var out struct {
		Tools []ToolInfo `json:"tools"`
	}
	if err := json.Unmarshal(merged, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Tools) != 2 {
		t.Fatalf("tools = %+v, want 2 entries", out.Tools)
	}
	// Exactly one upload_file, and it is the local one.
	count := 0
	for _, tool := range out.Tools {
		if tool.Name == "upload_file" {
			count++
			if !strings.Contains(tool.Description, "local") {
				t.Errorf("upstream duplicate survived: %+v", tool)
			}
		}
	}
	if count != 1 {
		t.Errorf("upload_file count = %d, want 1", count)
	}
}

func TestMergeToolsListResult_EmptyUpstreamTools(t *testing.T) {
	merged, _, err := MergeToolsListResult(json.RawMessage(`{"tools":[]}`), []ToolInfo{{Name: "upload_file"}})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	var out struct {
		Tools []ToolInfo `json:"tools"`
	}
	if err := json.Unmarshal(merged, &out); err != nil || len(out.Tools) != 1 {
		t.Fatalf("merged = %s (err %v)", merged, err)
	}
}

func TestMergeToolsListResult_MalformedResult(t *testing.T) {
	if _, _, err := MergeToolsListResult(json.RawMessage(`[1,2]`), nil); err == nil {
		t.Fatal("array result accepted")
	}
	if _, _, err := MergeToolsListResult(json.RawMessage(`{"tools":"nope"}`), nil); err == nil {
		t.Fatal("non-array tools accepted")
	}
}
