package workdir

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
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

// --- the list itself, at the file level ------------------------------------
//
// DeniedPath is the floor applied to every path a call names inside an
// accepted work directory (ADR-021 §7). The scenario it was built for —
// work_dir = ~/.config with gcloud/credentials.db under it — is pinned at the
// layer an agent reaches, in internal/proxy/sensitive_path_test.go. What was
// not pinned anywhere is the *list*: only .config/gcloud and this server's own
// state directory were ever exercised for a file, so an entry deleted from
// sensitiveHomeTrees would have taken no test with it.
//
// The home directory is redirected: writing a fixture into the operator's real
// ~/.ssh to prove a refusal would be the test damaging what it protects.

// credentialTrees is the list this test holds the implementation to, written
// out rather than read from sensitiveHomeTrees. Iterating the implementation's
// own slice looked like a table over every entry and was not one: an entry
// deleted from the slice simply stopped being visited, so dropping ".aws" or
// "Library/Keychains" left the suite green. Measured, not assumed — that
// version of this test passed both mutations.
var credentialTrees = []string{
	".ssh", ".aws", ".gnupg", ".config/gcloud", ".config/gem-agent",
	".config/lagent", ".claude", ".codex", "Library/Keychains",
}

// TestCredentialListHasNotDrifted is the half that makes deletion fail: the
// implementation's list must be exactly the list above. An entry added without
// a refusal test, or removed in passing, stops here.
func TestCredentialListHasNotDrifted(t *testing.T) {
	got := append([]string(nil), sensitiveHomeTrees...)
	want := append([]string(nil), credentialTrees...)
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("sensitiveHomeTrees has %d entries, the test knows %d:\n  impl: %v\n  test: %v",
			len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("entry %d: impl %q, test %q — update credentialTrees and give the new tree a refusal case",
				i, got[i], want[i])
		}
	}
}

// TestDeniedPathRefusesEveryEntryOnTheCredentialList gives each tree a file
// under it, which is the upload case. A tree that does not exist yet is not
// interesting: the floor compares paths and never stats them.
func TestDeniedPathRefusesEveryEntryOnTheCredentialList(t *testing.T) {
	home := realTempDir(t)
	t.Setenv("HOME", home)

	for _, rel := range credentialTrees {
		t.Run(rel, func(t *testing.T) {
			file := filepath.Join(home, rel, "secret")
			if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte("credential"), 0o600); err != nil {
				t.Fatal(err)
			}
			why := DeniedPath(file, file, nil)
			if why == "" {
				t.Errorf("DeniedPath(%q) allowed a file under ~/%s", file, rel)
				return
			}
			// The reason has to name the tree, or an operator reading the
			// refusal cannot tell which rule they hit.
			if !strings.Contains(why, rel) {
				t.Errorf("reason %q does not name ~/%s", why, rel)
			}
		})
	}
}

// The must-pass row. A floor that refused everything under the home directory
// would satisfy every assertion above while breaking ordinary use — and the
// work directory a caller passes is routinely somewhere under $HOME.
func TestDeniedPathLeavesAnOrdinaryHomePathAlone(t *testing.T) {
	home := realTempDir(t)
	t.Setenv("HOME", home)

	file := filepath.Join(home, "work", "report.txt")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("nothing secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if why := DeniedPath(file, file, nil); why != "" {
		t.Errorf("DeniedPath(%q) refused an ordinary file: %s", file, why)
	}
}
