// Package containment implements the file-access policy of
// slack-mcp-extender. The tool relays untrusted Slack content, reads local
// files, and sends data to an external service — an exfiltration primitive
// unless confined — so every file argument passes through this package
// before it is opened.
//
// The policy's only root is the work_dir the call named (ADR-0003): the
// operator allowlist it replaced could not express what it was for, because
// prefix matching has no per-repository granularity. The operator still owns
// the hidden opt-out and the size cap. Every other tool argument is untrusted
// input that must resolve inside the policy; none of them widens it.
//
// Check order of Resolve, the upload source (do not reorder; each stage
// assumes the previous ones):
//
//  1. place          where the path is or would be, every link followed, a
//     dangling one by its target (workdir.Where — for a path that exists,
//     what EvalSymlinks returns). Stages 2 and 3 run on this place, so `..`
//     tricks and symlink disguises are resolved away, and whether anything
//     exists there is asked only after them: an existing path and a missing
//     one get the same answer, message and details included
//  2. credential floor  the path, as named and placed, may not lie in a
//     credential or agent-control location, nor in this server's own
//     directories — nlink-jp/pathguard's judgement (ADR-0004, organization
//     ADR-021 §7), its Outbound policy, since an upload leaves the machine.
//     It runs on the file the call names, because an accepted work directory
//     does not make its contents safe to send: the list is a floor, and a
//     floor applied only to the directory argument is stepped over by naming
//     a file under an accepted parent
//  3. containment    the canonical path must be under one allowed root
//     (deny-by-default: no roots configured → nothing is allowed); then the
//     directory the file lies in may not be a system directory or the home
//     directory itself, which the file policies leave out
//  4. regular file   the file must exist (EvalSymlinks; if it resolves
//     elsewhere than it was placed, stages 2 and 3 run again on that), and
//     directories, devices, sockets, and anything else that is not a plain
//     file are rejected
//  5. hidden check   no path component below the matched root may start
//     with "." (`.git`, `.cache`, …) unless allow_hidden is set; the root
//     itself may live under a dot directory — that prefix was explicitly
//     operator-approved. `.env` and `.ssh` never get this far: the floor
//     refused them
//  6. size cap       the file must not exceed the configured maximum
//
// ResolveNewFile, the download target, keeps the same order: place the
// destination directory, then the floor (pathguard's Local policy, the target
// as named and placed), containment and the system-directory check on that
// place, and only then that it exists and is a directory, the hidden rule and
// that nothing is there yet.
//
// Stages 2 and 3 are independent and both are load-bearing. Containment
// answers "did the caller designate this?"; the floor answers "may this
// server touch it at all?". A credential file inside a designated directory
// passes the first and must still fail the second.
package containment

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nlink-jp/slack-mcp-extender/internal/workdir"
)

// Violation reason codes, carried in Violation.Reason and surfaced to the
// MCP client inside a structured path_denied error.
const (
	ReasonNoRoots         = "no_allowed_roots"
	ReasonNotAbsolute     = "not_absolute"
	ReasonNotFound        = "not_found"
	ReasonOutsideRoots    = "outside_allowed_roots"
	ReasonNotRegularFile  = "not_regular_file"
	ReasonSensitivePath   = "sensitive_path"
	ReasonHiddenComponent = "hidden_component"
	ReasonTooLarge        = "too_large"
	ReasonAlreadyExists   = "already_exists"
	ReasonBadFilename     = "bad_filename"
)

// Violation is a policy rejection. It is an error so callers can return it
// directly; the fields feed the structured path_denied tool error.
type Violation struct {
	Reason string   // one of the Reason* codes
	Path   string   // the offending path as resolved so far
	Roots  []string // the canonical allowed roots (for the error details)
	Detail string   // human-readable specifics
	// Floor is pathguard's reason for a ReasonSensitivePath refusal
	// (sensitive_path, server_dir, system_dir, home_dir, unresolvable_path,
	// home_unknown, unconfigured) — the same vocabulary work_dir_denied
	// carries — and empty for every other reason.
	Floor string
}

func (v *Violation) Error() string {
	return fmt.Sprintf("path denied (%s): %s", v.Reason, v.Detail)
}

// Policy is the operator-configured containment policy. Construct it with
// NewPolicy so the roots are canonicalized once, up front.
type Policy struct {
	roots       []string // canonical (EvalSymlinks-resolved) allowed roots
	serverDirs  []string // this server's own config and state directories
	allowHidden bool
	maxSize     int64 // bytes; <= 0 means no cap
}

