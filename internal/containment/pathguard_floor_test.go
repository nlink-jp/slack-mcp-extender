package containment

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A download stays on this machine, so it is judged by the Local policy: a
// file written under a secret's name, or into a directory named like a
// credential one inside the work directory, is ordinary — the same file is
// refused as an upload, which leaves the machine (ADR-0004).
func TestADownloadIsJudgedAsStayingOnTheMachine(t *testing.T) {
	t.Setenv("HOME", canonTemp(t))
	work := canonTemp(t)
	evidence := filepath.Join(work, "evidence", "home", "bob", ".ssh")
	if err := os.MkdirAll(evidence, 0o755); err != nil {
		t.Fatal(err)
	}
	p := mustPolicy(t, []string{work}, true, 0)
	for _, c := range []struct{ dir, name string }{
		{work, "id_rsa"}, {work, "credentials.json"}, {evidence, "known_hosts"},
	} {
		if _, err := p.ResolveNewFile("", c.dir, c.name); err != nil {
			t.Errorf("download to %s/%s: %v, want accepted", c.dir, c.name, err)
		}
		writeFile(t, filepath.Join(c.dir, c.name), "x")
		wantViolation(t, mustFail(p.Resolve("", filepath.Join(c.dir, c.name))), ReasonSensitivePath)
	}
}

// A refusal on the floor carries pathguard's reason, so path_denied and
// work_dir_denied name the same place the same way.
func TestAFloorRefusalCarriesPathguardsReason(t *testing.T) {
	root, stateDir := serverDirFixture(t)
	p := mustPolicyWithServerDirs(t, []string{root}, []string{stateDir}, false, 0)
	v := wantViolation(t, mustFail(p.Resolve("", filepath.Join(stateDir, "tokens.json"))), ReasonSensitivePath)
	if v.Floor != "server_dir" {
		t.Errorf("Floor = %q, want server_dir", v.Floor)
	}
	writeFile(t, filepath.Join(root, "ok.txt"), "x")
	if _, err := p.Resolve("", filepath.Join(root, "ok.txt")); err != nil {
		t.Errorf("an ordinary file: %v", err)
	}
}

// The file policies leave system places out by design, so the directory a
// file lies in, beneath the work directory, is judged by the list of what may
// not be a work directory: a work directory above a system tree — reachable
// only for a server running as root — does not make the files in it fair
// game, in either direction.
func TestAFileInASystemTreeIsRefusedBothWays(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no /usr on windows")
	}
	t.Setenv("HOME", canonTemp(t))
	usr, err := filepath.EvalSymlinks("/usr")
	if err != nil {
		t.Skipf("/usr: %v", err)
	}
	file, err := filepath.EvalSymlinks("/usr/bin/true")
	if err != nil || !strings.HasPrefix(file, usr+string(filepath.Separator)) {
		t.Skipf("/usr/bin/true is not a file under /usr here (%v)", err)
	}
	p := mustPolicy(t, []string{usr}, false, 0)
	v := wantViolation(t, mustFail(p.Resolve("", file)), ReasonSensitivePath)
	if v.Floor != "system_dir" {
		t.Errorf("upload from /usr/bin: Floor = %q, want system_dir", v.Floor)
	}
	v = wantViolation(t, mustFail(p.ResolveNewFile("", filepath.Join(usr, "share"), "x.txt")), ReasonSensitivePath)
	if v.Floor != "system_dir" {
		t.Errorf("download into /usr/share: Floor = %q, want system_dir", v.Floor)
	}
}

// A credential file or directory that does not exist is refused like one that
// does: "not found" would tell the caller which secrets are there.
func TestAMissingCredentialPathIsRefusedNotReportedMissing(t *testing.T) {
	home := canonTemp(t)
	t.Setenv("HOME", home)
	work := filepath.Join(home, ".config")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	p := mustPolicy(t, []string{work}, true, 0)
	wantViolation(t, mustFail(p.Resolve(work, filepath.Join("gcloud", "credentials.db"))), ReasonSensitivePath)
	wantViolation(t, mustFail(p.ResolveNewFile(work, filepath.Join("gh", "sub"), "hosts.yml")), ReasonSensitivePath)
	// The control: an ordinary missing file is still reported missing.
	wantViolation(t, mustFail(p.Resolve(work, "absent.txt")), ReasonNotFound)
}

