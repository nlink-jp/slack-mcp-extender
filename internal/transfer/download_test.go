package transfer

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeFilesAPI serves files.info plus a download endpoint.
func fakeFilesAPI(t *testing.T, infoResponse string, fileBytes []byte, downloadStatus int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/api/files.info", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("file"); got == "" {
			t.Errorf("files.info missing file param")
		}
		fmt.Fprint(w, strings.ReplaceAll(infoResponse, "DOWNLOAD_URL", srv.URL+"/dl/abc"))
	})
	mux.HandleFunc("/dl/abc", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("download missing Bearer token")
		}
		w.WriteHeader(downloadStatus)
		w.Write(fileBytes)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestInfoAndFetchTo(t *testing.T) {
	content := []byte("binary\x00payload")
	srv := fakeFilesAPI(t,
		`{"ok":true,"file":{"id":"F77","name":"data.bin","size":14,"url_private_download":"DOWNLOAD_URL"}}`,
		content, http.StatusOK)
	c := &Client{APIBase: srv.URL + "/api", Tokens: &fakeTokens{token: "tok"}}

	info, err := c.Info("F77")
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != "data.bin" || info.Size != 14 {
		t.Errorf("info = %+v", info)
	}

	target := filepath.Join(t.TempDir(), "data.bin")
	written, err := c.FetchTo(info, target, 1024)
	if err != nil {
		t.Fatalf("FetchTo: %v", err)
	}
	if written != int64(len(content)) {
		t.Errorf("written = %d", written)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != string(content) {
		t.Errorf("target content = %q, %v", got, err)
	}
}

func TestInfoFallsBackToURLPrivate(t *testing.T) {
	srv := fakeFilesAPI(t,
		`{"ok":true,"file":{"id":"F1","name":"n","size":1,"url_private":"DOWNLOAD_URL"}}`,
		[]byte("x"), http.StatusOK)
	c := &Client{APIBase: srv.URL + "/api", Tokens: &fakeTokens{token: "tok"}}
	info, err := c.Info("F1")
	if err != nil || info.DownloadURL == "" {
		t.Fatalf("info = %+v, %v", info, err)
	}
}

func TestInfoErrors(t *testing.T) {
	srv := fakeFilesAPI(t, `{"ok":false,"error":"file_not_found"}`, nil, http.StatusOK)
	c := &Client{APIBase: srv.URL + "/api", Tokens: &fakeTokens{token: "tok"}}
	_, err := c.Info("F404")
	var se *SlackError
	if !errors.As(err, &se) || se.Reason != "file_not_found" {
		t.Fatalf("err = %v", err)
	}
	if _, err := c.Info(""); err == nil {
		t.Error("empty file_id accepted")
	}

	noURL := fakeFilesAPI(t, `{"ok":true,"file":{"id":"F2","name":"n","size":1}}`, nil, http.StatusOK)
	c2 := &Client{APIBase: noURL.URL + "/api", Tokens: &fakeTokens{token: "tok"}}
	if _, err := c2.Info("F2"); err == nil || !strings.Contains(err.Error(), "no download URL") {
		t.Errorf("err = %v", err)
	}
}

func TestFetchToEnforcesCapOnWire(t *testing.T) {
	// The declared size lies (says 3); the stream carries 100 bytes. The
	// wire limit must catch it and leave no partial file behind.
	big := strings.Repeat("x", 100)
	srv := fakeFilesAPI(t,
		`{"ok":true,"file":{"id":"F9","name":"lie.bin","size":3,"url_private_download":"DOWNLOAD_URL"}}`,
		[]byte(big), http.StatusOK)
	c := &Client{APIBase: srv.URL + "/api", Tokens: &fakeTokens{token: "tok"}}

	info, err := c.Info("F9")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "lie.bin")
	_, err = c.FetchTo(info, target, 50)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("partial files left behind: %v", entries)
	}
}

func TestFetchToHTTPError(t *testing.T) {
	srv := fakeFilesAPI(t,
		`{"ok":true,"file":{"id":"F8","name":"gone.bin","size":3,"url_private_download":"DOWNLOAD_URL"}}`,
		[]byte("no"), http.StatusForbidden)
	c := &Client{APIBase: srv.URL + "/api", Tokens: &fakeTokens{token: "tok"}}
	info, err := c.Info("F8")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, err := c.FetchTo(info, filepath.Join(dir, "gone.bin"), 0); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("partial files left behind: %v", entries)
	}
}
