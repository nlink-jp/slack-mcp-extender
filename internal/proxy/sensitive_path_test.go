package proxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/slack-mcp-extender/internal/containment"
	"github.com/nlink-jp/slack-mcp-extender/internal/transfer"
	"github.com/nlink-jp/slack-mcp-extender/internal/workdir"
)

// The layer these tests observe is the injected tool handler: Handle for a
// real tool name, with real arguments, returning the payload an agent reads.
// That is where the defect was visible — the work_dir check accepted the
// directory and no later stage looked at the file — so it is where the
// refusal has to be pinned. The policy kernel's own mechanics are a layer
// below, in internal/containment.
//
// The credential floor is expressed relative to the home directory, so these
// tests redirect it. Writing a fixture into the operator's real
// ~/.config/gcloud to prove a refusal would be the test damaging the thing it
// exists to protect.

// fakeHome points os.UserHomeDir() at a temp directory and returns it,
// symlink-resolved.
func fakeHome(t *testing.T) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	if got, err := os.UserHomeDir(); err != nil || got != home {
		t.Skipf("the home directory cannot be redirected on this platform (got %q, %v)", got, err)
	}
	return home
}

func writeUnder(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// injectedUnder builds the tools with the audit log inside base, so a denial
// is recorded where the test can read it back.
func injectedUnder(t *testing.T, base string, uploader FileTransfer, serverDirs ...string) *InjectedTools {
	t.Helper()
	return &InjectedTools{
		MaxFileSize: 0,
		ServerDirs:  serverDirs,
		Uploader:    uploader,
		Audit:       &transfer.AuditLog{Path: filepath.Join(base, "audit.jsonl")},
	}
}

// requireAcceptedWorkDir fails the test if the work_dir check refuses dir.
// Every case below is about a file *inside a work directory the contract
// accepts*; if that premise ever stops holding, these tests would keep
// passing while proving something else entirely.
func requireAcceptedWorkDir(t *testing.T, dir string) {
	t.Helper()
	if _, err := workdir.Validate(dir, nil); err != nil {
		t.Fatalf("premise broken: work_dir %q is refused by the work_dir check itself (%v); "+
			"this test only means something while that check accepts it", dir, err)
	}
}

func wantPathDenied(t *testing.T, isErr bool, payload map[string]any, wantPath string) {
	t.Helper()
	if !isErr || payload["code"] != "path_denied" {
		t.Fatalf("not refused: isError=%v %v", isErr, payload)
	}
	details, ok := payload["details"].(map[string]any)
	if !ok {
		t.Fatalf("details = %v", payload["details"])
	}
	if details["reason"] != containment.ReasonSensitivePath {
		t.Fatalf("reason = %v, want %q", details["reason"], containment.ReasonSensitivePath)
	}
	msg, _ := payload["message"].(string)
	if !strings.Contains(msg, wantPath) {
		t.Errorf("message = %q, want it to name %q", msg, wantPath)
	}
}

// The defect itself: ~/.config is not a denied tree, so it passes as a work
// directory, and before the floor ran at the point of use the credential file
// under it was never looked at.
func TestUploadRefusesACredentialFileUnderAnAcceptedWorkDir(t *testing.T) {
	home := fakeHome(t)
	workDir := filepath.Join(home, ".config")
	secret := writeUnder(t, filepath.Join(workDir, "gcloud", "credentials.db"), "canary-must-not-reach-slack")
	requireAcceptedWorkDir(t, workDir)

	stub := &stubUploader{res: &transfer.UploadResult{}}
	it := injectedUnder(t, home, stub)

	isErr, payload := handleArgs(t, it, ToolFileUpload, map[string]any{
		"work_dir": workDir, "channel_id": "C1", "file": filepath.Join("gcloud", "credentials.db"),
	})
	wantPathDenied(t, isErr, payload, secret)

	// transfer.Client.Upload is the only thing that opens the file, so a
	// request that never arrived is a file whose contents were never read.
	if stub.req != nil {
		t.Errorf("the file was handed to the uploader anyway: %+v", stub.req)
	}
	// Every attempt is written down, denials included.
	audit, err := os.ReadFile(filepath.Join(home, "audit.jsonl"))
	if err != nil || !strings.Contains(string(audit), containment.ReasonSensitivePath) {
		t.Errorf("denial not audited: %q, %v", audit, err)
	}
}

// The same hole through the upload's other tool: one code path, but a reader
// of the tool list should not have to take that on trust.
func TestUploadToThreadRefusesACredentialFileToo(t *testing.T) {
	home := fakeHome(t)
	workDir := filepath.Join(home, "Library")
	secret := writeUnder(t, filepath.Join(workDir, "Keychains", "login.keychain-db"), "keychain")
	requireAcceptedWorkDir(t, workDir)

	stub := &stubUploader{res: &transfer.UploadResult{}}
	it := injectedUnder(t, home, stub)

	isErr, payload := handleArgs(t, it, ToolFileUploadToThread, map[string]any{
		"work_dir": workDir, "channel_id": "C1", "thread_ts": "1.2",
		"file": filepath.Join("Keychains", "login.keychain-db"),
	})
	wantPathDenied(t, isErr, payload, secret)
	if stub.req != nil {
		t.Errorf("the file was handed to the uploader anyway: %+v", stub.req)
	}
}

// A link inside an accepted work directory whose target is also inside it.
// Containment cannot see this one — the resolved path never leaves the root —
// so the floor is the only stage that refuses it.
func TestUploadRefusesASymlinkWithinAnAcceptedWorkDir(t *testing.T) {
	home := fakeHome(t)
	workDir := filepath.Join(home, ".config")
	secret := writeUnder(t, filepath.Join(workDir, "gcloud", "credentials.db"), "canary-must-not-reach-slack")
	requireAcceptedWorkDir(t, workDir)

	link := filepath.Join(workDir, "notes.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	stub := &stubUploader{res: &transfer.UploadResult{}}
	it := injectedUnder(t, home, stub)

	isErr, payload := handleArgs(t, it, ToolFileUpload, map[string]any{
		"work_dir": workDir, "channel_id": "C1", "file": "notes.txt",
	})
	wantPathDenied(t, isErr, payload, secret)
	if stub.req != nil {
		t.Errorf("the file was handed to the uploader anyway: %+v", stub.req)
	}
}

// A link out of an innocuous work directory into a credential tree.
// Containment already refused this as uncontained; what is pinned here is
// that the floor names the real reason, and that it does not depend on
// containment holding to do so.
func TestUploadRefusesASymlinkOutOfAnInnocuousWorkDir(t *testing.T) {
	home := fakeHome(t)
	secret := writeUnder(t, filepath.Join(home, ".ssh", "id_ed25519"), "PRIVATE KEY")

	workDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	requireAcceptedWorkDir(t, workDir)
	link := filepath.Join(workDir, "attachment.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	stub := &stubUploader{res: &transfer.UploadResult{}}
	it := injectedUnder(t, workDir, stub)

	isErr, payload := handleArgs(t, it, ToolFileUpload, map[string]any{
		"work_dir": workDir, "channel_id": "C1", "file": "attachment.txt",
	})
	wantPathDenied(t, isErr, payload, secret)
	if stub.req != nil {
		t.Errorf("the file was handed to the uploader anyway: %+v", stub.req)
	}
}

// The sharpest case: this server's own tokens. The state directory is refused
// as a work directory, but its parent is an ordinary directory, so naming the
// parent and the file below it was the whole bypass.
func TestUploadRefusesTheServersOwnTokensFromAnAcceptedParent(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(base, "ws.state")
	tokens := writeUnder(t, filepath.Join(stateDir, "tokens.json"), `{"access_token":"canary"}`)
	requireAcceptedWorkDir(t, base)

	stub := &stubUploader{res: &transfer.UploadResult{}}
	it := injectedUnder(t, base, stub, stateDir)

	isErr, payload := handleArgs(t, it, ToolFileUpload, map[string]any{
		"work_dir": base, "channel_id": "C1", "file": filepath.Join("ws.state", "tokens.json"),
	})
	wantPathDenied(t, isErr, payload, tokens)
	if d, _ := payload["details"].(map[string]any); d["floor_reason"] != "server_dir" {
		t.Errorf("details.floor_reason = %v, want server_dir (the reason work_dir_denied gives)", d["floor_reason"])
	}
	if stub.req != nil {
		t.Errorf("the OAuth token store was handed to the uploader: %+v", stub.req)
	}
}

// The write direction. A destination the caller names inside an accepted work
// directory can still be a credential or agent-control location, and a file
// dropped there is a file something else later reads as its own.
func TestDownloadRefusesASensitiveDestination(t *testing.T) {
	home := fakeHome(t)
	workDir := filepath.Join(home, ".config")
	if err := os.MkdirAll(filepath.Join(workDir, "gem-agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	requireAcceptedWorkDir(t, workDir)

	stub := &stubUploader{
		info:       &transfer.FileInfo{ID: "F1", Name: "mcp.json", Size: 2, DownloadURL: "stub://dl"},
		fetchBytes: []byte("{}"),
	}
	it := injectedUnder(t, home, stub)

	target := filepath.Join(workDir, "gem-agent", "mcp.json")
	isErr, payload := handleArgs(t, it, ToolFileDownload, map[string]any{
		"work_dir": workDir, "file_id": "F1", "dest_dir": "gem-agent",
	})
	wantPathDenied(t, isErr, payload, target)
	if stub.fetchedTo != "" {
		t.Errorf("the download was written to %q", stub.fetchedTo)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Errorf("%q exists after a refused download (%v)", target, err)
	}
}

// The control. A check that refuses everything would satisfy every test
// above, so an ordinary transfer in an ordinary work directory — with the
// home directory redirected and a server directory declared, exactly as the
// refusal cases have it — has to still work in both directions.
func TestOrdinaryTransfersStillWorkWithTheFloorInPlace(t *testing.T) {
	home := fakeHome(t)
	workDir := filepath.Join(home, "works", "exchange")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(home, ".local", "state", "slack-mcp-extender")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	requireAcceptedWorkDir(t, workDir)
	writeUnder(t, filepath.Join(workDir, "report.csv"), "a,b\n")

	stub := &stubUploader{
		res:        &transfer.UploadResult{FileID: "F9", Filename: "report.csv", Size: 4},
		info:       &transfer.FileInfo{ID: "F1", Name: "attachment.bin", Size: 4, DownloadURL: "stub://dl"},
		fetchBytes: []byte("data"),
	}
	it := injectedUnder(t, home, stub, stateDir)

	isErr, payload := handleArgs(t, it, ToolFileUpload, map[string]any{
		"work_dir": workDir, "channel_id": "C1", "file": "report.csv",
	})
	if isErr {
		t.Fatalf("ordinary upload refused: %v", payload)
	}
	if stub.req == nil || stub.req.Path != filepath.Join(workDir, "report.csv") {
		t.Errorf("uploader saw %+v", stub.req)
	}

	isErr, payload = handleArgs(t, it, ToolFileDownload, map[string]any{
		"work_dir": workDir, "file_id": "F1",
	})
	if isErr {
		t.Fatalf("ordinary download refused: %v", payload)
	}
	want := filepath.Join(workDir, "attachment.bin")
	if payload["path"] != want {
		t.Errorf("path = %v, want %q", payload["path"], want)
	}
	if data, err := os.ReadFile(want); err != nil || string(data) != "data" {
		t.Errorf("downloaded file = %q, %v", data, err)
	}
}
