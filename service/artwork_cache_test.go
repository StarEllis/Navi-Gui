package service

import (
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"navi-desktop/model"
)

func writeTestJPEG(t *testing.T, path string, width, height int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir image dir: %v", err)
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 120, A: 255})
		}
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create image: %v", err)
	}
	defer file.Close()
	if err := jpeg.Encode(file, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatalf("encode image: %v", err)
	}
}

func decodeImageSize(t *testing.T, path string) image.Point {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open cached image: %v", err)
	}
	defer file.Close()
	cfg, _, err := image.DecodeConfig(file)
	if err != nil {
		t.Fatalf("decode cached image: %v", err)
	}
	return image.Point{X: cfg.Width, Y: cfg.Height}
}

func TestArtworkCacheCachesBoundedMediaArtworkAndKeepsItAfterSourceDelete(t *testing.T) {
	mediaDir := t.TempDir()
	cacheDir := filepath.Join(t.TempDir(), "cache")
	mediaPath := filepath.Join(mediaDir, "ABC-123.mp4")
	if err := os.WriteFile(mediaPath, []byte("video"), 0644); err != nil {
		t.Fatalf("write media: %v", err)
	}
	posterSource := filepath.Join(mediaDir, "ABC-123-poster.jpg")
	fanartSource := filepath.Join(mediaDir, "ABC-123-fanart.jpg")
	writeTestJPEG(t, posterSource, 1200, 1800)
	writeTestJPEG(t, fanartSource, 2400, 1350)

	cache := NewArtworkCache(cacheDir, nil)
	media := &model.Media{ID: "media-1", FilePath: mediaPath}
	sidecars := collectDirectorySidecarFiles(mediaDir)
	posterPath, fanartPath, changed, err := cache.CacheMediaArtwork(media, sidecars)
	if err != nil {
		t.Fatalf("cache media artwork: %v", err)
	}
	if !changed {
		t.Fatalf("expected artwork paths to change")
	}
	if posterPath == "" || fanartPath == "" {
		t.Fatalf("expected cached poster and fanart paths, got %q / %q", posterPath, fanartPath)
	}
	if !strings.Contains(filepath.ToSlash(posterPath), "/artwork/poster/") {
		t.Fatalf("poster path should be inside poster cache, got %s", posterPath)
	}
	if !strings.Contains(filepath.ToSlash(fanartPath), "/artwork/fanart/") {
		t.Fatalf("fanart path should be inside fanart cache, got %s", fanartPath)
	}
	posterSize := decodeImageSize(t, posterPath)
	if posterSize.X > 400 || posterSize.Y > 540 {
		t.Fatalf("poster too large: %dx%d", posterSize.X, posterSize.Y)
	}
	fanartSize := decodeImageSize(t, fanartPath)
	if fanartSize.X > 1280 || fanartSize.Y > 720 {
		t.Fatalf("fanart too large: %dx%d", fanartSize.X, fanartSize.Y)
	}

	if err := os.Remove(posterSource); err != nil {
		t.Fatalf("remove poster source: %v", err)
	}
	if err := os.Remove(fanartSource); err != nil {
		t.Fatalf("remove fanart source: %v", err)
	}
	if _, err := os.Stat(posterPath); err != nil {
		t.Fatalf("cached poster should remain after source delete: %v", err)
	}
	if _, err := os.Stat(fanartPath); err != nil {
		t.Fatalf("cached fanart should remain after source delete: %v", err)
	}
}

func TestArtworkCacheCachesPreviewsAndCanRemoveOnlyMediaArtwork(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	previewSource := filepath.Join(t.TempDir(), "extrafanart", "ABC-123-preview-01.jpg")
	writeTestJPEG(t, previewSource, 1920, 1080)

	cache := NewArtworkCache(cacheDir, nil)
	media := &model.Media{ID: "media-2", FilePath: filepath.Join(t.TempDir(), "ABC-123.mp4")}
	previewPaths, err := cache.CacheMediaPreviews(media, []string{previewSource}, 4)
	if err != nil {
		t.Fatalf("cache previews: %v", err)
	}
	if len(previewPaths) != 1 {
		t.Fatalf("expected one cached preview, got %d", len(previewPaths))
	}
	if !strings.Contains(filepath.ToSlash(previewPaths[0]), "/artwork/preview/") {
		t.Fatalf("preview path should be inside preview cache, got %s", previewPaths[0])
	}
	size := decodeImageSize(t, previewPaths[0])
	if size.X > 1280 || size.Y > 720 {
		t.Fatalf("preview too large: %dx%d", size.X, size.Y)
	}

	if err := cache.RemoveMedia(media.ID); err != nil {
		t.Fatalf("remove media cache: %v", err)
	}
	if _, err := os.Stat(previewPaths[0]); !os.IsNotExist(err) {
		t.Fatalf("expected media preview cache to be removed, stat err=%v", err)
	}
}
