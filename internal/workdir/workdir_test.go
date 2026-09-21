package workdir

import (
	"errors"
	"os"
	"path/filepath"
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
