package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplicationLoggerWritesPersistentLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "navi.log")
	logger, file, err := newApplicationLogger(path)
	if err != nil {
		t.Fatalf("create logger: %v", err)
	}
	logger.Info("scan diagnostic marker")
	if err := file.Sync(); err != nil {
		t.Fatalf("sync log file: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close log file: %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(content), "scan diagnostic marker") {
		t.Fatalf("persistent log missing marker: %s", content)
	}
}
