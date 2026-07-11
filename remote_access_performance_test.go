package main

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"navi-desktop/model"
	"navi-desktop/repository"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type queryCounterLogger struct {
	mu      sync.Mutex
	queries []string
}

func (l *queryCounterLogger) LogMode(logger.LogLevel) logger.Interface      { return l }
func (l *queryCounterLogger) Info(context.Context, string, ...interface{})  {}
func (l *queryCounterLogger) Warn(context.Context, string, ...interface{})  {}
func (l *queryCounterLogger) Error(context.Context, string, ...interface{}) {}
func (l *queryCounterLogger) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	l.mu.Lock()
	l.queries = append(l.queries, sql)
	l.mu.Unlock()
}

func (l *queryCounterLogger) Reset() {
	l.mu.Lock()
	l.queries = nil
	l.mu.Unlock()
}

func (l *queryCounterLogger) Snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.queries...)
}

func attachQueryCounter(app *App) *queryCounterLogger {
	counter := &queryCounterLogger{}
	app.db = app.db.Session(&gorm.Session{Logger: counter})
	app.repos = repository.NewRepositories(app.db)
	return counter
}

func seedJellyfinMovies(tb testing.TB, app *App, count int) *model.Library {
	tb.Helper()
	library := &model.Library{ID: fmt.Sprintf("library-%d", count), Name: "Movies", Path: fmt.Sprintf("C:/media/%d", count), Type: "movie"}
	if err := app.repos.Library.Create(library); err != nil {
		tb.Fatalf("create library: %v", err)
	}
	const batchSize = 200
	for start := 0; start < count; start += batchSize {
		end := start + batchSize
		if end > count {
			end = count
		}
		media := make([]model.Media, 0, end-start)
		for i := start; i < end; i++ {
			media = append(media, model.Media{
				ID:        fmt.Sprintf("media-%06d", i),
				LibraryID: library.ID,
				Title:     fmt.Sprintf("Movie %06d", i),
				FilePath:  fmt.Sprintf("C:/media/%d/movie-%06d.mkv", count, i),
				MediaType: "movie",
				CreatedAt: time.Unix(int64(i+1), 0).UTC(),
			})
		}
		if err := app.db.Create(&media).Error; err != nil {
			tb.Fatalf("create media batch: %v", err)
		}
	}
	return library
}

func seedJellyfinSeries(tb testing.TB, app *App, seriesCount, episodesPerSeries int) *model.Library {
	tb.Helper()
	library := &model.Library{ID: "library-series", Name: "TV", Path: "C:/tv", Type: "tvshow"}
	if err := app.repos.Library.Create(library); err != nil {
		tb.Fatalf("create series library: %v", err)
	}
	for i := 0; i < seriesCount; i++ {
		series := model.Series{
			ID:           fmt.Sprintf("series-%04d", i),
			LibraryID:    library.ID,
			Title:        fmt.Sprintf("Series %04d", i),
			FolderPath:   fmt.Sprintf("C:/tv/series-%04d", i),
			SeasonCount:  1,
			EpisodeCount: episodesPerSeries,
		}
		if err := app.repos.Series.Create(&series); err != nil {
			tb.Fatalf("create series: %v", err)
		}
		episodes := make([]model.Media, 0, episodesPerSeries)
		for episode := 0; episode < episodesPerSeries; episode++ {
			episodes = append(episodes, model.Media{
				ID:           fmt.Sprintf("episode-%04d-%04d", i, episode),
				LibraryID:    library.ID,
				SeriesID:     series.ID,
				Title:        series.Title,
				EpisodeTitle: fmt.Sprintf("Episode %04d", episode),
				FilePath:     fmt.Sprintf("C:/tv/series-%04d/episode-%04d.mkv", i, episode),
				MediaType:    "episode",
				SeasonNum:    1,
				EpisodeNum:   episode + 1,
			})
		}
		if err := app.db.Create(&episodes).Error; err != nil {
			tb.Fatalf("create episodes: %v", err)
		}
	}
	return library
}

