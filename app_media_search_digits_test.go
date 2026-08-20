package main

import (
	"testing"

	"navi-desktop/model"
	"navi-desktop/repository"
)

// 搜番号尾号（例如 -025）以前会把整库同年份的片子翻出来：连字符归一化成空格后
// token 只剩 "025"，而 search_text 里的发行日期 "2025 02 14" 用任意位置子串一撞就中。
func TestGetMediaListDigitTokenDoesNotMatchReleaseYear(t *testing.T) {
	app := newMediaPaginationTestApp(t)
	items := []model.Media{
		{
			ID: "code-025", LibraryID: "library-a", Title: "淫荡AV女优露天混浴乱交感谢祭",
			Code: "SERO-025", FilePath: "C:/media/SERO-025.mp4", MediaType: "movie",
			ReleaseDateNormalized: "2018-06-13", Year: 2018,
		},
		{
			ID: "year-2025", LibraryID: "library-a", Title: "最强的两人",
			Code: "MIDA-039", FilePath: "C:/media/MIDA-039.mp4", MediaType: "movie",
			ReleaseDateNormalized: "2025-02-14", Year: 2025,
		},
		{
			ID: "url-digits", LibraryID: "library-a", Title: "学生妹诱惑",
			Code: "MIAA-395", FilePath: "C:/media/MIAA-395.mp4", MediaType: "movie",
			NfoExtraFields: `{"thumb":"https://soski.tv/images/thumbnails/70025464.jpg"}`,
		},
		{
			ID: "mid-number", LibraryID: "library-a", Title: "素人企划",
			Code: "FC2-702535", FilePath: "C:/media/FC2-702535.mp4", MediaType: "movie",
		},
	}
	if err := app.db.Create(&items).Error; err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if err := repository.RefreshMediaSearchIndex(app.db, item.ID); err != nil {
			t.Fatal(err)
		}
	}

	value, err := app.GetMediaList("library-a", 1, 20, "created_at", "desc", "-025", "", "")
	if err != nil {
		t.Fatal(err)
	}
	result, page := mediaPageResult(t, value)
	if result["total"] != int64(1) || len(page) != 1 || page[0].ID != "code-025" {
		ids := make([]string, 0, len(page))
		for _, item := range page {
			ids = append(ids, item.ID)
		}
		t.Fatalf("搜 -025 只应命中番号尾号 025 的那部，total=%v ids=%v", result["total"], ids)
	}
}

// 锚定只是不让数字 token 被从中段截取，整段数字本身还要能搜。
func TestGetMediaListDigitTokenStillMatchesWholeUnits(t *testing.T) {
	app := newMediaPaginationTestApp(t)
	items := []model.Media{
		{
			ID: "mida-039", LibraryID: "library-a", Title: "最强的两人",
			Code: "MIDA-039", FilePath: "C:/media/MIDA-039.mp4", MediaType: "movie",
			ReleaseDateNormalized: "2025-02-14", Year: 2025,
		},
		{
			ID: "sero-025", LibraryID: "library-a", Title: "感谢祭",
			Code: "SERO-025", FilePath: "C:/media/SERO-025.mp4", MediaType: "movie",
			ReleaseDateNormalized: "2018-06-13", Year: 2018,
		},
	}
	if err := app.db.Create(&items).Error; err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if err := repository.RefreshMediaSearchIndex(app.db, item.ID); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		keyword string
		wantID  string
	}{
		{keyword: "039", wantID: "mida-039"},      // 番号尾号
		{keyword: "MIDA-039", wantID: "mida-039"}, // 完整番号
		{keyword: "mida 039", wantID: "mida-039"}, // 空格写法
		{keyword: "2025", wantID: "mida-039"},     // 年份本身仍可搜
		{keyword: "sero 025", wantID: "sero-025"}, // 番号 + 尾号
	}
	for _, test := range cases {
		value, err := app.GetMediaList("library-a", 1, 20, "created_at", "desc", test.keyword, "", "")
		if err != nil {
			t.Fatalf("keyword %q: %v", test.keyword, err)
		}
		result, page := mediaPageResult(t, value)
		if result["total"] != int64(1) || len(page) != 1 || page[0].ID != test.wantID {
			t.Errorf("搜 %q 应该只命中 %s，实际 total=%v page=%v", test.keyword, test.wantID, result["total"], page)
		}
	}
}
