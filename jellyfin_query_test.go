package main

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"navi-desktop/model"
	"navi-desktop/service"
)

func TestJellyfinItemsQueryCountIsBoundedAndDTOUsesPrefetchedState(t *testing.T) {
	app := newTestApp(t)
	seedJellyfinMovies(t, app, 1000)
	counter := attachQueryCounter(app)

	for _, test := range []struct {
		limit      int
		wantRows   int
		maxQueries int
	}{{1, 1, 4}, {100, 100, 4}, {1000, jellyfinMaxItems, 6}} {
		counter.Reset()
		request := httptest.NewRequest("GET", fmt.Sprintf("/Items?recursive=true&includeItemTypes=Movie&limit=%d", test.limit), nil)
		items, total, _, err := app.queryJellyfinItems(request)
		if err != nil {
			t.Fatalf("limit %d: %v", test.limit, err)
		}
		if len(items) != test.wantRows || total != 1000 {
			t.Fatalf("limit %d rows=%d total=%d", test.limit, len(items), total)
		}
		beforeDTO := len(counter.Snapshot())
		for _, item := range items {
			if _, err := app.jellyfinItemDTO(&DesktopSettings{}, item); err != nil {
				t.Fatalf("limit %d DTO: %v", test.limit, err)
			}
		}
		if got := len(counter.Snapshot()); got != beforeDTO || got > test.maxQueries {
			t.Fatalf("limit %d queries=%d beforeDTO=%d max=%d", test.limit, got, beforeDTO, test.maxQueries)
		}
	}
}

