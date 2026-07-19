package transfer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// AuditEntry is one line of the egress audit log: when / which file / to
// which channel (thread) / with what outcome. This is the minimal record
// the threat model calls for — the tool sends local files to an external
// service, so every attempt is written down, including denied ones.
type AuditEntry struct {
	Time      string `json:"time"` // RFC3339 UTC
	Tool      string `json:"tool"`
	Path      string `json:"path,omitempty"`
	Size      int64  `json:"size,omitempty"`
	ChannelID string `json:"channel_id,omitempty"`
	ThreadTS  string `json:"thread_ts,omitempty"`
	FileID    string `json:"file_id,omitempty"`
	Outcome   string `json:"outcome"` // "ok", "denied", or "error"
	Error     string `json:"error,omitempty"`
}

// AuditLog appends JSONL entries to a file. The zero value (empty path)
// discards entries.
type AuditLog struct {
	Path string

	mu sync.Mutex
}

// Append writes one entry, stamping the time. Failures are returned but
// callers may treat them as non-fatal — an audit write must not turn a
// completed upload into a reported failure.
func (a *AuditLog) Append(entry AuditEntry) error {
	if a == nil || a.Path == "" {
		return nil
	}
	entry.Time = time.Now().UTC().Format(time.RFC3339)

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal audit entry: %w", err)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(a.Path), 0o700); err != nil {
		return fmt.Errorf("audit dir: %w", err)
	}
	f, err := os.OpenFile(a.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open audit log: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write audit log: %w", err)
	}
	return nil
}
