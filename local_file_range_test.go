package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// 预告片的悬停预热和播放器拖进度条都依赖 Range：前者只取文件头几百 KB，
// 后者要能跳着读，整份 76MB 拉下来才起播就没法用了。
func TestLocalFileHandlerServesRangeRequests(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trailer.mp4")
	body := bytes.Repeat([]byte("navi"), 4096)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write trailer: %v", err)
	}

	handler := &LocalFileHandler{}
	request := httptest.NewRequest(http.MethodGet, "/local/"+url.PathEscape(path), nil)
	request.Header.Set("Range", "bytes=0-1023")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusPartialContent)
	}
	if got := recorder.Body.Len(); got != 1024 {
		t.Fatalf("body length = %d, want 1024", got)
	}
	if !bytes.Equal(recorder.Body.Bytes(), body[:1024]) {
		t.Fatal("served bytes do not match the head of the file")
	}
	wantRange := fmt.Sprintf("bytes 0-1023/%d", len(body))
	if got := recorder.Header().Get("Content-Range"); got != wantRange {
		t.Fatalf("Content-Range = %q, want %q", got, wantRange)
	}
}
