package repository

import (
	"fmt"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"navi-desktop/model"
)

func newMediaRepoTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dbName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", dbName)), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.Exec("PRAGMA foreign_keys = OFF").Error; err != nil {
		t.Fatalf("disable sqlite foreign keys failed: %v", err)
	}
	if err := model.AutoMigrate(db); err != nil {
		t.Fatalf("migrate sqlite failed: %v", err)
	}
	return db
}

func seedMediaDeleteGraph(t *testing.T, db *gorm.DB) (library model.Library, keepMedia model.Media, deleteMedia model.Media) {
	t.Helper()

	user := model.User{ID: "user-1", Username: "user", Password: "hash"}
	library = model.Library{ID: "library-1", Name: "Library", Path: "C:/media"}
	keepMedia = model.Media{ID: "media-keep", LibraryID: library.ID, Title: "Keep", FilePath: "C:/media/keep.mp4"}
	deleteMedia = model.Media{ID: "media-delete", LibraryID: library.ID, Title: "Delete", FilePath: "C:/media/delete.mp4"}
	person := model.Person{ID: "person-delete", Name: "Actor"}
	tag := model.Tag{ID: "tag-1", Name: "Tag"}
	playlist := model.Playlist{ID: "playlist-1", UserID: user.ID, Name: "Playlist"}
	group := model.FamilyGroup{ID: "group-1", Name: "Group", OwnerID: user.ID}

	createAll(t, db, &user, &library, &keepMedia, &deleteMedia, &person, &tag, &playlist, &group)
	createAll(t, db,
		&model.MediaPerson{ID: "media-person-delete", MediaID: deleteMedia.ID, PersonID: person.ID, Role: "actor"},
		&model.WatchHistory{ID: "watch-delete", UserID: user.ID, MediaID: deleteMedia.ID},
		&model.Favorite{ID: "favorite-delete", UserID: user.ID, MediaID: deleteMedia.ID},
		&model.TranscodeTask{ID: "transcode-delete", MediaID: deleteMedia.ID},
		&model.PlaylistItem{ID: "playlist-item-delete", PlaylistID: playlist.ID, MediaID: deleteMedia.ID},
		&model.Bookmark{ID: "bookmark-delete", UserID: user.ID, MediaID: deleteMedia.ID, Title: "Bookmark"},
		&model.Comment{ID: "comment-delete", UserID: user.ID, MediaID: deleteMedia.ID, Content: "Comment"},
		&model.ContentRating{ID: "rating-delete", MediaID: deleteMedia.ID, Level: "PG"},
		&model.PlaybackStats{ID: "playback-delete", UserID: user.ID, MediaID: deleteMedia.ID, Date: "2026-06-22"},
		&model.VideoChapter{ID: "chapter-delete", MediaID: deleteMedia.ID, Title: "Chapter"},
		&model.VideoHighlight{ID: "highlight-delete", MediaID: deleteMedia.ID, Title: "Highlight"},
		&model.AIAnalysisTask{ID: "ai-delete", MediaID: deleteMedia.ID, TaskType: "scene_detect"},
		&model.CoverCandidate{ID: "cover-delete", MediaID: deleteMedia.ID, ImagePath: "cover.jpg"},
		&model.MediaTag{ID: "media-tag-delete", MediaID: deleteMedia.ID, TagID: tag.ID},
		&model.MediaShare{ID: "share-delete", UserID: user.ID, GroupID: group.ID, MediaID: deleteMedia.ID},
		&model.MediaLike{ID: "like-delete", UserID: user.ID, MediaID: deleteMedia.ID},
		&model.MediaRecommendation{ID: "recommendation-delete", FromUserID: user.ID, ToUserID: user.ID, MediaID: deleteMedia.ID},
		&model.ShareLink{ID: "share-link-delete", Code: "delete", CreatedBy: user.ID, MediaID: deleteMedia.ID},
	)

	createAll(t, db,
		&model.WatchHistory{ID: "watch-keep", UserID: user.ID, MediaID: keepMedia.ID},
		&model.Favorite{ID: "favorite-keep", UserID: user.ID, MediaID: keepMedia.ID},
	)
	return library, keepMedia, deleteMedia
}

func createAll(t *testing.T, db *gorm.DB, values ...interface{}) {
	t.Helper()

	for _, value := range values {
		if err := db.Create(value).Error; err != nil {
			t.Fatalf("create %T failed: %v", value, err)
		}
	}
}

