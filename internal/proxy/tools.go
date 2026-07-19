package proxy

import (
	"encoding/json"
	"errors"

	"github.com/nlink-jp/slack-mcp-extender/internal/containment"
	"github.com/nlink-jp/slack-mcp-extender/internal/jsonrpc"
	"github.com/nlink-jp/slack-mcp-extender/internal/transfer"
)

// Injected tool names. Everything this proxy adds lives in the ext_
// namespace so it can never collide with — or mask — an official slack_*
// tool (ADR-0001).
const (
	ToolFileUpload         = "ext_file_upload"
	ToolFileUploadToThread = "ext_file_upload_to_thread"
	ToolFileDownload       = "ext_file_download"
)

// FileTransfer abstracts the Slack file operations (satisfied by
// *transfer.Client; stubbed in tests).
type FileTransfer interface {
	Upload(req transfer.UploadRequest) (*transfer.UploadResult, error)
	Info(fileID string) (*transfer.FileInfo, error)
	FetchTo(info *transfer.FileInfo, target string, maxSize int64) (int64, error)
}

// InjectedTools holds the local tool implementations added to the upstream
// tool set: containment policy first, then transfer, then audit.
type InjectedTools struct {
	Policy   *containment.Policy
	Uploader FileTransfer
	Audit    *transfer.AuditLog
	// Logf receives non-fatal diagnostics (audit write failures).
	Logf func(format string, args ...any)
}

// Handles reports whether name is an injected tool.
func (it *InjectedTools) Handles(name string) bool {
	switch name {
	case ToolFileUpload, ToolFileUploadToThread, ToolFileDownload:
		return true
	}
	return false
}