func TestJellyfinPerformanceBaseline(t *testing.T) {
	for _, count := range []int{100, 1000, 10000} {
		t.Run(fmt.Sprintf("items_%d", count), func(t *testing.T) {
			app := newTestApp(t)
			seedJellyfinMovies(t, app, count)
			counter := attachQueryCounter(app)
			counter.Reset()
			items, total, _, err := app.queryJellyfinItems(httptest.NewRequest("GET", "/Items?recursive=true&includeItemTypes=Movie&limit=100", nil))
			if err != nil {
				t.Fatalf("query items: %v", err)
			}
			for _, item := range items {
				if _, err := app.jellyfinItemDTO(&DesktopSettings{}, item); err != nil {
					t.Fatalf("build item DTO: %v", err)
				}
			}
			queries := counter.Snapshot()
			t.Logf("rows=%d total=%d queries=%d", len(items), total, len(queries))
			if count == 10000 && len(queries) > 1 {
				t.Logf("items page plan=%v", explainDetails(t, app.db, queries[1]))
			}
		})
	}

	t.Run("series_list", func(t *testing.T) {
		app := newTestApp(t)
		seedJellyfinSeries(t, app, 100, 10)
		counter := attachQueryCounter(app)
		items, total, _, err := app.queryJellyfinItems(httptest.NewRequest("GET", "/Items?recursive=true&includeItemTypes=Series&limit=100", nil))
		if err != nil {
			t.Fatalf("query series: %v", err)
		}
		for _, item := range items {
			if _, err := app.jellyfinItemDTO(&DesktopSettings{}, item); err != nil {
				t.Fatalf("build series DTO: %v", err)
			}
		}
		t.Logf("rows=%d total=%d queries=%d", len(items), total, len(counter.Snapshot()))
	})

	t.Run("continue_watching_latest_and_detail", func(t *testing.T) {
		app := newTestApp(t)
		seedJellyfinMovies(t, app, 1000)
		histories := make([]model.WatchHistory, 0, 1000)
		for i := 0; i < 1000; i++ {
			histories = append(histories, model.WatchHistory{
				ID: fmt.Sprintf("history-%06d", i), UserID: desktopUserID, MediaID: fmt.Sprintf("media-%06d", i),
				Position: 10, Duration: 100, UpdatedAt: time.Unix(int64(i+1), 0).UTC(),
			})
		}
		if err := app.db.Create(&histories).Error; err != nil {
			t.Fatal(err)
		}
		counter := attachQueryCounter(app)

		counter.Reset()
		continued, err := app.repos.WatchHistory.ContinueWatching(desktopUserID, 100)
		if err != nil {
			t.Fatalf("continue watching: %v", err)
		}
		t.Logf("continue rows=%d queries=%d plan=%v", len(continued), len(counter.Snapshot()), explainDetails(t, app.db,
			"SELECT * FROM watch_histories WHERE user_id = ? AND completed = ? ORDER BY updated_at DESC LIMIT 100", desktopUserID, false))

		counter.Reset()
		latest, err := app.repos.Media.RecentNonEpisode(100)
		if err != nil {
			t.Fatalf("latest: %v", err)
		}
		t.Logf("latest rows=%d queries=%d plan=%v", len(latest), len(counter.Snapshot()), explainDetails(t, app.db,
			"SELECT * FROM media WHERE deleted_at IS NULL AND (series_id = '' OR series_id IS NULL) AND library_id != '' ORDER BY created_at DESC LIMIT 100"))

		counter.Reset()
		item, err := app.resolveJellyfinItem("media:media-000000")
		if err != nil {
			t.Fatalf("resolve detail: %v", err)
		}
		if _, err := app.jellyfinItemDTO(&DesktopSettings{}, item); err != nil {
			t.Fatalf("detail DTO: %v", err)
		}
		t.Logf("media detail queries=%d", len(counter.Snapshot()))
	})

	t.Run("recommendation_candidates", func(t *testing.T) {
		app := newTestApp(t)
		library := seedJellyfinMovies(t, app, 1000)
		t.Logf("recent plan=%v", explainDetails(t, app.db,
			"SELECT * FROM media WHERE deleted_at IS NULL AND library_id = ? AND media_type = ? AND id <> ? ORDER BY created_at DESC LIMIT 32",
			library.ID, "movie", "media-000000"))
		queries := []string{
			"SELECT media.* FROM media JOIN media_people ON media_people.media_id = media.id AND media_people.role = 'actor' WHERE media.deleted_at IS NULL AND media.library_id = ? AND media.media_type = ? AND media.id <> ? AND media_people.person_id IN (?) GROUP BY media.id ORDER BY COUNT(DISTINCT media_people.person_id) DESC, media.created_at DESC LIMIT 40",
		}
		for _, sql := range queries {
			t.Logf("actor plan=%v", explainDetails(t, app.db, sql, library.ID, "movie", "media-000000", "person-1"))
		}
	})
}

func explainDetails(tb testing.TB, db *gorm.DB, sql string, args ...interface{}) []string {
	tb.Helper()
	var rows []struct {
		Detail string `gorm:"column:detail"`
	}
	if err := db.Raw("EXPLAIN QUERY PLAN "+sql, args...).Scan(&rows).Error; err != nil {
		tb.Fatalf("explain query plan: %v", err)
	}
	details := make([]string, 0, len(rows))
	for _, row := range rows {
		details = append(details, row.Detail)
	}
	return details
}

