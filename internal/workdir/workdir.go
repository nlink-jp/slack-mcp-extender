// Package workdir resolves the directory one MCP call reads from and writes
// into: the caller's work directory, not this server's.
//
// It is the fleet's work-directory contract (organization ADR-021) in the
// shape this proxy needs. The value is per session and per calling runtime, so
// the server cannot know it; only the caller can. Of the channels a runtime
// could use, the per-call argument is the only one all four of our callers
// have — MCP roots come back empty from Codex and carry only the project
// directory from Claude Code, and Codex strips the environment before spawning
// a server. The `_meta` key is the second channel, for the runtimes we write
// ourselves: they can set it on every tools/call without knowing any schema.
//
// There is deliberately no third. A server-chosen default is readable by the
// caller only by coincidence.
package workdir

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MetaKey is the request-level `_meta` key a runtime sets on every tools/call
// to name the session work directory.
const MetaKey = "jp.nlink/work_dir"

// Error codes, shared with the rest of the fleet so a caller sees the same
// vocabulary from every server.
const (
	CodeRequired    = "work_dir_required"
	CodeInvalid     = "work_dir_invalid"
	CodeNotFound    = "work_dir_not_found"
	CodeNotWritable = "work_dir_not_writable"
	CodeDenied      = "work_dir_denied"
)