// Definitions returns the tool definitions merged into tools/list.
func (it *InjectedTools) Definitions() []jsonrpc.ToolInfo {
	uploadArgs := `
		"channel_id": {"type": "string", "description": "ID of the channel to post into (C…/G…/D…). Find it via the Slack tools, e.g. from a channel listing or search result."},
		"file": {"type": "string", "description": "File to upload: an absolute path, or a path relative to workspace_dir. Must resolve inside the operator-configured allowed roots."},
		"workspace_dir": {"type": "string", "description": "Absolute base directory for relative paths (e.g. the session working directory)."},
		"comment": {"type": "string", "description": "Optional message text posted together with the file."},
		"filename": {"type": "string", "description": "Display name shown in Slack (default: the file's basename)."}`

	return []jsonrpc.ToolInfo{
		{
			Name: ToolFileUpload,
			Description: "[extension] Upload a local file to Slack and post it as a new root message in a channel. " +
				"Not part of the official Slack MCP. The file must lie inside the operator-configured allowed roots; " +
				"the post appears under the authorizing user's own identity.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {` + uploadArgs + `},
				"required": ["channel_id", "file"]
			}`),
		},
		{
			Name: ToolFileUploadToThread,
			Description: "[extension] Upload a local file to Slack and post it as a reply in an existing thread. " +
				"Same containment rules as ext_file_upload; thread_ts is the timestamp of the thread's root message.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {` + uploadArgs + `,
					"thread_ts": {"type": "string", "description": "Timestamp (ts) of the message to reply to."}},
				"required": ["channel_id", "file", "thread_ts"]
			}`),
		},
		{
			Name: ToolFileDownload,
			Description: "[extension] Download a Slack file to the local disk. The symmetric counterpart of " +
				"ext_file_upload: use it to get real files (binaries, archives, anything too large for context) " +
				"into the operator-configured allowed roots. For reading textual content into context, prefer the " +
				"official slack_read_file. Never overwrites an existing file.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"file_id": {"type": "string", "description": "Slack file ID (F…), e.g. from a message's file attachment listing."},
					"dest_dir": {"type": "string", "description": "Directory to place the file in: absolute, or relative to workspace_dir. Defaults to workspace_dir. Must exist and resolve inside the allowed roots."},
					"workspace_dir": {"type": "string", "description": "Absolute base directory for relative paths (e.g. the session working directory)."},
					"filename": {"type": "string", "description": "Local filename to save as (default: the file's Slack name, sanitized to a bare basename)."}
				},
				"required": ["file_id"]
			}`),
		},
	}
}

// Handle executes an injected tool call. It always returns a ToolResult —
// failures become isError results carrying a structured JSON error
// ({code, message, details}), never protocol-level errors, so the agent can
// read and react to them.
func (it *InjectedTools) Handle(name string, args map[string]any) *jsonrpc.ToolResult {
	if name == ToolFileDownload {
		return it.handleDownload(args)
	}
	return it.handleUpload(name, args)
}

func (it *InjectedTools) handleUpload(name string, args map[string]any) *jsonrpc.ToolResult {
	channelID, _ := args["channel_id"].(string)
	file, _ := args["file"].(string)
	workspaceDir, _ := args["workspace_dir"].(string)
	comment, _ := args["comment"].(string)
	filename, _ := args["filename"].(string)
	threadTS, _ := args["thread_ts"].(string)

	if channelID == "" || file == "" {
		return errorResult("invalid_arguments", "channel_id and file are required", nil)
	}
	if name == ToolFileUploadToThread && threadTS == "" {
		return errorResult("invalid_arguments", "thread_ts is required for "+ToolFileUploadToThread, nil)
	}
	if name == ToolFileUpload {
		threadTS = "" // a root-message upload never threads
	}

	// Containment decides; everything below only executes.
	canonical, err := it.Policy.Resolve(workspaceDir, file)
	if err != nil {
		return it.pathDenied(name, err, channelID, threadTS)
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

func (it *InjectedTools) handleDownload(args map[string]any) *jsonrpc.ToolResult {
	fileID, _ := args["file_id"].(string)
	destDir, _ := args["dest_dir"].(string)
	workspaceDir, _ := args["workspace_dir"].(string)
	filename, _ := args["filename"].(string)

	if fileID == "" {
		return errorResult("invalid_arguments", "file_id is required", nil)
	}
	if destDir == "" {
		if workspaceDir == "" {
			return errorResult("invalid_arguments", "dest_dir (or workspace_dir) is required", nil)
		}
		destDir = workspaceDir
	}

	info, err := it.Uploader.Info(fileID)
	if err != nil {
		it.audit(transfer.AuditEntry{Tool: ToolFileDownload, FileID: fileID, Outcome: "error", Error: err.Error()})
		var se *transfer.SlackError
		if errors.As(err, &se) {
			return errorResult("slack_api_error", se.Error(), map[string]any{
				"method": se.Method,
				"reason": se.Reason,
			})
		}
		return errorResult("download_failed", err.Error(), nil)
	}

	// Size precheck against the declared size; the wire limit in FetchTo
	// re-enforces it during transfer.
	if cap := it.Policy.MaxSize(); cap > 0 && info.Size > cap {
		it.audit(transfer.AuditEntry{Tool: ToolFileDownload, FileID: fileID, Size: info.Size, Outcome: "denied", Error: "file_too_large"})
		return errorResult("file_too_large", "file exceeds the configured size cap", map[string]any{
			"size": info.Size,
			"cap":  cap,
		})
	}

	if filename == "" {
		filename = info.Name
	}
	target, err := it.Policy.ResolveNewFile(workspaceDir, destDir, filename)
	if err != nil {
		return it.pathDenied(ToolFileDownload, err, "", "")
	}

	written, err := it.Uploader.FetchTo(info, target, it.Policy.MaxSize())
	if err != nil {
		it.audit(transfer.AuditEntry{Tool: ToolFileDownload, Path: target, FileID: fileID, Outcome: "error", Error: err.Error()})
		if errors.Is(err, transfer.ErrTooLarge) {
			return errorResult("file_too_large", err.Error(), map[string]any{"cap": it.Policy.MaxSize()})
		}
		return errorResult("download_failed", err.Error(), nil)
	}

	it.audit(transfer.AuditEntry{
		Tool: ToolFileDownload, Path: target, Size: written, FileID: fileID, Outcome: "ok",
	})

	payload, _ := json.Marshal(map[string]any{
		"ok":       true,
		"file_id":  fileID,
		"path":     target,
		"filename": filename,
		"size":     written,
	})
	return &jsonrpc.ToolResult{
		Content: []jsonrpc.ToolContent{{Type: "text", Text: string(payload)}},
	}
}

// pathDenied shapes a containment violation into the structured tool error
// and audits the denial.
func (it *InjectedTools) pathDenied(tool string, err error, channelID, threadTS string) *jsonrpc.ToolResult {
	var v *containment.Violation
	if !errors.As(err, &v) {
		return errorResult("internal_error", err.Error(), nil)
	}
	it.audit(transfer.AuditEntry{
		Tool: tool, Path: v.Path, ChannelID: channelID, ThreadTS: threadTS,
		Outcome: "denied", Error: v.Reason,
	})
	return errorResult("path_denied", v.Error(), map[string]any{
		"reason":        v.Reason,
		"path":          v.Path,
		"allowed_roots": v.Roots,
	})
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
