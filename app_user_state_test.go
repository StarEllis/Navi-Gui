package main

import (
	"testing"

	"navi-desktop/model"
)

func TestEnsureDesktopUserCreatesRequiredForeignKeyParent(t *testing.T) {
	app := newTestApp(t)

	if err := app.ensureDesktopUser(); err != nil {
		t.Fatalf("ensure desktop user: %v", err)
	}

	var user model.User
	if err := app.db.First(&user, "id = ?", desktopUserID).Error; err != nil {
		t.Fatalf("load desktop user: %v", err)
	}
	if user.Username != desktopUserID || user.Role != "user" {
		t.Fatalf("unexpected desktop user: %+v", user)
	}
	if err := app.ensureDesktopUser(); err != nil {
		t.Fatalf("ensure desktop user repeatedly: %v", err)
	}
	var count int64
	if err := app.db.Unscoped().Model(&model.User{}).Where("id = ?", desktopUserID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("desktop user idempotency count=%d err=%v", count, err)
	}
}

func TestEnsureWatchedPersistsForDesktopUser(t *testing.T) {
	app := newTestApp(t)
	if err := app.ensureDesktopUser(); err != nil {
		t.Fatalf("ensure desktop user: %v", err)
	}

	library := model.Library{ID: "library-watch", Name: "Watch", Path: t.TempDir(), Type: "movie"}
	if err := app.repos.Library.Create(&library); err != nil {
		t.Fatalf("create library: %v", err)
	}
	media := model.Media{ID: "media-watch", LibraryID: library.ID, Title: "Watch", FilePath: library.Path + "/watch.mkv", MediaType: "movie"}
	if err := app.repos.Media.Create(&media); err != nil {
		t.Fatalf("create media: %v", err)
	}

	if err := app.ensureWatched(media.ID); err != nil {
		t.Fatalf("ensure watched: %v", err)
	}
	var history model.WatchHistory
	if err := app.db.Where("user_id = ? AND media_id = ?", desktopUserID, media.ID).First(&history).Error; err != nil {
		t.Fatalf("load watch history: %v", err)
	}
	if !history.Completed {
		t.Fatal("watch history was not marked completed")
	}
}
