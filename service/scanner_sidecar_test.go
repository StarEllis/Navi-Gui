package service

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
	"navi-desktop/model"
)

const unparsableNFO = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<movie>
  <title>SDNT-008 title</wrong>
</movie>
`

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestApplyLocalSidecarsKeepsArtworkWhenNFOFails(t *testing.T) {
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "SDNT-008-C.mp4")
	writeFile(t, mediaPath, "video")
	writeFile(t, filepath.Join(dir, "SDNT-008-C.nfo"), unparsableNFO)
	writeFile(t, filepath.Join(dir, "poster.jpg"), "poster")
	writeFile(t, filepath.Join(dir, "fanart.jpg"), "fanart")

	scanner := &ScannerService{logger: zap.NewNop().Sugar(), nfoService: NewNFOService(zap.NewNop().Sugar())}
	media := &model.Media{ID: "media-1", FilePath: mediaPath}
	sidecars := collectDirectorySidecarFiles(dir)

	if err := scanner.applyLocalSidecarsWithMode(media, mediaPath, sidecars, true); err == nil {
		t.Fatal("expected NFO parse error to be reported")
	}
	if media.PosterPath != filepath.Join(dir, "poster.jpg") {
		t.Fatalf("poster path = %q, want the local poster.jpg", media.PosterPath)
	}
	if media.BackdropPath != filepath.Join(dir, "fanart.jpg") {
		t.Fatalf("backdrop path = %q, want the local fanart.jpg", media.BackdropPath)
	}
}

func TestCollectMediaPreviewsIgnoresRootCoverArt(t *testing.T) {
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "MXGS-884-C-4K.mp4")
	writeFile(t, mediaPath, "video")
	writeFile(t, filepath.Join(dir, "MXGS-884-C-4K-thumb.jpg"), "thumb")
	writeFile(t, filepath.Join(dir, "MXGS-884-C-4K-poster.jpg"), "poster")
	writeFile(t, filepath.Join(dir, "MXGS-884-C-4K-fanart.jpg"), "fanart")

	scanner := &ScannerService{
		logger:       zap.NewNop().Sugar(),
		sidecarCache: make(map[string]directorySidecarCacheEntry),
	}
	if previews := scanner.CollectMediaPreviews(mediaPath); len(previews) != 0 {
		t.Fatalf("expected no previews from root cover art, got %v", previews)
	}

	writeFile(t, filepath.Join(dir, "extrafanart", "still-01.jpg"), "still")
	writeFile(t, filepath.Join(dir, "extrafanart", "still-02.jpg"), "still")

	previews := scanner.CollectMediaPreviews(mediaPath)
	if len(previews) != 2 {
		t.Fatalf("expected the two extrafanart stills, got %v", previews)
	}
	for _, preview := range previews {
		if filepath.Base(filepath.Dir(preview)) != "extrafanart" {
			t.Fatalf("preview %q did not come from extrafanart", preview)
		}
	}
}

func TestCollectMediaPreviewsOrdersStillsNaturally(t *testing.T) {
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "SONE-246-4K.mp4")
	writeFile(t, mediaPath, "video")
	// 刮削器就是这么命名的：fanart1 … fanart12，没有补零。
	for index := 1; index <= 12; index++ {
		writeFile(t, filepath.Join(dir, "extrafanart", fmt.Sprintf("fanart%d.jpg", index)), "still")
	}

	scanner := &ScannerService{
		logger:       zap.NewNop().Sugar(),
		sidecarCache: make(map[string]directorySidecarCacheEntry),
	}
	previews := scanner.CollectMediaPreviews(mediaPath)
	if len(previews) != 12 {
		t.Fatalf("expected 12 stills, got %d", len(previews))
	}
	for index, preview := range previews {
		want := fmt.Sprintf("fanart%d.jpg", index+1)
		if got := filepath.Base(preview); got != want {
			t.Fatalf("preview[%d] = %q, want %q", index, got, want)
		}
	}
}

func TestNaturalLess(t *testing.T) {
	cases := []struct {
		left  string
		right string
		want  bool
	}{
		{"fanart2.jpg", "fanart10.jpg", true},
		{"fanart10.jpg", "fanart2.jpg", false},
		{"fanart1.jpg", "fanart1.jpg", false},
		{"fanart02.jpg", "fanart10.jpg", true},
		{"fanart9.jpg", "fanart10.jpg", true},
		{"still-1-a.jpg", "still-1-b.jpg", true},
		{"a.jpg", "b.jpg", true},
		{"fanart.jpg", "fanart2.jpg", true},
		// 位数比大小，不走整型解析，超长数字也不会溢出
		{"x99999999999999999999.jpg", "x100000000000000000000.jpg", true},
	}
	for _, tc := range cases {
		if got := NaturalLess(tc.left, tc.right); got != tc.want {
			t.Fatalf("NaturalLess(%q, %q) = %v, want %v", tc.left, tc.right, got, tc.want)
		}
	}
}
