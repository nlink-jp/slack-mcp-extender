package proxy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/slack-mcp-extender/internal/containment"
	"github.com/nlink-jp/slack-mcp-extender/internal/transfer"
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
	it, _ := testInjected(t, &stubUploader{res: &transfer.UploadResult{}})

	isErr, payload := handleArgs(t, it, ToolFileUpload, map[string]any{"file": "/x"})
	if !isErr || payload["code"] != "invalid_arguments" {
		t.Errorf("missing channel_id: %v %v", isErr, payload)
	}

	isErr, payload = handleArgs(t, it, ToolFileUpload, map[string]any{"channel_id": "C1"})
	if !isErr || payload["code"] != "invalid_arguments" {
		t.Errorf("missing file: %v %v", isErr, payload)
	}

	isErr, payload = handleArgs(t, it, ToolFileUploadToThread, map[string]any{"channel_id": "C1", "file": "/x"})
	if !isErr || payload["code"] != "invalid_arguments" || !strings.Contains(payload["message"].(string), "thread_ts") {
		t.Errorf("missing thread_ts: %v %v", isErr, payload)
	}
}

func TestHandleNonStringArgsRejected(t *testing.T) {
	it, _ := testInjected(t, &stubUploader{res: &transfer.UploadResult{}})
	// Numeric channel_id type-asserts to "" and fails required validation
	// instead of panicking.
	isErr, payload := handleArgs(t, it, ToolFileUpload, map[string]any{"channel_id": 123, "file": "/x"})
	if !isErr || payload["code"] != "invalid_arguments" {
		t.Errorf("numeric channel_id: %v %v", isErr, payload)
	}
}

