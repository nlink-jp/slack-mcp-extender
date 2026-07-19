package transport

import (
	"net/http"
	"net/url"
)

// ClientAuthConfig selects how OAuth2 client credentials are sent to a
// token endpoint (RFC 6749 §2.3.1 / OIDC Core §9):
//
//   - "" / "post": client_id and client_secret as form body params (the
//     most widely accepted method, and what Slack expects — the default).
//   - "basic":     HTTP Basic auth.
//   - "none":      public clients; client_id in the form, never a secret.
type ClientAuthConfig struct {
	Method       string
	ClientID     string
	ClientSecret string
}

// ApplyClientAuth populates req/form with client credentials according to
// cfg.Method. form is mutated for "post" and "none"; for "basic" the
// credentials go into the Authorization header only (they must not appear
// twice). Call this BEFORE writing form into req.Body.
func ApplyClientAuth(req *http.Request, form url.Values, cfg ClientAuthConfig) {
	switch cfg.Method {
	case "basic":
		req.SetBasicAuth(cfg.ClientID, cfg.ClientSecret)
	case "none":
		form.Set("client_id", cfg.ClientID)
	default: // "" or "post"
		form.Set("client_id", cfg.ClientID)
		if cfg.ClientSecret != "" {
			form.Set("client_secret", cfg.ClientSecret)
		}
	}
}
