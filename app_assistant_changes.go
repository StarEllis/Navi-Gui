package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"navi-desktop/assistant"
	"navi-desktop/model"
	"navi-desktop/service"

	"gorm.io/gorm"
)

const (
	maxAssistantChangeMedia = 200
	maxAssistantRemoveMedia = 50
	maxAssistantChangeLines = 30
)

// nfoFieldLabels 是助手能改的 NFO 字段，和详情页"编辑 NFO"一致。
var nfoFieldLabels = map[string]string{
	"title": "标题", "code": "番号", "release_date": "发行日期", "director": "导演",
	"series": "系列", "publisher": "发行商", "maker": "片商", "genres": "分类",
	"actors": "演员", "plot": "简介", "runtime": "片长", "rating": "网站评分",
}

var scanModeLabels = map[string]string{
	"incremental":   "只加新片",
	"delete_update": "加新片、清掉已不存在的、更新改过的",
	"overwrite":     "全部重新读一遍",
}

// assistantExecutor 把助手提出的修改落到现有的 App 方法上，界面上的按钮改了什么，这里就改什么。
type assistantExecutor struct {
	app *App
}

func (e assistantExecutor) Preview(change assistant.Change) (assistant.ChangePreview, error) {
	preview, _, err := e.plan(change)
	return preview, err
}

// Apply 重新算一遍再执行：确认卡片挂着的时候，用户可能已经在界面上改过了。
func (e assistantExecutor) Apply(change assistant.Change) (string, error) {
	_, apply, err := e.plan(change)
	if err != nil {
		return "", err
	}
	return apply()
}

func (e assistantExecutor) OpenFolder(mediaID string) (string, error) {
	media, err := e.app.repos.Media.FindByID(mediaID)
	if err != nil {
		return "", errors.New("没有这部片")
	}
	dir := filepath.Dir(media.FilePath)
	return dir, buildOpenFolderCommand(dir).Start()
}

func (e assistantExecutor) Play(mediaID string) (string, error) {
	media, err := e.app.repos.Media.FindByID(mediaID)
	if err != nil {
		return "", errors.New("没有这部片")
	}
	if err := e.app.PlayMedia(media.ID, ""); err != nil {
		return "", err
	}
	if media.Code != "" {
		return media.Code, nil
	}
	return media.Title, nil
}

type changeMedia struct {
	ID   string
	Code string
}

func (m changeMedia) label() string {
	if m.Code != "" {
		return m.Code
	}
	return m.ID
}

func (e assistantExecutor) loadChangeMedia(ids []string, limit int) ([]changeMedia, error) {
	seen := map[string]bool{}
	clean := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" && !seen[id] {
			seen[id] = true
			clean = append(clean, id)
		}
	}
	if len(clean) == 0 {
		return nil, errors.New("没给要改的影片 media_ids")
	}
	if len(clean) > limit {
		return nil, fmt.Errorf("一次最多改 %d 部，这次给了 %d 部，分几次来", limit, len(clean))
	}
	var rows []model.Media
	if err := e.app.db.Select("id", "code").Where("id IN ?", clean).Find(&rows).Error; err != nil {
		return nil, err
	}
	byID := make(map[string]string, len(rows))
	for _, row := range rows {
		byID[row.ID] = row.Code
	}
	media := make([]changeMedia, 0, len(clean))
	var missing []string
	for _, id := range clean {
		code, ok := byID[id]
		if !ok {
			missing = append(missing, id)
			continue
		}
		media = append(media, changeMedia{ID: id, Code: code})
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("这些 id 不存在或已删除：%s", strings.Join(missing, ", "))
	}
	return media, nil
}

func mediaIDsOf(media []changeMedia) []string {
	ids := make([]string, len(media))
	for i, m := range media {
		ids[i] = m.ID
	}
	return ids
}

// limitLines 片多的时候确认卡片只列前面一些，免得一屏全是字。
func limitLines(lines []string) []string {
	if len(lines) <= maxAssistantChangeLines {
		return lines
	}
	rest := len(lines) - maxAssistantChangeLines
	return append(lines[:maxAssistantChangeLines:maxAssistantChangeLines], fmt.Sprintf("……还有 %d 条", rest))
}

