// Package config loads and validates the per-workspace configuration of
// slack-mcp-extender. Slack user tokens are workspace-scoped, so one config
// file describes exactly one workspace: its upstream, its OAuth client, its
// containment policy, and its state directory. Nothing in a config is ever
// shared implicitly between workspaces.
//
// The config file may carry an OAuth client secret, so Load enforces owner-
// only permissions (0600) and decoding is strict: unknown fields are errors,
// never silently ignored.
package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// Defaults applied by Load when the field is absent.
const (
	DefaultMaxFileSize = 50 * 1024 * 1024 // 50 MB
	DefaultTimeoutMs   = 120000           // upstream request timeout
	DefaultUpstreamURL = "https://mcp.slack.com/mcp"
)

// Config is one workspace's configuration.
type Config struct {
	Upstream Upstream `json:"upstream"`
	OAuth    OAuth    `json:"oauth"`

	// AllowedRoots is the containment boundary for the injected upload
	// tools. Empty means deny-by-default: no file access at all.
	AllowedRoots []string `json:"allowed_roots,omitempty"`
	AllowHidden  bool     `json:"allow_hidden,omitempty"`
	MaxFileSize  int64    `json:"max_file_size,omitempty"`

	// StateDir holds tokens.json and the audit log. Defaults to a
	// "<config-basename>.state" directory next to the config file, which
	// keeps state per-workspace automatically.
	StateDir  string `json:"state_dir,omitempty"`
	TimeoutMs int    `json:"timeout_ms,omitempty"`

	// Path is where this config was loaded from (not part of the file).
	Path string `json:"-"`
}

// Upstream identifies the MCP server being proxied.
type Upstream struct {
	URL string `json:"url"`
}

// OAuth configures the pre-registered OAuth2 client used for both the
// upstream proxy connection and the injected upload tools (single token).
type OAuth struct {
	AuthorizeURL string `json:"authorize_url"`
	TokenURL     string `json:"token_url"`
	ClientID     string `json:"client_id"`
	// ClientSecret is the literal secret; ClientSecretEnv names an
	// environment variable holding it. Set at most one — env is the
	// recommended form so the config file itself stays secret-free.
	ClientSecret     string   `json:"client_secret,omitempty"`
	ClientSecretEnv  string   `json:"client_secret_env,omitempty"`
	Scopes           []string `json:"scopes"`
	CallbackPort     int      `json:"callback_port,omitempty"`
	CallbackScheme   string   `json:"callback_scheme,omitempty"`   // "https" (default) or "http"
	ClientAuthMethod string   `json:"client_auth_method,omitempty"` // "post" (default), "basic", or "none"
}

// ResolveClientSecret returns the client secret from the literal field or
// the named environment variable.
func (o *OAuth) ResolveClientSecret() string {
	if o.ClientSecret != "" {
		return o.ClientSecret
	}
	if o.ClientSecretEnv != "" {
		return os.Getenv(o.ClientSecretEnv)
	}
	return ""
}

// Load reads, strictly decodes, defaults, and validates a config file.
func Load(path string) (*Config, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("config %q: %w", path, err)
	}
	// The file may carry an OAuth client secret: require owner-only
	// permissions, same as ssh keys. (Windows has no POSIX mode bits.)
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("config %q is group/world accessible (mode %o); run: chmod 600 %s", path, fi.Mode().Perm(), path)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("config %q: %w", path, err)
	}
	defer f.Close()

	var cfg Config
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("config %q: %w", path, err)
	}
	// Reject trailing content after the config object.
	if dec.More() {
		return nil, fmt.Errorf("config %q: trailing data after config object", path)
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("config %q: %w", path, err)
	}
	cfg.Path = abs
	cfg.applyDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config %q: %w", path, err)
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Upstream.URL == "" {
		c.Upstream.URL = DefaultUpstreamURL
	}
	if c.MaxFileSize == 0 {
		c.MaxFileSize = DefaultMaxFileSize
	}
	if c.TimeoutMs == 0 {
		c.TimeoutMs = DefaultTimeoutMs
	}
	if c.OAuth.CallbackScheme == "" {
		c.OAuth.CallbackScheme = "https"
	}
	if c.StateDir == "" && c.Path != "" {
		base := strings.TrimSuffix(filepath.Base(c.Path), filepath.Ext(c.Path))
		c.StateDir = filepath.Join(filepath.Dir(c.Path), base+".state")
	}
}

// Validate checks the config for structural errors. It does not touch the
// filesystem: allowed_roots existence is checked when the containment
// policy is built at server start.
func (c *Config) Validate() error {
	u, err := url.Parse(c.Upstream.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("upstream.url %q is not a valid http(s) URL", c.Upstream.URL)
	}

	if c.OAuth.AuthorizeURL == "" || c.OAuth.TokenURL == "" || c.OAuth.ClientID == "" {
		return fmt.Errorf("oauth.authorize_url, oauth.token_url, and oauth.client_id are required")
	}
	if c.OAuth.ClientSecret != "" && c.OAuth.ClientSecretEnv != "" {
		return fmt.Errorf("oauth.client_secret and oauth.client_secret_env are mutually exclusive")
	}
	if len(c.OAuth.Scopes) == 0 {
		return fmt.Errorf("oauth.scopes must not be empty")
	}
	switch c.OAuth.CallbackScheme {
	case "http", "https":
	default:
		return fmt.Errorf("oauth.callback_scheme %q must be \"http\" or \"https\"", c.OAuth.CallbackScheme)
	}
	switch c.OAuth.ClientAuthMethod {
	case "", "post", "basic", "none":
	default:
		return fmt.Errorf("oauth.client_auth_method %q must be \"post\", \"basic\", or \"none\"", c.OAuth.ClientAuthMethod)
	}

	for _, root := range c.AllowedRoots {
		if !filepath.IsAbs(root) {
			return fmt.Errorf("allowed_roots entry %q must be absolute", root)
		}
	}
	if c.MaxFileSize < 0 {
		return fmt.Errorf("max_file_size must not be negative")
	}
	if c.TimeoutMs < 0 {
		return fmt.Errorf("timeout_ms must not be negative")
	}
	return nil
}

// Warnings returns non-fatal advisories for `config validate` output.
func (c *Config) Warnings() []string {
	var w []string
	if !slices.Contains(c.OAuth.Scopes, "files:write") {
		w = append(w, "oauth.scopes does not include \"files:write\" — the injected upload tools will fail")
	}
	if len(c.AllowedRoots) == 0 {
		w = append(w, "allowed_roots is empty — file access is denied by default; uploads will be rejected until roots are registered")
	}
	if c.OAuth.ClientSecret != "" {
		w = append(w, "oauth.client_secret is stored literally in the config file — prefer client_secret_env")
	}
	return w
}

// Redacted returns a copy safe for display: the literal client secret is
// masked. Used by `config show`.
func (c *Config) Redacted() Config {
	out := *c
	if out.OAuth.ClientSecret != "" {
		out.OAuth.ClientSecret = "[redacted]"
	}
	return out
}
