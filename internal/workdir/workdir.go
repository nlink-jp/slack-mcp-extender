// Package workdir resolves the caller's work directory for one tool call and
// says which paths a call may never name — this server's adapter onto
// nlink-jp/pathguard/workdir (organization ADR-021; project ADR-0003,
// ADR-0005).
//
// The work directory comes from the tool's work_dir argument, else the
// runtime's hint in the request's _meta; there is no default, because a
// directory the server chose is one the caller can read back only by
// coincidence.
//
// The judgement — which directories may be a work directory, which paths are
// credential or agent-control locations, compared by file identity and by
// folded name — is pathguard's, and there is no copy of it here. An upload
// leaves the machine, so its source is judged by pathguard's Outbound policy
// (a credential name is refused wherever it sits); a download stays here, so
// its target is judged by the Local policy.
package workdir

import (
	"encoding/json"

	"github.com/nlink-jp/pathguard"
	pgwd "github.com/nlink-jp/pathguard/workdir"
)

// MetaKey is the request-level `_meta` key a runtime sets on every tools/call
// to name the session work directory.
const MetaKey = pgwd.MetaKey

// Error codes, shared with the rest of the fleet so a caller sees the same
// vocabulary from every server.
const (
	CodeRequired    = pgwd.CodeRequired
	CodeInvalid     = pgwd.CodeInvalid
	CodeNotFound    = pgwd.CodeNotFound
	CodeNotWritable = pgwd.CodeNotWritable
	CodeDenied      = pgwd.CodeDenied
)

// Error is a refusal carrying the code the caller branches on, and, for
// work_dir_denied, details {work_dir, resolved, reason}.
type Error = pgwd.Error

// requiredHint is this server's sentence for work_dir_required.
const requiredHint = "Uploads are taken from it and downloads land in it."

// resolver builds the judgement for one call. serverDirs are this server's
// own config and state directories — a parameter rather than a package
// variable, because a variable has an initialization order, and a call that
// arrived before it was set would look exactly like a call that was allowed.
// A caller with nothing to deny passes nil, and has to say so; an empty entry
// refuses every call rather than protecting nothing.
func resolver(serverDirs []string) pgwd.Resolver {
	places := make([]pathguard.Place, 0, len(serverDirs))
	for _, d := range serverDirs {
		places = append(places, pathguard.ServerDir(d, "which holds its configuration and its OAuth tokens"))
	}
	return pgwd.NewResolver(pgwd.Options{Protected: places, RequiredHint: requiredHint})
}

// Resolve returns the validated work directory for one call: the tool's
// work_dir argument, else the runtime hint in meta, else an error. The
// returned path is absolute and symlink-resolved.
func Resolve(arg string, meta map[string]json.RawMessage, serverDirs []string) (string, error) {
	return resolver(serverDirs).Resolve(arg, meta)
}

// Validate applies the closed list of checks and returns the resolved path.
func Validate(dir string, serverDirs []string) (string, error) {
	return resolver(serverDirs).Validate(dir)
}

// UploadDenied reports why a file may not be uploaded — it leaves the
// machine, so pathguard's Outbound policy plus this server's directories — or
// "". raw is the path as the caller spelled it and resolved its
// symlink-resolved form.
//
// It is the floor underneath containment in the work directory
// (internal/containment), applied to the file and not only to the directory:
// `~/.config` passes as a work directory, and a file under it such as
// `gcloud/credentials.db` is refused here. Neither stands in for the other.
func UploadDenied(raw, resolved string, serverDirs []string) string {
	_, why := resolver(serverDirs).OutboundPath(raw, resolved)
	return why
}

// DownloadDenied reports why a download may not be written to a path — the
// Local policy plus this server's directories — or "". Pass the same value
// twice for a target that does not exist yet.
func DownloadDenied(raw, resolved string, serverDirs []string) string {
	_, why := resolver(serverDirs).LocalPath(raw, resolved)
	return why
}