// plan 校验参数，算出确认卡片要显示的内容，以及真正执行的那一步。
func (e assistantExecutor) plan(change assistant.Change) (assistant.ChangePreview, func() (string, error), error) {
	a := e.app
	var preview assistant.ChangePreview
	action := strings.TrimSpace(change.Action)

	if action == "rescan_library" {
		lib, err := a.repos.Library.FindByID(strings.TrimSpace(change.LibraryID))
		if err != nil {
			return preview, nil, errors.New("没有这个媒体库，先查 libraries 表拿 id")
		}
		mode := strings.TrimSpace(change.ScanMode)
		if mode == "" {
			mode = "incremental"
		}
		modeLabel, ok := scanModeLabels[mode]
		if !ok {
			return preview, nil, fmt.Errorf("scan_mode 不认识：%s", mode)
		}
		lib.HydratePathConfig()
		preview.Title = fmt.Sprintf("重新扫描媒体库「%s」（%s）", lib.Name, modeLabel)
		preview.Lines = []string{"目录：" + strings.Join(lib.FolderPaths, "、"), "扫描在后台进行，片多的话要等一会儿"}
		// 全部重读会用 NFO 覆盖库里的信息，完全权限下也要问一声。
		preview.AlwaysConfirm = mode == "overwrite"
		return preview, func() (string, error) {
			if _, err := a.ScanLibraryWithMode(lib.ID, mode); err != nil {
				return "", err
			}
			return "已开始扫描（这个库本来就在扫的话，会并进正在进行的那次），进度看媒体库页面", nil
		}, nil
	}

	switch action {
	case "rename_tag", "merge_tags", "delete_tag":
		return e.planTagChange(action, change)
	}

	limit := maxAssistantChangeMedia
	switch action {
	case "remove_from_library":
		limit = maxAssistantRemoveMedia
	case "delete_files":
		limit = maxAssistantDeleteMedia
	}
	media, err := e.loadChangeMedia(change.MediaIDs, limit)
	if err != nil {
		return preview, nil, err
	}
	ids := mediaIDsOf(media)
	preview.MediaIDs = ids
	n := len(media)

	switch action {
	case "favorite", "unfavorite", "watched", "unwatched":
		favorites, watched := a.loadMediaStateSets(ids)
		want := action == "favorite" || action == "watched"
		state := favorites
		verb := map[string]string{"favorite": "收藏", "unfavorite": "取消收藏", "watched": "标成已看", "unwatched": "标成未看"}[action]
		if action == "watched" || action == "unwatched" {
			state = watched
		}
		var todo []string
		for _, id := range ids {
			if state[id] != want {
				todo = append(todo, id)
			}
		}
		if len(todo) == 0 {
			return preview, nil, fmt.Errorf("这 %d 部已经都是这个状态了，不用改", n)
		}
		preview.Title = fmt.Sprintf("%s %d 部", verb, len(todo))
		if skipped := n - len(todo); skipped > 0 {
			preview.Lines = []string{fmt.Sprintf("另外 %d 部本来就是这个状态，跳过", skipped)}
		}
		return preview, func() (string, error) {
			for _, id := range todo {
				var err error
				switch action {
				case "favorite", "unfavorite":
					err = a.ToggleFavorite(id)
				case "watched":
					err = a.ensureWatched(id)
				case "unwatched":
					err = a.ToggleWatched(id)
				}
				if err != nil {
					return "", fmt.Errorf("改到一半出错：%w", err)
				}
			}
			return fmt.Sprintf("已%s %d 部", verb, len(todo)), nil
		}, nil

	case "set_rating":
		if change.Rating < 0 || change.Rating > 5 {
			return preview, nil, errors.New("rating 只能是 0~5")
		}
		var todo []string
		for _, id := range ids {
			current, err := a.GetMyRating(id)
			if err != nil {
				return preview, nil, err
			}
			if current != change.Rating {
				todo = append(todo, id)
			}
		}
		if len(todo) == 0 {
			return preview, nil, fmt.Errorf("这 %d 部的评分已经是这样了，不用改", n)
		}
		if change.Rating == 0 {
			preview.Title = fmt.Sprintf("清掉 %d 部的我的评分", len(todo))
		} else {
			preview.Title = fmt.Sprintf("把 %d 部的我的评分设为 %d 星", len(todo), change.Rating)
		}
		return preview, func() (string, error) {
			for _, id := range todo {
				// SetMyRating 遇到同分会清掉，前面已经排除了同分的。
				if err := a.SetMyRating(id, change.Rating); err != nil {
					return "", fmt.Errorf("改到一半出错：%w", err)
				}
			}
			return fmt.Sprintf("已改 %d 部的评分", len(todo)), nil
		}, nil

	case "add_tag", "remove_tag":
		name := strings.TrimSpace(change.Tag)
		if name == "" {
			return preview, nil, errors.New("没给标签名 tag")
		}
		var tag model.Tag
		err := a.db.Where("name = ?", name).First(&tag).Error
		exists := err == nil
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return preview, nil, err
		}
		if action == "remove_tag" {
			if !exists {
				return preview, nil, fmt.Errorf("没有叫「%s」的标签", name)
			}
			preview.Title = fmt.Sprintf("给 %d 部去掉标签「%s」", n, name)
			return preview, func() (string, error) {
				if err := a.BatchRemoveTag(ids, tag.ID); err != nil {
					return "", err
				}
				return fmt.Sprintf("已去掉标签「%s」", name), nil
			}, nil
		}
		preview.Title = fmt.Sprintf("给 %d 部打上标签「%s」", n, name)
		if !exists {
			preview.Lines = []string{fmt.Sprintf("还没有「%s」这个标签，会新建一个", name)}
		}
		return preview, func() (string, error) {
			created, err := a.CreateMyTag(name, "")
			if err != nil {
				return "", err
			}
			if err := a.BatchAddTag(ids, created.ID); err != nil {
				return "", err
			}
			return fmt.Sprintf("已打上标签「%s」", name), nil
		}, nil

	case "edit_fields":
		return e.planEditFields(change, media)

	case "delete_files":
		return e.planDeleteFiles(media)

	case "backfill_resources":
		preview.Title = fmt.Sprintf("给 %d 部联网补预告片和剧照（只补缺的）", n)
		preview.Lines = []string{"在后台下载，进度看各片的详情页"}
		return preview, func() (string, error) {
			started, err := a.StartMediaResourceBackfillBatch(ids, BackfillOptions{
				WantTrailer: true, WantPreviewImages: true, OnlyMissing: true, MaxImages: 20, MaxRetries: 1,
			})
			if err != nil {
				return fmt.Sprintf("开始了 %d 部，有的没开始：%v", len(started), err), nil
			}
			return fmt.Sprintf("已开始补 %d 部", len(started)), nil
		}, nil

	case "remove_from_library":
		preview.Title = fmt.Sprintf("把 %d 部从库里移除", n)
		preview.Danger = true
		preview.AlwaysConfirm = true
		preview.Lines = []string{
			"只删库里的记录，视频文件不动",
			"文件还在的话，以后重新扫描可能又会加回来",
		}
		return preview, func() (string, error) {
			for i, id := range ids {
				if err := a.DeleteMedia(id); err != nil {
					return "", fmt.Errorf("移除了 %d 部后出错：%w", i, err)
				}
			}
			return fmt.Sprintf("已移除 %d 部", n), nil
		}, nil
	}
	return preview, nil, fmt.Errorf("action 不认识：%s", action)
}

