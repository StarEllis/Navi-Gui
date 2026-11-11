package assistant

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const (
	toolRunSQL    = "run_sql"
	toolShowMedia = "show_media"
	// stepThinking 不是工具，只是时间线上的一条"思考"。
	stepThinking = "thinking"

	maxShowMedia = 40
	// 一次查询结果回给模型的上限。超过就从尾部砍行，免得一条 SELECT * 撑爆上下文。
	maxToolResultChars = 40000
)

type toolSpec struct {
	Name        string
	Description string
	Properties  map[string]any
	Required    []string
}

var toolSpecs = []toolSpec{
	{
		Name:        toolRunSQL,
		Description: "在影片库的 SQLite 数据库上执行一条只读 SELECT 或 WITH 查询，最多返回 50 行。写操作会被拒绝。",
		Properties: map[string]any{
			"sql": map[string]any{"type": "string", "description": "一条 SELECT 或 WITH 语句"},
		},
		Required: []string{"sql"},
	},
	{
		Name:        toolShowMedia,
		Description: "把影片以海报卡片展示给用户。传 media.id 列表，按相关程度排序，最多 40 个。",
		Properties: map[string]any{
			"media_ids": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "media 表的 id",
			},
		},
		Required: []string{"media_ids"},
	},
	{
		Name: toolProposeChange,
		Description: "提出一次修改。界面会先给用户看确认卡片，用户点了执行才会真的改，然后把执行结果返回给你。" +
			"一次只做一种 action。action 取值：" +
			"favorite / unfavorite（收藏、取消收藏）；watched / unwatched（标成已看、未看）；" +
			"set_rating（我的评分，rating 1~5，0 表示清掉）；add_tag / remove_tag（用户自建标签，tag 传标签名，加的时候没有就新建）；" +
			"edit_fields（改影片信息，写进影片旁边的 NFO 文件：fields 里放要整体替换的字段，add_genres / remove_genres 增删单个分类）；" +
			"backfill_resources（联网补预告片和剧照，只补缺的）；" +
			"rescan_library（重新扫描媒体库，传 library_id 和 scan_mode）；" +
			"remove_from_library（把影片记录从库里移除，不删视频文件）；" +
			"rename_tag（标签改名：tag 原名，new_name 新名）；merge_tags（把 tag 并进 into_tag，tag 删掉，打过它的片都改打 into_tag）；" +
			"delete_tag（删掉标签 tag，片子本身不动）。这三个不用传 media_ids；" +
			"delete_files（删视频文件：连同封面、NFO 挪到同一个盘的 Navi回收站 文件夹，再从库里移除。只有完全权限能用，一次最多 20 部）。",
		Properties: map[string]any{
			"action": map[string]any{
				"type": "string",
				"enum": []string{"favorite", "unfavorite", "watched", "unwatched", "set_rating", "add_tag", "remove_tag",
					"edit_fields", "backfill_resources", "rescan_library", "remove_from_library", "delete_files",
					"rename_tag", "merge_tags", "delete_tag"},
			},
			"media_ids": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "要改的影片 media.id，rescan_library 和标签管理不用传",
			},
			"rating":     map[string]any{"type": "integer", "description": "set_rating 用，1~5，0 表示清掉"},
			"tag":        map[string]any{"type": "string", "description": "标签名：add_tag / remove_tag 要加或去的，rename_tag / merge_tags / delete_tag 要处理的那个"},
			"new_name":   map[string]any{"type": "string", "description": "rename_tag 用，新名字"},
			"into_tag":   map[string]any{"type": "string", "description": "merge_tags 用，并进去的那个标签名（留下的）"},
			"library_id": map[string]any{"type": "string", "description": "rescan_library 用，libraries.id"},
			"scan_mode": map[string]any{
				"type":        "string",
				"enum":        []string{"incremental", "delete_update", "overwrite"},
				"description": "rescan_library 用：incremental 只加新片（默认）；delete_update 加新片、清掉已不存在的、更新改过的；overwrite 全部重新读一遍",
			},
			"fields": map[string]any{
				"type":                 "object",
				"additionalProperties": map[string]any{"type": "string"},
				"description": "edit_fields 用，字段名 → 新值，整体替换。可用字段：title 标题、code 番号、release_date 发行日期（YYYY-MM-DD）、" +
					"director 导演、series 系列、publisher 发行商、maker 片商、genres 分类（多个用 / 分隔）、actors 演员（多个用 / 分隔）、" +
					"plot 简介、runtime 片长（分钟）、rating 网站评分",
			},
			"add_genres":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "edit_fields 用，要加的分类"},
			"remove_genres": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "edit_fields 用，要去掉的分类"},
		},
		Required: []string{"action"},
	},
	{
		Name:        toolOpenFolder,
		Description: "在资源管理器里打开影片所在的文件夹，不用确认。用户想删视频文件时用它，让用户自己删。一次最多 10 部。",
		Properties: map[string]any{
			"media_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		Required: []string{"media_ids"},
	},
	{
		Name: toolPlayMedia,
		Description: "用播放器播放一部影片，不用确认。用户说「放」「播」「看一部」时用它；" +
			"要随便挑一部就先用 run_sql 加 ORDER BY RANDOM() LIMIT 1 选出来。一次只放一部。",
		Properties: map[string]any{
			"media_id": map[string]any{"type": "string", "description": "media 表的 id"},
		},
		Required: []string{"media_id"},
	},
}

type toolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

type toolResult struct {
	CallID  string
	Content string
	IsError bool
}

// Step 是一次工具调用，界面上用来展示"查了什么"。
type Step struct {
	Tool    string `json:"tool"`
	Input   string `json:"input"`
	Summary string `json:"summary"`
	Error   string `json:"error,omitempty"`
}

// MediaCard 的字段名和媒体列表一致，前端可以直接交给 MediaCard 组件渲染。
type MediaCard struct {
	ID           string  `json:"id"`
	Code         string  `json:"code"`
	Title        string  `json:"title"`
	Year         int     `json:"year"`
	Resolution   string  `json:"resolution"`
	Duration     float64 `json:"duration"`
	PosterPath   string  `json:"poster_path"`
	BackdropPath string  `json:"backdrop_path"`
	UpdatedAt    string  `json:"updated_at"`
	FilePath     string  `json:"file_path"`
	IsFavorite   bool    `json:"is_favorite"`
	IsWatched    bool    `json:"is_watched"`
	MyRating     int     `json:"my_rating"`
}

// turnState 收集一轮对话里的查询步骤和要展示的影片。
type turnState struct {
	steps []Step
	media []MediaCard
	shown map[string]bool
	// queriedMedia 记下这一轮有没有查出过带 id 列的结果，用来判断是不是找片的问题。
	queriedMedia bool
	changes      []Proposal
	permission   string
}

func (s *Session) runTool(ctx context.Context, call toolCall, turn *turnState) toolResult {
	var step Step
	var content string
	var err error
	switch call.Name {
	case toolRunSQL:
		step, content, err = s.runSQL(ctx, call.Input, turn)
	case toolShowMedia:
		step, content, err = s.showMedia(ctx, call.Input, turn)
	case toolProposeChange:
		step, content, err = s.proposeChange(ctx, call.Input, turn)
	case toolOpenFolder:
		step, content, err = s.openFolder(call.Input)
	case toolPlayMedia:
		step, content, err = s.playMedia(ctx, call.Input, turn)
	default:
		step = Step{Tool: call.Name}
		err = fmt.Errorf("没有这个工具：%s", call.Name)
	}
	if err != nil {
		step.Error = err.Error()
		content = err.Error()
	}
	s.recordStep(turn, step)
	return toolResult{CallID: call.ID, Content: content, IsError: err != nil}
}

func (s *Session) recordStep(turn *turnState, step Step) {
	turn.steps = append(turn.steps, step)
	if s.OnStep != nil {
		s.OnStep(step)
	}
}

func (s *Session) runSQL(ctx context.Context, raw json.RawMessage, turn *turnState) (Step, string, error) {
	var input struct {
		SQL string `json:"sql"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return Step{Tool: toolRunSQL}, "", fmt.Errorf("参数不是合法 JSON：%w", err)
	}
	step := Step{Tool: toolRunSQL, Input: strings.TrimSpace(input.SQL)}
	result, err := s.sandbox.Query(ctx, input.SQL)
	if err != nil {
		return step, "", err
	}
	if len(result.Rows) > 0 && len(result.Columns) > 0 && strings.EqualFold(result.Columns[0], "id") {
		turn.queriedMedia = true
	}

	note := ""
	content, err := json.Marshal(result)
	for err == nil && len(content) > maxToolResultChars && len(result.Rows) > 1 {
		result.Rows = result.Rows[:len(result.Rows)/2]
		result.Truncated = true
		note = "结果太长，只保留了前面几行，少选几列再查"
		content, err = json.Marshal(result)
	}
	if err != nil {
		return step, "", err
	}
	step.Summary = fmt.Sprintf("返回 %d 行", len(result.Rows))
	if result.Truncated {
		step.Summary += "（已截断）"
	}
	if note != "" {
		return step, string(content) + "\n" + note, nil
	}
	return step, string(content), nil
}

func (s *Session) showMedia(ctx context.Context, raw json.RawMessage, turn *turnState) (Step, string, error) {
	var input struct {
		MediaIDs []string `json:"media_ids"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return Step{Tool: toolShowMedia}, "", fmt.Errorf("参数不是合法 JSON：%w", err)
	}
	ids := make([]string, 0, len(input.MediaIDs))
	picked := make(map[string]bool, len(input.MediaIDs))
	for _, id := range input.MediaIDs {
		id = strings.TrimSpace(id)
		if id != "" && !turn.shown[id] && !picked[id] && len(ids) < maxShowMedia {
			picked[id] = true
			ids = append(ids, id)
		}
	}
	step := Step{Tool: toolShowMedia, Input: fmt.Sprintf("%d 部", len(ids))}
	cards, err := s.sandbox.loadMediaCards(ctx, ids, s.userID)
	if err != nil {
		return step, "", err
	}

	found := make(map[string]bool, len(cards))
	for _, card := range cards {
		found[card.ID] = true
		turn.shown[card.ID] = true
	}
	turn.media = append(turn.media, cards...)
	var missing []string
	for _, id := range ids {
		if !found[id] {
			missing = append(missing, id)
		}
	}
	step.Summary = fmt.Sprintf("展示 %d 部", len(cards))
	content := fmt.Sprintf("已展示 %d 部。", len(cards))
	if len(missing) > 0 {
		content += "这些 id 不存在或已删除：" + strings.Join(missing, ", ")
	}
	return step, content, nil
}

