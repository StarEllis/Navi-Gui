package repository

import (
	"testing"

	"navi-desktop/model"
)

const atomicLibraryID = "atomic-library"

func seedAtomicLibraryGraph(t *testing.T) (*Repositories, string) {
	t.Helper()
	db := newMediaRepoTestDB(t)
	repos := NewRepositories(db)

	user := model.User{ID: "library-user", Username: "library-user", Password: "hash"}
	library := model.Library{ID: atomicLibraryID, Name: "Atomic", Path: t.TempDir(), Type: "tvshow"}
	series := model.Series{ID: "library-series", LibraryID: library.ID, Title: "Series", FolderPath: "series-folder", EpisodeCount: 1}
	media := model.Media{ID: "library-media", LibraryID: library.ID, SeriesID: series.ID, Title: "Episode", FilePath: "episode.mkv", MediaType: "episode"}
	personMedia := model.Person{ID: "person-media", Name: "Media Actor"}
	personSeries := model.Person{ID: "person-series", Name: "Series Actor"}
	scrapeTask := model.ScrapeTask{ID: "library-scrape", URL: "local", Source: "local", MediaID: media.ID, SeriesID: series.ID}

	for _, value := range []interface{}{
		&user,
		&library,
		&series,
		&media,
		&personMedia,
		&personSeries,
		&model.MediaPerson{ID: "media-person", MediaID: media.ID, PersonID: personMedia.ID, Role: "actor"},
		&model.MediaPerson{ID: "series-person", SeriesID: series.ID, PersonID: personSeries.ID, Role: "actor"},
		&model.Favorite{ID: "library-favorite", UserID: user.ID, MediaID: media.ID},
		&model.WatchHistory{ID: "library-history", UserID: user.ID, MediaID: media.ID},
		&model.TranscodeTask{ID: "library-transcode", MediaID: media.ID},
		&scrapeTask,
		&model.ScrapeHistory{ID: "library-scrape-history", TaskID: scrapeTask.ID, Action: "created"},
		&model.MediaShare{ID: "library-share", UserID: user.ID, GroupID: "group", SeriesID: series.ID},
		&model.MediaLike{ID: "library-like", UserID: user.ID, SeriesID: series.ID},
		&model.MediaRecommendation{ID: "library-recommendation", FromUserID: user.ID, ToUserID: user.ID, SeriesID: series.ID},
		&model.ShareLink{ID: "library-link", Code: "atomic", CreatedBy: user.ID, SeriesID: series.ID},
	} {
		if err := db.Create(value).Error; err != nil {
			t.Fatalf("seed %T: %v", value, err)
		}
	}
	return repos, media.ID
}

func assertAtomicLibraryCoreRows(t *testing.T, repos *Repositories, want int64) {
	t.Helper()
	for name, target := range map[string]interface{}{
		"library":       &model.Library{},
		"series":        &model.Series{},
		"media":         &model.Media{},
		"favorite":      &model.Favorite{},
		"watch history": &model.WatchHistory{},
	} {
		var count int64
		if err := repos.db.Model(target).Count(&count).Error; err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		if count != want {
			t.Fatalf("expected %s rows=%d, got %d", name, want, count)
		}
	}
}

func TestDeleteLibraryAtomicRollsBackMediaAssociationFailure(t *testing.T) {
	repos, _ := seedAtomicLibraryGraph(t)
	trigger := `
		CREATE TRIGGER fail_library_favorite_delete
		BEFORE DELETE ON favorites
		BEGIN
			SELECT RAISE(ABORT, 'injected media association failure');
		END`
	if err := repos.db.Exec(trigger).Error; err != nil {
		t.Fatalf("create association trigger: %v", err)
	}

	if _, err := repos.DeleteLibraryAtomic(atomicLibraryID); err == nil {
		t.Fatal("expected media association cleanup to fail")
	}
	assertAtomicLibraryCoreRows(t, repos, 1)
}

func TestDeleteLibraryAtomicRollsBackSeriesDeleteFailure(t *testing.T) {
	repos, _ := seedAtomicLibraryGraph(t)
	trigger := `
		CREATE TRIGGER fail_library_series_delete
		BEFORE DELETE ON series
		BEGIN
			SELECT RAISE(ABORT, 'injected series delete failure');
		END`
	if err := repos.db.Exec(trigger).Error; err != nil {
		t.Fatalf("create series trigger: %v", err)
	}

	if _, err := repos.DeleteLibraryAtomic(atomicLibraryID); err == nil {
		t.Fatal("expected series deletion to fail")
	}
	assertAtomicLibraryCoreRows(t, repos, 1)
}

func TestDeleteLibraryAtomicRollsBackLibraryDeleteFailure(t *testing.T) {
	repos, _ := seedAtomicLibraryGraph(t)
	trigger := `
		CREATE TRIGGER fail_library_row_delete
		BEFORE DELETE ON libraries
		BEGIN
			SELECT RAISE(ABORT, 'injected library delete failure');
		END`
	if err := repos.db.Exec(trigger).Error; err != nil {
		t.Fatalf("create library trigger: %v", err)
	}

	if _, err := repos.DeleteLibraryAtomic(atomicLibraryID); err == nil {
		t.Fatal("expected library deletion to fail")
	}
	assertAtomicLibraryCoreRows(t, repos, 1)
}

func TestDeleteLibraryAtomicRemovesCompleteDatabaseGraph(t *testing.T) {
	repos, mediaID := seedAtomicLibraryGraph(t)
	result, err := repos.DeleteLibraryAtomic(atomicLibraryID)
	if err != nil {
		t.Fatalf("delete library: %v", err)
	}
	if len(result.MediaIDs) != 1 || result.MediaIDs[0] != mediaID {
		t.Fatalf("unexpected deleted media IDs: %v", result.MediaIDs)
	}

	for name, target := range map[string]interface{}{
		"library":            &model.Library{},
		"series":             &model.Series{},
		"media":              &model.Media{},
		"media people":       &model.MediaPerson{},
		"favorites":          &model.Favorite{},
		"watch histories":    &model.WatchHistory{},
		"transcode tasks":    &model.TranscodeTask{},
		"scrape tasks":       &model.ScrapeTask{},
		"scrape histories":   &model.ScrapeHistory{},
		"series shares":      &model.MediaShare{},
		"series likes":       &model.MediaLike{},
		"recommendations":    &model.MediaRecommendation{},
		"series share links": &model.ShareLink{},
		"orphaned people":    &model.Person{},
	} {
		var count int64
		if err := repos.db.Model(target).Count(&count).Error; err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		if count != 0 {
			t.Fatalf("expected %s to be empty, got %d", name, count)
		}
	}
}
