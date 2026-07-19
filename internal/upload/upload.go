// Package upload implements the Slack external upload 3-step used by the
// injected tools: files.getUploadURLExternal → POST the bytes →
// files.completeUploadExternal. Passing channel_id (plus initial_comment /
// thread_ts) to the complete call folds upload and share into one
// operation, so no orphaned files are ever created — channel is required
// by design.
//
// The uploader authenticates with the same TokenProvider as the proxy
// connection: one user token for everything.
package upload

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
)

// DefaultAPIBase is the Slack Web API root.
const DefaultAPIBase = "https://slack.com/api"

// TokenProvider is the minimal token interface the uploader needs
// (satisfied by transport.TokenProvider).
type TokenProvider interface {
	Token() (string, error)
	Invalidate()
}

// SlackError is a Slack Web API failure ({"ok":false,"error":...}),
// carrying which method failed for the structured tool error.
type SlackError struct {
	Method string // e.g. "files.completeUploadExternal"
	Reason string // e.g. "not_in_channel"
}

func (e *SlackError) Error() string {
	return fmt.Sprintf("%s: %s", e.Method, e.Reason)
}

// Uploader performs external uploads against the Slack Web API.
type Uploader struct {
	APIBase string       // "" means DefaultAPIBase
	Client  *http.Client // nil means http.DefaultClient
	Tokens  TokenProvider
}

// Request describes one upload. Path must already have passed containment —
// this package never applies policy, it only executes.
type Request struct {
	Path      string // canonical path of the file to upload
	Filename  string // display name; "" means basename of Path
	ChannelID string // required: the share target
	Comment   string // optional initial_comment
	ThreadTS  string // optional: share as a reply to this thread
}

// Result is the successful outcome.
type Result struct {
	FileID    string `json:"file_id"`
	Filename  string `json:"filename"`
	Size      int64  `json:"size"`
	ChannelID string `json:"channel_id"`
	ThreadTS  string `json:"thread_ts,omitempty"`
}

// Upload runs the 3-step flow.
func (u *Uploader) Upload(req Request) (*Result, error) {
	if req.ChannelID == "" {
		return nil, fmt.Errorf("channel_id is required")
	}
	if req.Path == "" {
		return nil, fmt.Errorf("path is required")
	}
	filename := req.Filename
	if filename == "" {
		filename = filepath.Base(req.Path)
	}

	fi, err := os.Stat(req.Path)
	if err != nil {
		return nil, fmt.Errorf("stat file: %w", err)
	}

	// Step 1: obtain an upload URL.
	var urlResp struct {
		UploadURL string `json:"upload_url"`
		FileID    string `json:"file_id"`
	}
	params := url.Values{
		"filename": {filename},
		"length":   {fmt.Sprintf("%d", fi.Size())},
	}
	if err := u.apiCall(http.MethodGet, "files.getUploadURLExternal", params, nil, &urlResp); err != nil {
		return nil, err
	}
	if urlResp.UploadURL == "" || urlResp.FileID == "" {
		return nil, fmt.Errorf("files.getUploadURLExternal: response missing upload_url/file_id")
	}

	// Step 2: send the bytes to the upload URL (no Authorization header —
	// the URL itself is the credential).
	f, err := os.Open(req.Path)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	uploadReq, err := http.NewRequest(http.MethodPost, urlResp.UploadURL, f)
	if err != nil {
		return nil, fmt.Errorf("build upload request: %w", err)
	}
	uploadReq.Header.Set("Content-Type", "application/octet-stream")
	uploadReq.ContentLength = fi.Size()
	resp, err := u.client().Do(uploadReq)
	if err != nil {
		return nil, fmt.Errorf("upload bytes: %w", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upload bytes: HTTP %d: %s", resp.StatusCode, string(body))
	}

	// Step 3: complete the upload, sharing into the channel (and thread).
	complete := map[string]any{
		"files":      []map[string]string{{"id": urlResp.FileID}},
		"channel_id": req.ChannelID,
	}
	if req.Comment != "" {
		complete["initial_comment"] = req.Comment
	}
	if req.ThreadTS != "" {
		complete["thread_ts"] = req.ThreadTS
	}
	if err := u.apiCall(http.MethodPost, "files.completeUploadExternal", nil, complete, nil); err != nil {
		return nil, err
	}

	return &Result{
		FileID:    urlResp.FileID,
		Filename:  filename,
		Size:      fi.Size(),
		ChannelID: req.ChannelID,
		ThreadTS:  req.ThreadTS,
	}, nil
}

// apiCall performs an authenticated Slack Web API call and decodes the
// {"ok":...} envelope into out (which may be nil). Auth-revocation errors
// invalidate the token and retry once — Slack signals these inside a
// HTTP 200 envelope, not as HTTP 401.
func (u *Uploader) apiCall(method, apiMethod string, query url.Values, jsonBody any, out any) error {
	retried := false
	for {
		err := u.apiCallOnce(method, apiMethod, query, jsonBody, out)
		var slackErr *SlackError
		if err != nil && !retried && errors.As(err, &slackErr) && isAuthError(slackErr.Reason) && u.Tokens != nil {
			u.Tokens.Invalidate()
			retried = true
			continue
		}
		return err
	}
}

func (u *Uploader) apiCallOnce(method, apiMethod string, query url.Values, jsonBody any, out any) error {
	endpoint := u.apiBase() + "/" + apiMethod
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}

	var bodyReader io.Reader
	contentType := ""
	if jsonBody != nil {
		data, err := json.Marshal(jsonBody)
		if err != nil {
			return fmt.Errorf("%s: marshal body: %w", apiMethod, err)
		}
		bodyReader = bytes.NewReader(data)
		contentType = "application/json; charset=utf-8"
	}

	req, err := http.NewRequest(method, endpoint, bodyReader)
	if err != nil {
		return fmt.Errorf("%s: build request: %w", apiMethod, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if u.Tokens != nil {
		token, err := u.Tokens.Token()
		if err != nil {
			return fmt.Errorf("%s: obtain token: %w", apiMethod, err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := u.client().Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", apiMethod, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("%s: read response: %w", apiMethod, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d: %s", apiMethod, resp.StatusCode, truncate(body, 200))
	}

	var envelope struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("%s: parse response: %w", apiMethod, err)
	}
	if !envelope.OK {
		return &SlackError{Method: apiMethod, Reason: envelope.Error}
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("%s: parse response fields: %w", apiMethod, err)
		}
	}
	return nil
}

func (u *Uploader) apiBase() string {
	if u.APIBase != "" {
		return u.APIBase
	}
	return DefaultAPIBase
}

func (u *Uploader) client() *http.Client {
	if u.Client != nil {
		return u.Client
	}
	return http.DefaultClient
}

// isAuthError reports whether a Slack error string means the token itself
// is no longer valid (retry-once-after-invalidate territory).
func isAuthError(reason string) bool {
	switch reason {
	case "invalid_auth", "token_revoked", "token_expired", "not_authed":
		return true
	}
	return false
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