// Error is a refusal carrying the code the caller branches on.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func newErr(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// writable reports whether this process can create an entry in dir.
//
// The test is a create-and-remove rather than an access(2) call: syscall.Access
// does not exist on Windows, and these servers cross-compile there. Doing what
// the server is about to do anyway is also the more honest check — an access
// bit can say yes on a filesystem that then refuses the write.
func writable(dir string) error {
	f, err := os.CreateTemp(dir, ".work_dir-check-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

// deniedTrees are locations a work directory may never be, together with
// everything under them. Paths are in resolved form (/etc and /var are
// symlinks on darwin).
var deniedTrees = []string{
	"/bin", "/sbin", "/usr", "/System", "/Library", "/Applications", "/private/etc",
}

// deniedExact are denied as the work directory itself but not as ancestors.
// /private/var holds the per-user temporary directory, a legitimate place to
// work in.
var deniedExact = []string{"/", "/private/var"}

// sensitiveHomeTrees hold credentials or steer an agent. A work directory here
// would make this server upload a private key to Slack on a model's say-so,
// which is the one outcome an upload path must not have.
var sensitiveHomeTrees = []string{
	".ssh", ".aws", ".gnupg", ".config/gcloud", ".config/gem-agent",
	".config/lagent", ".claude", ".codex", "Library/Keychains",
}

// Resolve returns the validated work directory for one call: the tool's
// work_dir argument, else the runtime hint in the request's `_meta`, else an
// error. The returned path is absolute and symlink-resolved.
//
// serverDirs are this server's own config and state directories, refused
// along with everything under them (organization ADR-021 §4). It is a
// parameter rather than a package variable set at startup because a variable
// has an initialization order: a call that arrived before it was set would
// resolve with nothing denied, and would look exactly like a call that was
// allowed. A caller that has nothing to deny passes nil, and has to say so.
func Resolve(arg string, meta map[string]json.RawMessage, serverDirs []string) (string, error) {
	dir := strings.TrimSpace(arg)
	if dir == "" {
		hint, err := metaHint(meta)
		if err != nil {
			return "", err
		}
		dir = hint
	}
	if dir == "" {
		return "", newErr(CodeRequired,
			"work_dir is required: pass the absolute path of a directory you can read back "+
				"(your session or working directory). Uploads are taken from it and downloads land in it.")
	}
	return Validate(dir, serverDirs)
}

// Validate applies the closed list of checks and returns the resolved path.
func Validate(dir string, serverDirs []string) (string, error) {
	if strings.HasPrefix(dir, "~") {
		return "", newErr(CodeInvalid, "work_dir %q starts with ~: nothing expands it on this path — pass the absolute path", dir)
	}
	if !filepath.IsAbs(dir) {
		return "", newErr(CodeInvalid, "work_dir %q must be an absolute path", dir)
	}
	for _, seg := range strings.Split(filepath.ToSlash(dir), "/") {
		if seg == ".." {
			return "", newErr(CodeInvalid, "work_dir %q contains a .. segment; pass the path you mean", dir)
		}
	}

	resolved, err := filepath.EvalSymlinks(filepath.Clean(dir))
	if err != nil {
		return "", newErr(CodeNotFound,
			"work_dir %q does not exist — it is your directory, so this is a typo, not something to create here", dir)
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return "", newErr(CodeNotFound, "work_dir %q: %v", dir, err)
	}
	if !fi.IsDir() {
		return "", newErr(CodeNotFound, "work_dir %q is not a directory", dir)
	}
	if why := denied(dir, resolved, serverDirs); why != "" {
		return "", newErr(CodeDenied, "work_dir %q is refused: %s", dir, why)
	}
	if err := writable(resolved); err != nil {
		return "", newErr(CodeNotWritable, "work_dir %q is not writable by this server", dir)
	}
	return resolved, nil
}

// DeniedPath reports why a path lies in a location this server must never read
// from or write to, or "" when it does not. raw is the path as the caller
// spelled it and resolved is its symlink-resolved form; pass the same value
// twice for a path that does not exist yet, such as a download target.
//
// It applies the same closed list Validate applies to the work directory, at
// the other end of the call. That list is deliberately a floor and not a
// boundary — organization ADR-021 §7: "The blacklist is a floor, not a
// boundary. The next secret file is not on it." — and a floor tested only
// against the directory argument is not even a floor: `~/.config` is not
// itself a denied tree, so it is accepted as a work directory, and a call
// naming `gcloud/credentials.db` under it was never looked at. The same holds
// for `~/Library` above `Library/Keychains`, and for the parent of this
// server's own state directory above `tokens.json`.
//
// Containment in the work directory remains the boundary and is enforced
// separately (internal/containment); this is the floor underneath it, and
// neither stands in for the other.
func DeniedPath(raw, resolved string, serverDirs []string) string {
	return denied(raw, resolved, serverDirs)
}

// denied reports why the path may not be a work directory, or "".
//
// Both spellings are checked — as given and symlink-resolved — against both
// spellings of every entry, because a blacklisted directory may itself be a
// symlink: ~/.ssh/config pointing into a cloud-sync folder stops looking like
// ~/.ssh once resolved, and comparing only the unresolved form lets a planted
// link through instead.
//
// serverDirs — this server's own config and state directories — are checked
// before the home-relative list, because that list gives up when the home
// directory cannot be determined and this check must not depend on it.
func denied(raw, resolved string, serverDirs []string) string {
	forms := pathForms(raw, resolved)
	for _, d := range deniedExact {
		for _, p := range forms {
			if p == d {
				return "it is a system directory"
			}
		}
	}
	for _, d := range deniedTrees {
		for _, p := range forms {
			if within(p, d) {
				return "it is inside the system directory " + d
			}
		}
	}
	for _, d := range serverDirs {
		if d == "" {
			continue
		}
		for _, entry := range pathForms(d, d) {
			for _, p := range forms {
				if within(p, entry) {
					return "it is inside this server's own directory " + d +
						", which holds its configuration and its OAuth tokens"
				}
			}
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	for _, h := range pathForms(home, home) {
		for _, p := range forms {
			if p == h {
				return "it is the home directory itself; pass a directory inside it"
			}
		}
		for _, rel := range sensitiveHomeTrees {
			for _, entry := range pathForms(filepath.Join(h, rel), filepath.Join(h, rel)) {
				for _, p := range forms {
					if within(p, entry) {
						return "~/" + rel + " holds credentials or agent control files"
					}
				}
			}
		}
	}
	return ""
}

// pathForms expands paths into every spelling worth comparing: absolute and
// cleaned, plus the symlink-resolved form when it differs.
func pathForms(paths ...string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, p := range paths {
		if p == "" {
			continue
		}
		if abs, err := filepath.Abs(p); err == nil {
			add(filepath.Clean(abs))
		}
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			if abs, aerr := filepath.Abs(resolved); aerr == nil {
				add(filepath.Clean(abs))
			}
		}
	}
	return out
}

func within(path, root string) bool {
	if path == root {
		return true
	}
	return strings.HasPrefix(path, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator))
}

// metaHint reads the work directory a runtime attached to the request. A
// present but non-string value is an error rather than a silent miss.
func metaHint(meta map[string]json.RawMessage) (string, error) {
	raw, ok := meta[MetaKey]
	if !ok {
		return "", nil
	}
	var dir string
	if err := json.Unmarshal(raw, &dir); err != nil {
		return "", newErr(CodeInvalid, "request _meta[%q] is not a string", MetaKey)
	}
	return strings.TrimSpace(dir), nil
}
