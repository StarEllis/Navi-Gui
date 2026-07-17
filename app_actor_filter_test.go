package main

import (
	"os"
	"path/filepath"
	"testing"

	"navi-desktop/model"
)

func TestGetMediaDetailDoesNotPersistActorRelations(t *testing.T) {
	app := newTestApp(t)
	mediaDir := t.TempDir()
	mediaPath := filepath.Join(mediaDir, "SONE-065.mp4")
	nfoPath := filepath.Join(mediaDir, "SONE-065.nfo")
	if err := os.WriteFile(mediaPath, []byte("video"), 0o644); err != nil {
		t.Fatalf("write media: %v", err)
	}
	if err := os.WriteFile(nfoPath, []byte(`<movie><title>SONE-065</title><actor><name>三田真铃</name></actor></movie>`), 0o644); err != nil {
		t.Fatalf("write nfo: %v", err)
	}

	library := &model.Library{ID: "actor-detail-library", Name: "Actors", Path: mediaDir, Type: "movie"}
	if err := app.repos.Library.Create(library); err != nil {
		t.Fatalf("create library: %v", err)
	}
	media := &model.Media{
		ID:            "actor-detail-media",
		LibraryID:     library.ID,
		Title:         "SONE-065",
		FilePath:      mediaPath,
		MediaType:     "movie",
		MetadataPhase: "full",
	}
	if err := app.repos.Media.Create(media); err != nil {
		t.Fatalf("create media: %v", err)
	}

	detail, err := app.GetMediaDetail(media.ID)
	if err != nil {
		t.Fatalf("get media detail: %v", err)
	}
	if len(detail.Actors) != 1 || detail.Actors[0].Name != "三田真铃" {
		t.Fatalf("detail actors = %+v", detail.Actors)
	}

	var peopleCount int64
	if err := app.db.Model(&model.Person{}).Count(&peopleCount).Error; err != nil {
		t.Fatalf("count people: %v", err)
	}
	var relationCount int64
	if err := app.db.Model(&model.MediaPerson{}).Count(&relationCount).Error; err != nil {
		t.Fatalf("count media people: %v", err)
	}
	if peopleCount != 0 || relationCount != 0 {
		t.Fatalf("detail read persisted people=%d relations=%d", peopleCount, relationCount)
	}
}
