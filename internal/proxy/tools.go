package proxy

import (
	"encoding/json"
	"errors"

	"github.com/nlink-jp/slack-mcp-extender/internal/containment"
	"github.com/nlink-jp/slack-mcp-extender/internal/jsonrpc"
	"github.com/nlink-jp/slack-mcp-extender/internal/transfer"
)

// Injected tool names.
const (
	ToolUploadFile         = "upload_file"
	ToolUploadFileToThread = "upload_file_to_thread"
)

// FileUploader abstracts the Slack upload flow (satisfied by
// *transfer.Client; stubbed in tests).
type FileUploader interface {
	Upload(req transfer.UploadRequest) (*transfer.UploadResult, error)
}

// InjectedTools holds the local tool implementations added to the upstream
// tool set: containment policy first, then upload, then audit.
type InjectedTools struct {
	Policy   *containment.Policy
	Uploader FileUploader
	Audit    *transfer.AuditLog
	// Logf receives non-fatal diagnostics (audit write failures).
	Logf func(format string, args ...any)
}

// Handles reports whether name is an injected tool.
func (it *InjectedTools) Handles(name string) bool {
	return name == ToolUploadFile || name == ToolUploadFileToThread
}

// Definitions returns the tool definitions merged into tools/list.
func (it *InjectedTools) Definitions() []jsonrpc.ToolInfo {
	fileArgs := `
		"channel_id": {"type": "string", "description": "ID of the channel to post into (C…/G…/D…). Find it via the Slack tools, e.g. from a channel listing or search result."},
		"file": {"type": "string", "description": "File to upload: an absolute path, or a path relative to workspace_dir. Must resolve inside the operator-configured allowed roots."},
		"workspace_dir": {"type": "string", "description": "Absolute base directory for a relative file path (e.g. the session working directory)."},
		"comment": {"type": "string", "description": "Optional message text posted together with the file."},
		"filename": {"type": "string", "description": "Display name shown in Slack (default: the file's basename)."}`

	return []jsonrpc.ToolInfo{
		{
			Name: ToolUploadFile,
			Description: "Upload a local file to Slack and post it as a new root message in a channel. " +
				"The file must lie inside the operator-configured allowed roots; the post appears under the authorizing user's own identity.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {` + fileArgs + `},
				"required": ["channel_id", "file"]
			}`),
		},
		{
			Name: ToolUploadFileToThread,
			Description: "Upload a local file to Slack and post it as a reply in an existing thread. " +
				"Same containment rules as upload_file; thread_ts is the timestamp of the thread's root message.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {` + fileArgs + `,
					"thread_ts": {"type": "string", "description": "Timestamp (ts) of the message to reply to."}},
				"required": ["channel_id", "file", "thread_ts"]
			}`),
		},
	}
}

// Handle executes an injected tool call. It always returns a ToolResult —
// failures become isError results carrying a structured JSON error
// ({code, message, details}), never protocol-level errors, so the agent can
// read and react to them.
func (it *InjectedTools) Handle(name string, args map[string]any) *jsonrpc.ToolResult {
	channelID, _ := args["channel_id"].(string)
	file, _ := args["file"].(string)
	workspaceDir, _ := args["workspace_dir"].(string)
	comment, _ := args["comment"].(string)
	filename, _ := args["filename"].(string)
	threadTS, _ := args["thread_ts"].(string)

	if channelID == "" || file == "" {
		return errorResult("invalid_arguments", "channel_id and file are required", nil)
	}
	if name == ToolUploadFileToThread && threadTS == "" {
		return errorResult("invalid_arguments", "thread_ts is required for "+ToolUploadFileToThread, nil)
	}
	if name == ToolUploadFile {
		threadTS = "" // a root-message upload never threads
	}

	// Containment decides; everything below only executes.
	canonical, err := it.Policy.Resolve(workspaceDir, file)
	if err != nil {
		var v *containment.Violation
		if errors.As(err, &v) {
			it.audit(transfer.AuditEntry{
				Tool: name, Path: v.Path, ChannelID: channelID, ThreadTS: threadTS,
				Outcome: "denied", Error: v.Reason,
			})
			return errorResult("path_denied", v.Error(), map[string]any{
				"reason":        v.Reason,
				"path":          v.Path,
				"allowed_roots": v.Roots,
			})
		}
		return errorResult("internal_error", err.Error(), nil)
	}

	result, err := it.Uploader.Upload(transfer.UploadRequest{
		Path:      canonical,
		Filename:  filename,
		ChannelID: channelID,
		Comment:   comment,
		ThreadTS:  threadTS,
	})
	if err != nil {
		it.audit(transfer.AuditEntry{
			Tool: name, Path: canonical, ChannelID: channelID, ThreadTS: threadTS,
			Outcome: "error", Error: err.Error(),
		})
		var se *transfer.SlackError
		if errors.As(err, &se) {
			return errorResult("slack_api_error", se.Error(), map[string]any{
				"method": se.Method,
				"reason": se.Reason,
			})
		}
		return errorResult("upload_failed", err.Error(), nil)
	}

	it.audit(transfer.AuditEntry{
		Tool: name, Path: canonical, Size: result.Size, ChannelID: result.ChannelID,
		ThreadTS: result.ThreadTS, FileID: result.FileID, Outcome: "ok",
	})

	payload, _ := json.Marshal(map[string]any{
		"ok":         true,
		"file_id":    result.FileID,
		"filename":   result.Filename,
		"size":       result.Size,
		"channel_id": result.ChannelID,
		"thread_ts":  result.ThreadTS,
	})
	return &jsonrpc.ToolResult{
		Content: []jsonrpc.ToolContent{{Type: "text", Text: string(payload)}},
	}
}

func (it *InjectedTools) audit(entry transfer.AuditEntry) {
	if err := it.Audit.Append(entry); err != nil && it.Logf != nil {
		// Audit failures are surfaced but never turn an outcome into a
		// reported failure.
		it.Logf("slack-mcp-extender: audit write failed: %v\n", err)
	}
}

// errorResult builds an isError tool result carrying a structured JSON
// error object ({code, message, details}).
func errorResult(code, message string, details map[string]any) *jsonrpc.ToolResult {
	obj := map[string]any{"code": code, "message": message}
	if len(details) > 0 {
		obj["details"] = details
	}
	payload, _ := json.Marshal(obj)
	return &jsonrpc.ToolResult{
		Content: []jsonrpc.ToolContent{{Type: "text", Text: string(payload)}},
		IsError: true,
	}
}
