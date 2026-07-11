package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"navi-desktop/config"
	"navi-desktop/model"
	"navi-desktop/repository"
)

func newScannerRetryTestService(t *testing.T) (*ScannerService, *gorm.DB, *repository.Repositories) {
	t.Helper()
	dbName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", dbName)), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := model.AutoMigrate(db); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	for _, statement := range []string{
		"CREATE UNIQUE INDEX idx_libraries_path_key_active ON libraries(path_key) WHERE deleted_at IS NULL AND path_key <> ''",
		"CREATE UNIQUE INDEX idx_media_library_path_active ON media(library_id, path_key) WHERE deleted_at IS NULL AND path_key <> ''",
		"CREATE UNIQUE INDEX idx_series_library_folder_active ON series(library_id, folder_path_key) WHERE deleted_at IS NULL AND folder_path_key <> ''",
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("create sqlite index: %v", err)
		}
	}
	repos := repository.NewRepositories(db)
	scanner := NewScannerService(repos.Media, repos.Series, repos.Person, repos.MediaPerson, config.NewConfig(), zap.NewNop().Sugar())
	t.Cleanup(func() { _ = scanner.Shutdown(context.Background()) })
	return scanner.cloneForScanContext(context.Background(), "retry-test"), db, repos
}

func TestPersistQuickMediaRetriesThreeTimesBeforeSuccess(t *testing.T) {
	scanner, db, repos := newScannerRetryTestService(t)
	library := model.Library{ID: "library-retry", Name: "Retry", Path: t.TempDir(), Type: "movie"}
	if err := repos.Library.Create(&library); err != nil {
		t.Fatalf("create library: %v", err)
	}

	attempts := 0
	callbackName := "test:fail_first_three_media_creates"
	if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "media" {
			attempts++
			if attempts <= scanWriteRetryCount {
				tx.AddError(errors.New("temporary database write failure"))
			}
		}
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callbackName) })

	media := &model.Media{LibraryID: library.ID, Title: "Retry", FilePath: library.Path + "/retry.mkv", MediaType: "movie"}
	if err := scanner.persistQuickMedia(media); err != nil {
		t.Fatalf("persist after retries: %v", err)
	}
	if attempts != scanWriteRetryCount+1 {
		t.Fatalf("attempts = %d, want %d", attempts, scanWriteRetryCount+1)
	}
	if partialErr := scanner.partialScanError(); partialErr != nil {
		t.Fatalf("successful retry was recorded as partial failure: %v", partialErr)
	}
}

func TestPersistQuickMediaFinalFailureMarksScanPartial(t *testing.T) {
	scanner, db, repos := newScannerRetryTestService(t)
	library := model.Library{ID: "library-fail", Name: "Fail", Path: t.TempDir(), Type: "movie"}
	if err := repos.Library.Create(&library); err != nil {
		t.Fatalf("create library: %v", err)
	}

	attempts := 0
	callbackName := "test:fail_all_media_creates"
	if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "media" {
			attempts++
			tx.AddError(errors.New("persistent database write failure"))
		}
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callbackName) })

	media := &model.Media{LibraryID: library.ID, Title: "Fail", FilePath: library.Path + "/fail.mkv", MediaType: "movie"}
	if err := scanner.persistQuickMedia(media); err == nil {
		t.Fatal("persist unexpectedly succeeded")
	}
	if attempts != scanWriteRetryCount+1 {
		t.Fatalf("attempts = %d, want %d", attempts, scanWriteRetryCount+1)
	}
	if partialErr := scanner.partialScanError(); !IsScanPartial(partialErr) {
		t.Fatalf("final failure did not mark scan partial: %v", partialErr)
	}
}

func TestScanPartialFailureUsesIncompleteTerminalState(t *testing.T) {
	err := &ScanPartialError{Failed: 1, Err: errors.New("write failed")}
	if event := scanTerminalEvent(err); event != EventScanIncomplete {
		t.Fatalf("terminal event = %q, want %q", event, EventScanIncomplete)
	}
	if stage := scanFailureStage(err); stage != "database" {
		t.Fatalf("failure stage = %q, want database", stage)
	}
}
