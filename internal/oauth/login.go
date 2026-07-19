// Package oauth implements the interactive OAuth2 authorization_code login
// of slack-mcp-extender. One login per workspace: the resulting user token
// is stored in that workspace's state directory and serves both the
// upstream proxy connection and the injected upload tools.
package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/nlink-jp/slack-mcp-extender/internal/config"
	"github.com/nlink-jp/slack-mcp-extender/internal/transport"
)

// Options tunes Login. The zero value works for interactive use.
type Options struct {
	// Out receives progress messages (default: io.Discard).
	Out io.Writer
	// OpenBrowser opens the authorize URL (default: the OS browser).
	// Tests replace this to drive the callback themselves.
	OpenBrowser func(url string)
	// HTTPClient is used for the token exchange (default: http.DefaultClient).
	HTTPClient *http.Client
	// Timeout bounds the wait for the authorization callback
	// (default: 5 minutes).
	Timeout time.Duration
}

// Login runs the authorization_code flow with PKCE for the workspace
// described by cfg: starts a loopback callback server (TLS when the config
// says https, as Slack requires), opens the browser, exchanges the code,
// and stores the tokens in the workspace state directory.
func Login(cfg *config.Config, opts Options) error {
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	if opts.OpenBrowser == nil {
		opts.OpenBrowser = openBrowser
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = http.DefaultClient
	}
	if opts.Timeout == 0 {
		opts.Timeout = 5 * time.Minute
	}

	oauthCfg := cfg.OAuth

	// The callback server binds first: pre-registered OAuth apps (Slack)
	// require an exact redirect_uri, so the configured port must be free.
	tcpListener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", oauthCfg.CallbackPort))
	if err != nil {
		return fmt.Errorf("start callback server on port %d: %w (adjust oauth.callback_port)", oauthCfg.CallbackPort, err)
	}
	port := tcpListener.Addr().(*net.TCPAddr).Port

	listener := net.Listener(tcpListener)
	host := "127.0.0.1"
	if oauthCfg.CallbackScheme == "https" {
		cert, certErr := generateLoopbackCert()
		if certErr != nil {
			tcpListener.Close()
			return fmt.Errorf("generate self-signed cert for https callback: %w", certErr)
		}
		listener = tls.NewListener(tcpListener, &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		})
		host = "localhost"
		fmt.Fprintln(opts.Out, "Note: the browser will show a \"not secure\" warning for the self-signed TLS callback — clicking through is expected.")
	}
	redirectURI := fmt.Sprintf("%s://%s:%d/callback", oauthCfg.CallbackScheme, host, port)

	// PKCE + CSRF state.
	verifier, err := randomToken(32)
	if err != nil {
		listener.Close()
		return fmt.Errorf("generate PKCE verifier: %w", err)
	}
	challengeSum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeSum[:])
	stateParam, err := randomToken(16)
	if err != nil {
		listener.Close()
		return fmt.Errorf("generate state: %w", err)
	}

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("state")), []byte(stateParam)) != 1 {
			errCh <- fmt.Errorf("state parameter mismatch (possible CSRF)")
			http.Error(w, "State mismatch", http.StatusBadRequest)
			return
		}
		if errMsg := r.URL.Query().Get("error"); errMsg != "" {
			desc := r.URL.Query().Get("error_description")
			errCh <- fmt.Errorf("authorization error: %s: %s", errMsg, desc)
			fmt.Fprintf(w, "<html><body><h2>Authorization failed</h2><p>%s: %s</p><p>You can close this window.</p></body></html>",
				html.EscapeString(errMsg), html.EscapeString(desc))
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			errCh <- fmt.Errorf("no authorization code in callback")
			http.Error(w, "No code", http.StatusBadRequest)
			return
		}
		codeCh <- code
		fmt.Fprint(w, "<html><body><h2>Authorization successful</h2><p>You can close this window and return to the terminal.</p></body></html>")
	})
	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	defer server.Close()

	// Authorization URL. Security-critical params are set last so nothing
	// can override them.
	authBase, err := url.Parse(oauthCfg.AuthorizeURL)
	if err != nil {
		return fmt.Errorf("parse oauth.authorize_url: %w", err)
	}
	params := authBase.Query()
	if len(oauthCfg.Scopes) > 0 {
		// Space-joined "scope" — proven against Slack's v2_user authorize
		// endpoint by the mcp-guardian login flow this mirrors.
		params.Set("scope", strings.Join(oauthCfg.Scopes, " "))
	}
	params.Set("response_type", "code")
	params.Set("client_id", oauthCfg.ClientID)
	params.Set("redirect_uri", redirectURI)
	params.Set("state", stateParam)
	params.Set("code_challenge", challenge)
	params.Set("code_challenge_method", "S256")
	authBase.RawQuery = params.Encode()
	authURL := authBase.String()

	fmt.Fprintf(opts.Out, "Opening browser for authentication...\nIf the browser doesn't open, visit:\n%s\n\n", authURL)
	opts.OpenBrowser(authURL)

	fmt.Fprintln(opts.Out, "Waiting for authorization callback...")
	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		return err
	case <-time.After(opts.Timeout):
		return fmt.Errorf("authorization timed out (%s)", opts.Timeout)
	}

	// Exchange the code for tokens.
	fmt.Fprintln(opts.Out, "Exchanging authorization code for tokens...")
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequest(http.MethodPost, oauthCfg.TokenURL, nil)
	if err != nil {
		return fmt.Errorf("build token exchange request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	transport.ApplyClientAuth(req, form, transport.ClientAuthConfig{
		Method:       oauthCfg.ClientAuthMethod,
		ClientID:     oauthCfg.ClientID,
		ClientSecret: oauthCfg.ResolveClientSecret(),
	})
	encoded := form.Encode()
	req.Body = io.NopCloser(strings.NewReader(encoded))
	req.ContentLength = int64(len(encoded))

	resp, err := opts.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("token exchange request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("token exchange failed (HTTP %d): %s", resp.StatusCode, body)
	}

	tokens, err := transport.ParseTokenResponse(body)
	if err != nil {
		return err
	}
	if err := transport.SaveTokens(cfg.StateDir, tokens); err != nil {
		return fmt.Errorf("save tokens: %w", err)
	}

	fmt.Fprintf(opts.Out, "Login successful. Tokens saved to %s/tokens.json\n", cfg.StateDir)
	if tokens.RefreshToken != "" {
		fmt.Fprintln(opts.Out, "Refresh token stored — access tokens will be renewed automatically.")
	} else {
		fmt.Fprintln(opts.Out, "No refresh token received (non-rotating token) — login again only if Slack revokes it.")
	}
	return nil
}

// randomToken returns n random bytes, base64url-encoded.
func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// openBrowser opens a URL in the default browser.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return
	}
	_ = cmd.Start()
}
