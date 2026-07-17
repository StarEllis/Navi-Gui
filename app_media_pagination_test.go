package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"navi-desktop/model"
	"navi-desktop/repository"
)

func newMediaPaginationTestApp(t *testing.T) *App {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:media-pagination-%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := db.AutoMigrate(&model.Media{}, &model.Favorite{}, &model.WatchHistory{}, &model.Person{}, &model.MediaPerson{}); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	return &App{db: db, logger: zap.NewNop().Sugar()}
}

func TestGetMediaListPreservesCompleteServerSearchSemantics(t *testing.T) {
	app := newMediaPaginationTestApp(t)
	items := []model.Media{
		{
			ID: "target", LibraryID: "library-a", Title: "中国电影", OrigTitle: "Original Feature",
			Code: "ABP-123", Maker: "星空制作", Label: "经典标签", Studio: "Example Publisher",
			Genres: "剧情, 收藏", FilePath: "C:/media/target.mp4", ReleaseDateNormalized: "2024-05-06",
			Year: 2024, MediaType: "movie",
		},
		{ID: "decoy", LibraryID: "library-a", Title: "Other", FilePath: "C:/media/other.mp4", MediaType: "movie"},
		{ID: "other-library", LibraryID: "library-b", Title: "中国电影", Code: "ABP-123", FilePath: "C:/other/target.mp4", MediaType: "episode"},
	}
	if err := app.db.Create(&items).Error; err != nil {
		t.Fatal(err)
	}
	person := model.Person{ID: "actor-target", Name: "演员甲", OrigName: "Actor Alpha"}
	if err := app.db.Create(&person).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.db.Create(&model.MediaPerson{ID: "cast-target", MediaID: "target", PersonID: person.ID, Role: "actor"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := repository.RefreshMediaSearchIndex(app.db, "target"); err != nil {
		t.Fatal(err)
	}
	if err := app.db.Create(&model.Favorite{ID: "favorite-target", UserID: desktopUserID, MediaID: "target"}).Error; err != nil {
		t.Fatal(err)
	}

	terms := []string{
		"ABP-123", "星空制作", "经典标签", "2024", "中国电影", "zhongguodianying", "zgdy",
		"Original Feature", "Example Publisher", "剧情", "演员甲", "Actor Alpha", "target.mp4",
	}
	for _, term := range terms {
		t.Run(strings.ReplaceAll(term, " ", "_"), func(t *testing.T) {
			value, err := app.GetMediaList("library-a", 1, 20, "created_at", "desc", "  "+strings.ToUpper(term)+"  ", "favorite", "true")
			if err != nil {
				t.Fatal(err)
			}
			result, page := mediaPageResult(t, value)
			if result["total"] != int64(1) || len(page) != 1 || page[0].ID != "target" {
				t.Fatalf("term %q result total=%v page=%v", term, result["total"], page)
			}
		})
	}
	value, err := app.GetMediaList("library-a", 1, 20, "created_at", "desc", "zhongguo", "media_type", "episode")
	if err != nil {
		t.Fatal(err)
	}
	result, page := mediaPageResult(t, value)
	if result["total"] != int64(0) || len(page) != 0 {
		t.Fatalf("combined library/type filter escaped scope: total=%v page=%v", result["total"], page)
	}
}

func TestGetMediaListActorFilterCoalescesSimplifiedAndTraditionalNames(t *testing.T) {
	app := newMediaPaginationTestApp(t)
	person := model.Person{ID: "actor-mita", Name: "三田真铃"}
	if err := app.db.Create(&person).Error; err != nil {
		t.Fatal(err)
	}
	items := []model.Media{
		{ID: "mita-simplified", LibraryID: "library-a", Title: "三田真铃", FilePath: "C:/media/simplified.mp4", MediaType: "movie"},
		{ID: "mita-traditional", LibraryID: "library-a", Title: "三田真鈴", FilePath: "C:/media/traditional.mp4", MediaType: "movie"},
	}
	if err := app.db.Create(&items).Error; err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if err := app.db.Create(&model.MediaPerson{
			ID: item.ID + "-actor", MediaID: item.ID, PersonID: person.ID, Role: "actor",
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := repository.RefreshMediaSearchIndex(app.db, item.ID); err != nil {
			t.Fatal(err)
		}
	}

	value, err := app.GetMediaList("library-a", 1, 20, "created_at", "desc", "", "actor", "三田真鈴")
	if err != nil {
		t.Fatal(err)
	}
	result, page := mediaPageResult(t, value)
	if result["total"] != int64(2) || len(page) != 2 {
		t.Fatalf("traditional actor filter total=%v page=%v", result["total"], page)
	}

	if err := app.db.Model(&model.Media{}).
		Where("id IN ?", []string{"mita-simplified", "mita-traditional"}).
		Update("search_text", "legacy index without actor name").Error; err != nil {
		t.Fatal(err)
	}
	value, err = app.GetMediaList("library-a", 1, 20, "created_at", "desc", "三田真鈴", "actor", person.ID)
	if err != nil {
		t.Fatal(err)
	}
	result, page = mediaPageResult(t, value)
	if result["total"] != int64(2) || len(page) != 2 {
		t.Fatalf("traditional actor search total=%v page=%v", result["total"], page)
	}
}

func TestGetMediaListSpaceSeparatedActorNamesAreExplicitIntersection(t *testing.T) {
	app := newMediaPaginationTestApp(t)
	people := []model.Person{
		{ID: "actor-komatsu", Name: "小松空"},
		{ID: "actor-mita", Name: "三田真铃"},
	}
	if err := app.db.Create(&people).Error; err != nil {
		t.Fatal(err)
	}
	items := []model.Media{
		{ID: "komatsu-only", LibraryID: "library-a", Title: "小松空单人作品", FilePath: "C:/media/komatsu.mp4", MediaType: "movie"},
		{ID: "mita-only", LibraryID: "library-a", Title: "三田真铃单人作品", FilePath: "C:/media/mita.mp4", MediaType: "movie"},
		{ID: "both-actors", LibraryID: "library-a", Title: "共同出演", FilePath: "C:/media/both.mp4", MediaType: "movie"},
	}
	if err := app.db.Create(&items).Error; err != nil {
		t.Fatal(err)
	}
	relations := []model.MediaPerson{
		{ID: "komatsu-only-cast", MediaID: "komatsu-only", PersonID: "actor-komatsu", Role: "actor"},
		{ID: "mita-only-cast", MediaID: "mita-only", PersonID: "actor-mita", Role: "actor"},
		{ID: "both-komatsu-cast", MediaID: "both-actors", PersonID: "actor-komatsu", Role: "actor"},
		{ID: "both-mita-cast", MediaID: "both-actors", PersonID: "actor-mita", Role: "actor"},
	}
	if err := app.db.Create(&relations).Error; err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if err := repository.RefreshMediaSearchIndex(app.db, item.ID); err != nil {
			t.Fatal(err)
		}
	}

	value, err := app.GetMediaList("library-a", 1, 20, "created_at", "desc", "小松空 三田", "", "")
	if err != nil {
		t.Fatal(err)
	}
	result, page := mediaPageResult(t, value)
	if result["total"] != int64(1) || len(page) != 1 || page[0].ID != "both-actors" {
		t.Fatalf("space-separated actor intersection total=%v page=%v", result["total"], page)
	}
}

func TestGetMediaListTenThousandRowsRemainServerPaginated(t *testing.T) {
	app := newMediaPaginationTestApp(t)
	items := make([]model.Media, 10000)
	for index := range items {
		items[index] = model.Media{
			ID: fmt.Sprintf("large-%05d", index), LibraryID: "large-library",
			Title: fmt.Sprintf("Large Media %05d", index), FilePath: fmt.Sprintf("C:/large/%05d.mp4", index), MediaType: "movie",
		}
	}
	if err := app.db.CreateInBatches(&items, 200).Error; err != nil {
		t.Fatal(err)
	}
	value, err := app.GetMediaList("large-library", 1, 120, "created_at", "desc", "large media", "", "")
	if err != nil {
		t.Fatal(err)
	}
	result, page := mediaPageResult(t, value)
	if result["total"] != int64(10000) || len(page) != 120 || result["page_size"] != 120 {
		t.Fatalf("large page total=%v len=%d size=%v", result["total"], len(page), result["page_size"])
	}
}

func mediaPageResult(t *testing.T, value interface{}) (map[string]interface{}, []model.Media) {
	t.Helper()
	result, ok := value.(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected result type %T", value)
	}
	items, ok := result["items"].([]model.Media)
	if !ok {
		t.Fatalf("unexpected items type %T", result["items"])
	}
	return result, items
}

func TestGetMediaListUsesBoundedSQLPaginationAndClampsLastPage(t *testing.T) {
	app := newMediaPaginationTestApp(t)
	items := make([]model.Media, 250)
	for index := range items {
		items[index] = model.Media{
			ID:        fmt.Sprintf("media-%03d", index),
			LibraryID: "library-a",
			Title:     fmt.Sprintf("Media %03d", index),
			FilePath:  fmt.Sprintf("C:/media/%03d.mp4", index),
			MediaType: "movie",
			Rating:    float64(index),
		}
	}
	if err := app.db.Create(&items).Error; err != nil {
		t.Fatalf("seed media: %v", err)
	}

	value, err := app.GetMediaList("library-a", 1, 0, "rating", "asc", "", "", "")
	if err != nil {
		t.Fatalf("get default page: %v", err)
	}
	result, page := mediaPageResult(t, value)
	if len(page) != 120 || result["total"] != int64(250) || result["page_size"] != 120 {
		t.Fatalf("unexpected default page: len=%d total=%v size=%v", len(page), result["total"], result["page_size"])
	}

	value, err = app.GetMediaList("library-a", 99, 1000, "rating", "asc", "", "", "")
	if err != nil {
		t.Fatalf("get clamped page: %v", err)
	}
	result, page = mediaPageResult(t, value)
	if len(page) != 50 || result["page"] != 2 || result["page_size"] != 200 {
		t.Fatalf("unexpected clamped page: len=%d page=%v size=%v", len(page), result["page"], result["page_size"])
	}
}

func TestGetMediaListCountMatchesServerFilters(t *testing.T) {
	app := newMediaPaginationTestApp(t)
	items := []model.Media{
		{ID: "movie-favorite", LibraryID: "library-a", Title: "Alpha", FilePath: "C:/media/a.mp4", MediaType: "movie", Genres: "Drama"},
		{ID: "episode-watched", LibraryID: "library-a", Title: "Beta", FilePath: "C:/media/b.mp4", MediaType: "episode", Genres: "Action"},
		{ID: "movie-unwatched", LibraryID: "library-a", Title: "Gamma", FilePath: "C:/media/c.mp4", MediaType: "movie", Genres: "Drama"},
	}
	if err := app.db.Create(&items).Error; err != nil {
		t.Fatalf("seed media: %v", err)
	}
	if err := app.db.Create(&model.Favorite{ID: "favorite-1", UserID: desktopUserID, MediaID: "movie-favorite"}).Error; err != nil {
		t.Fatalf("seed favorite: %v", err)
	}
	if err := app.db.Create(&model.WatchHistory{ID: "watch-1", UserID: desktopUserID, MediaID: "episode-watched", Completed: true}).Error; err != nil {
		t.Fatalf("seed watch history: %v", err)
	}

	tests := []struct {
		name        string
		keyword     string
		filterType  string
		filterValue string
		wantID      string
		wantTotal   int64
	}{
		{name: "media type", filterType: "media_type", filterValue: "episode", wantID: "episode-watched", wantTotal: 1},
		{name: "watched", filterType: "watched", filterValue: "true", wantID: "episode-watched", wantTotal: 1},
		{name: "unwatched", filterType: "unwatched", filterValue: "true", wantID: "movie-favorite", wantTotal: 2},
		{name: "favorite", filterType: "favorite", filterValue: "true", wantID: "movie-favorite", wantTotal: 1},
		{name: "not favorite", filterType: "favorite", filterValue: "false", wantID: "episode-watched", wantTotal: 2},
		{name: "tag", filterType: "tag", filterValue: "Action", wantID: "episode-watched", wantTotal: 1},
		{name: "search", keyword: "Gamma", wantID: "movie-unwatched", wantTotal: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value, err := app.GetMediaList("library-a", 1, 120, "rating", "desc", test.keyword, test.filterType, test.filterValue)
			if err != nil {
				t.Fatalf("get filtered page: %v", err)
			}
			result, page := mediaPageResult(t, value)
			if result["total"] != test.wantTotal || len(page) == 0 || page[0].ID != test.wantID {
				t.Fatalf("unexpected filter result: total=%v items=%v", result["total"], page)
			}
		})
	}
}
