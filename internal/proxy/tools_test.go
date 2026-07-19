package proxy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/slack-mcp-extender/internal/upload"
)

// Direct Handle tests — argument validation and error shaping without the
// proxy loop.

func handleArgs(t *testing.T, it *InjectedTools, tool string, args map[string]any) (isError bool, payload map[string]any) {
	t.Helper()
	result := it.Handle(tool, args)
	if len(result.Content) != 1 || result.Content[0].Type != "text" {
		t.Fatalf("content = %+v", result.Content)
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &payload); err != nil {
		t.Fatalf("payload not JSON: %v (%s)", err, result.Content[0].Text)
	}
	return result.IsError, payload
}

func TestHandleMissingRequiredArgs(t *testing.T) {
	it, _ := testInjected(t, &stubUploader{res: &upload.Result{}})

	isErr, payload := handleArgs(t, it, ToolUploadFile, map[string]any{"file": "/x"})
	if !isErr || payload["code"] != "invalid_arguments" {
		t.Errorf("missing channel_id: %v %v", isErr, payload)
	}

	isErr, payload = handleArgs(t, it, ToolUploadFile, map[string]any{"channel_id": "C1"})
	if !isErr || payload["code"] != "invalid_arguments" {
		t.Errorf("missing file: %v %v", isErr, payload)
	}

	isErr, payload = handleArgs(t, it, ToolUploadFileToThread, map[string]any{"channel_id": "C1", "file": "/x"})
	if !isErr || payload["code"] != "invalid_arguments" || !strings.Contains(payload["message"].(string), "thread_ts") {
		t.Errorf("missing thread_ts: %v %v", isErr, payload)
	}
}

func TestHandleNonStringArgsRejected(t *testing.T) {
	it, _ := testInjected(t, &stubUploader{res: &upload.Result{}})
	// Numeric channel_id type-asserts to "" and fails required validation
	// instead of panicking.
	isErr, payload := handleArgs(t, it, ToolUploadFile, map[string]any{"channel_id": 123, "file": "/x"})
	if !isErr || payload["code"] != "invalid_arguments" {
		t.Errorf("numeric channel_id: %v %v", isErr, payload)
	}
}

func TestHandlePathDeniedDetails(t *testing.T) {
	it, root := testInjected(t, &stubUploader{res: &upload.Result{}})
	isErr, payload := handleArgs(t, it, ToolUploadFile, map[string]any{
		"channel_id": "C1", "file": filepath.Join(root, "missing.txt"),
	})
	if !isErr || payload["code"] != "path_denied" {
		t.Fatalf("payload = %v", payload)
	}
	details, ok := payload["details"].(map[string]any)
	if !ok {
		t.Fatalf("details = %v", payload["details"])
	}
	if details["reason"] != "not_found" {
		t.Errorf("reason = %v", details["reason"])
	}
	roots, ok := details["allowed_roots"].([]any)
	if !ok || len(roots) != 1 {
		t.Errorf("allowed_roots = %v", details["allowed_roots"])
	}
}

func TestHandleSlackErrorShaping(t *testing.T) {
	stub := &stubUploader{err: &upload.SlackError{Method: "files.completeUploadExternal", Reason: "not_in_channel"}}
	it, root := testInjected(t, stub)
	file := filepath.Join(root, "f.txt")
	writeTestFile(t, file)

	isErr, payload := handleArgs(t, it, ToolUploadFile, map[string]any{"channel_id": "C1", "file": file})
	if !isErr || payload["code"] != "slack_api_error" {
		t.Fatalf("payload = %v", payload)
	}
	details := payload["details"].(map[string]any)
	if details["reason"] != "not_in_channel" {
		t.Errorf("details = %v", details)
	}
}

func TestDefinitionsSchemasAreValidJSON(t *testing.T) {
	it := &InjectedTools{}
	defs := it.Definitions()
	if len(defs) != 2 {
		t.Fatalf("defs = %d", len(defs))
	}
	for _, def := range defs {
		var schema map[string]any
		if err := json.Unmarshal(def.InputSchema, &schema); err != nil {
			t.Errorf("%s schema invalid: %v", def.Name, err)
		}
		required, _ := schema["required"].([]any)
		if def.Name == ToolUploadFileToThread && len(required) != 3 {
			t.Errorf("%s required = %v", def.Name, required)
		}
	}
}

func writeTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}