func TestHandlePathDeniedDetails(t *testing.T) {
	it, root := testInjected(t, &stubUploader{res: &transfer.UploadResult{}})
	isErr, payload := handleArgs(t, it, ToolFileUpload, map[string]any{
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
	stub := &stubUploader{err: &transfer.SlackError{Method: "files.completeUploadExternal", Reason: "not_in_channel"}}
	it, root := testInjected(t, stub)
	file := filepath.Join(root, "f.txt")
	writeTestFile(t, file)

	isErr, payload := handleArgs(t, it, ToolFileUpload, map[string]any{"channel_id": "C1", "file": file})
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
	if len(defs) != 3 {
		t.Fatalf("defs = %d", len(defs))
	}
	for _, def := range defs {
		var schema map[string]any
		if err := json.Unmarshal(def.InputSchema, &schema); err != nil {
			t.Errorf("%s schema invalid: %v", def.Name, err)
		}
		required, _ := schema["required"].([]any)
		if def.Name == ToolFileUploadToThread && len(required) != 3 {
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

// --- ext_file_download ---

func TestHandleDownloadHappyPath(t *testing.T) {
	stub := &stubUploader{
		info:       &transfer.FileInfo{ID: "F1", Name: "report.pdf", Size: 4, DownloadURL: "stub://dl"},
		fetchBytes: []byte("data"),
	}
	it, root := testInjected(t, stub)

	isErr, payload := handleArgs(t, it, ToolFileDownload, map[string]any{
		"file_id": "F1", "dest_dir": root,
	})
	if isErr {
		t.Fatalf("download failed: %v", payload)
	}
	wantPath := filepath.Join(root, "report.pdf")
	if payload["path"] != wantPath || payload["filename"] != "report.pdf" {
		t.Errorf("payload = %v", payload)
	}
	data, err := os.ReadFile(wantPath)
	if err != nil || string(data) != "data" {
		t.Errorf("file = %q, %v", data, err)
	}
}

func TestHandleDownloadDestDirDefaultsToWorkspace(t *testing.T) {
	stub := &stubUploader{fetchBytes: []byte("x")}
	it, root := testInjected(t, stub)

	isErr, payload := handleArgs(t, it, ToolFileDownload, map[string]any{
		"file_id": "F1", "workspace_dir": root,
	})
	if isErr {
		t.Fatalf("download failed: %v", payload)
	}
	if payload["path"] != filepath.Join(root, "stub.bin") {
		t.Errorf("path = %v", payload["path"])
	}
}

func TestHandleDownloadInvalidArgs(t *testing.T) {
	it, root := testInjected(t, &stubUploader{})
	_ = root

	isErr, payload := handleArgs(t, it, ToolFileDownload, map[string]any{"dest_dir": root})
	if !isErr || payload["code"] != "invalid_arguments" {
		t.Errorf("missing file_id: %v", payload)
	}
	isErr, payload = handleArgs(t, it, ToolFileDownload, map[string]any{"file_id": "F1"})
	if !isErr || payload["code"] != "invalid_arguments" {
		t.Errorf("missing dest_dir/workspace_dir: %v", payload)
	}
}

func TestHandleDownloadSizePrecheck(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	policy, err := containment.NewPolicy([]string{root}, false, 10)
	if err != nil {
		t.Fatal(err)
	}
	it := &InjectedTools{
		Policy:   policy,
		Uploader: &stubUploader{info: &transfer.FileInfo{ID: "F1", Name: "big.bin", Size: 11, DownloadURL: "stub://dl"}},
		Audit:    &transfer.AuditLog{},
	}
	isErr, payload := handleArgs(t, it, ToolFileDownload, map[string]any{"file_id": "F1", "dest_dir": root})
	if !isErr || payload["code"] != "file_too_large" {
		t.Errorf("payload = %v", payload)
	}
}

func TestHandleDownloadRefusesOverwrite(t *testing.T) {
	stub := &stubUploader{info: &transfer.FileInfo{ID: "F1", Name: "taken.txt", Size: 1, DownloadURL: "stub://dl"}}
	it, root := testInjected(t, stub)
	writeTestFile(t, filepath.Join(root, "taken.txt"))

	isErr, payload := handleArgs(t, it, ToolFileDownload, map[string]any{"file_id": "F1", "dest_dir": root})
	if !isErr || payload["code"] != "path_denied" {
		t.Fatalf("payload = %v", payload)
	}
	details := payload["details"].(map[string]any)
	if details["reason"] != "already_exists" {
		t.Errorf("reason = %v", details["reason"])
	}
}

func TestHandleDownloadHostileSlackFilename(t *testing.T) {
	// A hostile Slack-side filename influences the name only, never the
	// directory; hidden names are rejected outright.
	stub := &stubUploader{
		info:       &transfer.FileInfo{ID: "F1", Name: "../../evil.sh", Size: 1, DownloadURL: "stub://dl"},
		fetchBytes: []byte("x"),
	}
	it, root := testInjected(t, stub)
	isErr, payload := handleArgs(t, it, ToolFileDownload, map[string]any{"file_id": "F1", "dest_dir": root})
	if isErr {
		t.Fatalf("payload = %v", payload)
	}
	if payload["path"] != filepath.Join(root, "evil.sh") {
		t.Errorf("path = %v (directory influence!)", payload["path"])
	}

	stub.info = &transfer.FileInfo{ID: "F2", Name: ".zshrc", Size: 1, DownloadURL: "stub://dl"}
	isErr, payload = handleArgs(t, it, ToolFileDownload, map[string]any{"file_id": "F2", "dest_dir": root})
	if !isErr || payload["code"] != "path_denied" {
		t.Fatalf("hidden filename accepted: %v", payload)
	}
}

func TestHandleDownloadSlackErrorShaping(t *testing.T) {
	stub := &stubUploader{infoErr: &transfer.SlackError{Method: "files.info", Reason: "file_not_found"}}
	it, root := testInjected(t, stub)
	isErr, payload := handleArgs(t, it, ToolFileDownload, map[string]any{"file_id": "F404", "dest_dir": root})
	if !isErr || payload["code"] != "slack_api_error" {
		t.Fatalf("payload = %v", payload)
	}
	details := payload["details"].(map[string]any)
	if details["reason"] != "file_not_found" {
		t.Errorf("details = %v", details)
	}
}

func TestHandleDownloadWireLimitShaping(t *testing.T) {
	stub := &stubUploader{
		info:     &transfer.FileInfo{ID: "F1", Name: "f.bin", Size: 1, DownloadURL: "stub://dl"},
		fetchErr: fmt.Errorf("wrapped: %w", transfer.ErrTooLarge),
	}
	it, root := testInjected(t, stub)
	isErr, payload := handleArgs(t, it, ToolFileDownload, map[string]any{"file_id": "F1", "dest_dir": root})
	if !isErr || payload["code"] != "file_too_large" {
		t.Fatalf("payload = %v", payload)
	}
}
