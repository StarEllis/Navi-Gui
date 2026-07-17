package repository

import (
	"testing"

	"navi-desktop/model"
)

func TestMergeEquivalentPeoplePreservesOneRelationPerMedia(t *testing.T) {
	db := newMediaRepoTestDB(t)
	library := model.Library{ID: "person-library", Name: "People", Path: "C:/people", Type: "movie"}
	mediaOne := model.Media{ID: "person-media-1", LibraryID: library.ID, Title: "One", FilePath: "C:/people/one.mp4", MediaType: "movie"}
	mediaTwo := model.Media{ID: "person-media-2", LibraryID: library.ID, Title: "Two", FilePath: "C:/people/two.mp4", MediaType: "movie"}
	simplified := model.Person{ID: "person-simplified", Name: "三田真铃"}
	traditional := model.Person{ID: "person-traditional", Name: "三田真鈴"}
	createAll(t, db, &library, &mediaOne, &mediaTwo, &simplified, &traditional)
	createAll(t, db,
		&model.MediaPerson{ID: "relation-preferred", MediaID: mediaOne.ID, PersonID: simplified.ID, Role: "actor"},
		&model.MediaPerson{ID: "relation-duplicate-same-media", MediaID: mediaOne.ID, PersonID: traditional.ID, Role: "actor"},
		&model.MediaPerson{ID: "relation-duplicate-other-media", MediaID: mediaTwo.ID, PersonID: traditional.ID, Role: "actor"},
	)

	stats, err := NewRepositories(db).Person.MergeEquivalentPeople()
	if err != nil {
		t.Fatalf("merge equivalent people: %v", err)
	}
	if stats.PeopleMerged != 1 || stats.RelationsMoved != 1 || stats.DuplicateRelations != 1 || stats.MediaReindexed != 2 {
		t.Fatalf("merge stats = %+v", stats)
	}

	var people []model.Person
	if err := db.Find(&people).Error; err != nil {
		t.Fatalf("load people: %v", err)
	}
	if len(people) != 1 || people[0].ID != simplified.ID {
		t.Fatalf("people = %+v", people)
	}
	for _, mediaID := range []string{mediaOne.ID, mediaTwo.ID} {
		var relations []model.MediaPerson
		if err := db.Where("media_id = ? AND role = ?", mediaID, "actor").Find(&relations).Error; err != nil {
			t.Fatalf("load media relations: %v", err)
		}
		if len(relations) != 1 || relations[0].PersonID != simplified.ID {
			t.Fatalf("relations for %s = %+v", mediaID, relations)
		}
	}
}