func TestJellyfinBatchIDsHandleEmptyMissingAndLargeInputs(t *testing.T) {
	app := newTestApp(t)
	seedJellyfinMovies(t, app, 1000)
	counter := attachQueryCounter(app)

	counter.Reset()
	items, err := app.resolveJellyfinItems(nil)
	if err != nil || len(items) != 0 || len(counter.Snapshot()) != 0 {
		t.Fatalf("empty IDs items=%d queries=%d err=%v", len(items), len(counter.Snapshot()), err)
	}

	ids := make([]string, 0, 1002)
	ids = append(ids, "media:missing")
	for i := 0; i < 1000; i++ {
		ids = append(ids, fmt.Sprintf("media:media-%06d", i))
	}
	ids = append(ids, "series:missing")
	counter.Reset()
	items, err = app.resolveJellyfinItems(ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1000 {
		t.Fatalf("large IDs rows=%d", len(items))
	}
	if got := len(counter.Snapshot()); got > 7 {
		t.Fatalf("large IDs queries=%d want<=7", got)
	}
}

func TestJellyfinFiltersPaginationStableSortAndUserIsolation(t *testing.T) {
	app := newTestApp(t)
	seedJellyfinMovies(t, app, 6)
	now := time.Now().UTC().Truncate(time.Second)
	rows := []interface{}{
		&model.Favorite{ID: "favorite-desktop", UserID: desktopUserID, MediaID: "media-000001", CreatedAt: now},
		&model.Favorite{ID: "favorite-other", UserID: "other-user", MediaID: "media-000002", CreatedAt: now},
		&model.WatchHistory{ID: "history-desktop", UserID: desktopUserID, MediaID: "media-000003", Completed: true, Position: 100, Duration: 100, UpdatedAt: now},
		&model.WatchHistory{ID: "history-other", UserID: "other-user", MediaID: "media-000004", Completed: true, Position: 100, Duration: 100, UpdatedAt: now},
	}
	for _, row := range rows {
		if err := app.db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}

	favorites, total, _, err := app.queryJellyfinItems(httptest.NewRequest("GET", "/Items?recursive=true&includeItemTypes=Movie&isFavorite=true&limit=10", nil))
	if err != nil || total != 1 || len(favorites) != 1 || favorites[0].Media.ID != "media-000001" {
		t.Fatalf("favorite filter total=%d items=%v err=%v", total, mediaIDs(favorites), err)
	}
	played, total, _, err := app.queryJellyfinItems(httptest.NewRequest("GET", "/Items?recursive=true&includeItemTypes=Movie&isPlayed=true&limit=10", nil))
	if err != nil || total != 1 || len(played) != 1 || played[0].Media.ID != "media-000003" {
		t.Fatalf("played filter total=%d items=%v err=%v", total, mediaIDs(played), err)
	}

	page, total, start, err := app.queryJellyfinItems(httptest.NewRequest("GET", "/Items?recursive=true&includeItemTypes=Movie&searchTerm=Movie&sortBy=SortName&sortOrder=Ascending&startIndex=2&limit=2", nil))
	if err != nil || total != 6 || start != 2 || strings.Join(mediaIDs(page), ",") != "media-000002,media-000003" {
		t.Fatalf("stable page total=%d start=%d items=%v err=%v", total, start, mediaIDs(page), err)
	}
	all, err := app.resolveJellyfinItems([]string{"media:media-000002"})
	if err != nil || len(all) != 1 || all[0].UserData["IsFavorite"] != false || all[0].UserData["Played"] != false {
		t.Fatalf("other-user state leaked: %#v err=%v", all, err)
	}
}

func TestJellyfinSeriesStatsAndMissingAssociations(t *testing.T) {
	app := newTestApp(t)
	library := seedJellyfinSeries(t, app, 1, 3)
	if err := app.db.Model(&model.Media{}).Where("id = ?", "episode-0000-0002").Update("season_num", 2).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.db.Model(&model.Series{}).Where("id = ?", "series-0000").Updates(map[string]interface{}{"episode_count": 99, "season_count": 99}).Error; err != nil {
		t.Fatal(err)
	}
	items, total, _, err := app.queryJellyfinItems(httptest.NewRequest("GET", "/Items?recursive=true&includeItemTypes=Series&limit=10", nil))
	if err != nil || total != 1 || len(items) != 1 {
		t.Fatalf("series total=%d rows=%d err=%v", total, len(items), err)
	}
	if items[0].Series.EpisodeCount != 3 || items[0].Series.SeasonCount != 2 {
		t.Fatalf("series stats episodes=%d seasons=%d", items[0].Series.EpisodeCount, items[0].Series.SeasonCount)
	}

	orphan := model.Media{ID: "orphan-episode", LibraryID: library.ID, SeriesID: "missing-series", Title: "Orphan", FilePath: "C:/tv/orphan.mkv", MediaType: "episode"}
	if err := app.db.Create(&orphan).Error; err != nil {
		t.Fatal(err)
	}
	missing, err := app.resolveJellyfinItems([]string{"media:" + orphan.ID})
	if err != nil || len(missing) != 1 {
		t.Fatalf("missing association rows=%d err=%v", len(missing), err)
	}
	if _, err := app.jellyfinItemDTO(&DesktopSettings{}, missing[0]); err != nil {
		t.Fatalf("missing association DTO: %v", err)
	}
}

func TestJellyfinResumeAndLatestUseBoundedSQLQueries(t *testing.T) {
	app := newTestApp(t)
	seedJellyfinMovies(t, app, 5)
	now := time.Now().UTC().Truncate(time.Second)
	histories := []model.WatchHistory{
		{ID: "resume-old", UserID: desktopUserID, MediaID: "media-000000", Position: 10, Duration: 100, UpdatedAt: now.Add(-time.Hour)},
		{ID: "resume-new", UserID: desktopUserID, MediaID: "media-000001", Position: 20, Duration: 100, UpdatedAt: now},
		{ID: "resume-complete", UserID: desktopUserID, MediaID: "media-000002", Completed: true, UpdatedAt: now.Add(time.Hour)},
		{ID: "resume-other", UserID: "other-user", MediaID: "media-000003", Position: 30, Duration: 100, UpdatedAt: now.Add(2 * time.Hour)},
	}
	if err := app.db.Create(&histories).Error; err != nil {
		t.Fatal(err)
	}
	counter := attachQueryCounter(app)
	counter.Reset()
	resume, total, start, err := app.queryJellyfinResumeItems(httptest.NewRequest("GET", "/Users/desktop/Items/Resume?startIndex=0&limit=10", nil))
	if err != nil || total != 2 || start != 0 || strings.Join(mediaIDs(resume), ",") != "media-000001,media-000000" {
		t.Fatalf("resume total=%d rows=%v err=%v", total, mediaIDs(resume), err)
	}
	if got := len(counter.Snapshot()); got != 4 {
		t.Fatalf("resume queries=%d", got)
	}

	counter.Reset()
	latest, err := app.queryJellyfinLatestItems(httptest.NewRequest("GET", "/Items/Latest?limit=2", nil))
	if err != nil || strings.Join(mediaIDs(latest), ",") != "media-000004,media-000003" {
		t.Fatalf("latest rows=%v err=%v", mediaIDs(latest), err)
	}
	if got := len(counter.Snapshot()); got != 4 {
		t.Fatalf("latest queries=%d", got)
	}
}

func TestRecommendationCandidatesAreBoundedFilteredAndNeverRandomSorted(t *testing.T) {
	for _, count := range []int{100, 1000} {
		t.Run(fmt.Sprintf("library_%d", count), func(t *testing.T) {
			app := newTestApp(t)
			library := seedJellyfinMovies(t, app, count)
			if err := app.db.Model(&model.Media{}).Where("library_id = ?", library.ID).Updates(map[string]interface{}{"genres": "Action", "rating": 8.0}).Error; err != nil {
				t.Fatal(err)
			}
			counter := attachQueryCounter(app)
			response, err := app.GetDetailRecommendations("media-000000", 12)
			if err != nil {
				t.Fatal(err)
			}
			seen := make(map[string]bool)
			items := append(append([]service.RelatedMediaItem(nil), response.ContinueWatching...), response.MoreLikeThis...)
			if len(items) > 12 {
				t.Fatalf("recommendations=%d want<=12", len(items))
			}
			for _, item := range items {
				if seen[item.Media.ID] {
					t.Fatalf("duplicate recommendation %s", item.Media.ID)
				}
				seen[item.Media.ID] = true
				if item.Media.LibraryID != library.ID || item.Media.MediaType != "movie" || item.Media.ID == "media-000000" {
					t.Fatalf("invalid candidate %#v", item.Media)
				}
			}
			queries := counter.Snapshot()
			if len(queries) > 15 {
				t.Fatalf("queries=%d want<=15", len(queries))
			}
			for _, sql := range queries {
				if strings.Contains(strings.ToUpper(sql), "ORDER BY RANDOM(") {
					t.Fatalf("unbounded random sort: %s", sql)
				}
			}
		})
	}

	t.Run("source_only", func(t *testing.T) {
		app := newTestApp(t)
		seedJellyfinMovies(t, app, 1)
		response, err := app.GetDetailRecommendations("media-000000", 12)
		if err != nil || response == nil || len(response.ContinueWatching)+len(response.MoreLikeThis) != 0 {
			t.Fatalf("response=%#v err=%v", response, err)
		}
	})
}

func mediaIDs(items []*jellyfinResolvedItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if item != nil && item.Media != nil {
			ids = append(ids, item.Media.ID)
		}
	}
	return ids
}
