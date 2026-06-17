package service

import (
	"os"
	"path/filepath"
	"testing"

	"navi-desktop/repository"
)

func TestShouldRefreshExistingMovieMediaIncrementalUpdatesChangedSidecar(t *testing.T) {
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "sample.mp4")
	nfoPath := filepath.Join(dir, "sample.nfo")

	if err := os.WriteFile(mediaPath, []byte("video"), 0644); err != nil {
		t.Fatalf("write media: %v", err)
	}
	if err := os.WriteFile(nfoPath, []byte("<movie><title>old</title></movie>"), 0644); err != nil {
		t.Fatalf("write nfo: %v", err)
	}

	info, err := os.Stat(mediaPath)
	if err != nil {
		t.Fatalf("stat media: %v", err)
	}
	sidecars := collectDirectorySidecarFiles(dir)
	signature := repository.MediaFileSignature{
		VideoFingerprint:   buildVideoFingerprintFromInfo(info),
		SidecarFingerprint: buildSidecarFingerprint(mediaPath, sidecars),
	}

	if err := os.WriteFile(nfoPath, []byte("<movie><title>changed sidecar</title></movie>"), 0644); err != nil {
		t.Fatalf("rewrite nfo: %v", err)
	}
	nextSidecars := collectDirectorySidecarFiles(dir)

	shouldRefresh, refreshVideo := shouldRefreshExistingMovieMedia(
		ScanOptions{Incremental: true},
		signature,
		mediaPath,
		info,
		nextSidecars,
	)

	if !shouldRefresh {
		t.Fatal("expected incremental scan to refresh when the NFO sidecar changes")
	}
	if refreshVideo {
		t.Fatal("expected sidecar-only incremental refresh to skip video probing")
	}
}

func TestShouldRefreshExistingMovieMediaIncrementalSkipsVideoOnlyChange(t *testing.T) {
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "sample.mp4")

	if err := os.WriteFile(mediaPath, []byte("video"), 0644); err != nil {
		t.Fatalf("write media: %v", err)
	}

	info, err := os.Stat(mediaPath)
	if err != nil {
		t.Fatalf("stat media: %v", err)
	}
	sidecars := collectDirectorySidecarFiles(dir)
	signature := repository.MediaFileSignature{
		VideoFingerprint:   buildVideoFingerprintFromInfo(info),
		SidecarFingerprint: buildSidecarFingerprint(mediaPath, sidecars),
	}

	if err := os.WriteFile(mediaPath, []byte("video content changed"), 0644); err != nil {
		t.Fatalf("rewrite media: %v", err)
	}
	changedInfo, err := os.Stat(mediaPath)
	if err != nil {
		t.Fatalf("stat changed media: %v", err)
	}

	shouldRefresh, _ := shouldRefreshExistingMovieMedia(
		ScanOptions{Incremental: true},
		signature,
		mediaPath,
		changedInfo,
		sidecars,
	)

	if shouldRefresh {
		t.Fatal("expected incremental scan to keep skipping existing media when only the video fingerprint changes")
	}
}
