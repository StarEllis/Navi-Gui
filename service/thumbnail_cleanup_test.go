package service

import (
	"os"
	"path/filepath"
	"testing"

	"navi-desktop/model"
)

func TestRemoveLegacyGeneratedThumbnailFilesKeepsNFOArtwork(t *testing.T) {
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "movie.mp4")
	media := &model.Media{
		FilePath:             mediaPath,
		MediaType:            "movie",
		ThumbnailStatus:      ThumbnailStatusGenerated,
		ThumbnailFingerprint: "generated-for-this-video",
	}
	paths := []string{
		mediaPath,
		generatedPosterPath(mediaPath),
		generatedBackdropPath(mediaPath),
		filepath.Join(previewDirectory(mediaPath), generatedPreviewName(mediaPath, 1)),
		filepath.Join(dir, "movie.nfo"),
	}
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := RemoveLegacyGeneratedThumbnailFiles(media)
	if err != nil {
		t.Fatalf("remove legacy generated thumbnails: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed %d files for media with NFO", removed)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("NFO media file was removed: %s err=%v", path, err)
		}
	}
}
