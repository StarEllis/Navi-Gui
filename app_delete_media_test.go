package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/gorm"
	"navi-desktop/model"
	"navi-desktop/service"
)

func TestDeleteMediaRemovesGeneratedThumbnailsButKeepsLocalFiles(t *testing.T) {
	app := newTestApp(t)
	app.artworkCache = service.NewArtworkCache(filepath.Join(t.TempDir(), "cache"), app.logger)
	t.Cleanup(app.artworkCache.Shutdown)

	mediaDir := t.TempDir()
	mediaPath := filepath.Join(mediaDir, "movie.mp4")
	localPoster := filepath.Join(mediaDir, "poster.jpg")
	for path, contents := range map[string][]byte{
		mediaPath:   []byte("video"),
		localPoster: []byte("user artwork"),
	} {
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			t.Fatalf("write local file %s: %v", path, err)
		}
	}

	media := &model.Media{
		ID:                   "delete-generated-thumbnails",
		LibraryID:            "library",
		Title:                "movie",
		FilePath:             mediaPath,
		MediaType:            "movie",
		ThumbnailStatus:      service.ThumbnailStatusGenerated,
		ThumbnailFingerprint: "generated-for-this-video",
	}
	if err := app.db.Create(media).Error; err != nil {
		t.Fatalf("create media: %v", err)
	}
	cacheGenerated := []string{
		app.artworkCache.GeneratedMediaArtworkPath(media, "poster"),
		app.artworkCache.GeneratedMediaArtworkPath(media, "fanart"),
		app.artworkCache.GeneratedMediaPreviewPath(media, 1),
	}
	legacyStem := strings.TrimSuffix(mediaPath, filepath.Ext(mediaPath))
	legacyGenerated := []string{
		legacyStem + "-poster.jpg",
		legacyStem + "-fanart.jpg",
		filepath.Join(mediaDir, "extrafanart", "movie-preview-01.jpg"),
		filepath.Join(mediaDir, "extrafanart", "movie-preview-02.jpg"),
	}
	siblingPreview := filepath.Join(mediaDir, "extrafanart", "another-movie-preview-01.jpg")
	for _, path := range append(append([]string{}, cacheGenerated...), append(legacyGenerated, siblingPreview)...) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create thumbnail directory: %v", err)
		}
		if err := os.WriteFile(path, []byte("generated"), 0o600); err != nil {
			t.Fatalf("write generated thumbnail: %v", err)
		}
	}

	if err := app.DeleteMedia(media.ID); err != nil {
		t.Fatalf("delete media: %v", err)
	}
	if _, err := app.repos.Media.FindByID(media.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("media database row still exists: %v", err)
	}
	for _, path := range append(cacheGenerated, legacyGenerated...) {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("generated thumbnail remains: %s err=%v", path, err)
		}
	}
	for _, path := range []string{mediaPath, localPoster, siblingPreview} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("local file was removed: %s err=%v", path, err)
		}
	}
}
