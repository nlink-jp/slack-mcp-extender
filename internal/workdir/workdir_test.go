package workdir

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The layer these tests observe is the resolver itself: Validate's closed list
// of checks. What the tools do with it — that they pass the server's own
// directories at all, and that the assembled server knows what those are — is
// pinned a layer up, in internal/proxy and internal/app.

func code(t *testing.T, err error) string {
	t.Helper()
	var we *Error
	if !errors.As(err, &we) {
		t.Fatalf("error %v is not a structured work_dir error", err)
	}
	return we.Code
}

func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestValidateRefusesServerOwnedDirectories(t *testing.T) {
	own := realTempDir(t)
	inside := filepath.Join(own, "tokens")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{own, inside} {
		if _, err := Validate(dir, []string{own}); code(t, err) != CodeDenied {
			t.Errorf("Validate(%q) = %v, want %s", dir, err, CodeDenied)
		}
	}
}

// A sibling is not denied by prefix alone: "<own>-elsewhere" starts with
// "<own>" as a string but is not under it as a path.
func TestValidateAcceptsSiblingOfServerOwnedDirectory(t *testing.T) {
	base := realTempDir(t)
	own := filepath.Join(base, "state")
	sibling := filepath.Join(base, "state-elsewhere")
	for _, d := range []string{own, sibling} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Validate(sibling, []string{own})
	if err != nil {
		t.Fatalf("Validate(%q) = %v, want accepted", sibling, err)
	}
	if got != sibling {
		t.Errorf("Validate = %q, want %q", got, sibling)
	}
}

// A server directory reached through a symlink is the case the fleet's
// both-spellings rule exists for: the state directory may itself be a link
// (into a cloud-sync folder, say), and comparing one spelling only walks past
// the list.
func TestValidateRefusesServerDirectoryThroughASymlink(t *testing.T) {
	base := realTempDir(t)
	target := filepath.Join(base, "target-state")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "state-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// Declared as the link, named as the target path.
	if _, err := Validate(target, []string{link}); code(t, err) != CodeDenied {
		t.Errorf("Validate(target, denied=link) = %v, want %s", err, CodeDenied)
	}
	// Declared as the target path, named as the link.
	if _, err := Validate(link, []string{target}); code(t, err) != CodeDenied {
		t.Errorf("Validate(link, denied=target) = %v, want %s", err, CodeDenied)
	}
}

func TestValidateAcceptsAnOrdinaryDirectory(t *testing.T) {
	dir := realTempDir(t)
	got, err := Validate(dir, nil)
	if err != nil {
		t.Fatalf("Validate(%q) = %v, want accepted", dir, err)
	}
	if got != dir {
		t.Errorf("Validate = %q, want the symlink-resolved %q", got, dir)
	}
}

// The server-directory check was inserted ahead of the home-relative list,
// which gives up when the home directory cannot be determined. These are the
// checks that had to stay untouched.
func TestValidateStillRefusesTheOriginalLocations(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory on this machine")
	}
	cases := []struct {
		name string
		dir  string
		want string
	}{
		{"filesystem root", "/", CodeDenied},
		{"system tree", "/usr", CodeDenied},
		{"home itself", home, CodeDenied},
		{"credential tree", filepath.Join(home, ".ssh"), CodeDenied},
		{"relative", "relative/dir", CodeInvalid},
		{"tilde", "~/work", CodeInvalid},
		{"parent segment", "/tmp/../etc", CodeInvalid},
		{"absent", filepath.Join(realTempDir(t), "nope"), CodeNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Validate(tc.dir, nil); code(t, err) != tc.want {
				t.Errorf("Validate(%q) = %v, want %s", tc.dir, err, tc.want)
			}
		})
	}
}

func TestResolveRequiresAWorkDirectory(t *testing.T) {
	if _, err := Resolve("", nil, nil); code(t, err) != CodeRequired {
		t.Errorf("Resolve(\"\") = %v, want %s", err, CodeRequired)
	}
}

// --- the floor, at the file level ------------------------------------------
//
// The list is pathguard's (and the runtimes'), tested there. What this server
// owns is which policy each direction gets: an upload leaves the machine
// (Outbound), a download stays (Local). The home directory is redirected:
// writing a fixture into the operator's real ~/.ssh to prove a refusal would
// be the test damaging what it protects.

// A file under each credential tree is refused both ways, and the reason
// names the tree.
func TestEveryCredentialTreeIsRefusedForUploadAndDownload(t *testing.T) {
	home := realTempDir(t)
	t.Setenv("HOME", home)
	for _, rel := range []string{".ssh", ".aws", ".gnupg", ".config/gcloud", ".config/gem-agent",
		".config/lagent", ".claude", ".codex", "Library/Keychains", ".kube", ".config/gh"} {
		file := filepath.Join(home, rel, "secret")
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("credential"), 0o600); err != nil {
			t.Fatal(err)
		}
		for name, why := range map[string]string{
			"upload":   upWhy(file, nil),
			"download": downWhy(file, nil),
		} {
			if why == "" || !strings.Contains(why, rel) {
				t.Errorf("%s of ~/%s: reason %q, want a refusal naming the tree", name, rel, why)
			}
		}
	}
}