// NewPolicy canonicalizes the allowed roots and returns a Policy. Every root
// must exist and be a directory: a root that cannot be canonicalized cannot
// be enforced, so it is a configuration error, not a silent skip.
// An empty roots list is valid and yields a deny-everything policy.
//
// serverDirs are this server's own config and state directories, refused as a
// file location along with everything under them: the state directory holds
// tokens.json, and an upload leaves the machine. They are a constructor
// parameter for the same reason workdir.Resolve takes them rather than reading
// a package variable — a variable has an initialization order, and a call that
// arrived before it was set would look exactly like a call that was allowed. A
// caller with nothing to deny passes nil, and has to say so.
func NewPolicy(roots, serverDirs []string, allowHidden bool, maxSize int64) (*Policy, error) {
	canonical := make([]string, 0, len(roots))
	for _, root := range roots {
		if !filepath.IsAbs(root) {
			return nil, fmt.Errorf("allowed root %q is not absolute", root)
		}
		resolved, err := filepath.EvalSymlinks(filepath.Clean(root))
		if err != nil {
			return nil, fmt.Errorf("allowed root %q: %w", root, err)
		}
		fi, err := os.Stat(resolved)
		if err != nil {
			return nil, fmt.Errorf("allowed root %q: %w", root, err)
		}
		if !fi.IsDir() {
			return nil, fmt.Errorf("allowed root %q is not a directory", root)
		}
		canonical = append(canonical, resolved)
	}
	return &Policy{
		roots:       canonical,
		serverDirs:  serverDirs,
		allowHidden: allowHidden,
		maxSize:     maxSize,
	}, nil
}

// sensitive builds the stage-2 refusal for a path on the credential floor, or
// returns nil. raw is the caller's spelling and resolved the real path, and
// deny is the direction's judgement: workdir.UploadDenied for a file that
// leaves the machine (pathguard's Outbound policy), workdir.DownloadDenied for
// one written here (the Local policy). Both compare by file identity and by
// folded name, links followed (organization ADR-021 §7).
//
// The wording and the reason come from pathguard, so a caller reads the same
// sentence and reason whether the refused path arrived as work_dir or as
// file.
func (p *Policy) sensitive(raw, resolved string, deny func(raw, resolved string, serverDirs []string) (string, string)) *Violation {
	reason, why := deny(raw, resolved, p.serverDirs)
	if why == "" {
		return nil
	}
	return &Violation{
		Reason: ReasonSensitivePath,
		Path:   resolved,
		Roots:  p.Roots(),
		Detail: fmt.Sprintf("%q is refused: %s", resolved, why),
		Floor:  reason,
	}
}

// deniedDir builds the refusal for a file whose directory, beneath the work
// directory, is a system directory or the home directory itself
// (workdir.DirDenied), or returns nil.
func (p *Policy) deniedDir(dir, path string) *Violation {
	reason, why := workdir.DirDenied(dir, p.serverDirs)
	if why == "" {
		return nil
	}
	return &Violation{
		Reason: ReasonSensitivePath,
		Path:   path,
		Roots:  p.Roots(),
		Detail: why,
		Floor:  reason,
	}
}

// matchRoot returns the allowed root path lies under, or "". A file must lie
// strictly below its root; a destination directory may be the root itself
// (allowRoot).
func (p *Policy) matchRoot(path string, allowRoot bool) string {
	for _, root := range p.roots {
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if rel == "." && !allowRoot {
			continue
		}
		return root
	}
	return ""
}

func (p *Policy) outside(where, raw string) *Violation {
	return &Violation{
		Reason: ReasonOutsideRoots,
		Path:   where,
		Roots:  p.Roots(),
		Detail: fmt.Sprintf("%q resolves outside every allowed root", raw),
	}
}

func (p *Policy) outsideDest(where, raw string) *Violation {
	return &Violation{
		Reason: ReasonOutsideRoots,
		Path:   where,
		Roots:  p.Roots(),
		Detail: fmt.Sprintf("dest_dir %q resolves outside every allowed root", raw),
	}
}

// Roots returns the canonical allowed roots (for error details and logs).
func (p *Policy) Roots() []string {
	out := make([]string, len(p.roots))
	copy(out, p.roots)
	return out
}