// findTag 按名字找用户自建标签。
func (e assistantExecutor) findTag(name string) (model.Tag, int64, error) {
	var tag model.Tag
	name = strings.TrimSpace(name)
	if name == "" {
		return tag, 0, errors.New("没给标签名")
	}
	err := e.app.db.Where("name = ?", name).First(&tag).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tag, 0, fmt.Errorf("没有叫「%s」的标签，先查 tags 表看看准确的名字", name)
	}
	if err != nil {
		return tag, 0, err
	}
	var count int64
	if err := e.app.db.Model(&model.MediaTag{}).Where("tag_id = ?", tag.ID).Count(&count).Error; err != nil {
		return tag, 0, err
	}
	return tag, count, nil
}

// planTagChange 处理标签本身：改名、合并、删除。改名改错了能改回来；合并和删除改不回来，总要用户确认。
func (e assistantExecutor) planTagChange(action string, change assistant.Change) (assistant.ChangePreview, func() (string, error), error) {
	a := e.app
	var preview assistant.ChangePreview
	tag, count, err := e.findTag(change.Tag)
	if err != nil {
		return preview, nil, err
	}
	switch action {
	case "rename_tag":
		name := strings.TrimSpace(change.NewName)
		if name == "" {
			return preview, nil, errors.New("没给新名字 new_name")
		}
		if name == tag.Name {
			return preview, nil, errors.New("新名字和原来一样，不用改")
		}
		preview.Title = fmt.Sprintf("把标签「%s」改名为「%s」", tag.Name, name)
		preview.Lines = []string{fmt.Sprintf("打了这个标签的 %d 部跟着改", count)}
		return preview, func() (string, error) {
			if err := a.RenameMyTag(tag.ID, name); err != nil {
				return "", err
			}
			return fmt.Sprintf("已改名为「%s」", name), nil
		}, nil

	case "merge_tags":
		into, intoCount, err := e.findTag(change.IntoTag)
		if err != nil {
			return preview, nil, err
		}
		if into.ID == tag.ID {
			return preview, nil, errors.New("不能把标签并进它自己")
		}
		preview.Title = fmt.Sprintf("把标签「%s」并进「%s」", tag.Name, into.Name)
		preview.Lines = []string{
			fmt.Sprintf("「%s」的 %d 部都改打「%s」（它现在有 %d 部，重复的只算一次）", tag.Name, count, into.Name, intoCount),
			fmt.Sprintf("「%s」这个标签会删掉，合并后分不开", tag.Name),
		}
		preview.Danger = true
		preview.AlwaysConfirm = true
		return preview, func() (string, error) {
			if err := a.MergeMyTags(tag.ID, into.ID); err != nil {
				return "", err
			}
			return fmt.Sprintf("已把「%s」并进「%s」", tag.Name, into.Name), nil
		}, nil
	}

	// delete_tag
	preview.Title = fmt.Sprintf("删掉标签「%s」", tag.Name)
	preview.Lines = []string{fmt.Sprintf("%d 部片会去掉这个标签，片子本身不动", count)}
	preview.Danger = true
	preview.AlwaysConfirm = true
	return preview, func() (string, error) {
		if err := a.DeleteMyTag(tag.ID); err != nil {
			return "", err
		}
		return fmt.Sprintf("已删掉标签「%s」", tag.Name), nil
	}, nil
}

