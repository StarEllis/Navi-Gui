package main

import (
	"testing"

	"navi-desktop/model"
)

// seedUserTagLibrary 建一个库 + 若干片子，标题用来验证 keyword 和条件是叠加关系。
func seedUserTagLibrary(t *testing.T, app *App, titles map[string]string) {
	t.Helper()
	library := model.Library{ID: "lib-tags", Name: "Tags", Path: t.TempDir(), Type: "movie"}
	if err := app.repos.Library.Create(&library); err != nil {
		t.Fatalf("create library: %v", err)
	}
	for id, title := range titles {
		media := model.Media{
			ID:        id,
			LibraryID: library.ID,
			Title:     title,
			FilePath:  library.Path + "/" + id + ".mkv",
			MediaType: "movie",
		}
		if err := app.repos.Media.Create(&media); err != nil {
			t.Fatalf("create media %s: %v", id, err)
		}
	}
}

func mustCreateTag(t *testing.T, app *App, name, category string) *model.Tag {
	t.Helper()
	tag, err := app.CreateMyTag(name, category)
	if err != nil {
		t.Fatalf("create tag %s: %v", name, err)
	}
	return tag
}

func filteredIDs(t *testing.T, app *App, keyword string, uf UserFilter) []string {
	t.Helper()
	value, err := app.GetMediaListFiltered("lib-tags", 1, 50, "created_at", "desc", keyword, "", "", uf)
	if err != nil {
		t.Fatalf("filtered list: %v", err)
	}
	payload, ok := value.(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected payload type %T", value)
	}
	items, ok := payload["items"].([]model.Media)
	if !ok {
		t.Fatalf("unexpected items type %T", payload["items"])
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

func containsAll(ids []string, wanted ...string) bool {
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		seen[id] = true
	}
	for _, id := range wanted {
		if !seen[id] {
			return false
		}
	}
	return len(ids) == len(wanted)
}

func TestSetMyRatingTogglesOffOnSameScore(t *testing.T) {
	app := newTestApp(t)
	seedUserTagLibrary(t, app, map[string]string{"m1": "Alpha"})

	if err := app.SetMyRating("m1", 4); err != nil {
		t.Fatalf("set rating: %v", err)
	}
	if score, _ := app.GetMyRating("m1"); score != 4 {
		t.Fatalf("expected 4 stars, got %d", score)
	}

	// 再点同一颗星 = 清空
	if err := app.SetMyRating("m1", 4); err != nil {
		t.Fatalf("clear rating: %v", err)
	}
	if score, _ := app.GetMyRating("m1"); score != 0 {
		t.Fatalf("expected rating cleared, got %d", score)
	}
	var rows int64
	if err := app.db.Model(&model.MediaRating{}).Where("media_id = ?", "m1").Count(&rows).Error; err != nil || rows != 0 {
		t.Fatalf("expected no rating row, count=%d err=%v", rows, err)
	}
}

func TestBatchSetMyRatingKeepsEqualScoresInsteadOfClearing(t *testing.T) {
	app := newTestApp(t)
	seedUserTagLibrary(t, app, map[string]string{"m1": "Alpha", "m2": "Bravo"})

	if err := app.SetMyRating("m1", 5); err != nil {
		t.Fatalf("set rating: %v", err)
	}
	if err := app.BatchSetMyRating([]string{"m1", "m2"}, 5); err != nil {
		t.Fatalf("batch rating: %v", err)
	}
	for _, id := range []string{"m1", "m2"} {
		if score, _ := app.GetMyRating(id); score != 5 {
			t.Fatalf("%s expected 5 stars after batch, got %d", id, score)
		}
	}
}

func TestUserFilterOrsWithinGroupAndAndsAcrossGroups(t *testing.T) {
	app := newTestApp(t)
	seedUserTagLibrary(t, app, map[string]string{
		"m1": "Alpha", "m2": "Bravo", "m3": "Charlie", "m4": "Delta",
	})

	low := mustCreateTag(t, app, "low", "look")
	high := mustCreateTag(t, app, "high", "look")
	shape := mustCreateTag(t, app, "shape", "body")

	// m1: low + shape / m2: high + shape / m3: low / m4: shape
	if err := app.SetMediaTags("m1", []string{low.ID, shape.ID}); err != nil {
		t.Fatalf("tag m1: %v", err)
	}
	if err := app.SetMediaTags("m2", []string{high.ID, shape.ID}); err != nil {
		t.Fatalf("tag m2: %v", err)
	}
	if err := app.SetMediaTags("m3", []string{low.ID}); err != nil {
		t.Fatalf("tag m3: %v", err)
	}
	if err := app.SetMediaTags("m4", []string{shape.ID}); err != nil {
		t.Fatalf("tag m4: %v", err)
	}

	// (low 或 high) 且 shape → m1, m2
	ids := filteredIDs(t, app, "", UserFilter{
		TagGroups: [][]string{{low.ID, high.ID}, {shape.ID}},
	})
	if !containsAll(ids, "m1", "m2") {
		t.Fatalf("expected m1+m2 for (low OR high) AND shape, got %v", ids)
	}

	// 单组多选就是纯 OR → m1, m2, m3
	ids = filteredIDs(t, app, "", UserFilter{TagGroups: [][]string{{low.ID, high.ID}}})
	if !containsAll(ids, "m1", "m2", "m3") {
		t.Fatalf("expected m1+m2+m3 for low OR high, got %v", ids)
	}
}

func TestUserFilterStacksWithKeywordAndRating(t *testing.T) {
	app := newTestApp(t)
	seedUserTagLibrary(t, app, map[string]string{"m1": "Alpha", "m2": "Alpha Two", "m3": "Bravo"})
	tag := mustCreateTag(t, app, "keep", "")
	for _, id := range []string{"m1", "m2", "m3"} {
		if err := app.SetMediaTags(id, []string{tag.ID}); err != nil {
			t.Fatalf("tag %s: %v", id, err)
		}
		if err := app.SetMyRating(id, 5); err != nil {
			t.Fatalf("rate %s: %v", id, err)
		}
	}

	// 筛出 3 部之后搜索只在这 3 部里搜，且搜索和条件是叠加不是二选一
	ids := filteredIDs(t, app, "Alpha", UserFilter{
		Scores:    []int{5},
		TagGroups: [][]string{{tag.ID}},
	})
	if !containsAll(ids, "m1", "m2") {
		t.Fatalf("expected keyword to narrow within the filtered set, got %v", ids)
	}

	// 评分档不匹配时，关键词命中的也要被挡掉
	ids = filteredIDs(t, app, "Alpha", UserFilter{Scores: []int{0}})
	if len(ids) != 0 {
		t.Fatalf("expected no unrated matches, got %v", ids)
	}
}

func TestGetFilterFacetsExcludesOwnGroupSoSameGroupStaysSelectable(t *testing.T) {
	app := newTestApp(t)
	seedUserTagLibrary(t, app, map[string]string{"m1": "Alpha", "m2": "Bravo", "m3": "Charlie"})

	low := mustCreateTag(t, app, "low", "look")
	high := mustCreateTag(t, app, "high", "look")
	shape := mustCreateTag(t, app, "shape", "body")

	if err := app.SetMediaTags("m1", []string{low.ID, shape.ID}); err != nil {
		t.Fatalf("tag m1: %v", err)
	}
	if err := app.SetMediaTags("m2", []string{high.ID, shape.ID}); err != nil {
		t.Fatalf("tag m2: %v", err)
	}
	if err := app.SetMediaTags("m3", []string{high.ID}); err != nil {
		t.Fatalf("tag m3: %v", err)
	}

	// 已经选了 low：同组的 high 必须仍然有计数，否则用户没法再多选一档
	facets, err := app.GetFilterFacets("lib-tags", "", "", "", UserFilter{
		TagGroups: [][]string{{shape.ID}, {low.ID}},
	})
	if err != nil {
		t.Fatalf("facets: %v", err)
	}
	if facets.Tags[high.ID] == 0 {
		t.Fatalf("same-group option collapsed to 0: %+v", facets.Tags)
	}
	// 别的组仍然按全部条件算
	if facets.Total != 1 {
		t.Fatalf("expected 1 match for low AND shape, got %d", facets.Total)
	}
}

func TestGetFilterFacetsCountsRatingBuckets(t *testing.T) {
	app := newTestApp(t)
	seedUserTagLibrary(t, app, map[string]string{"m1": "Alpha", "m2": "Bravo", "m3": "Charlie"})
	if err := app.SetMyRating("m1", 5); err != nil {
		t.Fatalf("rate m1: %v", err)
	}
	if err := app.SetMyRating("m2", 3); err != nil {
		t.Fatalf("rate m2: %v", err)
	}

	facets, err := app.GetFilterFacets("lib-tags", "", "", "", UserFilter{})
	if err != nil {
		t.Fatalf("facets: %v", err)
	}
	if facets.Ratings["5"] != 1 || facets.Ratings["3"] != 1 {
		t.Fatalf("unexpected rating buckets: %+v", facets.Ratings)
	}
	if facets.Ratings["unrated"] != 1 {
		t.Fatalf("expected 1 unrated, got %d", facets.Ratings["unrated"])
	}
	if facets.Ratings["4"] != 0 {
		t.Fatalf("expected empty bucket to be present as 0, got %d", facets.Ratings["4"])
	}
}

func TestMergeMyTagsMovesMediaAndRecountsUsage(t *testing.T) {
	app := newTestApp(t)
	seedUserTagLibrary(t, app, map[string]string{"m1": "Alpha", "m2": "Bravo"})

	source := mustCreateTag(t, app, "7080", "")
	target := mustCreateTag(t, app, "70-80分美女", "look")

	if err := app.SetMediaTags("m1", []string{source.ID}); err != nil {
		t.Fatalf("tag m1: %v", err)
	}
	// m2 两个标签都有：合并后不该出现重复关联
	if err := app.SetMediaTags("m2", []string{source.ID, target.ID}); err != nil {
		t.Fatalf("tag m2: %v", err)
	}

	if err := app.MergeMyTags(source.ID, target.ID); err != nil {
		t.Fatalf("merge: %v", err)
	}

	if _, err := app.repos.Tag.FindByID(source.ID); err == nil {
		t.Fatal("source tag still exists after merge")
	}

	tags, err := app.ListMyTags()
	if err != nil {
		t.Fatalf("list tags: %v", err)
	}
	if len(tags) != 1 || tags[0].ID != target.ID || tags[0].Count != 2 {
		t.Fatalf("unexpected tags after merge: %+v", tags)
	}

	var usage int
	if err := app.db.Model(&model.Tag{}).Where("id = ?", target.ID).
		Select("usage_count").Scan(&usage).Error; err != nil {
		t.Fatalf("read usage: %v", err)
	}
	if usage != 2 {
		t.Fatalf("expected usage_count 2, got %d", usage)
	}

	ids := filteredIDs(t, app, "", UserFilter{TagGroups: [][]string{{target.ID}}})
	if !containsAll(ids, "m1", "m2") {
		t.Fatalf("expected both media on the target tag, got %v", ids)
	}
}

func TestCreateMyTagReturnsExistingOnDuplicateName(t *testing.T) {
	app := newTestApp(t)
	first := mustCreateTag(t, app, "画质好", "")
	second, err := app.CreateMyTag("  画质好  ", "quality")
	if err != nil {
		t.Fatalf("create duplicate: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("expected the existing tag back, got %s vs %s", second.ID, first.ID)
	}
	var count int64
	if err := app.db.Model(&model.Tag{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("expected a single tag row, count=%d err=%v", count, err)
	}
}

func TestMediaListHydratesRatingsAndTagsInBatch(t *testing.T) {
	app := newTestApp(t)
	titles := map[string]string{}
	for _, id := range []string{"m1", "m2", "m3"} {
		titles[id] = id
	}
	seedUserTagLibrary(t, app, titles)
	tag := mustCreateTag(t, app, "留着重看", "mood")
	if err := app.SetMediaTags("m1", []string{tag.ID}); err != nil {
		t.Fatalf("tag m1: %v", err)
	}
	if err := app.SetMyRating("m1", 5); err != nil {
		t.Fatalf("rate m1: %v", err)
	}

	value, err := app.GetMediaList("lib-tags", 1, 50, "created_at", "desc", "", "", "")
	if err != nil {
		t.Fatalf("media list: %v", err)
	}
	items := value.(map[string]interface{})["items"].([]model.Media)
	byID := make(map[string]model.Media, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	if byID["m1"].MyRating != 5 {
		t.Fatalf("expected my_rating 5 on m1, got %d", byID["m1"].MyRating)
	}
	if len(byID["m1"].MyTags) != 1 || byID["m1"].MyTags[0].Name != "留着重看" {
		t.Fatalf("expected my_tags on m1, got %+v", byID["m1"].MyTags)
	}
	if byID["m2"].MyRating != 0 || len(byID["m2"].MyTags) != 0 {
		t.Fatalf("expected m2 to stay clean, got %+v", byID["m2"])
	}
}

func TestUserDataSurvivesMediaRescrape(t *testing.T) {
	app := newTestApp(t)
	seedUserTagLibrary(t, app, map[string]string{"m1": "Alpha"})
	tag := mustCreateTag(t, app, "画质好", "")
	if err := app.SetMediaTags("m1", []string{tag.ID}); err != nil {
		t.Fatalf("tag m1: %v", err)
	}
	if err := app.SetMyRating("m1", 4); err != nil {
		t.Fatalf("rate m1: %v", err)
	}

	// 重新刮削会重写 media 行上的元数据字段，评分和标签在别的表里，不该受影响
	media, err := app.repos.Media.FindByID("m1")
	if err != nil {
		t.Fatalf("load media: %v", err)
	}
	media.Title = "Alpha (rescraped)"
	media.Rating = 9.1 // 刮削评分，和我的评分是两码事
	if err := app.repos.Media.Update(media); err != nil {
		t.Fatalf("update media: %v", err)
	}

	if score, _ := app.GetMyRating("m1"); score != 4 {
		t.Fatalf("my rating lost after rescrape: %d", score)
	}
	links, err := app.repos.MediaTag.ListByMediaID("m1")
	if err != nil || len(links) != 1 {
		t.Fatalf("my tags lost after rescrape: %d err=%v", len(links), err)
	}
}

func TestHydrationEmitsEmptyTagSliceInsteadOfNil(t *testing.T) {
	app := newTestApp(t)
	seedUserTagLibrary(t, app, map[string]string{"m1": "Alpha"})

	value, err := app.GetMediaList("lib-tags", 1, 20, "created_at", "desc", "", "", "")
	if err != nil {
		t.Fatalf("media list: %v", err)
	}
	items := value.(map[string]interface{})["items"].([]model.Media)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	// nil 会被 JSON 编成 null，前端就分不清「一个标签都没有」和「这次没带标签信息」，
	// 摘掉最后一个标签之后缓存永远清不掉。
	if items[0].MyTags == nil {
		t.Fatal("MyTags is nil; it must marshal as [] so an empty tag set is representable")
	}
	if len(items[0].MyTags) != 0 {
		t.Fatalf("expected no tags, got %d", len(items[0].MyTags))
	}

	detail, err := app.GetMediaDetail("m1")
	if err != nil {
		t.Fatalf("media detail: %v", err)
	}
	if detail.MyTags == nil {
		t.Fatal("detail MyTags is nil; it must marshal as []")
	}
}

func TestSortByMyRatingOrdersHighToLowWithUnratedLast(t *testing.T) {
	app := newTestApp(t)
	seedUserTagLibrary(t, app, map[string]string{
		"m1": "Alpha", "m2": "Bravo", "m3": "Charlie", "m4": "Delta",
	})
	if err := app.SetMyRating("m2", 3); err != nil {
		t.Fatalf("rate m2: %v", err)
	}
	if err := app.SetMyRating("m3", 5); err != nil {
		t.Fatalf("rate m3: %v", err)
	}
	if err := app.SetMyRating("m4", 1); err != nil {
		t.Fatalf("rate m4: %v", err)
	}

	value, err := app.GetMediaListFiltered("lib-tags", 1, 20, "my_rating", "desc", "", "", "", UserFilter{})
	if err != nil {
		t.Fatalf("sorted list: %v", err)
	}
	items := value.(map[string]interface{})["items"].([]model.Media)
	got := make([]int, 0, len(items))
	for _, item := range items {
		got = append(got, item.MyRating)
	}
	// 5, 3, 1, 未评分(0) —— 未评分靠 COALESCE 落到最后
	want := []int{5, 3, 1, 0}
	if len(got) != len(want) {
		t.Fatalf("expected %d rows, got %d", len(want), len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("my_rating desc order = %v, want %v", got, want)
		}
	}
}

func TestScoreFilterOrsAcrossSelectedStars(t *testing.T) {
	app := newTestApp(t)
	seedUserTagLibrary(t, app, map[string]string{
		"m5": "Five", "m4": "Four", "m3": "Three", "m1": "One", "m0": "Unrated",
	})
	for id, score := range map[string]int{"m5": 5, "m4": 4, "m3": 3, "m1": 1} {
		if err := app.SetMyRating(id, score); err != nil {
			t.Fatalf("rate %s: %v", id, err)
		}
	}

	// 选 5 星 + 4 星 = 四星以上
	if ids := filteredIDs(t, app, "", UserFilter{Scores: []int{5, 4}}); !containsAll(ids, "m5", "m4") {
		t.Fatalf("5+4 should behave like 「4 星以上」, got %v", ids)
	}
	// 选 5 星 + 3 星 = 只要这两档，四星不能混进来
	if ids := filteredIDs(t, app, "", UserFilter{Scores: []int{5, 3}}); !containsAll(ids, "m5", "m3") {
		t.Fatalf("5+3 should be exactly those two bands, got %v", ids)
	}
	// 单选就是单档
	if ids := filteredIDs(t, app, "", UserFilter{Scores: []int{5}}); !containsAll(ids, "m5") {
		t.Fatalf("single band, got %v", ids)
	}
	// 未评分和星级可以一起选
	if ids := filteredIDs(t, app, "", UserFilter{Scores: []int{5, 0}}); !containsAll(ids, "m5", "m0") {
		t.Fatalf("stars OR unrated, got %v", ids)
	}
	// 只选未评分
	if ids := filteredIDs(t, app, "", UserFilter{Scores: []int{0}}); !containsAll(ids, "m0") {
		t.Fatalf("unrated only, got %v", ids)
	}
	// 不选 = 不限
	if ids := filteredIDs(t, app, "", UserFilter{}); len(ids) != 5 {
		t.Fatalf("empty score set must not filter, got %v", ids)
	}
}

func TestScoreFacetsStaySelectableWithinTheRatingGroup(t *testing.T) {
	app := newTestApp(t)
	seedUserTagLibrary(t, app, map[string]string{"m5": "Five", "m4": "Four", "m0": "Unrated"})
	if err := app.SetMyRating("m5", 5); err != nil {
		t.Fatalf("rate m5: %v", err)
	}
	if err := app.SetMyRating("m4", 4); err != nil {
		t.Fatalf("rate m4: %v", err)
	}

	// 已经选了 5 星，4 星必须还有计数，否则没法再多选一档凑成「4 星以上」
	facets, err := app.GetFilterFacets("lib-tags", "", "", "", UserFilter{Scores: []int{5}})
	if err != nil {
		t.Fatalf("facets: %v", err)
	}
	if facets.Ratings["4"] != 1 {
		t.Fatalf("same-group star collapsed: %+v", facets.Ratings)
	}
	if facets.Ratings["unrated"] != 1 {
		t.Fatalf("unrated collapsed: %+v", facets.Ratings)
	}
	if facets.Total != 1 {
		t.Fatalf("expected 1 match for 5 stars, got %d", facets.Total)
	}
}

// 早先 GetFilterFacets 把符合条件的 media.id 全捞回 Go，再当成 IN 的绑定参数塞回去，
// 库一大就撞上 SQLite 的变量上限（实测 4 万部直接 too many SQL variables）。
// 这里造一个远超上限的库，确保 facet 走的是子查询而不是参数列表。
func TestGetFilterFacetsDoesNotBindOneParameterPerMedia(t *testing.T) {
	app := newTestApp(t)
	library := model.Library{ID: "lib-tags", Name: "Big", Path: t.TempDir(), Type: "movie"}
	if err := app.repos.Library.Create(&library); err != nil {
		t.Fatalf("create library: %v", err)
	}

	// 逐行 INSERT 四万次太慢，用递归 CTE 一条语句造出来
	const size = 40000
	if err := app.db.Exec(`
		WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < ?)
		INSERT INTO media (id, library_id, title, file_path, path_key, media_type, created_at, updated_at)
		SELECT printf('m%06d', n), ?, printf('T%06d', n), printf('/x/%d.mkv', n),
		       printf('/x/%d.mkv', n), 'movie', datetime('now'), datetime('now')
		FROM seq`, size, library.ID).Error; err != nil {
		t.Fatalf("seed media: %v", err)
	}

	tag := mustCreateTag(t, app, "big", "cat")
	if err := app.BatchAddTag([]string{"m000001", "m000002"}, tag.ID); err != nil {
		t.Fatalf("tag: %v", err)
	}
	if err := app.SetMyRating("m000003", 5); err != nil {
		t.Fatalf("rate: %v", err)
	}

	facets, err := app.GetFilterFacets(library.ID, "", "", "", UserFilter{})
	if err != nil {
		t.Fatalf("facets over %d media: %v", size, err)
	}
	if facets.Total != size {
		t.Fatalf("total = %d, want %d", facets.Total, size)
	}
	if facets.Tags[tag.ID] != 2 {
		t.Fatalf("tag facet = %d, want 2", facets.Tags[tag.ID])
	}
	if facets.Ratings["5"] != 1 || facets.Ratings["unrated"] != size-1 {
		t.Fatalf("rating facets wrong: %+v", facets.Ratings)
	}
}

func TestDeletingMediaAndLibraryCleansUpRatings(t *testing.T) {
	app := newTestApp(t)
	seedUserTagLibrary(t, app, map[string]string{"m1": "Alpha", "m2": "Bravo"})
	tag := mustCreateTag(t, app, "keep", "")
	for _, id := range []string{"m1", "m2"} {
		if err := app.SetMyRating(id, 4); err != nil {
			t.Fatalf("rate %s: %v", id, err)
		}
		if err := app.SetMediaTags(id, []string{tag.ID}); err != nil {
			t.Fatalf("tag %s: %v", id, err)
		}
	}

	// 单条删除：评分和标签关联都不该留下孤儿
	if _, err := app.repos.Media.DeleteByIDs([]string{"m1"}); err != nil {
		t.Fatalf("delete media: %v", err)
	}
	var ratings, links int64
	app.db.Model(&model.MediaRating{}).Where("media_id = ?", "m1").Count(&ratings)
	app.db.Model(&model.MediaTag{}).Where("media_id = ?", "m1").Count(&links)
	if ratings != 0 || links != 0 {
		t.Fatalf("orphans after media delete: ratings=%d tags=%d", ratings, links)
	}

	// 删库：剩下那条也要跟着走
	if _, err := app.repos.DeleteLibraryAtomic("lib-tags"); err != nil {
		t.Fatalf("delete library: %v", err)
	}
	app.db.Model(&model.MediaRating{}).Count(&ratings)
	app.db.Model(&model.MediaTag{}).Count(&links)
	if ratings != 0 || links != 0 {
		t.Fatalf("orphans after library delete: ratings=%d tags=%d", ratings, links)
	}
}