// loadMediaCards 按传入顺序取卡片数据，顺带查出收藏、已看和我的评分，
// 卡片上的按钮状态才对得上。
func (s *Sandbox) loadMediaCards(ctx context.Context, ids []string, userID string) ([]MediaCard, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := []any{userID, userID, userID}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT m.id, COALESCE(m.code, ''), COALESCE(m.title, ''), COALESCE(m.year, 0),
       COALESCE(m.resolution, ''), COALESCE(m.duration, 0),
       COALESCE(m.poster_path, ''), COALESCE(m.backdrop_path, ''), COALESCE(CAST(m.updated_at AS TEXT), ''),
       COALESCE(m.file_path, ''),
       EXISTS(SELECT 1 FROM favorites f WHERE f.media_id = m.id AND f.user_id = ?),
       EXISTS(SELECT 1 FROM watch_histories w WHERE w.media_id = m.id AND w.user_id = ? AND w.completed = 1),
       COALESCE((SELECT r.score FROM media_ratings r WHERE r.media_id = m.id AND r.user_id = ?), 0)
FROM media m
WHERE m.deleted_at IS NULL AND m.id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byID := make(map[string]MediaCard, len(ids))
	for rows.Next() {
		var card MediaCard
		var favorite, watched sql.NullBool
		if err := rows.Scan(
			&card.ID, &card.Code, &card.Title, &card.Year,
			&card.Resolution, &card.Duration,
			&card.PosterPath, &card.BackdropPath, &card.UpdatedAt,
			&card.FilePath, &favorite, &watched, &card.MyRating,
		); err != nil {
			return nil, err
		}
		card.IsFavorite = favorite.Bool
		card.IsWatched = watched.Bool
		byID[card.ID] = card
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	cards := make([]MediaCard, 0, len(byID))
	for _, id := range ids {
		if card, ok := byID[id]; ok {
			cards = append(cards, card)
		}
	}
	return cards, nil
}

// 番号的样子：FC2-1234567 / FC2-PPV-1234567，或 2~6 个字母加 2~5 位数字（SSIS-338）。
var mediaCodePattern = regexp.MustCompile(`(?i)\b(?:FC2-(?:PPV-)?\d{5,8}|[A-Z]{2,6}-\d{2,5})\b`)

// showMediaFromAnswer 兜底：模型回答里点了番号却忘了调用 show_media，
// 就按番号把片库里有的卡片补上，免得界面上一张卡都没有。
func (s *Session) showMediaFromAnswer(ctx context.Context, answer string, turn *turnState) {
	var codes []string
	seen := map[string]bool{}
	for _, code := range mediaCodePattern.FindAllString(answer, -1) {
		code = strings.ToUpper(code)
		if !seen[code] {
			seen[code] = true
			codes = append(codes, code)
		}
	}
	if len(codes) == 0 {
		return
	}
	ids, err := s.sandbox.mediaIDsByCode(ctx, codes)
	if err != nil || len(ids) == 0 {
		return
	}
	if len(ids) > maxShowMedia {
		ids = ids[:maxShowMedia]
	}
	cards, err := s.sandbox.loadMediaCards(ctx, ids, s.userID)
	if err != nil || len(cards) == 0 {
		return
	}
	turn.media = append(turn.media, cards...)
	s.recordStep(turn, Step{
		Tool:    toolShowMedia,
		Input:   fmt.Sprintf("%d 部", len(cards)),
		Summary: fmt.Sprintf("按回答里的番号补上 %d 部", len(cards)),
	})
}

// mediaIDsByCode 按番号找片，顺序跟传入一致；同一番号有多个文件时只取一个。
func (s *Sandbox) mediaIDsByCode(ctx context.Context, codes []string) ([]string, error) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(codes)), ",")
	args := make([]any, len(codes))
	for i, code := range codes {
		args[i] = code
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT UPPER(code), id FROM media
WHERE deleted_at IS NULL AND UPPER(code) IN (`+placeholders+`)
ORDER BY library_added_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byCode := map[string]string{}
	for rows.Next() {
		var code, id string
		if err := rows.Scan(&code, &id); err != nil {
			return nil, err
		}
		if _, ok := byCode[code]; !ok {
			byCode[code] = id
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(byCode))
	for _, code := range codes {
		if id, ok := byCode[code]; ok {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
