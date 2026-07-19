package containment

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// canonTemp returns a symlink-resolved temp dir: on macOS t.TempDir() lives
// under /var/folders which is itself a symlink target area — resolving here
// keeps expected-path comparisons honest.
func canonTemp(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks(TempDir): %v", err)
	}
	return dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func mustPolicy(t *testing.T, roots []string, allowHidden bool, maxSize int64) *Policy {
	t.Helper()
	p, err := NewPolicy(roots, allowHidden, maxSize)
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	return p
}

func wantViolation(t *testing.T, err error, reason string) *Violation {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s violation, got nil error", reason)
	}
	var v *Violation
	if !errors.As(err, &v) {
		t.Fatalf("expected *Violation, got %T: %v", err, err)
	}
	if v.Reason != reason {
		t.Fatalf("violation reason = %q, want %q (detail: %s)", v.Reason, reason, v.Detail)
	}
	return v
}

func TestDenyByDefaultWithNoRoots(t *testing.T) {
	p := mustPolicy(t, nil, false, 0)
	_, err := p.Resolve("", "/etc/hosts")
	v := wantViolation(t, err, ReasonNoRoots)
	if !strings.Contains(v.Detail, "denied by default") {
		t.Errorf("detail = %q", v.Detail)
	}
}

func TestHappyPathAbsolute(t *testing.T) {
	root := canonTemp(t)
	file := filepath.Join(root, "report.csv")
	writeFile(t, file, "a,b\n")

	p := mustPolicy(t, []string{root}, false, 0)
	got, err := p.Resolve("", file)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != file {
		t.Errorf("canonical = %q, want %q", got, file)
	}
}