// Resolve validates a file argument against the policy and returns the
// canonical path to open. file may be absolute, or relative to workDir
// (which must then be absolute — it is an untrusted tool argument and gets
// no default). Any violation is returned as *Violation.
func (p *Policy) Resolve(workDir, file string) (string, error) {
	if len(p.roots) == 0 {
		return "", &Violation{
			Reason: ReasonNoRoots,
			Path:   file,
			Detail: "no root configured for this policy; the root is the work_dir the call named (ADR-0003)",
		}
	}

	// Assemble the raw path from the untrusted arguments.
	raw := file
	if !filepath.IsAbs(raw) {
		if workDir == "" {
			return "", &Violation{
				Reason: ReasonNotAbsolute,
				Path:   file,
				Roots:  p.Roots(),
				Detail: fmt.Sprintf("relative file %q requires work_dir", file),
			}
		}
		if !filepath.IsAbs(workDir) {
			return "", &Violation{
				Reason: ReasonNotAbsolute,
				Path:   workDir,
				Roots:  p.Roots(),
				Detail: fmt.Sprintf("work_dir %q must be absolute", workDir),
			}
		}
		raw = filepath.Join(workDir, raw)
	}

	// Stage 1: place the path — where it is, or would be, every link on it
	// followed, a dangling one by its target (workdir.Where; for a path that
	// exists, what EvalSymlinks returns). The floor, containment and the
	// directory check are all made on this place, and only after them does it
	// matter whether anything is there: a path that exists and one that does
	// not get the same answer, message and details included, so no answer
	// tells the caller which files exist.
	where := workdir.Where(filepath.Clean(raw))

	// Stage 2: the credential floor, on the path as named and as placed. It
	// runs before containment so the refusal names the actual problem — a link
	// out of the work directory into ~/.ssh is refused as a credential file,
	// not merely as an uncontained one — and because the floor does not
	// depend on containment to hold: the hole this stage closes was a work
	// directory that containment accepted, `~/.config`, with
	// `gcloud/credentials.db` named under it.
	if v := p.sensitive(raw, where, workdir.UploadDenied); v != nil {
		return "", v
	}

	// Stage 3: containment under one allowed root, then the directory the file
	// lies in may not be a system directory or the home directory itself.
	matchedRoot := p.matchRoot(where, false)
	if matchedRoot == "" {
		return "", p.outside(where, raw)
	}
	if v := p.deniedDir(filepath.Dir(where), where); v != nil {
		return "", v
	}

	// Only now: an upload source must exist. If it resolves anywhere but
	// where it was placed (it changed in between), the judgements above are
	// made again on what it resolves to.
	canonical, err := filepath.EvalSymlinks(filepath.Clean(raw))
	if err != nil {
		return "", &Violation{
			Reason: ReasonNotFound,
			Path:   raw,
			Roots:  p.Roots(),
			Detail: fmt.Sprintf("cannot resolve %q: %v", raw, err),
		}
	}
	if canonical != where {
		if v := p.sensitive(raw, canonical, workdir.UploadDenied); v != nil {
			return "", v
		}
		if matchedRoot = p.matchRoot(canonical, false); matchedRoot == "" {
			return "", p.outside(canonical, raw)
		}
		if v := p.deniedDir(filepath.Dir(canonical), canonical); v != nil {
			return "", v
		}
	}

	// Stage 4: regular file only.
	fi, err := os.Stat(canonical)
	if err != nil {
		return "", &Violation{
			Reason: ReasonNotFound,
			Path:   canonical,
			Roots:  p.Roots(),
			Detail: fmt.Sprintf("cannot stat %q: %v", canonical, err),
		}
	}
	if !fi.Mode().IsRegular() {
		return "", &Violation{
			Reason: ReasonNotRegularFile,
			Path:   canonical,
			Roots:  p.Roots(),
			Detail: fmt.Sprintf("%q is not a regular file (mode %s)", canonical, fi.Mode()),
		}
	}

	// Stage 5: hidden components below the matched root. The prefix up to
	// the root was operator-approved; only the part below it is checked.
	if !p.allowHidden {
		rel, _ := filepath.Rel(matchedRoot, canonical)
		for component := range strings.SplitSeq(rel, string(filepath.Separator)) {
			if strings.HasPrefix(component, ".") {
				return "", &Violation{
					Reason: ReasonHiddenComponent,
					Path:   canonical,
					Roots:  p.Roots(),
					Detail: fmt.Sprintf("path component %q below allowed root is hidden (set allow_hidden to permit)", component),
				}
			}
		}
	}

	// Stage 6: size cap.
	if p.maxSize > 0 && fi.Size() > p.maxSize {
		return "", &Violation{
			Reason: ReasonTooLarge,
			Path:   canonical,
			Roots:  p.Roots(),
			Detail: fmt.Sprintf("file is %d bytes, cap is %d", fi.Size(), p.maxSize),
		}
	}

	return canonical, nil
}

// MaxSize returns the configured size cap in bytes (<= 0 means no cap).
func (p *Policy) MaxSize() int64 {
	return p.maxSize
}

// SanitizeFilename reduces an untrusted filename (agent-supplied, or taken
// from Slack file metadata — attacker-controllable data) to a bare base
// component: no separators, no parent references, no control characters.
// The hidden-component rule is enforced separately by ResolveNewFile.
func SanitizeFilename(name string) (string, error) {
	base := filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if base == "." || base == ".." || base == string(filepath.Separator) || base == "" {
		return "", fmt.Errorf("filename %q reduces to no usable name", name)
	}
	for _, r := range base {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("filename %q contains control characters", name)
		}
	}
	return base, nil
}

