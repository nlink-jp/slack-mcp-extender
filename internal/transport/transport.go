// Package transport provides the upstream MCP channel (Streamable HTTP /
// SSE) and the OAuth2 token machinery shared by the proxy connection and
// the injected upload tools — one user token, one OAuth session, used for
// both. Modeled on the mcp-guardian transport layer, reduced to what this
// tool needs.
package transport

// Transport is a bidirectional JSON-RPC message channel to the upstream.
type Transport interface {
	// Send writes a JSON-RPC message to the upstream.
	Send(data []byte) error

	// ReadLine returns the next JSON-RPC message from the upstream.
	// The second value is false when the channel is closed.
	ReadLine() ([]byte, bool)

	// Close shuts down the transport.
	Close() error
}

// TokenProvider supplies Bearer tokens for authenticated HTTP requests.
type TokenProvider interface {
	// Token returns a valid access token. Implementations cache and
	// refresh internally.
	Token() (string, error)

	// Invalidate marks the current token as expired, forcing the next
	// Token() call to fetch a fresh one (called on HTTP 401).
	Invalidate()
}
