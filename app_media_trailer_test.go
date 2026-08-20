package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTrailerFixture(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestGetMediaTrailerByPath(t *testing.T) {
	t.Run("trailers 子目录共用一份", func(t *testing.T) {
		dir := t.TempDir()
		media := filepath.Join(dir, "SONE-246-4K.mp4")
		writeTrailerFixture(t, media)
		trailer := filepath.Join(dir, "trailers", "trailer.mp4")
		writeTrailerFixture(t, trailer)

		if got := getMediaTrailerByPath(media); got != trailer {
			t.Fatalf("trailer = %q, want %q", got, trailer)
		}
	})

	t.Run("同级带片名的优先", func(t *testing.T) {
		dir := t.TempDir()
		media := filepath.Join(dir, "SONE-246-4K.mp4")
		writeTrailerFixture(t, media)
		writeTrailerFixture(t, filepath.Join(dir, "trailers", "trailer.mp4"))
		named := filepath.Join(dir, "SONE-246-4K-trailer.mp4")
		writeTrailerFixture(t, named)

		if got := getMediaTrailerByPath(media); got != named {
			t.Fatalf("trailer = %q, want %q", got, named)
		}
	})

	t.Run("多视频目录里认片名", func(t *testing.T) {
		dir := t.TempDir()
		media := filepath.Join(dir, "SONE-246-4K.mp4")
		writeTrailerFixture(t, media)
		writeTrailerFixture(t, filepath.Join(dir, "MIDE-999.mp4"))
		writeTrailerFixture(t, filepath.Join(dir, "trailers", "MIDE-999-trailer.mp4"))
		owned := filepath.Join(dir, "trailers", "SONE-246-4K-trailer.mp4")
		writeTrailerFixture(t, owned)

		if got := getMediaTrailerByPath(media); got != owned {
			t.Fatalf("trailer = %q, want %q", got, owned)
		}
	})

	t.Run("没有预告片时返回空", func(t *testing.T) {
		dir := t.TempDir()
		media := filepath.Join(dir, "SONE-246-4K.mp4")
		writeTrailerFixture(t, media)

		if got := getMediaTrailerByPath(media); got != "" {
			t.Fatalf("trailer = %q, want empty", got)
		}
	})
}
