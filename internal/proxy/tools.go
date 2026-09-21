package proxy

import (
	"encoding/json"
	"errors"

	"github.com/nlink-jp/slack-mcp-extender/internal/containment"
	"github.com/nlink-jp/slack-mcp-extender/internal/jsonrpc"
	"github.com/nlink-jp/slack-mcp-extender/internal/transfer"
	"github.com/nlink-jp/slack-mcp-extender/internal/workdir"
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
	// AllowHidden and MaxFileSize are the operator's knobs on transfers. The
	// containment boundary is not one of them any more: it is the work_dir
	// the caller names on every call (ADR-0003).
	AllowHidden bool
	MaxFileSize int64
	// ServerDirs are this server's own config and state directories, refused
	// as a work directory along with everything under them (organization
	// ADR-021 §4) and, since they are also handed to the containment policy,
	// as the location of any file a call names inside an accepted work
	// directory (§7's floor). They come from the loaded config
	// (config.Config.ServerOwnedDirs), because the state directory is
	// per-workspace and only the config knows where it is.
	ServerDirs []string
	Uploader   FileTransfer
	Audit      *transfer.AuditLog
	// Logf receives non-fatal diagnostics (audit write failures).
	Logf func(format string, args ...any)
}

// policyFor resolves the caller's work directory and builds the containment
// policy for one call, with that directory as the only root.
//
// The boundary used to be an operator allowlist. It could not express what it
// was for: prefix matching has no per-repository granularity, so covering a
// work root meant listing the home directory — which admits the files the list
// existed to keep out. The caller naming one directory per call is the same
// guard at the granularity the config could never reach.
//
// This is the only place a work directory is resolved, so it is the only
// place the server's own directories have to be refused — a tool added later
// reaches a validated directory or a refusal, never a raw argument.
//
// The same denial list goes on to the policy, because an accepted work
// directory does not make its contents safe to send: `~/.config` is not
// itself a denied tree, so it passes as a work directory, and the file the
// call names under it is checked by the policy's credential floor rather than
// by this resolution (organization ADR-021 §7 — the list is a floor, not a
// boundary). Containment and the floor are both enforced, neither replaces
// the other.
func (it *InjectedTools) policyFor(arg string, meta map[string]json.RawMessage) (string, *containment.Policy, *jsonrpc.ToolResult) {
	dir, err := workdir.Resolve(arg, meta, it.ServerDirs)
	if err != nil {
		var we *workdir.Error
		if errors.As(err, &we) {
			return "", nil, errorResult(we.Code, we.Message, nil)
		}
		return "", nil, errorResult("internal_error", err.Error(), nil)
	}
	policy, perr := containment.NewPolicy([]string{dir}, it.ServerDirs, it.AllowHidden, it.MaxFileSize)
	if perr != nil {
		return "", nil, errorResult("internal_error", perr.Error(), nil)
	}
	return dir, policy, nil
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
		"file": {"type": "string", "description": "File to upload: a path relative to work_dir, or an absolute path inside it. Nothing outside work_dir can be uploaded — this file leaves the machine."},
		"work_dir": {"type": "string", "description": "Absolute path to a directory you can read back — your session or working directory. Uploads are taken from inside it and downloads land in it. It must already exist, and nothing here expands ~ or resolves a relative path."},
		"comment": {"type": "string", "description": "Optional message text posted together with the file."},
		"filename": {"type": "string", "description": "Display name shown in Slack (default: the file's basename)."}`

	return []jsonrpc.ToolInfo{
		{
			Name: ToolFileUpload,
			Description: "[extension] Upload a local file to Slack and post it as a new root message in a channel. " +
				"Not part of the official Slack MCP. The file must lie inside the work_dir you name on the call — " +
				"it leaves the machine, so nothing outside the directory you are working in can be sent; " +
				"the post appears under the authorizing user's own identity.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {` + uploadArgs + `},
				"required": ["work_dir", "channel_id", "file"]
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
				"required": ["work_dir", "channel_id", "file", "thread_ts"]
			}`),
		},
		{
			Name: ToolFileDownload,
			Description: "[extension] Download a Slack file to the local disk. The symmetric counterpart of " +
				"ext_file_upload: use it to get real files (binaries, archives, anything too large for context) " +
				"into the work_dir you name on the call. For reading textual content into context, prefer the " +
				"official slack_read_file. Never overwrites an existing file.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"file_id": {"type": "string", "description": "Slack file ID (F…), e.g. from a message's file attachment listing."},
					"dest_dir": {"type": "string", "description": "Directory to place the file in: relative to work_dir, or an absolute path inside it. Defaults to work_dir itself. Must exist."},
					"work_dir": {"type": "string", "description": "Absolute path to a directory you can read back — your session or working directory. Downloads land in it. It must already exist, and nothing here expands ~ or resolves a relative path."},
					"filename": {"type": "string", "description": "Local filename to save as (default: the file's Slack name, sanitized to a bare basename)."}
				},
				"required": ["work_dir", "file_id"]
			}`),
		},
	}
}

// Handle executes an injected tool call. It always returns a ToolResult —
// failures become isError results carrying a structured JSON error
// ({code, message, details}), never protocol-level errors, so the agent can
// read and react to them.
func (it *InjectedTools) Handle(name string, args map[string]any, meta map[string]json.RawMessage) *jsonrpc.ToolResult {
	if name == ToolFileDownload {
		return it.handleDownload(args, meta)
	}
	return it.handleUpload(name, args, meta)
}

func (it *InjectedTools) handleUpload(name string, args map[string]any, meta map[string]json.RawMessage) *jsonrpc.ToolResult {
	channelID, _ := args["channel_id"].(string)
	file, _ := args["file"].(string)
	workDirArg, _ := args["work_dir"].(string)
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

	// The work directory is the containment boundary: a file this tool sends
	// leaves the machine, so it may only come from inside the directory the
	// caller is working in (organization ADR-021 §7's one exception).
	workDir, policy, errResult := it.policyFor(workDirArg, meta)
	if errResult != nil {
		return errResult
	}

	// Containment decides; everything below only executes.
	canonical, err := policy.Resolve(workDir, file)
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

func (it *InjectedTools) handleDownload(args map[string]any, meta map[string]json.RawMessage) *jsonrpc.ToolResult {
	fileID, _ := args["file_id"].(string)
	destDir, _ := args["dest_dir"].(string)
	workDirArg, _ := args["work_dir"].(string)
	filename, _ := args["filename"].(string)

	if fileID == "" {
		return errorResult("invalid_arguments", "file_id is required", nil)
	}
	workDir, policy, errResult := it.policyFor(workDirArg, meta)
	if errResult != nil {
		return errResult
	}
	if destDir == "" {
		destDir = workDir
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
	if cap := policy.MaxSize(); cap > 0 && info.Size > cap {
		it.audit(transfer.AuditEntry{Tool: ToolFileDownload, FileID: fileID, Size: info.Size, Outcome: "denied", Error: "file_too_large"})
		return errorResult("file_too_large", "file exceeds the configured size cap", map[string]any{
			"size": info.Size,
			"cap":  cap,
		})
	}

	if filename == "" {
		filename = info.Name
	}
	target, err := policy.ResolveNewFile(workDir, destDir, filename)
	if err != nil {
		return it.pathDenied(ToolFileDownload, err, "", "")
	}

	written, err := it.Uploader.FetchTo(info, target, policy.MaxSize())
	if err != nil {
		it.audit(transfer.AuditEntry{Tool: ToolFileDownload, Path: target, FileID: fileID, Outcome: "error", Error: err.Error()})
		if errors.Is(err, transfer.ErrTooLarge) {
			return errorResult("file_too_large", err.Error(), map[string]any{"cap": policy.MaxSize()})
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
		"reason":   v.Reason,
		"path":     v.Path,
		"work_dir": v.Roots,
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
