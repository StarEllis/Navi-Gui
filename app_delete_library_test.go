package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"navi-desktop/model"
	"navi-desktop/repository"
	"navi-desktop/service"
)

func newDeleteLibraryAppFixture(t *testing.T, cacheBase string) (*App, string, string) {
	t.Helper()
	dbName := "delete_library_app_" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", dbName)), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}
	if err := model.AutoMigrate(db); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	if err := db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
		t.Fatalf("re-enable foreign keys after migration: %v", err)
	}

	mediaRoot := t.TempDir()
	mediaPath := filepath.Join(mediaRoot, "movie.mp4")
	if err := os.WriteFile(mediaPath, []byte("real media stays"), 0644); err != nil {
		t.Fatalf("write media file: %v", err)
	}
	library := model.Library{ID: "delete-library", Name: "Delete", Path: mediaRoot, Type: "movie"}
	series := model.Series{ID: "delete-series", LibraryID: library.ID, Title: "Series", FolderPath: "series"}
	media := model.Media{ID: "delete-media", LibraryID: library.ID, SeriesID: series.ID, Title: "Movie", FilePath: mediaPath, MediaType: "movie"}
	for _, value := range []interface{}{&library, &series, &media} {
		if err := db.Create(value).Error; err != nil {
			t.Fatalf("seed %T: %v", value, err)
		}
	}

	logger := zap.NewNop().Sugar()
	return &App{
		db:           db,
		repos:        repository.NewRepositories(db),
		artworkCache: service.NewArtworkCache(cacheBase, logger),
		logger:       logger,
	}, media.ID, mediaPath
}

func writeDeleteLibraryCacheMarker(t *testing.T, cacheBase, mediaID string) string {
	t.Helper()
	path := filepath.Join(cacheBase, "artwork", "poster", mediaID+"-marker.jpg")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("create cache directory: %v", err)
	}
	if err := os.WriteFile(path, []byte("cache"), 0644); err != nil {
		t.Fatalf("write cache marker: %v", err)
	}
	return path
}

func TestDeleteLibraryCommitFailureDoesNotCleanCache(t *testing.T) {
	cacheBase := filepath.Join(t.TempDir(), "cache")
	app, mediaID, mediaPath := newDeleteLibraryAppFixture(t, cacheBase)
	cacheMarker := writeDeleteLibraryCacheMarker(t, cacheBase, mediaID)

	for _, statement := range []string{
		`CREATE TABLE commit_parent (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE commit_guard (
			id INTEGER PRIMARY KEY,
			parent_id INTEGER,
			FOREIGN KEY(parent_id) REFERENCES commit_parent(id) DEFERRABLE INITIALLY DEFERRED
		)`,
	} {
		if err := app.db.Exec(statement).Error; err != nil {
			t.Fatalf("install commit failure fixture: %v", err)
		}
	}
	if err := app.db.Transaction(func(tx *gorm.DB) error {
		return tx.Exec("INSERT INTO commit_guard(id, parent_id) VALUES (100, 999)").Error
	}); err == nil {
		t.Fatal("deferred foreign key fixture did not fail at commit")
	}
	if err := app.db.Exec(`CREATE TRIGGER fail_delete_library_commit
		AFTER DELETE ON libraries
		BEGIN
			INSERT INTO commit_guard(id, parent_id) VALUES (1, 999);
		END`).Error; err != nil {
		t.Fatalf("install commit failure trigger: %v", err)
	}

	result, err := app.DeleteLibrary("delete-library")
	if err == nil {
		t.Fatal("expected deferred commit failure")
	}
	if result != nil && result.Deleted {
		t.Fatal("failed database transaction reported Deleted=true")
	}
	for name, target := range map[string]interface{}{
		"library": &model.Library{},
		"media":   &model.Media{},
	} {
		var count int64
		if err := app.db.Model(target).Count(&count).Error; err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		if count != 1 {
			t.Fatalf("expected %s rollback, got %d rows", name, count)
		}
	}
	if _, err := os.Stat(cacheMarker); err != nil {
		t.Fatalf("cache was cleaned before commit: %v", err)
	}
	if _, err := os.Stat(mediaPath); err != nil {
		t.Fatalf("real media file was touched: %v", err)
	}
}

func TestDeleteLibraryCleansCacheOnlyAfterDatabaseSuccess(t *testing.T) {
	cacheBase := filepath.Join(t.TempDir(), "cache")
	app, mediaID, mediaPath := newDeleteLibraryAppFixture(t, cacheBase)
	cacheMarker := writeDeleteLibraryCacheMarker(t, cacheBase, mediaID)

	result, err := app.DeleteLibrary("delete-library")
	if err != nil {
		t.Fatalf("delete library: %v", err)
	}
	if result == nil || !result.Deleted || result.Warning != "" {
		t.Fatalf("unexpected success result: %+v", result)
	}
	for name, target := range map[string]interface{}{
		"library": &model.Library{},
		"media":   &model.Media{},
	} {
		var count int64
		if err := app.db.Model(target).Count(&count).Error; err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		if count != 0 {
			t.Fatalf("expected %s deletion, got %d rows", name, count)
		}
	}
	if _, err := os.Stat(cacheMarker); !os.IsNotExist(err) {
		t.Fatalf("cache was not cleaned after commit: %v", err)
	}
	if _, err := os.Stat(mediaPath); err != nil {
		t.Fatalf("real media file was deleted: %v", err)
	}
}

func TestDeleteLibraryCacheFailureReturnsCommittedWarning(t *testing.T) {
	cacheBase := filepath.Join(t.TempDir(), "cache")
	app, _, _ := newDeleteLibraryAppFixture(t, cacheBase)
	app.removeMediaCache = func(string) error { return errors.New("injected cache cleanup failure") }

	result, err := app.DeleteLibrary("delete-library")
	if err != nil {
		t.Fatalf("cache cleanup warning returned as error: %v", err)
	}
	if result == nil || !result.Deleted || result.Warning == "" {
		t.Fatalf("expected Deleted=true with warning, got %+v", result)
	}
	for name, target := range map[string]interface{}{
		"library": &model.Library{},
		"media":   &model.Media{},
	} {
		var count int64
		if err := app.db.Model(target).Count(&count).Error; err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		if count != 0 {
			t.Fatalf("cache warning disguised database rollback: %s rows=%d", name, count)
		}
	}
}
