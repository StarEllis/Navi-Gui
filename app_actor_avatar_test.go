package main

import (
	"fmt"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
	"navi-desktop/model"
	"navi-desktop/service"
)

func TestGetActorStatsBackfillsAndPersistsMissingAvatar(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/Filetree.json":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"Content":{"StudioA":{"有馬美玖.jpg":"有馬美玖.jpg"}}}`)
		case strings.HasSuffix(r.URL.EscapedPath(), "/Content/StudioA/%E6%9C%89%E9%A6%AC%E7%BE%8E%E7%8E%96.jpg"):
			w.Header().Set("Content-Type", "image/jpeg")
			sourcePath := filepath.Join(cacheDir, "source.jpg")
			writeActorTestJPEG(t, sourcePath)
			http.ServeFile(w, r, sourcePath)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	app := newTestApp(t)
	artwork := service.NewArtworkCache(cacheDir, zap.NewNop().Sugar())
	app.avatarService = service.NewGfriendsAvatarService(service.GfriendsAvatarOptions{
		CacheDir:     cacheDir,
		ArtworkCache: artwork,
		Logger:       zap.NewNop().Sugar(),
		IndexURLs:    []string{server.URL + "/Filetree.json"},
		ContentBases: []string{server.URL + "/Content/"},
	})

	library := model.Library{ID: "library-1", Name: "Library", Path: `C:\media`}
	media := model.Media{ID: "media-1", LibraryID: library.ID, Title: "Movie", FilePath: `C:\media\movie.mp4`}
	person := model.Person{ID: "person-1", Name: "有马美玖"}
	relation := model.MediaPerson{ID: "relation-1", MediaID: media.ID, PersonID: person.ID, Role: "actor"}
	for _, value := range []any{&library, &media, &person, &relation} {
		if err := app.db.Create(value).Error; err != nil {
			t.Fatalf("seed test data: %v", err)
		}
	}

	stats, err := app.GetActorStats(library.ID)
	if err != nil {
		t.Fatalf("GetActorStats: %v", err)
	}
	if len(stats) != 1 || strings.TrimSpace(stats[0].Image) == "" {
		t.Fatalf("expected returned actor avatar, got %+v", stats)
	}

	var stored model.Person
	if err := app.db.First(&stored, "id = ?", person.ID).Error; err != nil {
		t.Fatalf("reload person: %v", err)
	}
	if stored.ProfileURL != stats[0].Image {
		t.Fatalf("stored profile URL %q does not match returned %q", stored.ProfileURL, stats[0].Image)
	}
}

func writeActorTestJPEG(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	canvas := image.NewRGBA(image.Rect(0, 0, 800, 1200))
	for index := 0; index < len(canvas.Pix); index += 4 {
		canvas.Pix[index] = 90
		canvas.Pix[index+1] = 120
		canvas.Pix[index+2] = 160
		canvas.Pix[index+3] = 255
	}
	if err := jpeg.Encode(file, canvas, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
}