// A download whose directory is reached through a chain of links passing
// through a credential directory is refused: the end of the chain is an
// ordinary directory, and only the path as named shows the hop through
// ~/.config/gcloud.
func TestADownloadThroughALinkChainInACredentialDirectoryIsRefused(t *testing.T) {
	home := canonTemp(t)
	t.Setenv("HOME", home)
	work := filepath.Join(home, ".config")
	for _, d := range []string{filepath.Join(work, "gcloud", "sub"), filepath.Join(work, "end")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join("..", "..", "end"), filepath.Join(work, "gcloud", "sub", "hop")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join("gcloud", "sub", "hop"), filepath.Join(work, "out")); err != nil {
		t.Fatal(err)
	}
	p := mustPolicy(t, []string{work}, false, 0)
	wantViolation(t, mustFail(p.ResolveNewFile(work, "out", "x.txt")), ReasonSensitivePath)
	if _, err := p.ResolveNewFile(work, "end", "x.txt"); err != nil {
		t.Errorf("the same directory named directly: %v, want accepted", err)
	}
}

// Whether a path exists is never the difference between two answers: an
// existing credential path and a missing one are refused alike, and so are an
// existing path outside the roots and a missing one — in both directions, the
// whole answer (reason, pathguard's reason, path and message) and not only
// the code. Each pair differs in one name; the missing answer is compared
// with that name swapped back.
func TestExistenceIsNotRevealed(t *testing.T) {
	base := canonTemp(t)
	home := filepath.Join(base, "home")
	t.Setenv("HOME", home)
	// ~/.config is a link into a dotfiles tree, as on many machines.
	dot := filepath.Join(home, "dotfiles", "config")
	writeFile(t, filepath.Join(dot, "gcloud", "credentials.db"), "secret")
	writeFile(t, filepath.Join(dot, "gh", "hosts.yml"), "secret")
	if err := os.Symlink(dot, filepath.Join(home, ".config")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	writeFile(t, filepath.Join(home, ".aws", "credentials"), "secret")
	writeFile(t, filepath.Join(home, "notes.txt"), "x")
	other := canonTemp(t)
	writeFile(t, filepath.Join(other, "f_e.txt"), "x")
	writeFile(t, filepath.Join(other, "d_e", "keep"), "x")
	writeFile(t, filepath.Join(other, "b.txt"), "x")

	work := filepath.Join(home, ".config")
	// Links a caller could plant in its own work directory.
	plant := canonTemp(t)
	for link, target := range map[string]string{
		"lnk_e": filepath.Join(other, "f_e.txt"), "lnk_m": filepath.Join(other, "f_m.txt"),
		"dir_e": filepath.Join(other, "d_e"), "dir_m": filepath.Join(other, "d_m"),
		"aws": filepath.Join(home, ".aws"),
	} {
		if err := os.Symlink(target, filepath.Join(plant, link)); err != nil {
			t.Fatal(err)
		}
	}
	// A /var spelling of a temp directory, where there is one.
	varSpelling := plant
	if rest, ok := strings.CutPrefix(plant, "/private/var/"); ok {
		varSpelling = "/var/" + rest
	}

	pw := mustPolicy(t, []string{work}, true, 0)
	pp := mustPolicy(t, []string{plant}, true, 0)
	pb := mustPolicy(t, []string{base}, true, 0) // a work_dir above $HOME

	type answer struct{ reason, floor, path, detail string }
	got := func(err error) answer {
		var v *Violation
		if !errors.As(err, &v) {
			return answer{reason: "accepted or " + fmt.Sprint(err)}
		}
		return answer{v.Reason, v.Floor, v.Path, v.Detail}
	}
	for _, c := range []struct {
		name            string
		exists, missing func() error
		e, m            string // the one name that differs
	}{
		{"upload of a credential file",
			func() error { return mustFail(pw.Resolve(work, "gcloud/credentials.db")) },
			func() error { return mustFail(pw.Resolve(work, "gcloud/other.db")) }, "credentials.db", "other.db"},
		{"upload of a credential file through a dotfiles link",
			func() error { return mustFail(pw.Resolve(work, "gh/hosts.yml")) },
			func() error { return mustFail(pw.Resolve(work, "gh/other.yml")) }, "hosts.yml", "other.yml"},
		{"upload outside the root",
			func() error { return mustFail(pw.Resolve(work, filepath.Join(other, "b.txt"))) },
			func() error { return mustFail(pw.Resolve(work, filepath.Join(other, "c.txt"))) }, "b.txt", "c.txt"},
		{"upload through a planted link out of the root",
			func() error { return mustFail(pp.Resolve(plant, "lnk_e")) },
			func() error { return mustFail(pp.Resolve(plant, "lnk_m")) }, "_e", "_m"},
		{"upload through a planted link into a credential directory",
			func() error { return mustFail(pp.Resolve(plant, "aws/credentials")) },
			func() error { return mustFail(pp.Resolve(plant, "aws/config")) }, "credentials", "config"},
		{"upload of a file directly in the home directory",
			func() error { return mustFail(pb.Resolve(base, "home/notes.txt")) },
			func() error { return mustFail(pb.Resolve(base, "home/other.txt")) }, "notes.txt", "other.txt"},
		{"download into a credential file named as dest_dir",
			func() error { return mustFail(pw.ResolveNewFile(work, "gh/hosts.yml", "x.txt")) },
			func() error { return mustFail(pw.ResolveNewFile(work, "gh/other.yml", "x.txt")) }, "hosts.yml", "other.yml"},
		{"download into a credential path outside the root",
			func() error {
				return mustFail(pw.ResolveNewFile(work, filepath.Join(home, ".aws", "credentials"), "x.txt"))
			},
			func() error { return mustFail(pw.ResolveNewFile(work, filepath.Join(home, ".aws", "config"), "x.txt")) }, "credentials", "config"},
		{"download into a credential directory outside the root",
			func() error { return mustFail(pw.ResolveNewFile(work, filepath.Join(home, ".aws"), "x.txt")) },
			func() error { return mustFail(pw.ResolveNewFile(work, filepath.Join(home, ".kube"), "x.txt")) }, ".aws", ".kube"},
		{"download outside the root",
			func() error { return mustFail(pw.ResolveNewFile(work, filepath.Join(other, "b.txt"), "x.txt")) },
			func() error { return mustFail(pw.ResolveNewFile(work, filepath.Join(other, "c.txt"), "x.txt")) }, "b.txt", "c.txt"},
		{"download through a planted link out of the root",
			func() error { return mustFail(pp.ResolveNewFile(plant, "dir_e", "x.txt")) },
			func() error { return mustFail(pp.ResolveNewFile(plant, "dir_m", "x.txt")) }, "_e", "_m"},
		{"download through a planted link into a credential directory",
			func() error { return mustFail(pp.ResolveNewFile(plant, "aws/credentials", "x.txt")) },
			func() error { return mustFail(pp.ResolveNewFile(plant, "aws/config", "x.txt")) }, "credentials", "config"},
	} {
		e, m := got(c.exists()), got(c.missing())
		sw := func(x string) string { return strings.ReplaceAll(x, c.m, c.e) }
		if e.reason == ReasonNotFound || strings.HasPrefix(e.reason, "accepted") {
			t.Errorf("%s: %+v, want a refusal", c.name, e)
			continue
		}
		if m.reason != e.reason || m.floor != e.floor || sw(m.path) != sw(e.path) || sw(m.detail) != sw(e.detail) {
			t.Errorf("%s: the answer tells them apart\n  existing: %+v\n  missing:  %+v", c.name, e, m)
		}
	}

	// The control: inside the root, a typo is still reported missing — also
	// under a /var spelling of the work directory.
	wantViolation(t, mustFail(pp.Resolve(varSpelling, "typo.txt")), ReasonNotFound)
	wantViolation(t, mustFail(pp.ResolveNewFile(varSpelling, "nodir", "x.txt")), ReasonNotFound)
}

// An upload through a chain of links that passes through a credential
// directory is refused, although its end is an ordinary file inside the work
// directory: the floor gets the path as named, not only the resolved end.
func TestAnUploadThroughALinkChainInACredentialDirectoryIsRefused(t *testing.T) {
	home := canonTemp(t)
	t.Setenv("HOME", home)
	work := filepath.Join(home, ".config")
	writeFile(t, filepath.Join(work, "end", "x.txt"), "x")
	if err := os.MkdirAll(filepath.Join(work, "gcloud", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "..", "end"), filepath.Join(work, "gcloud", "sub", "hop")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join("gcloud", "sub", "hop"), filepath.Join(work, "out")); err != nil {
		t.Fatal(err)
	}
	p := mustPolicy(t, []string{work}, false, 0)
	wantViolation(t, mustFail(p.Resolve(work, filepath.Join("out", "x.txt"))), ReasonSensitivePath)
	if _, err := p.Resolve(work, filepath.Join("end", "x.txt")); err != nil {
		t.Errorf("the same file named directly: %v, want accepted", err)
	}
}

// A directory named like a .env file — a Python virtual environment called
// .env — is ordinary with allow_hidden, in both directions: only a file of
// that name holds credentials.
func TestAnEnvNamedDirectoryIsOrdinary(t *testing.T) {
	t.Setenv("HOME", canonTemp(t))
	work := canonTemp(t)
	writeFile(t, filepath.Join(work, ".env", "pyvenv.cfg"), "home = /usr/bin")
	p := mustPolicy(t, []string{work}, true, 0)
	if _, err := p.Resolve(work, filepath.Join(".env", "pyvenv.cfg")); err != nil {
		t.Errorf("upload of .env/pyvenv.cfg: %v, want accepted", err)
	}
	if _, err := p.ResolveNewFile(work, ".env", "notes.txt"); err != nil {
		t.Errorf("download into .env/: %v, want accepted", err)
	}
	writeFile(t, filepath.Join(work, "app", ".env"), "TOKEN=x")
	wantViolation(t, mustFail(p.Resolve(work, filepath.Join("app", ".env"))), ReasonSensitivePath)
}
