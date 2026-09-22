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
// existing path outside the roots and a missing one — in both directions.
func TestExistenceIsNotRevealed(t *testing.T) {
	home := canonTemp(t)
	t.Setenv("HOME", home)
	work := filepath.Join(home, ".config")
	writeFile(t, filepath.Join(work, "gcloud", "credentials.db"), "secret")
	writeFile(t, filepath.Join(work, "gh", "hosts.yml"), "secret")
	writeFile(t, filepath.Join(home, ".aws", "credentials"), "secret")
	other := canonTemp(t)
	writeFile(t, filepath.Join(other, "b.txt"), "x")
	p := mustPolicy(t, []string{work}, true, 0)

	type answer struct{ reason, floor string }
	got := func(err error) answer {
		var v *Violation
		if !errors.As(err, &v) {
			return answer{"accepted or " + fmt.Sprint(err), ""}
		}
		return answer{v.Reason, v.Floor}
	}
	for _, c := range []struct {
		name            string
		exists, missing func() error
	}{
		{"upload of a credential file",
			func() error { return mustFail(p.Resolve(work, "gcloud/credentials.db")) },
			func() error { return mustFail(p.Resolve(work, "gcloud/other.db")) }},
		{"upload outside the root",
			func() error { return mustFail(p.Resolve(work, filepath.Join(other, "b.txt"))) },
			func() error { return mustFail(p.Resolve(work, filepath.Join(other, "c.txt"))) }},
		{"download into a credential file named as dest_dir",
			func() error { return mustFail(p.ResolveNewFile(work, "gh/hosts.yml", "x.txt")) },
			func() error { return mustFail(p.ResolveNewFile(work, "gh/other.yml", "x.txt")) }},
		{"download into a credential path outside the root",
			func() error { return mustFail(p.ResolveNewFile(work, filepath.Join(home, ".aws", "credentials"), "x.txt")) },
			func() error { return mustFail(p.ResolveNewFile(work, filepath.Join(home, ".aws", "config"), "x.txt")) }},
		{"download into a credential directory outside the root",
			func() error { return mustFail(p.ResolveNewFile(work, filepath.Join(home, ".aws"), "x.txt")) },
			func() error { return mustFail(p.ResolveNewFile(work, filepath.Join(home, ".kube"), "x.txt")) }},
		{"download outside the root",
			func() error { return mustFail(p.ResolveNewFile(work, filepath.Join(other, "b.txt"), "x.txt")) },
			func() error { return mustFail(p.ResolveNewFile(work, filepath.Join(other, "sub"), "x.txt")) }},
	} {
		e, m := got(c.exists()), got(c.missing())
		if e != m {
			t.Errorf("%s: existing → %v, missing → %v; the answer tells them apart", c.name, e, m)
		}
		if e.reason == ReasonNotFound {
			t.Errorf("%s: %v, want a refusal", c.name, e)
		}
	}
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
