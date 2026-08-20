package main

import (
	"testing"
	"time"

	"navi-desktop/model"
)

// 「加入日期」排序必须只反映视频文件本身进库的先后。重新刮削会改写 NFO 的
// 修改时间，一旦它参与排序，补刮一批老片就会把它们顶到最前面，冒充新入库。
func TestGetMediaListAddedSortIgnoresRescrapedNFOTime(t *testing.T) {
	app := newMediaPaginationTestApp(t)

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rescraped := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	items := make([]model.Media, 0, 3)
	for index, name := range []string{"oldest", "middle", "newest"} {
		fileCreatedAt := base.AddDate(0, 0, index)
		items = append(items, model.Media{
			ID:            name,
			LibraryID:     "library-a",
			Title:         name,
			FilePath:      "C:/media/" + name + ".mp4",
			MediaType:     "movie",
			FileCreatedAt: &fileCreatedAt,
		})
	}
	// 最老的那部刚被重新刮削过，NFO 的时间戳是全库最新的。
	items[0].NfoModTime = &rescraped
	for index := range items {
		if err := app.db.Create(&items[index]).Error; err != nil {
			t.Fatal(err)
		}
	}

	value, err := app.GetMediaList("library-a", 1, 20, "created_at", "desc", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	_, page := mediaPageResult(t, value)

	got := make([]string, 0, len(page))
	for _, media := range page {
		got = append(got, media.ID)
	}
	want := []string{"newest", "middle", "oldest"}
	for index, id := range want {
		if index >= len(got) || got[index] != id {
			t.Fatalf("加入日期排序 = %v, want %v（重刮的条目不该被顶到前面）", got, want)
		}
	}
}

// file_created_at 缺失时（比如网络盘拿不到创建时间）要退回文件修改时间，
// 不能整批塌缩到入库时间——那个字段批量扫描时是同一个值，排不出先后。
func TestGetMediaListAddedSortFallsBackToFileModTime(t *testing.T) {
	app := newMediaPaginationTestApp(t)

	older := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	items := []model.Media{
		{ID: "no-create-old", LibraryID: "library-a", Title: "old", FilePath: "C:/media/a.mp4", MediaType: "movie", FileModTime: &older},
		{ID: "no-create-new", LibraryID: "library-a", Title: "new", FilePath: "C:/media/b.mp4", MediaType: "movie", FileModTime: &newer},
	}
	for index := range items {
		if err := app.db.Create(&items[index]).Error; err != nil {
			t.Fatal(err)
		}
	}

	value, err := app.GetMediaList("library-a", 1, 20, "created_at", "desc", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	_, page := mediaPageResult(t, value)
	if len(page) != 2 || page[0].ID != "no-create-new" {
		t.Fatalf("回退到 file_mod_time 失败: %+v", page)
	}
}
