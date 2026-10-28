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
}

func (s *Session) runTool(ctx context.Context, call toolCall, turn *turnState) toolResult {
	var step Step
	var content string
	var err error
	switch call.Name {
	case toolRunSQL:
		step, content, err = s.runSQL(ctx, call.Input)
	case toolShowMedia:
		step, content, err = s.showMedia(ctx, call.Input, turn)
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

func (s *Session) runSQL(ctx context.Context, raw json.RawMessage) (Step, string, error) {
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
