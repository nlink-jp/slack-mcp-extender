package transport

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const tokensFile = "tokens.json"

// StoredTokens holds OAuth2 tokens persisted to the per-workspace state
// directory.
type StoredTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	// ExpiresAt is a unix timestamp; 0 means "no known expiry" — Slack
	// without token rotation issues non-expiring tokens with neither
	// refresh_token nor expires_in, and login records 0 for them.
	ExpiresAt int64 `json:"expires_at"`
}

// StoredTokenConfig configures the stored token provider.
type StoredTokenConfig struct {
	StateDir         string
	TokenURL         string
	ClientID         string
	ClientSecret     string
	ClientAuthMethod string
	// HTTPClient overrides the client used for refresh (tests). Nil
	// means http.DefaultClient.
	HTTPClient *http.Client
}

// storedTokenProvider implements TokenProvider from tokens on disk,
// refreshing via the refresh token when one exists.
type storedTokenProvider struct {
	cfg StoredTokenConfig

	mu     sync.Mutex
	tokens *StoredTokens
}

// NewStoredTokenProvider reads tokens from stateDir/tokens.json. Returns an
// error when no tokens are stored yet (run `login` first).
func NewStoredTokenProvider(cfg StoredTokenConfig) (TokenProvider, error) {
	tokens, err := LoadTokens(cfg.StateDir)
	if err != nil {
		return nil, fmt.Errorf("no stored tokens (run `slack-mcp-extender login` first): %w", err)
	}
	return &storedTokenProvider{cfg: cfg, tokens: tokens}, nil
}

func (p *storedTokenProvider) Token() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Without a refresh token there is no way to renew, so a stored
	// expiry is unactionable: return the access token as-is and let a
	// genuine revocation surface as an upstream 401.
	if p.tokens.RefreshToken == "" {
		if p.tokens.AccessToken == "" {
			return "", fmt.Errorf("no access token available (run `slack-mcp-extender login`)")
		}
		return p.tokens.AccessToken, nil
	}

	// Cached token still valid (30s safety margin)?
	if p.tokens.AccessToken != "" && time.Now().Add(30*time.Second).Before(time.Unix(p.tokens.ExpiresAt, 0)) {
		return p.tokens.AccessToken, nil
	}

	refreshed, err := refreshTokens(p.cfg, p.tokens.RefreshToken)
	if err != nil {
		return "", fmt.Errorf("token refresh failed: %w", err)
	}
	// Providers may omit the refresh token on renewal — keep the old one.
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = p.tokens.RefreshToken
	}
	p.tokens = refreshed

	if err := SaveTokens(p.cfg.StateDir, refreshed); err != nil {
		// Non-fatal: the in-memory token works for this session.
		fmt.Fprintf(os.Stderr, "slack-mcp-extender: warning: failed to save refreshed tokens: %v\n", err)
	}
	return p.tokens.AccessToken, nil
}

func (p *storedTokenProvider) Invalidate() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tokens.AccessToken = ""
	p.tokens.ExpiresAt = 0
}

// refreshTokens exchanges a refresh token for new tokens.
func refreshTokens(cfg StoredTokenConfig, refreshToken string) (*StoredTokens, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	}
	req, err := http.NewRequest(http.MethodPost, cfg.TokenURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ApplyClientAuth(req, form, ClientAuthConfig{
		Method:       cfg.ClientAuthMethod,
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
	})
	encoded := form.Encode()
	req.Body = io.NopCloser(strings.NewReader(encoded))
	req.ContentLength = int64(len(encoded))

	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read refresh response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh failed (HTTP %d): %s", resp.StatusCode, truncateBytes(body, 200))
	}

	tokens, err := ParseTokenResponse(body)
	if err != nil {
		return nil, err
	}
	return tokens, nil
}

// ParseTokenResponse decodes an OAuth2 token endpoint response into
// StoredTokens, applying the expiry policy shared by login and refresh:
//
//   - expires_in present                  → honour it
//   - no expires_in but a refresh_token  → probe again in 1 hour
//   - neither                            → non-expiring token; store 0
func ParseTokenResponse(body []byte) (*StoredTokens, error) {
	var tokenResp struct {
		OK           *bool  `json:"ok"` // Slack-style envelope, absent elsewhere
		Error        string `json:"error"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}
	// Slack returns HTTP 200 with {"ok":false,"error":"..."} on failure.
	if tokenResp.OK != nil && !*tokenResp.OK {
		return nil, fmt.Errorf("token endpoint error: %s", tokenResp.Error)
	}
	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("empty access_token in token response")
	}

	var expiresAt int64
	switch {
	case tokenResp.ExpiresIn > 0:
		expiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Unix()
	case tokenResp.RefreshToken != "":
		expiresAt = time.Now().Add(1 * time.Hour).Unix()
	}
	return &StoredTokens{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		TokenType:    tokenResp.TokenType,
		ExpiresAt:    expiresAt,
	}, nil
}

// LoadTokens reads stored tokens from stateDir/tokens.json.
func LoadTokens(stateDir string) (*StoredTokens, error) {
	data, err := os.ReadFile(filepath.Join(stateDir, tokensFile))
	if err != nil {
		return nil, err
	}
	var tokens StoredTokens
	if err := json.Unmarshal(data, &tokens); err != nil {
		return nil, err
	}
	return &tokens, nil
}

// SaveTokens writes tokens to stateDir/tokens.json atomically (0600).
func SaveTokens(stateDir string, tokens *StoredTokens) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(tokens, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(stateDir, tokensFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
