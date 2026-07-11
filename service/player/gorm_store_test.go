package player

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"navi-desktop/model"
)

func TestGormHistoryStoreUpsertReloadsPersistedCompletedByUserAndMedia(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := model.AutoMigrate(db); err != nil {
		t.Fatal(err)
	}

	user := model.User{ID: "desktop_user", Username: "desktop_user", Password: "!", Role: "user"}
	library := model.Library{ID: "library", Name: "Library", Path: t.TempDir(), Type: "movie"}
	media := model.Media{ID: "media", LibraryID: library.ID, Title: "Media", FilePath: "C:/media.mkv", MediaType: "movie"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&library).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&media).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.WatchHistory{UserID: user.ID, MediaID: media.ID, Position: 10, Duration: 100}).Error; err != nil {
		t.Fatal(err)
	}

	store := NewGormHistoryStore(db, user.ID)
	history := History{MediaID: media.ID, Position: 20 * time.Second, Duration: 100 * time.Second, Completed: true, UpdatedAt: time.Now()}
	if err := store.Save(context.Background(), &history); err != nil {
		t.Fatalf("save existing history: %v", err)
	}
	if !history.Completed {
		t.Fatal("persisted watched state was not reloaded")
	}

	var persisted model.WatchHistory
	if err := db.Where("user_id = ? AND media_id = ?", user.ID, media.ID).First(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if !persisted.Completed || persisted.Position != 20 {
		t.Fatalf("unexpected persisted history: %+v", persisted)
	}
}
