package proxy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nlink-jp/slack-mcp-extender/internal/transfer"
)

// The layer these tests observe is the injected tool handler: `Handle` for a
// real tool name, which reaches workdir.Resolve through policyFor carrying
// InjectedTools.ServerDirs. The resolver's own path mechanics are a layer
// below; what is pinned here is that the tools pass the list at all. A test
// calling workdir.Validate with its own list would prove the mechanism and
// keep passing with the wiring deleted.
//
// The construction layer above — buildProxy filling ServerDirs from the
// loaded config — is pinned separately in internal/app.

// injectedWithServerDirs returns the tools with one state directory declared
// as the server's own, plus that directory and an ordinary work directory.
func injectedWithServerDirs(t *testing.T) (it *InjectedTools, stateDir, ordinary string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stateDir = filepath.Join(base, "ws.state")
	ordinary = filepath.Join(base, "exchange")
	for _, d := range []string{stateDir, ordinary} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// tokens.json is what makes this the directory that must not be a
	// workspace: an upload is taken from inside work_dir and leaves the
	// machine (ADR-021 §7).
	if err := os.WriteFile(filepath.Join(stateDir, "tokens.json"),
		[]byte(`{"access_token":"placeholder"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return &InjectedTools{
		MaxFileSize: 0,
		ServerDirs:  []string{stateDir},
		Uploader:    &stubUploader{res: &transfer.UploadResult{}},
		Audit:       &transfer.AuditLog{Path: filepath.Join(base, "audit.jsonl")},
	}, stateDir, ordinary
}

func TestWorkDirRefusesServerStateDir(t *testing.T) {
	it, stateDir, _ := injectedWithServerDirs(t)

	isErr, payload := handleArgs(t, it, ToolFileUpload, map[string]any{
		"work_dir": stateDir, "channel_id": "C1", "file": "tokens.json",
	})
	if !isErr || payload["code"] != "work_dir_denied" {
		t.Fatalf("work_dir = the server's own state directory was accepted: isError=%v %v",
			isErr, payload)
	}
	// The reason reaches the caller, so it can tell which rule it hit.
	if details, _ := payload["details"].(map[string]any); details["reason"] != "server_dir" {
		t.Errorf("details = %v, want reason server_dir", payload["details"])
	}
}

// A subdirectory is the obvious way around a check that only compares the
// directory itself.
func TestWorkDirRefusesInsideServerStateDir(t *testing.T) {
	it, stateDir, _ := injectedWithServerDirs(t)
	inside := filepath.Join(stateDir, "nested")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}

	isErr, payload := handleArgs(t, it, ToolFileDownload, map[string]any{
		"work_dir": inside, "file_id": "F1",
	})
	if !isErr || payload["code"] != "work_dir_denied" {
		t.Fatalf("work_dir inside the server's own state directory was accepted: isError=%v %v",
			isErr, payload)
	}
}

func TestWorkDirAcceptsOrdinaryDirAlongsideServerDirs(t *testing.T) {
	it, _, ordinary := injectedWithServerDirs(t)
	file := filepath.Join(ordinary, "report.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	isErr, payload := handleArgs(t, it, ToolFileUpload, map[string]any{
		"work_dir": ordinary, "channel_id": "C1", "file": "report.txt",
	})
	if isErr {
		t.Fatalf("ordinary work_dir refused: %v", payload)
	}
}
