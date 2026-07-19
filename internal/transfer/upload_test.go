package transfer

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

type fakeTokens struct {
	token       string
	invalidated atomic.Int32
}

func (f *fakeTokens) Token() (string, error) { return f.token, nil }
func (f *fakeTokens) Invalidate()            { f.invalidated.Add(1) }

// fakeSlack is an httptest server implementing the three upload endpoints.
type fakeSlack struct {
	srv *httptest.Server

	getURLResponses  []string // consumed in order; last repeats
	completeResponse string
	uploadStatus     int

	uploadedBytes  atomic.Pointer[[]byte]
	completeBody   atomic.Pointer[[]byte]
	getURLQuery    atomic.Pointer[string]
	authHeaders    []string
	getURLCalls    atomic.Int32
	completeCalls  atomic.Int32
	uploadCalls    atomic.Int32
}

func newFakeSlack(t *testing.T) *fakeSlack {
	f := &fakeSlack{uploadStatus: http.StatusOK, completeResponse: `{"ok":true,"files":[{"id":"F123"}]}`}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/files.getUploadURLExternal", func(w http.ResponseWriter, r *http.Request) {
		n := int(f.getURLCalls.Add(1)) - 1
		q := r.URL.RawQuery
		f.getURLQuery.Store(&q)
		f.authHeaders = append(f.authHeaders, r.Header.Get("Authorization"))
		if n >= len(f.getURLResponses) {
			n = len(f.getURLResponses) - 1
		}
		resp := strings.ReplaceAll(f.getURLResponses[n], "UPLOAD_URL", f.srv.URL+"/upload/abc")
		fmt.Fprint(w, resp)
	})
	mux.HandleFunc("/upload/abc", func(w http.ResponseWriter, r *http.Request) {
		f.uploadCalls.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Error("Authorization header sent to upload URL")
		}
		body, _ := io.ReadAll(r.Body)
		f.uploadedBytes.Store(&body)
		w.WriteHeader(f.uploadStatus)
	})
	mux.HandleFunc("/api/files.completeUploadExternal", func(w http.ResponseWriter, r *http.Request) {
		f.completeCalls.Add(1)
		body, _ := io.ReadAll(r.Body)
		f.completeBody.Store(&body)
		fmt.Fprint(w, f.completeResponse)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	f.getURLResponses = []string{`{"ok":true,"upload_url":"UPLOAD_URL","file_id":"F123"}`}
	return f
}

func (f *fakeSlack) uploader(tokens TokenProvider) *Client {
	return &Client{APIBase: f.srv.URL + "/api", Tokens: tokens}
}

func testFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "report.csv")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestUploadRootMessage(t *testing.T) {
	slack := newFakeSlack(t)
	tokens := &fakeTokens{token: "utok"}
	path := testFile(t, "a,b\n1,2\n")

	res, err := slack.uploader(tokens).Upload(UploadRequest{
		Path: path, ChannelID: "C123", Comment: "weekly report",
	})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if res.FileID != "F123" || res.Filename != "report.csv" || res.ChannelID != "C123" {
		t.Errorf("result = %+v", res)
	}
	if res.Size != int64(len("a,b\n1,2\n")) {
		t.Errorf("size = %d", res.Size)
	}

	// Step 1 carried filename + length and the Bearer token.
	q := *slack.getURLQuery.Load()
	if !strings.Contains(q, "filename=report.csv") || !strings.Contains(q, "length=8") {
		t.Errorf("getUploadURL query = %s", q)
	}
	if slack.authHeaders[0] != "Bearer utok" {
		t.Errorf("auth = %q", slack.authHeaders[0])
	}

	// Step 2 carried the exact bytes.
	if got := string(*slack.uploadedBytes.Load()); got != "a,b\n1,2\n" {
		t.Errorf("uploaded bytes = %q", got)
	}

	// Step 3 shared into the channel with the comment, no thread_ts.
	var complete map[string]any
	if err := json.Unmarshal(*slack.completeBody.Load(), &complete); err != nil {
		t.Fatal(err)
	}
	if complete["channel_id"] != "C123" || complete["initial_comment"] != "weekly report" {
		t.Errorf("complete body = %v", complete)
	}
	if _, has := complete["thread_ts"]; has {
		t.Error("thread_ts sent for a root-message upload")
	}
}

func TestUploadThreadReply(t *testing.T) {
	slack := newFakeSlack(t)
	path := testFile(t, "x")

	res, err := slack.uploader(&fakeTokens{token: "t"}).Upload(UploadRequest{
		Path: path, ChannelID: "C123", ThreadTS: "1721355600.000100",
	})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if res.ThreadTS != "1721355600.000100" {
		t.Errorf("result thread_ts = %q", res.ThreadTS)
	}
	var complete map[string]any
	if err := json.Unmarshal(*slack.completeBody.Load(), &complete); err != nil {
		t.Fatal(err)
	}
	if complete["thread_ts"] != "1721355600.000100" {
		t.Errorf("complete body = %v", complete)
	}
}