// ResolveNewFile validates a destination for a NEW file (the write-side
// mirror of Resolve). destDir may be absolute or relative to workDir;
// it must exist, canonicalize into an allowed root, and — unless
// allow_hidden is set — contribute no hidden component below the matched
// root. filename is sanitized to a base component; the target must not
// already exist. Returns the canonical target path to create.
func (p *Policy) ResolveNewFile(workDir, destDir, filename string) (string, error) {
	if len(p.roots) == 0 {
		return "", &Violation{
			Reason: ReasonNoRoots,
			Path:   destDir,
			Detail: "no root configured for this policy; the root is the work_dir the call named (ADR-0003)",
		}
	}

	base, err := SanitizeFilename(filename)
	if err != nil {
		return "", &Violation{
			Reason: ReasonBadFilename,
			Path:   filename,
			Roots:  p.Roots(),
			Detail: err.Error(),
		}
	}

	raw := destDir
	if !filepath.IsAbs(raw) {
		if workDir == "" {
			return "", &Violation{
				Reason: ReasonNotAbsolute,
				Path:   destDir,
				Roots:  p.Roots(),
				Detail: fmt.Sprintf("relative dest_dir %q requires work_dir", destDir),
			}
		}
		if !filepath.IsAbs(workDir) {
			return "", &Violation{
				Reason: ReasonNotAbsolute,
				Path:   workDir,
				Roots:  p.Roots(),
				Detail: fmt.Sprintf("work_dir %q must be absolute", workDir),
			}
		}
		raw = filepath.Join(workDir, raw)
	}

	// The order is Resolve's: place the destination (workdir.Where), then the
	// credential floor, containment and the directory check on that place,
	// and only then whether it exists and is a directory — so a path that
	// exists and one that does not get the same answer.
	named := filepath.Join(filepath.Clean(raw), base)
	whereDir := workdir.Where(filepath.Clean(raw))
	target := filepath.Join(whereDir, base)

	// The credential floor on the write side. A destination inside a work
	// directory the caller legitimately named can still be a credential or
	// agent-control location — `work_dir = ~/.config` with `dest_dir =
	// gem-agent` — and a file this server drops there is a file something
	// else later reads as its own configuration. The target as named goes
	// with the placed one, so a chain of links through a credential directory
	// is seen.
	if v := p.sensitive(named, target, workdir.DownloadDenied); v != nil {
		return "", v
	}
	matchedRoot := p.matchRoot(whereDir, true)
	if matchedRoot == "" {
		return "", p.outsideDest(whereDir, raw)
	}
	// And the directory it lands in may not be a system directory or the
	// home directory itself, which the Local policy leaves out.
	if v := p.deniedDir(whereDir, target); v != nil {
		return "", v
	}

	// Only now: the destination must exist and be a directory. If it resolves
	// anywhere but where it was placed, the judgements are made again.
	canonicalDir, err := filepath.EvalSymlinks(filepath.Clean(raw))
	if err != nil {
		return "", &Violation{
			Reason: ReasonNotFound,
			Path:   raw,
			Roots:  p.Roots(),
			Detail: fmt.Sprintf("cannot resolve dest_dir %q: %v", raw, err),
		}
	}
	if canonicalDir != whereDir {
		target = filepath.Join(canonicalDir, base)
		if v := p.sensitive(named, target, workdir.DownloadDenied); v != nil {
			return "", v
		}
		if matchedRoot = p.matchRoot(canonicalDir, true); matchedRoot == "" {
			return "", p.outsideDest(canonicalDir, raw)
		}
		if v := p.deniedDir(canonicalDir, target); v != nil {
			return "", v
		}
	}
	fi, err := os.Stat(canonicalDir)
	if err != nil || !fi.IsDir() {
		return "", &Violation{
			Reason: ReasonNotFound,
			Path:   canonicalDir,
			Roots:  p.Roots(),
			Detail: fmt.Sprintf("dest_dir %q is not an existing directory", raw),
		}
	}

	// Hidden components below the matched root, including the new basename.
	if !p.allowHidden {
		rel, _ := filepath.Rel(matchedRoot, target)
		for component := range strings.SplitSeq(rel, string(filepath.Separator)) {
			if strings.HasPrefix(component, ".") {
				return "", &Violation{
					Reason: ReasonHiddenComponent,
					Path:   target,
					Roots:  p.Roots(),
					Detail: fmt.Sprintf("path component %q below allowed root is hidden (set allow_hidden to permit)", component),
				}
			}
		}
	}

	// Never overwrite: Lstat so even a dangling symlink at the target
	// counts as occupied.
	if _, err := os.Lstat(target); err == nil {
		return "", &Violation{
			Reason: ReasonAlreadyExists,
			Path:   target,
			Roots:  p.Roots(),
			Detail: fmt.Sprintf("%q already exists; choose another filename", target),
		}
	}

	return target, nil
}
