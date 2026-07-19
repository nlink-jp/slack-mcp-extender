// Package containment implements the file-access policy of
// slack-mcp-extender. The tool relays untrusted Slack content, reads local
// files, and sends data to an external service — an exfiltration primitive
// unless confined — so every file argument passes through this package
// before it is opened.
//
// The policy is defined ONLY by the operator's config (allowed roots, hidden
// opt-out, size cap). Tool arguments — including workspace_dir — are
// untrusted inputs that must resolve inside the policy; they never widen it.
//
// Check order (do not reorder; each stage assumes the previous ones):
//
//  1. canonicalize   Abs + Clean + EvalSymlinks — all later checks run on
//     the real path, so `..` tricks and symlink disguises are resolved away
//  2. containment    the canonical path must be under one allowed root
//     (deny-by-default: no roots configured → nothing is allowed)
//  3. regular file   directories, devices, sockets, and anything else that
//     is not a plain file are rejected
//  4. hidden check   no path component below the matched root may start
//     with "." (`.git`, `.env`, `.ssh`, …) unless allow_hidden is set; the
//     root itself may live under a dot directory — that prefix was
//     explicitly operator-approved
//  5. size cap       the file must not exceed the configured maximum
package containment

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Violation reason codes, carried in Violation.Reason and surfaced to the
// MCP client inside a structured path_denied error.
const (
	ReasonNoRoots         = "no_allowed_roots"
	ReasonNotAbsolute     = "not_absolute"
	ReasonNotFound        = "not_found"
	ReasonOutsideRoots    = "outside_allowed_roots"
	ReasonNotRegularFile  = "not_regular_file"
	ReasonHiddenComponent = "hidden_component"
	ReasonTooLarge        = "too_large"
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
	allowHidden bool
	maxSize     int64 // bytes; <= 0 means no cap
}

// NewPolicy canonicalizes the allowed roots and returns a Policy. Every root
// must exist and be a directory: a root that cannot be canonicalized cannot
// be enforced, so it is a configuration error, not a silent skip.
// An empty roots list is valid and yields a deny-everything policy.
func NewPolicy(roots []string, allowHidden bool, maxSize int64) (*Policy, error) {
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
	return &Policy{roots: canonical, allowHidden: allowHidden, maxSize: maxSize}, nil
}

// Roots returns the canonical allowed roots (for error details and logs).
func (p *Policy) Roots() []string {
	out := make([]string, len(p.roots))
	copy(out, p.roots)
	return out
}

// Resolve validates a file argument against the policy and returns the
// canonical path to open. file may be absolute, or relative to workspaceDir
// (which must then be absolute — it is an untrusted tool argument and gets
// no default). Any violation is returned as *Violation.
func (p *Policy) Resolve(workspaceDir, file string) (string, error) {
	if len(p.roots) == 0 {
		return "", &Violation{
			Reason: ReasonNoRoots,
			Path:   file,
			Detail: "no allowed_roots configured; file access is denied by default (register roots via the operator config)",
		}
	}

	// Assemble the raw path from the untrusted arguments.
	raw := file
	if !filepath.IsAbs(raw) {
		if workspaceDir == "" {
			return "", &Violation{
				Reason: ReasonNotAbsolute,
				Path:   file,
				Roots:  p.Roots(),
				Detail: fmt.Sprintf("relative file %q requires workspace_dir", file),
			}
		}
		if !filepath.IsAbs(workspaceDir) {
			return "", &Violation{
				Reason: ReasonNotAbsolute,
				Path:   workspaceDir,
				Roots:  p.Roots(),
				Detail: fmt.Sprintf("workspace_dir %q must be absolute", workspaceDir),
			}
		}
		raw = filepath.Join(workspaceDir, raw)
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

	// Stage 2: containment under one allowed root.
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

	// Stage 3: regular file only.
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

	// Stage 4: hidden components below the matched root. The prefix up to
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

	// Stage 5: size cap.
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