func assertNoRowsForMedia(t *testing.T, db *gorm.DB, mediaID string) {
	t.Helper()

	tables := []string{
		"media_people",
		"watch_histories",
		"favorites",
		"transcode_tasks",
		"playlist_items",
		"bookmarks",
		"comments",
		"content_ratings",
		"playback_stats",
		"video_chapters",
		"video_highlights",
		"ai_analysis_tasks",
		"cover_candidates",
		"media_tags",
		"media_shares",
		"media_likes",
		"media_recommendations",
		"share_links",
	}

	for _, table := range tables {
		var count int64
		if err := db.Table(table).Where("media_id = ?", mediaID).Count(&count).Error; err != nil {
			t.Fatalf("count %s failed: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("expected %s rows for %s to be deleted, got %d", table, mediaID, count)
		}
	}
}

func assertRowsForMedia(t *testing.T, db *gorm.DB, table string, mediaID string, want int64) {
	t.Helper()

	var count int64
	if err := db.Table(table).Where("media_id = ?", mediaID).Count(&count).Error; err != nil {
		t.Fatalf("count %s failed: %v", table, err)
	}
	if count != want {
		t.Fatalf("expected %s rows for %s = %d, got %d", table, mediaID, want, count)
	}
}

func TestMediaRepoDeleteByIDDeletesAssociatedRows(t *testing.T) {
	db := newMediaRepoTestDB(t)
	repo := &MediaRepo{db: db}
	_, keepMedia, deleteMedia := seedMediaDeleteGraph(t, db)

	if err := repo.DeleteByID(deleteMedia.ID); err != nil {
		t.Fatalf("delete media failed: %v", err)
	}

	assertNoRowsForMedia(t, db, deleteMedia.ID)
	assertRowsForMedia(t, db, "watch_histories", keepMedia.ID, 1)
	assertRowsForMedia(t, db, "favorites", keepMedia.ID, 1)
}

func TestMediaRepoDeleteByIDsDeletesAssociatedRows(t *testing.T) {
	db := newMediaRepoTestDB(t)
	repo := &MediaRepo{db: db}
	_, _, deleteMedia := seedMediaDeleteGraph(t, db)

	deleted, err := repo.DeleteByIDs([]string{deleteMedia.ID})
	if err != nil {
		t.Fatalf("delete media failed: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected one media row deleted, got %d", deleted)
	}

	assertNoRowsForMedia(t, db, deleteMedia.ID)
}

func TestMediaRepoDeleteByLibraryIDDeletesAssociatedRowsBeforeMediaRows(t *testing.T) {
	db := newMediaRepoTestDB(t)
	repo := &MediaRepo{db: db}
	library, _, deleteMedia := seedMediaDeleteGraph(t, db)

	if err := repo.DeleteByLibraryID(library.ID); err != nil {
		t.Fatalf("delete library media failed: %v", err)
	}

	assertNoRowsForMedia(t, db, deleteMedia.ID)
}

func TestMediaRepoCleanOrphanedMediaAssociationsDeletesExistingOrphans(t *testing.T) {
	db := newMediaRepoTestDB(t)
	repo := &MediaRepo{db: db}
	_, keepMedia, deleteMedia := seedMediaDeleteGraph(t, db)

	if err := db.Unscoped().Delete(&model.Media{}, "id = ?", deleteMedia.ID).Error; err != nil {
		t.Fatalf("force delete media failed: %v", err)
	}

	cleaned, err := repo.CleanOrphanedMediaAssociations()
	if err != nil {
		t.Fatalf("clean orphaned associations failed: %v", err)
	}

	if cleaned.MediaPeople == 0 || cleaned.WatchHistories == 0 {
		t.Fatalf("expected orphan counts to include media people and watch histories, got %+v", cleaned)
	}
	if cleaned.People != 1 {
		t.Fatalf("expected one orphan person to be deleted, got %d", cleaned.People)
	}
	assertNoRowsForMedia(t, db, deleteMedia.ID)
	assertRowsForMedia(t, db, "watch_histories", keepMedia.ID, 1)
}

func TestMediaRepoDeleteByIDsIgnoresEmptyIDs(t *testing.T) {
	db := newMediaRepoTestDB(t)
	repo := &MediaRepo{db: db}

	deleted, err := repo.DeleteByIDs(nil)
	if err != nil {
		t.Fatalf("delete empty ids failed: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("expected no deleted rows, got %d", deleted)
	}
}

func TestMediaRepoCleanOrphanedMediaAssociationsSummaryTotal(t *testing.T) {
	cleaned := OrphanedMediaCleanupResult{
		MediaPeople:     1,
		WatchHistories:  2,
		Favorites:       3,
		TranscodeTasks:  4,
		PlaylistItems:   5,
		Bookmarks:       6,
		Comments:        7,
		ContentRatings:  8,
		PlaybackStats:   9,
		VideoChapters:   10,
		VideoHighlights: 11,
		AIAnalysisTasks: 12,
		CoverCandidates: 13,
		MediaTags:       14,
		MediaShares:     15,
		MediaLikes:      16,
		Recommendations: 17,
		ShareLinks:      18,
		People:          19,
	}

	if got, want := cleaned.Total(), int64(190); got != want {
		t.Fatalf("expected total %d, got %d (%s)", want, got, fmt.Sprintf("%+v", cleaned))
	}
}

func TestMediaRepoSearchHandlesQuotedKeyword(t *testing.T) {
	db := newMediaRepoTestDB(t)
	repo := &MediaRepo{db: db}
	createAll(t, db,
		&model.Media{ID: "search-1", Title: "Bob's Movie", OrigTitle: "Original", Genres: "Drama", Rating: 8.2},
		&model.Media{ID: "search-2", Title: "Other Movie", OrigTitle: "Other", Genres: "Drama", Rating: 9.1},
	)

	results, total, err := repo.Search("Bob's", 1, 10)
	if err != nil {
		t.Fatalf("search quoted keyword failed: %v", err)
	}
	if total != 1 {
		t.Fatalf("expected one quoted-keyword match, got total=%d results=%d", total, len(results))
	}
	if len(results) != 1 || results[0].ID != "search-1" {
		t.Fatalf("expected Bob's Movie result, got %+v", results)
	}
}