func TestUploadFilenameOverride(t *testing.T) {
	slack := newFakeSlack(t)
	path := testFile(t, "x")
	res, err := slack.uploader(&fakeTokens{token: "t"}).Upload(UploadRequest{
		Path: path, ChannelID: "C1", Filename: "shown-name.txt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Filename != "shown-name.txt" {
		t.Errorf("filename = %q", res.Filename)
	}
	if q := *slack.getURLQuery.Load(); !strings.Contains(q, "filename=shown-name.txt") {
		t.Errorf("query = %s", q)
	}
}

func TestUploadValidation(t *testing.T) {
	u := &Client{}
	if _, err := u.Upload(UploadRequest{Path: "/x"}); err == nil || !strings.Contains(err.Error(), "channel_id") {
		t.Errorf("missing channel_id: %v", err)
	}
	if _, err := u.Upload(UploadRequest{ChannelID: "C1"}); err == nil || !strings.Contains(err.Error(), "path") {
		t.Errorf("missing path: %v", err)
	}
	if _, err := u.Upload(UploadRequest{ChannelID: "C1", Path: filepath.Join(os.TempDir(), "definitely-missing-xyz")}); err == nil {
		t.Error("missing file accepted")
	}
}

func TestUploadSlackErrorStep1(t *testing.T) {
	slack := newFakeSlack(t)
	slack.getURLResponses = []string{`{"ok":false,"error":"invalid_arguments"}`}
	_, err := slack.uploader(&fakeTokens{token: "t"}).Upload(UploadRequest{Path: testFile(t, "x"), ChannelID: "C1"})
	var se *SlackError
	if !errors.As(err, &se) || se.Method != "files.getUploadURLExternal" || se.Reason != "invalid_arguments" {
		t.Fatalf("err = %v", err)
	}
}

func TestUploadHTTPErrorStep2(t *testing.T) {
	slack := newFakeSlack(t)
	slack.uploadStatus = http.StatusInternalServerError
	_, err := slack.uploader(&fakeTokens{token: "t"}).Upload(UploadRequest{Path: testFile(t, "x"), ChannelID: "C1"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("err = %v", err)
	}
	if slack.completeCalls.Load() != 0 {
		t.Error("complete called after failed byte upload")
	}
}

func TestUploadNotInChannelStep3(t *testing.T) {
	slack := newFakeSlack(t)
	slack.completeResponse = `{"ok":false,"error":"not_in_channel"}`
	_, err := slack.uploader(&fakeTokens{token: "t"}).Upload(UploadRequest{Path: testFile(t, "x"), ChannelID: "C1"})
	var se *SlackError
	if !errors.As(err, &se) || se.Reason != "not_in_channel" {
		t.Fatalf("err = %v", err)
	}
}

func TestUploadAuthErrorInvalidatesAndRetriesOnce(t *testing.T) {
	slack := newFakeSlack(t)
	slack.getURLResponses = []string{
		`{"ok":false,"error":"invalid_auth"}`,
		`{"ok":true,"upload_url":"UPLOAD_URL","file_id":"F9"}`,
	}
	tokens := &fakeTokens{token: "t"}
	res, err := slack.uploader(tokens).Upload(UploadRequest{Path: testFile(t, "x"), ChannelID: "C1"})
	if err != nil {
		t.Fatalf("Upload after auth retry: %v", err)
	}
	if res.FileID != "F9" {
		t.Errorf("file id = %q", res.FileID)
	}
	if tokens.invalidated.Load() != 1 {
		t.Errorf("Invalidate calls = %d", tokens.invalidated.Load())
	}
	if slack.getURLCalls.Load() != 2 {
		t.Errorf("getUploadURL calls = %d", slack.getURLCalls.Load())
	}
}

func TestUploadAuthErrorNoInfiniteRetry(t *testing.T) {
	slack := newFakeSlack(t)
	slack.getURLResponses = []string{`{"ok":false,"error":"invalid_auth"}`}
	_, err := slack.uploader(&fakeTokens{token: "t"}).Upload(UploadRequest{Path: testFile(t, "x"), ChannelID: "C1"})
	var se *SlackError
	if !errors.As(err, &se) || se.Reason != "invalid_auth" {
		t.Fatalf("err = %v", err)
	}
	if slack.getURLCalls.Load() != 2 {
		t.Errorf("calls = %d, want exactly 2 (one retry)", slack.getURLCalls.Load())
	}
}

func TestAuditLogAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "audit.jsonl")
	log := &AuditLog{Path: path}

	if err := log.Append(AuditEntry{Tool: "upload_file", Path: "/a", Size: 3, ChannelID: "C1", FileID: "F1", Outcome: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := log.Append(AuditEntry{Tool: "upload_file", Path: "/b", Outcome: "denied", Error: "path denied"}); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("audit mode = %o", perm)
	}

	data, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %d", len(lines))
	}
	var first AuditEntry
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if first.Outcome != "ok" || first.FileID != "F1" || first.Time == "" {
		t.Errorf("first = %+v", first)
	}
	var second AuditEntry
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatal(err)
	}
	if second.Outcome != "denied" {
		t.Errorf("second = %+v", second)
	}
}

func TestAuditLogDisabled(t *testing.T) {
	var log *AuditLog
	if err := log.Append(AuditEntry{}); err != nil {
		t.Errorf("nil log errored: %v", err)
	}
	empty := &AuditLog{}
	if err := empty.Append(AuditEntry{}); err != nil {
		t.Errorf("empty-path log errored: %v", err)
	}
}
