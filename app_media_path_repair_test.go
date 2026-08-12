package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"navi-desktop/model"
	"navi-desktop/repository"
	"navi-desktop/service"
)

func TestGetMediaDetailBundleRepairsRenamedOnlyVideoFile(t *testing.T) {
	app, mediaID, oldPath := newDeleteLibraryAppFixture(t, filepath.Join(t.TempDir(), "cache"))
	if err := os.Remove(oldPath); err != nil {
		t.Fatalf("remove old media file: %v", err)
	}
	newPath := filepath.Join(filepath.Dir(oldPath), "renamed-movie.mkv")
	if err := os.WriteFile(newPath, []byte("replacement media"), 0644); err != nil {
		t.Fatalf("write replacement media file: %v", err)
	}

	bundle, err := app.GetMediaDetailBundle(mediaID)
	if err != nil {
		t.Fatalf("GetMediaDetailBundle() error = %v", err)
	}
	if bundle.Detail.FilePath != newPath {
		t.Fatalf("detail file path = %q, want %q", bundle.Detail.FilePath, newPath)
	}
	if len(bundle.Files) != 1 || bundle.Files[0] != newPath {
		t.Fatalf("bundle files = %#v, want [%q]", bundle.Files, newPath)
	}

	stored, err := app.repos.Media.FindByID(mediaID)
	if err != nil {
		t.Fatalf("reload media: %v", err)
	}
	if stored.FilePath != newPath {
		t.Fatalf("stored file path = %q, want %q", stored.FilePath, newPath)
	}
	if stored.PathKey != model.NormalizePathKey(newPath) {
		t.Fatalf("stored path key = %q, want %q", stored.PathKey, model.NormalizePathKey(newPath))
	}
}

func TestRepairMissingMediaFilePathUsesUniqueMatchingCode(t *testing.T) {
	app, mediaID, oldPath := newDeleteLibraryAppFixture(t, filepath.Join(t.TempDir(), "cache"))
	media, err := app.repos.Media.FindByID(mediaID)
	if err != nil {
		t.Fatalf("load media: %v", err)
	}
	media.Code = "IPX-536"
	media.FilePath = filepath.Join(filepath.Dir(oldPath), "IPX-536-old.mkv")
	if err := app.repos.Media.Update(media); err != nil {
		t.Fatalf("seed missing media path: %v", err)
	}
	if err := os.Remove(oldPath); err != nil {
		t.Fatalf("remove old media file: %v", err)
	}

	matchingPath := filepath.Join(filepath.Dir(oldPath), "IPX-536-4K-C.mp4")
	otherPath := filepath.Join(filepath.Dir(oldPath), "SSIS-001.mp4")
	for _, path := range []string{matchingPath, otherPath} {
		if err := os.WriteFile(path, []byte("replacement media"), 0644); err != nil {
			t.Fatalf("write replacement media file %s: %v", path, err)
		}
	}

	repaired, err := app.repairMissingMediaFilePath(media)
	if err != nil {
		t.Fatalf("repairMissingMediaFilePath() error = %v", err)
	}
	if !repaired {
		t.Fatal("repairMissingMediaFilePath() repaired = false, want true")
	}
	if media.FilePath != matchingPath {
		t.Fatalf("repaired file path = %q, want %q", media.FilePath, matchingPath)
	}
}

func TestRepairMissingMediaFilePathLeavesAmbiguousCandidatesUnchanged(t *testing.T) {
	app, mediaID, oldPath := newDeleteLibraryAppFixture(t, filepath.Join(t.TempDir(), "cache"))
	media, err := app.repos.Media.FindByID(mediaID)
	if err != nil {
		t.Fatalf("load media: %v", err)
	}
	media.Code = "IPX-536"
	media.FilePath = filepath.Join(filepath.Dir(oldPath), "IPX-536-old.mkv")
	if err := app.repos.Media.Update(media); err != nil {
		t.Fatalf("seed missing media path: %v", err)
	}
	if err := os.Remove(oldPath); err != nil {
		t.Fatalf("remove old media file: %v", err)
	}

	for _, name := range []string{"IPX-536-A.mp4", "IPX-536-B.mkv"} {
		path := filepath.Join(filepath.Dir(oldPath), name)
		if err := os.WriteFile(path, []byte("replacement media"), 0644); err != nil {
			t.Fatalf("write replacement media file %s: %v", path, err)
		}
	}

	repaired, err := app.repairMissingMediaFilePath(media)
	if err != nil {
		t.Fatalf("repairMissingMediaFilePath() error = %v", err)
	}
	if repaired {
		t.Fatal("repairMissingMediaFilePath() repaired = true, want false")
	}
	if media.FilePath == "" || filepath.Base(media.FilePath) != "IPX-536-old.mkv" {
		t.Fatalf("ambiguous repair changed file path to %q", media.FilePath)
	}
}

func TestRepairMissingMediaFilePathSyncsActorsFromReplacementNFO(t *testing.T) {
	app, mediaID, oldPath := newDeleteLibraryAppFixture(t, filepath.Join(t.TempDir(), "cache"))
	oldActor := model.Person{ID: "old-actor", Name: "Old Actor"}
	if err := app.db.Create(&oldActor).Error; err != nil {
		t.Fatalf("seed old actor: %v", err)
	}
	if err := app.db.Create(&model.MediaPerson{
		ID:       "old-actor-relation",
		MediaID:  mediaID,
		PersonID: oldActor.ID,
		Role:     "actor",
	}).Error; err != nil {
		t.Fatalf("seed old actor relation: %v", err)
	}
	if err := repository.RefreshMediaSearchIndex(app.db, mediaID); err != nil {
		t.Fatalf("seed old actor search index: %v", err)
	}

	if err := os.Remove(oldPath); err != nil {
		t.Fatalf("remove old media file: %v", err)
	}
	newPath := filepath.Join(filepath.Dir(oldPath), "renamed-movie.mkv")
	if err := os.WriteFile(newPath, []byte("replacement media"), 0644); err != nil {
		t.Fatalf("write replacement media file: %v", err)
	}
	newNFOPath := strings.TrimSuffix(newPath, filepath.Ext(newPath)) + ".nfo"
	if err := os.WriteFile(newNFOPath, []byte(`<movie><title>Movie</title><actor><name>New Actor</name></actor></movie>`), 0644); err != nil {
		t.Fatalf("write replacement NFO: %v", err)
	}

	app.scanner = service.NewScannerService(
		app.repos.Media,
		app.repos.Series,
		app.repos.Person,
		app.repos.MediaPerson,
		nil,
		app.logger,
	)
	defer func() {
		if err := app.scanner.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown scanner: %v", err)
		}
	}()

	if _, err := app.GetMediaDetailBundle(mediaID); err != nil {
		t.Fatalf("GetMediaDetailBundle() error = %v", err)
	}

	var relations []model.MediaPerson
	if err := app.db.Preload("Person").Where("media_id = ? AND role = ?", mediaID, "actor").Find(&relations).Error; err != nil {
		t.Fatalf("load actor relations: %v", err)
	}
	if len(relations) != 1 || relations[0].Person.Name != "New Actor" {
		t.Fatalf("actor relations = %+v, want New Actor", relations)
	}

	stored, err := app.repos.Media.FindByID(mediaID)
	if err != nil {
		t.Fatalf("reload media: %v", err)
	}
	if !strings.Contains(stored.SearchText, "new actor") || strings.Contains(stored.SearchText, "old actor") {
		t.Fatalf("stored search text = %q, want only replacement actor", stored.SearchText)
	}
}
