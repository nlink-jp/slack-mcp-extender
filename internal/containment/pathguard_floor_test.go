package containment

import (
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
