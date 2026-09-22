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
// Check order (do not reorder; each stage assumes the previous ones):
//
//  1. canonicalize   Abs + Clean + EvalSymlinks — all later checks run on
//     the real path, so `..` tricks and symlink disguises are resolved away
//  2. credential floor  the real path may not lie in a credential or
//     agent-control location, nor in this server's own directories
//     (internal/workdir's list, organization ADR-021 §7). It runs on the
//     file the call names, because an accepted work directory does not make
//     its contents safe to send: the list is a floor, and a floor applied
//     only to the directory argument is stepped over by naming a file under
//     an accepted parent
//  3. containment    the canonical path must be under one allowed root
//     (deny-by-default: no roots configured → nothing is allowed)
//  4. regular file   directories, devices, sockets, and anything else that
//     is not a plain file are rejected
//  5. hidden check   no path component below the matched root may start
//     with "." (`.git`, `.env`, `.ssh`, …) unless allow_hidden is set; the
//     root itself may live under a dot directory — that prefix was
//     explicitly operator-approved
//  6. size cap       the file must not exceed the configured maximum
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
// The wording of the reason comes from internal/workdir, so a caller reads the
// same sentence whether the refused path arrived as work_dir or as file.
func (p *Policy) sensitive(raw, resolved string, deny func(raw, resolved string, serverDirs []string) string) *Violation {
	why := deny(raw, resolved, p.serverDirs)
	if why == "" {
		return nil
	}
	return &Violation{
		Reason: ReasonSensitivePath,
		Path:   resolved,
		Roots:  p.Roots(),
		Detail: fmt.Sprintf("%q is refused: %s", resolved, why),
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

	// Stage 1: canonicalize. EvalSymlinks requires the path to exist —
	// an upload source must exist anyway, so absence is a violation here.
	canonical, err := filepath.EvalSymlinks(filepath.Clean(raw))
	if err != nil {
		return "", &Violation{
			Reason: ReasonNotFound,
			Path:   raw,
			Roots:  p.Roots(),
			Detail: fmt.Sprintf("cannot resolve %q: %v", raw, err),
		}
	}

	// Stage 2: the credential floor, on the real path. It runs before
	// containment so the refusal names the actual problem — a link out of the
	// work directory into ~/.ssh is refused as a credential file, not merely
	// as an uncontained one — and because the floor does not depend on
	// containment to hold: the hole this stage closes was a work directory
	// that containment accepted, `~/.config`, with `gcloud/credentials.db`
	// named under it.
	if v := p.sensitive(raw, canonical, workdir.UploadDenied); v != nil {
		return "", v
	}

	// Stage 3: containment under one allowed root.
	matchedRoot := ""
	for _, root := range p.roots {
		if rel, err := filepath.Rel(root, canonical); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "." {
			matchedRoot = root
			break
		}
	}
	if matchedRoot == "" {
		return "", &Violation{
			Reason: ReasonOutsideRoots,
			Path:   canonical,
			Roots:  p.Roots(),
			Detail: fmt.Sprintf("%q resolves outside every allowed root", raw),
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

	// The parent directory must exist so it can be canonicalized — all
	// later checks run on the real path.
	canonicalDir, err := filepath.EvalSymlinks(filepath.Clean(raw))
	if err != nil {
		return "", &Violation{
			Reason: ReasonNotFound,
			Path:   raw,
			Roots:  p.Roots(),
			Detail: fmt.Sprintf("cannot resolve dest_dir %q: %v", raw, err),
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

	matchedRoot := ""
	for _, root := range p.roots {
		if rel, err := filepath.Rel(root, canonicalDir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			matchedRoot = root
			break
		}
	}
	if matchedRoot == "" {
		return "", &Violation{
			Reason: ReasonOutsideRoots,
			Path:   canonicalDir,
			Roots:  p.Roots(),
			Detail: fmt.Sprintf("dest_dir %q resolves outside every allowed root", raw),
		}
	}

	target := filepath.Join(canonicalDir, base)

	// The credential floor on the write side. A destination inside a work
	// directory the caller legitimately named can still be a credential or
	// agent-control location — `work_dir = ~/.config` with `dest_dir =
	// gem-agent` — and a file this server drops there is a file something
	// else later reads as its own configuration. canonicalDir is already
	// symlink-resolved and base is a single sanitized component, so the
	// target needs no second spelling.
	if v := p.sensitive(target, target, workdir.DownloadDenied); v != nil {
		return "", v
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