type nfoEdit struct {
	media changeMedia
	data  *service.NFOEditorData
}

func (e assistantExecutor) planEditFields(change assistant.Change, media []changeMedia) (assistant.ChangePreview, func() (string, error), error) {
	preview := assistant.ChangePreview{MediaIDs: mediaIDsOf(media), AlwaysConfirm: true}
	for name := range change.Fields {
		if _, ok := nfoFieldLabels[name]; !ok {
			return preview, nil, fmt.Errorf("字段不认识：%s", name)
		}
	}
	if len(change.Fields) == 0 && len(change.AddGenres) == 0 && len(change.RemoveGenres) == 0 {
		return preview, nil, errors.New("没说要改什么：fields、add_genres、remove_genres 至少给一个")
	}

	var edits []nfoEdit
	var lines []string
	for _, m := range media {
		data, err := e.app.GetNFOEditorData(m.ID)
		if err != nil {
			return preview, nil, fmt.Errorf("%s 的 NFO 读不出来：%w", m.label(), err)
		}
		var updated []string
		set := func(name, value string) {
			field := nfoEditorField(data, name)
			if *field == value {
				return
			}
			lines = append(lines, fmt.Sprintf("%s %s：%s → %s", m.label(), nfoFieldLabels[name], shortText(*field), shortText(value)))
			*field = value
			updated = append(updated, name)
		}
		for name, value := range change.Fields {
			set(name, strings.TrimSpace(value))
		}
		if len(change.AddGenres) > 0 || len(change.RemoveGenres) > 0 {
			set("genres", editGenres(data.Genres, change.AddGenres, change.RemoveGenres))
		}
		if len(updated) > 0 {
			data.UpdatedFields = updated
			edits = append(edits, nfoEdit{media: m, data: data})
		}
	}
	if len(edits) == 0 {
		return preview, nil, errors.New("这些片子已经是这样了，不用改")
	}
	preview.Title = fmt.Sprintf("改 %d 部的影片信息（写进 NFO 文件）", len(edits))
	preview.Lines = limitLines(lines)
	return preview, func() (string, error) {
		for i, edit := range edits {
			if err := e.app.SaveNFOEditorData(edit.media.ID, edit.data); err != nil {
				return "", fmt.Errorf("改好了 %d 部，%s 出错：%w", i, edit.media.label(), err)
			}
		}
		return fmt.Sprintf("已改 %d 部", len(edits)), nil
	}, nil
}

func nfoEditorField(data *service.NFOEditorData, name string) *string {
	switch name {
	case "title":
		return &data.Title
	case "code":
		return &data.Code
	case "release_date":
		return &data.ReleaseDate
	case "director":
		return &data.Director
	case "series":
		return &data.Series
	case "publisher":
		return &data.Publisher
	case "maker":
		return &data.Maker
	case "genres":
		return &data.Genres
	case "actors":
		return &data.Actors
	case "plot":
		return &data.Plot
	case "runtime":
		return &data.Runtime
	default: // rating，字段名在 plan 里已经校验过
		return &data.Rating
	}
}

// editGenres 在编辑器的分类串（" / " 分隔）上增删，保持原有顺序。
func editGenres(current string, add, remove []string) string {
	removeSet := map[string]bool{}
	for _, g := range remove {
		removeSet[strings.TrimSpace(g)] = true
	}
	var out []string
	seen := map[string]bool{}
	appendGenre := func(g string) {
		g = strings.TrimSpace(g)
		if g != "" && !seen[g] && !removeSet[g] {
			seen[g] = true
			out = append(out, g)
		}
	}
	for _, g := range strings.Split(current, "/") {
		appendGenre(g)
	}
	for _, g := range add {
		appendGenre(g)
	}
	return strings.Join(out, " / ")
}

func shortText(text string) string {
	if text == "" {
		return "（空）"
	}
	runes := []rune(strings.Join(strings.Fields(text), " "))
	if len(runes) > 40 {
		return string(runes[:40]) + "…"
	}
	return string(runes)
}
