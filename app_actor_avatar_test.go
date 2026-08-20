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
	"time"

	"go.uber.org/zap"
	"navi-desktop/model"
	"navi-desktop/service"
)

// newAvatarBackfillApp seeds one actor without an avatar, served by a local
// stand-in for gfriends.
func newAvatarBackfillApp(t *testing.T) (*App, model.Library, model.Person) {
	t.Helper()
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
	t.Cleanup(server.Close)

	app := newTestApp(t)
	app.avatarStore = service.NewActorAvatarStore(cacheDir, zap.NewNop().Sugar())
	app.avatarService = service.NewGfriendsAvatarService(service.GfriendsAvatarOptions{
		CacheDir:     cacheDir,
		ArtworkCache: service.NewArtworkCache(cacheDir, zap.NewNop().Sugar()),
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
	return app, library, person
}

func TestManualAvatarSurvivesOverwriteRescan(t *testing.T) {
	app, library, person := newAvatarBackfillApp(t)

	sourcePath := filepath.Join(t.TempDir(), "manual.jpg")
	writeActorTestJPEG(t, sourcePath)
	storedPath, err := app.SetActorAvatarFromFile(person.ID, sourcePath)
	if err != nil {
		t.Fatalf("set avatar from file: %v", err)
	}

	// An overwrite rescan wipes the media rows, takes the people with them, and
	// rebuilds the same actor under a fresh id.
	if err := app.db.Unscoped().Where("1 = 1").Delete(&model.MediaPerson{}).Error; err != nil {
		t.Fatalf("clear relations: %v", err)
	}
	if err := app.db.Unscoped().Where("1 = 1").Delete(&model.Person{}).Error; err != nil {
		t.Fatalf("clear people: %v", err)
	}
	rebuilt := model.Person{ID: "person-rebuilt", Name: person.Name}
	relation := model.MediaPerson{ID: "relation-rebuilt", MediaID: "media-1", PersonID: rebuilt.ID, Role: "actor"}
	for _, value := range []any{&rebuilt, &relation} {
		if err := app.db.Create(value).Error; err != nil {
			t.Fatalf("reseed: %v", err)
		}
	}

	if result, err := app.BackfillActorAvatars(library.ID); err != nil || result.Filled != 1 {
		t.Fatalf("backfill after rescan: result=%+v err=%v", result, err)
	}

	var restored model.Person
	if err := app.db.First(&restored, "id = ?", rebuilt.ID).Error; err != nil {
		t.Fatalf("reload rebuilt person: %v", err)
	}
	if restored.ProfileURL != storedPath {
		t.Fatalf("profile URL = %q, want the imported avatar %q", restored.ProfileURL, storedPath)
	}
	if !fileExistsForTest(storedPath) {
		t.Fatalf("imported avatar file should still exist at %s", storedPath)
	}
}

func TestActorRowsMissingAvatarSkipsPlaceholderActor(t *testing.T) {
	rows := []actorStatsRow{
		{ID: "1", Name: "未知演员"},
		{ID: "2", Name: "未知演員"},
		{ID: "3", Name: "有马美玖"},
		{ID: "4", Name: "白桃花", ProfileURL: `C:\cache\artwork\actor\4\a.jpg`},
	}
	pending := actorRowsMissingAvatar(rows)
	if len(pending) != 1 || pending[0].ID != "3" {
		t.Fatalf("pending = %+v, want only the real actor without an avatar", pending)
	}
}

func TestBackfillActorAvatarsFillsOnDemandAndReportsCount(t *testing.T) {
	app, library, person := newAvatarBackfillApp(t)

	result, err := app.BackfillActorAvatars(library.ID)
	if err != nil {
		t.Fatalf("BackfillActorAvatars: %v", err)
	}
	if result.Filled != 1 || result.Pending != 1 {
		t.Fatalf("result = %+v, want filled=1 pending=1", result)
	}

	var stored model.Person
	if err := app.db.First(&stored, "id = ?", person.ID).Error; err != nil {
		t.Fatalf("reload person: %v", err)
	}
	if !fileExistsForTest(stored.ProfileURL) {
		t.Fatalf("expected cached avatar file at %q", stored.ProfileURL)
	}

	// 补完之后再点一次：pending 归零，界面据此说「所有演员都已有头像」，
	// 而不是含糊的「没有补到新的头像」。
	result, err = app.BackfillActorAvatars(library.ID)
	if err != nil || result.Filled != 0 || result.Pending != 0 {
		t.Fatalf("second run: result=%+v err=%v", result, err)
	}
}

func TestGetActorStatsBackfillsAndPersistsMissingAvatar(t *testing.T) {
	app, library, person := newAvatarBackfillApp(t)

	// The first visit answers without waiting for the download.
	stats, err := app.GetActorStats(library.ID)
	if err != nil {
		t.Fatalf("GetActorStats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("expected one actor, got %+v", stats)
	}

	stored := waitForActorAvatar(t, app, person.ID)
	if !fileExistsForTest(stored.ProfileURL) {
		t.Fatalf("expected cached avatar file at %q", stored.ProfileURL)
	}

	// The next visit serves it from what the backfill persisted.
	stats, err = app.GetActorStats(library.ID)
	if err != nil {
		t.Fatalf("GetActorStats: %v", err)
	}
	if len(stats) != 1 || stats[0].Image != stored.ProfileURL {
		t.Fatalf("expected persisted avatar %q in stats, got %+v", stored.ProfileURL, stats)
	}
}

func waitForActorAvatar(t *testing.T, app *App, personID string) model.Person {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var stored model.Person
		if err := app.db.First(&stored, "id = ?", personID).Error; err != nil {
			t.Fatalf("reload person: %v", err)
		}
		if strings.TrimSpace(stored.ProfileURL) != "" {
			return stored
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the background avatar backfill")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func fileExistsForTest(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
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
