package transfer

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
)

// ErrTooLarge reports that a download exceeded the size cap while
// streaming — the declared size cannot be trusted, so the limit is
// enforced on the wire as well (ADR-0002).
var ErrTooLarge = errors.New("file exceeds the configured size cap")

// FileInfo is the metadata needed to download one Slack file.
type FileInfo struct {
	ID   string
	Name string // Slack-supplied, attacker-controllable — sanitize before use
	Size int64
	// DownloadURL is url_private_download; the GET must carry the Bearer
	// token.
	DownloadURL string
}

// Info fetches file metadata via files.info.
func (u *Client) Info(fileID string) (*FileInfo, error) {
	if fileID == "" {
		return nil, fmt.Errorf("file_id is required")
	}
	var resp struct {
		File struct {
			ID                 string `json:"id"`
			Name               string `json:"name"`
			Size               int64  `json:"size"`
			URLPrivateDownload string `json:"url_private_download"`
			URLPrivate         string `json:"url_private"`
		} `json:"file"`
	}
	if err := u.apiCall(http.MethodGet, "files.info", url.Values{"file": {fileID}}, nil, &resp); err != nil {
		return nil, err
	}
	downloadURL := resp.File.URLPrivateDownload
	if downloadURL == "" {
		downloadURL = resp.File.URLPrivate
	}
	if downloadURL == "" {
		return nil, fmt.Errorf("files.info: no download URL for %s (external or deleted file?)", fileID)
	}
	return &FileInfo{
		ID:          resp.File.ID,
		Name:        resp.File.Name,
		Size:        resp.File.Size,
		DownloadURL: downloadURL,
	}, nil
}

// FetchTo streams the file behind info into target (which must already
// have passed write-side containment). The write is atomic (tmp+rename)
// and hard-limited to maxSize bytes when maxSize > 0 — a lying size field
// cannot bypass the cap. On any failure no partial file remains.
func (u *Client) FetchTo(info *FileInfo, target string, maxSize int64) (int64, error) {
	req, err := http.NewRequest(http.MethodGet, info.DownloadURL, nil)
	if err != nil {
		return 0, fmt.Errorf("build download request: %w", err)
	}
	if u.Tokens != nil {
		token, err := u.Tokens.Token()
		if err != nil {
			return 0, fmt.Errorf("obtain token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := u.client().Do(req)
	if err != nil {
		return 0, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return 0, fmt.Errorf("download: HTTP %d: %s", resp.StatusCode, truncate(body, 200))
	}

	tmp, err := os.CreateTemp(filepath.Dir(target), ".smx-download-*")
	if err != nil {
		return 0, fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpPath)
	}

	reader := io.Reader(resp.Body)
	if maxSize > 0 {
		reader = io.LimitReader(resp.Body, maxSize+1)
	}
	written, err := io.Copy(tmp, reader)
	if err != nil {
		cleanup()
		return 0, fmt.Errorf("write download: %w", err)
	}
	if maxSize > 0 && written > maxSize {
		cleanup()
		return 0, fmt.Errorf("%w (cap %d bytes)", ErrTooLarge, maxSize)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return 0, fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		os.Remove(tmpPath)
		return 0, fmt.Errorf("chmod: %w", err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		os.Remove(tmpPath)
		return 0, fmt.Errorf("finalize download: %w", err)
	}
	return written, nil
}