func TestHappyPathWorkspaceRelative(t *testing.T) {
	root := canonTemp(t)
	writeFile(t, filepath.Join(root, "session", "out", "deck.pdf"), "x")

	p := mustPolicy(t, []string{root}, false, 0)
	got, err := p.Resolve(filepath.Join(root, "session"), filepath.Join("out", "deck.pdf"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if want := filepath.Join(root, "session", "out", "deck.pdf"); got != want {
		t.Errorf("canonical = %q, want %q", got, want)
	}
}

func TestRelativeFileRequiresWorkspaceDir(t *testing.T) {
	p := mustPolicy(t, []string{canonTemp(t)}, false, 0)
	_, err := p.Resolve("", "out/deck.pdf")
	wantViolation(t, err, ReasonNotAbsolute)
}

func TestRelativeWorkspaceDirRejected(t *testing.T) {
	p := mustPolicy(t, []string{canonTemp(t)}, false, 0)
	_, err := p.Resolve("relative/dir", "deck.pdf")
	wantViolation(t, err, ReasonNotAbsolute)
}

func TestDotDotTraversalEscape(t *testing.T) {
	base := canonTemp(t)
	root := filepath.Join(base, "allowed")
	writeFile(t, filepath.Join(root, "ok.txt"), "x")
	secret := filepath.Join(base, "secret.txt")
	writeFile(t, secret, "s")

	p := mustPolicy(t, []string{root}, false, 0)

	// Absolute path with .. escaping the root.
	_, err := p.Resolve("", filepath.Join(root, "..", "secret.txt"))
	wantViolation(t, err, ReasonOutsideRoots)

	// Relative path with .. escaping via workspace_dir.
	_, err = p.Resolve(root, filepath.Join("..", "secret.txt"))
	wantViolation(t, err, ReasonOutsideRoots)
}

func TestSiblingPrefixNotContained(t *testing.T) {
	// root=<base>/data must not match <base>/database — string-prefix
	// containment bugs pass this; filepath.Rel containment must not.
	base := canonTemp(t)
	root := filepath.Join(base, "data")
	writeFile(t, filepath.Join(root, "ok.txt"), "x")
	writeFile(t, filepath.Join(base, "database", "f.txt"), "x")

	p := mustPolicy(t, []string{root}, false, 0)
	_, err := p.Resolve("", filepath.Join(base, "database", "f.txt"))
	wantViolation(t, err, ReasonOutsideRoots)
}

func TestSymlinkEscape(t *testing.T) {
	base := canonTemp(t)
	root := filepath.Join(base, "allowed")
	writeFile(t, filepath.Join(root, "placeholder"), "x")
	outside := filepath.Join(base, "outside.txt")
	writeFile(t, outside, "secret")

	link := filepath.Join(root, "innocent.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	p := mustPolicy(t, []string{root}, false, 0)
	_, err := p.Resolve("", link)
	v := wantViolation(t, err, ReasonOutsideRoots)
	if v.Path != outside {
		t.Errorf("violation path = %q, want resolved target %q", v.Path, outside)
	}
}

func TestNotFound(t *testing.T) {
	root := canonTemp(t)
	p := mustPolicy(t, []string{root}, false, 0)
	_, err := p.Resolve("", filepath.Join(root, "missing.txt"))
	wantViolation(t, err, ReasonNotFound)
}

func TestDirectoryRejected(t *testing.T) {
	root := canonTemp(t)
	sub := filepath.Join(root, "subdir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	p := mustPolicy(t, []string{root}, false, 0)
	_, err := p.Resolve("", sub)
	wantViolation(t, err, ReasonNotRegularFile)
}

func TestHiddenComponentDirect(t *testing.T) {
	root := canonTemp(t)
	writeFile(t, filepath.Join(root, ".env"), "SECRET=1")
	writeFile(t, filepath.Join(root, ".git", "config"), "[core]")
	writeFile(t, filepath.Join(root, "sub", ".ssh", "id_rsa"), "key")

	p := mustPolicy(t, []string{root}, false, 0)
	for _, f := range []string{
		filepath.Join(root, ".env"),
		filepath.Join(root, ".git", "config"),
		filepath.Join(root, "sub", ".ssh", "id_rsa"),
	} {
		_, err := p.Resolve("", f)
		wantViolation(t, err, ReasonHiddenComponent)
	}
}

func TestHiddenViaSymlinkResolution(t *testing.T) {
	// A benign-looking name that resolves to a dotfile must be caught:
	// the hidden check runs on the canonical (EvalSymlinks-resolved) path.
	root := canonTemp(t)
	writeFile(t, filepath.Join(root, ".env"), "SECRET=1")
	link := filepath.Join(root, "safe.txt")
	if err := os.Symlink(filepath.Join(root, ".env"), link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	p := mustPolicy(t, []string{root}, false, 0)
	_, err := p.Resolve("", link)
	wantViolation(t, err, ReasonHiddenComponent)
}

func TestDotParentedAllowedRootStillWorks(t *testing.T) {
	// The allowed root itself lives under a dot directory (e.g. a cowork
	// sessions parent under ~/.something). Components up to the root are
	// operator-approved; only components below it are checked.
	base := canonTemp(t)
	root := filepath.Join(base, ".cowork-sessions", "work")
	file := filepath.Join(root, "output.txt")
	writeFile(t, file, "x")

	p := mustPolicy(t, []string{root}, false, 0)
	got, err := p.Resolve("", file)
	if err != nil {
		t.Fatalf("Resolve rejected dot-parented root: %v", err)
	}
	if got != file {
		t.Errorf("canonical = %q, want %q", got, file)
	}
}

func TestAllowHiddenOptOut(t *testing.T) {
	root := canonTemp(t)
	file := filepath.Join(root, ".config-export.json")
	writeFile(t, file, "{}")

	p := mustPolicy(t, []string{root}, true, 0)
	if _, err := p.Resolve("", file); err != nil {
		t.Fatalf("allow_hidden=true still rejected: %v", err)
	}
}

func TestSizeCap(t *testing.T) {
	root := canonTemp(t)
	file := filepath.Join(root, "big.bin")
	writeFile(t, file, strings.Repeat("x", 100))

	over := mustPolicy(t, []string{root}, false, 99)
	_, err := over.Resolve("", file)
	wantViolation(t, err, ReasonTooLarge)

	exact := mustPolicy(t, []string{root}, false, 100)
	if _, err := exact.Resolve("", file); err != nil {
		t.Fatalf("file at exactly the cap rejected: %v", err)
	}
}

func TestMultipleRootsSecondMatches(t *testing.T) {
	rootA := canonTemp(t)
	rootB := canonTemp(t)
	file := filepath.Join(rootB, "f.txt")
	writeFile(t, file, "x")

	p := mustPolicy(t, []string{rootA, rootB}, false, 0)
	if _, err := p.Resolve("", file); err != nil {
		t.Fatalf("second root not matched: %v", err)
	}
}

func TestNewPolicyValidation(t *testing.T) {
	if _, err := NewPolicy([]string{"relative/root"}, false, 0); err == nil {
		t.Error("relative root accepted")
	}
	if _, err := NewPolicy([]string{filepath.Join(canonTemp(t), "missing")}, false, 0); err == nil {
		t.Error("nonexistent root accepted")
	}
	root := canonTemp(t)
	file := filepath.Join(root, "afile")
	writeFile(t, file, "x")
	if _, err := NewPolicy([]string{file}, false, 0); err == nil {
		t.Error("file (non-directory) root accepted")
	}
}

// --- write-side (ResolveNewFile) ---

func TestResolveNewFileHappyPath(t *testing.T) {
	root := canonTemp(t)
	p := mustPolicy(t, []string{root}, false, 0)
	got, err := p.ResolveNewFile("", root, "report.pdf")
	if err != nil {
		t.Fatalf("ResolveNewFile: %v", err)
	}
	if want := filepath.Join(root, "report.pdf"); got != want {
		t.Errorf("target = %q, want %q", got, want)
	}
}

func TestResolveNewFileWorkspaceRelative(t *testing.T) {
	root := canonTemp(t)
	sub := filepath.Join(root, "session", "in")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	p := mustPolicy(t, []string{root}, false, 0)
	got, err := p.ResolveNewFile(filepath.Join(root, "session"), "in", "data.bin")
	if err != nil {
		t.Fatalf("ResolveNewFile: %v", err)
	}
	if want := filepath.Join(sub, "data.bin"); got != want {
		t.Errorf("target = %q, want %q", got, want)
	}
}

func TestResolveNewFileDenials(t *testing.T) {
	base := canonTemp(t)
	root := filepath.Join(base, "allowed")
	if err := os.MkdirAll(filepath.Join(root, ".hidden-sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "taken.txt"), "x")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	// A symlinked escape hatch inside the root.
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	// A dangling symlink occupying a target name.
	if err := os.Symlink(filepath.Join(base, "nowhere"), filepath.Join(root, "dangling")); err != nil {
		t.Fatal(err)
	}

	p := mustPolicy(t, []string{root}, false, 0)
	tests := []struct {
		name             string
		wsDir, dir, file string
		reason           string
	}{
		{"no roots handled separately", "", "", "", ""}, // placeholder, skipped below
		{"outside root", "", outside, "f.txt", ReasonOutsideRoots},
		{"traversal via dest_dir", "", filepath.Join(root, ".."), "f.txt", ReasonOutsideRoots},
		{"symlink escape dir", "", link, "f.txt", ReasonOutsideRoots},
		{"missing dest dir", "", filepath.Join(root, "nope"), "f.txt", ReasonNotFound},
		{"relative without workspace", "", "in", "f.txt", ReasonNotAbsolute},
		{"hidden dest dir", "", filepath.Join(root, ".hidden-sub"), "f.txt", ReasonHiddenComponent},
		{"hidden filename", "", root, ".env", ReasonHiddenComponent},
		{"traversal filename becomes base", "", root, "../taken.txt", ReasonAlreadyExists},
		{"existing target", "", root, "taken.txt", ReasonAlreadyExists},
		{"dangling symlink target occupied", "", root, "dangling", ReasonAlreadyExists},
		{"empty filename", "", root, "", ReasonBadFilename},
		{"dot filename", "", root, "..", ReasonBadFilename},
		{"control chars", "", root, "a\x00b", ReasonBadFilename},
	}
	for _, tt := range tests {
		if tt.reason == "" {
			continue
		}
		t.Run(tt.name, func(t *testing.T) {
			_, err := p.ResolveNewFile(tt.wsDir, tt.dir, tt.file)
			wantViolation(t, err, tt.reason)
		})
	}

	// Deny-by-default with no roots.
	empty := mustPolicy(t, nil, false, 0)
	_, err := empty.ResolveNewFile("", root, "f.txt")
	wantViolation(t, err, ReasonNoRoots)
}

func TestResolveNewFileSlackFilenameNeutralized(t *testing.T) {
	// A hostile Slack-supplied filename can influence the NAME only, never
	// the directory: path components are stripped to the base.
	root := canonTemp(t)
	p := mustPolicy(t, []string{root}, false, 0)
	got, err := p.ResolveNewFile("", root, "../../etc/passwd")
	if err != nil {
		t.Fatalf("ResolveNewFile: %v", err)
	}
	if want := filepath.Join(root, "passwd"); got != want {
		t.Errorf("target = %q, want %q (name-only influence)", got, want)
	}
	// Backslash-separated variants are neutralized too.
	got, err = p.ResolveNewFile("", root, `..\..\evil.exe`)
	if err != nil || filepath.Base(got) != "evil.exe" || filepath.Dir(got) != root {
		t.Errorf("backslash variant: %q, %v", got, err)
	}
}

func TestSanitizeFilename(t *testing.T) {
	for name, want := range map[string]string{
		"report.pdf":      "report.pdf",
		"a/b/c.txt":       "c.txt",
		"../../etc/hosts": "hosts",
		`..\..\x.bin`:     "x.bin",
	} {
		got, err := SanitizeFilename(name)
		if err != nil || got != want {
			t.Errorf("SanitizeFilename(%q) = %q, %v; want %q", name, got, err, want)
		}
	}
	for _, bad := range []string{"", ".", "..", "/", "a\nb"} {
		if _, err := SanitizeFilename(bad); err == nil {
			t.Errorf("SanitizeFilename(%q) accepted", bad)
		}
	}
}

func TestViolationErrorAndRootsExposed(t *testing.T) {
	root := canonTemp(t)
	p := mustPolicy(t, []string{root}, false, 0)
	_, err := p.Resolve("", "/nonexistent-far-away/f.txt")
	var v *Violation
	if !errors.As(err, &v) {
		t.Fatalf("not a violation: %v", err)
	}
	if len(v.Roots) != 1 || v.Roots[0] != root {
		t.Errorf("Roots = %v, want [%s]", v.Roots, root)
	}
	if !strings.Contains(v.Error(), "path denied") {
		t.Errorf("Error() = %q", v.Error())
	}
}
