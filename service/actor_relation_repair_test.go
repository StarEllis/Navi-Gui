package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"navi-desktop/model"
)

func TestPersistQuickMediaSynchronizesActorsBeforeMetadataCompletion(t *testing.T) {
	scanner, db, repos := newScannerRetryTestService(t)
	scanner.deferMetadata = true
	mediaDir := t.TempDir()
	mediaPath := filepath.Join(mediaDir, "SONE-065.mp4")
	nfoPath := filepath.Join(mediaDir, "SONE-065.nfo")
	if err := os.WriteFile(mediaPath, []byte("video"), 0o644); err != nil {
		t.Fatalf("write media: %v", err)
	}
	if err := os.WriteFile(nfoPath, []byte(`<movie><title>SONE-065</title><actor><name>三田真铃</name></actor></movie>`), 0o644); err != nil {
		t.Fatalf("write nfo: %v", err)
	}

	library := model.Library{ID: "quick-actor-library", Name: "Actors", Path: mediaDir, Type: "movie"}
	if err := repos.Library.Create(&library); err != nil {
		t.Fatalf("create library: %v", err)
	}
	media := &model.Media{
		ID:            "quick-actor-media",
		LibraryID:     library.ID,
		Title:         "SONE-065",
		FilePath:      mediaPath,
		MediaType:     "movie",
		MetadataPhase: MetadataPhaseQuick,
	}
	if err := scanner.persistQuickMedia(media); err != nil {
		t.Fatalf("persist quick media: %v", err)
	}

	var relationCount int64
	if err := db.Model(&model.MediaPerson{}).
		Where("media_id = ? AND role = ?", media.ID, "actor").
		Count(&relationCount).Error; err != nil {
		t.Fatalf("count actor relations: %v", err)
	}
	if relationCount != 1 {
		t.Fatalf("actor relations = %d, want 1 before metadata completion", relationCount)
	}
}

func TestRepairMissingActorRelationsCoalescesSimplifiedAndTraditionalNames(t *testing.T) {
	scanner, db, repos := newScannerRetryTestService(t)
	mediaDir := t.TempDir()
	library := model.Library{ID: "repair-actor-library", Name: "Actors", Path: mediaDir, Type: "movie"}
	if err := repos.Library.Create(&library); err != nil {
		t.Fatalf("create library: %v", err)
	}

	for index, actorName := range []string{"三田真铃", "三田真鈴", "濑名光", "瀨名光", "後藤里香", "后藤里香"} {
		stem := "movie-" + string(rune('a'+index))
		mediaPath := filepath.Join(mediaDir, stem+".mp4")
		nfoPath := filepath.Join(mediaDir, stem+".nfo")
		if err := os.WriteFile(mediaPath, []byte("video"), 0o644); err != nil {
			t.Fatalf("write media: %v", err)
		}
		nfo := `<movie><title>` + stem + `</title><actor><name>` + actorName + `</name></actor></movie>`
		if index == 0 {
			nfo = "root-adjacent text\n<movie><title>" + stem + "</title><actor><name>" + actorName + "</name></actor><7bad>broken</7bad></movie>"
		}
		if err := os.WriteFile(nfoPath, []byte(nfo), 0o644); err != nil {
			t.Fatalf("write nfo: %v", err)
		}
		if index == 0 {
			if _, err := scanner.nfoService.GetActorMetadataFromNFO(nfoPath); err == nil {
				t.Fatal("strict actor parser unexpectedly accepted root-adjacent text")
			}
			actors, _, err := scanner.nfoService.GetActorsFromNFO(nfoPath)
			if err != nil || len(actors) != 1 {
				t.Fatalf("tolerant actor parser = %+v, %v; want one actor", actors, err)
			}
		}
		media := &model.Media{
			ID:            stem,
			LibraryID:     library.ID,
			Title:         stem,
			FilePath:      mediaPath,
			MediaType:     "movie",
			MetadataPhase: MetadataPhaseQuick,
		}
		if err := repos.Media.Create(media); err != nil {
			t.Fatalf("create media: %v", err)
		}
	}
	director := model.Person{ID: "director-existing", Name: "Existing Director"}
	if err := db.Create(&director).Error; err != nil {
		t.Fatalf("create director: %v", err)
	}
	if err := db.Create(&model.MediaPerson{
		ID: "director-relation", MediaID: "movie-a", PersonID: director.ID, Role: "director",
	}).Error; err != nil {
		t.Fatalf("create director relation: %v", err)
	}

	stats, err := scanner.RepairMissingActorRelations(context.Background())
	if err != nil {
		t.Fatalf("repair actor relations: %v", err)
	}
	if stats.Candidates != 6 || stats.Synced != 6 {
		t.Fatalf("repair stats = %+v", stats)
	}

	var people []model.Person
	if err := db.Where("name IN ?", []string{"三田真铃", "三田真鈴", "濑名光", "瀨名光", "後藤里香", "后藤里香"}).
		Order("name ASC").
		Find(&people).Error; err != nil {
		t.Fatalf("load people: %v", err)
	}
	if len(people) != 3 {
		t.Fatalf("people = %+v, want three canonical actors", people)
	}

	var relations []model.MediaPerson
	if err := db.Where("role = ?", "actor").Order("media_id ASC").Find(&relations).Error; err != nil {
		t.Fatalf("load actor relations: %v", err)
	}
	if len(relations) != 6 ||
		relations[0].PersonID != relations[1].PersonID ||
		relations[2].PersonID != relations[3].PersonID ||
		relations[4].PersonID != relations[5].PersonID ||
		relations[0].PersonID == relations[2].PersonID ||
		relations[0].PersonID == relations[4].PersonID ||
		relations[2].PersonID == relations[4].PersonID {
		t.Fatalf("relations = %+v", relations)
	}
	var directorCount int64
	if err := db.Model(&model.MediaPerson{}).
		Where("media_id = ? AND role = ?", "movie-a", "director").
		Count(&directorCount).Error; err != nil {
		t.Fatalf("count director relations: %v", err)
	}
	if directorCount != 1 {
		t.Fatalf("director relations = %d, want preserved", directorCount)
	}
}