func BenchmarkJellyfinItems(b *testing.B) {
	for _, count := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("library_%d_limit_100", count), func(b *testing.B) {
			app := newTestApp(b)
			seedJellyfinMovies(b, app, count)
			counter := attachQueryCounter(app)
			request := httptest.NewRequest("GET", "/Items?recursive=true&includeItemTypes=Movie&limit=100", nil)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				counter.Reset()
				items, _, _, err := app.queryJellyfinItems(request)
				if err != nil {
					b.Fatal(err)
				}
				for _, item := range items {
					if _, err := app.jellyfinItemDTO(&DesktopSettings{}, item); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}

func BenchmarkContinueWatching(b *testing.B) {
	app := newTestApp(b)
	seedJellyfinMovies(b, app, 1000)
	histories := make([]model.WatchHistory, 0, 1000)
	for i := 0; i < 1000; i++ {
		histories = append(histories, model.WatchHistory{
			ID: fmt.Sprintf("history-%06d", i), UserID: desktopUserID, MediaID: fmt.Sprintf("media-%06d", i),
			Position: 10, Duration: 100, UpdatedAt: time.Unix(int64(i+1), 0).UTC(),
		})
	}
	if err := app.db.Create(&histories).Error; err != nil {
		b.Fatal(err)
	}
	counter := attachQueryCounter(app)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		counter.Reset()
		rows, err := app.repos.WatchHistory.ContinueWatching(desktopUserID, 100)
		if err != nil || len(rows) != 100 {
			b.Fatalf("rows=%d err=%v", len(rows), err)
		}
	}
}

func BenchmarkMediaDetail(b *testing.B) {
	app := newTestApp(b)
	seedJellyfinMovies(b, app, 1)
	counter := attachQueryCounter(app)
	settings := &DesktopSettings{}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		counter.Reset()
		item, err := app.resolveJellyfinItem("media:media-000000")
		if err != nil {
			b.Fatal(err)
		}
		if _, err := app.jellyfinItemDTO(settings, item); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkJellyfinSeriesList(b *testing.B) {
	app := newTestApp(b)
	seedJellyfinSeries(b, app, 100, 10)
	counter := attachQueryCounter(app)
	request := httptest.NewRequest("GET", "/Items?recursive=true&includeItemTypes=Series&limit=100", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		counter.Reset()
		items, _, _, err := app.queryJellyfinItems(request)
		if err != nil {
			b.Fatal(err)
		}
		for _, item := range items {
			if _, err := app.jellyfinItemDTO(&DesktopSettings{}, item); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkJellyfinResume(b *testing.B) {
	app := newTestApp(b)
	seedJellyfinMovies(b, app, 1000)
	histories := make([]model.WatchHistory, 0, 1000)
	for i := 0; i < 1000; i++ {
		histories = append(histories, model.WatchHistory{ID: fmt.Sprintf("resume-%06d", i), UserID: desktopUserID, MediaID: fmt.Sprintf("media-%06d", i), Position: 10, Duration: 100, UpdatedAt: time.Unix(int64(i+1), 0).UTC()})
	}
	if err := app.db.Create(&histories).Error; err != nil {
		b.Fatal(err)
	}
	counter := attachQueryCounter(app)
	request := httptest.NewRequest("GET", "/Users/desktop/Items/Resume?limit=100", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		counter.Reset()
		items, _, _, err := app.queryJellyfinResumeItems(request)
		if err != nil || len(items) != 100 {
			b.Fatalf("rows=%d err=%v", len(items), err)
		}
	}
}

func BenchmarkJellyfinLatest(b *testing.B) {
	app := newTestApp(b)
	seedJellyfinMovies(b, app, 10000)
	counter := attachQueryCounter(app)
	request := httptest.NewRequest("GET", "/Items/Latest?limit=100", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		counter.Reset()
		items, err := app.queryJellyfinLatestItems(request)
		if err != nil || len(items) != 100 {
			b.Fatalf("rows=%d err=%v", len(items), err)
		}
	}
}

func BenchmarkDetailRecommendation(b *testing.B) {
	for _, count := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("library_%d", count), func(b *testing.B) {
			app := newTestApp(b)
			seedJellyfinMovies(b, app, count)
			if err := app.db.Model(&model.Media{}).Where("library_id = ?", fmt.Sprintf("library-%d", count)).Updates(map[string]interface{}{"genres": "Action", "rating": 8.0}).Error; err != nil {
				b.Fatal(err)
			}
			counter := attachQueryCounter(app)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				counter.Reset()
				response, err := app.GetDetailRecommendations("media-000000", 12)
				if err != nil || response == nil {
					b.Fatalf("response=%v err=%v", response, err)
				}
			}
		})
	}
}

func hasFullScan(plan []string, table string) bool {
	needle := "SCAN " + strings.ToLower(table)
	for _, detail := range plan {
		if strings.Contains(strings.ToLower(detail), strings.ToLower(needle)) {
			return true
		}
	}
	return false
}