// An upload leaves the machine, so a credential name is refused wherever it
// sits; a download written under that name elsewhere is ordinary.
func TestAnUploadIsJudgedAsLeavingTheMachine(t *testing.T) {
	home := realTempDir(t)
	t.Setenv("HOME", home)
	work := realTempDir(t)
	for _, rel := range []string{"id_rsa", "evidence/home/bob/.ssh/known_hosts", "service-account.json"} {
		file := filepath.Join(work, rel)
		if why := upWhy(file, nil); why == "" {
			t.Errorf("UploadDenied(%s) = \"\", want a refusal", rel)
		}
		if why := downWhy(file, nil); why != "" {
			t.Errorf("DownloadDenied(%s) = %q, want accepted", rel, why)
		}
	}
	env := filepath.Join(work, ".env")
	if upWhy(env, nil) == "" || downWhy(env, nil) == "" {
		t.Error("a .env file was not refused both ways")
	}
}

// The must-pass row. A floor that refused everything under the home directory
// would satisfy every assertion above while breaking ordinary use — and the
// work directory a caller passes is routinely somewhere under $HOME.
func TestAnOrdinaryHomePathIsLeftAlone(t *testing.T) {
	home := realTempDir(t)
	t.Setenv("HOME", home)
	file := filepath.Join(home, "work", "report.txt")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("nothing secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if why := upWhy(file, nil); why != "" {
		t.Errorf("UploadDenied(%q) refused an ordinary file: %s", file, why)
	}
	if why := downWhy(file, nil); why != "" {
		t.Errorf("DownloadDenied(%q) refused an ordinary file: %s", file, why)
	}
}

// The server's own directories are refused as a file location in both
// directions, and a work_dir_denied carries the reason.
func TestServerDirectoriesAreRefusedEverywhere(t *testing.T) {
	own := realTempDir(t)
	tokens := filepath.Join(own, "tokens.json")
	if upWhy(tokens, []string{own}) == "" || downWhy(tokens, []string{own}) == "" {
		t.Error("the server's state directory was not refused both ways")
	}
	_, err := Validate(own, []string{own})
	var we *Error
	if !errors.As(err, &we) || we.Details["reason"] != "server_dir" {
		t.Errorf("Validate(server dir) = %v, want work_dir_denied with reason server_dir", err)
	}
}

// upWhy and downWhy are the sentence of a refusal for a path given once.
func upWhy(p string, serverDirs []string) string {
	_, why := UploadDenied(p, p, serverDirs)
	return why
}

func downWhy(p string, serverDirs []string) string {
	_, why := DownloadDenied(p, p, serverDirs)
	return why
}

// The file policies return pathguard's reason with the sentence, the same
// vocabulary work_dir_denied carries.
func TestTheFilePoliciesCarryPathguardsReason(t *testing.T) {
	home := realTempDir(t)
	t.Setenv("HOME", home)
	own := realTempDir(t)
	tokens := filepath.Join(own, "tokens.json")
	if reason, _ := UploadDenied(tokens, tokens, []string{own}); reason != "server_dir" {
		t.Errorf("UploadDenied(tokens.json) reason = %q, want server_dir", reason)
	}
	key := filepath.Join(home, ".ssh", "id_ed25519")
	if reason, _ := DownloadDenied(key, key, nil); reason != "sensitive_path" {
		t.Errorf("DownloadDenied(~/.ssh/id_ed25519) reason = %q, want sensitive_path", reason)
	}
}

// A directory beneath the work directory that no work directory may be — a
// system tree — is refused with its reason; an ordinary one is not. The file
// policies leave system places out by design.
func TestDirDeniedAppliesTheWorkDirectoryList(t *testing.T) {
	home := realTempDir(t)
	t.Setenv("HOME", home)
	if reason, _ := DirDenied(home, nil); reason != "home_dir" {
		t.Errorf("DirDenied(home) reason = %q, want home_dir", reason)
	}
	if reason, why := DirDenied("/usr/share", nil); reason != "system_dir" || why == "" {
		t.Errorf("DirDenied(/usr/share) = %q, %q; want system_dir", reason, why)
	}
	if reason, why := DirDenied(realTempDir(t), nil); why != "" {
		t.Errorf("DirDenied(an ordinary directory) = %q, %q; want accepted", reason, why)
	}
	// The places the file policies judge by their own rules are left to them.
	for _, d := range []string{filepath.Join(home, ".ssh"), filepath.Join(realTempDir(t), ".env")} {
		if reason, why := DirDenied(d, nil); why != "" {
			t.Errorf("DirDenied(%s) = %q, %q; want it left to the file policies", d, reason, why)
		}
	}
	own := realTempDir(t)
	if reason, why := DirDenied(own, []string{own}); why != "" {
		t.Errorf("DirDenied(the server's own directory) = %q, %q; want it left to the file policies", reason, why)
	}
}
