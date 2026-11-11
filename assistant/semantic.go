package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	toolSemanticSearch = "semantic_search"

	defaultSemanticLimit = 30
	maxSemanticLimit     = 60
	// candidate_sql 圈出的范围最多这么多部，再多就不算"圈范围"了。
	maxSemanticCandidates = 5000
	semanticSnippetRunes  = 160
)

// semanticSearchSpec 只在配好向量模型、坐标也建好时才给模型。
var semanticSearchSpec = toolSpec{
	Name: toolSemanticSearch,
	Description: "按意思找片：把一句剧情或设定描述跟每部片的标题、标签、简介比较，按意思接近程度排序，返回 id、番号、标题、相似度和简介开头。" +
		"同义说法、换了措辞的简介也能找到，不用自己猜关键词。" +
		"它只懂\"讲的是不是同一类事\"，分不清\"谁对谁做了什么\"，所以结果要读一遍简介，把方向反了、明显不对题的去掉。" +
		"有分辨率、片商、看没看过这类条件时，用 candidate_sql 先圈出范围，再在范围里按意思排。",
	Properties: map[string]any{
		"query": map[string]any{
			"type":        "string",
			"description": "用一句完整的中文描述想找的剧情或设定，比如「女上司被下属反过来压制」，不要只堆关键词",
		},
		"candidate_sql": map[string]any{
			"type": "string",
			"description": "可选。先圈范围：一条只读 SELECT，必须返回 id 列（media.id），记得带 deleted_at IS NULL。" +
				"例：SELECT id FROM media WHERE deleted_at IS NULL AND resolution = '4K'",
		},
		"limit": map[string]any{"type": "integer", "description": "要几部，默认 30，最多 60"},
	},
	Required: []string{"query"},
}

// toolsFor 返回这次要给模型的工具。没开按意思找片时，模型根本看不到这个工具。
func toolsFor(semantic bool) []toolSpec {
	if !semantic {
		return toolSpecs
	}
	return append(append([]toolSpec{}, toolSpecs...), semanticSearchSpec)
}

func (s *Session) semanticSearch(ctx context.Context, raw json.RawMessage, turn *turnState) (Step, string, error) {
	step := Step{Tool: toolSemanticSearch}
	if s.Index == nil || !turn.embedding.Enabled() {
		return step, "", errors.New("没开按意思找片，改用 run_sql 按文字查")
	}
	var input struct {
		Query        string `json:"query"`
		CandidateSQL string `json:"candidate_sql"`
		Limit        int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return step, "", fmt.Errorf("参数不是合法 JSON：%w", err)
	}
	query := strings.TrimSpace(input.Query)
	if query == "" {
		return step, "", errors.New("没给 query")
	}
	step.Input = query
	limit := input.Limit
	if limit <= 0 {
		limit = defaultSemanticLimit
	}
	limit = min(limit, maxSemanticLimit)

	var candidates map[string]bool
	if sql := strings.TrimSpace(input.CandidateSQL); sql != "" {
		ids, err := s.sandbox.queryIDs(ctx, sql, maxSemanticCandidates)
		if err != nil {
			return step, "", fmt.Errorf("candidate_sql 出错：%w", err)
		}
		candidates = make(map[string]bool, len(ids))
		for _, id := range ids {
			candidates[id] = true
		}
		step.Input += fmt.Sprintf("（先圈出 %d 部）", len(ids))
		if len(ids) == 0 {
			step.Summary = "范围里没有片"
			return step, "candidate_sql 圈出来是 0 部，条件可能写严了，放宽后再试。", nil
		}
	}

	hits, note, err := s.search(ctx, turn.embedding, query, candidates, limit)
	if err != nil {
		// 接口不通时别让整句话失败：告诉模型退回按文字查。
		return step, "", fmt.Errorf("按意思找暂时用不了（%v），改用 run_sql 按文字查", err)
	}
	turn.queriedMedia = true
	step.Summary = fmt.Sprintf("挑出 %d 部", len(hits))
	if len(hits) == 0 {
		return step, "一部都没找到。" + note, nil
	}

	details, err := s.sandbox.loadHitDetails(ctx, hits)
	if err != nil {
		return step, "", err
	}
	var b strings.Builder
	// 相似度的绝对值随模型和问法浮动，不给门槛，免得模型按分数把对题的片扔掉。
	fmt.Fprintf(&b, "按意思接近程度从高到低排好了。相似度只看相对高低，别按分数线取舍，以简介为准：读一遍，把不对题的去掉，再 show_media。\n")
	if note != "" {
		b.WriteString(note + "\n")
	}
	b.WriteString("id | 番号 | 标题 | 相似度 | 简介开头\n")
	for _, hit := range hits {
		d := details[hit.ID]
		fmt.Fprintf(&b, "%s | %s | %s | %.2f | %s\n", hit.ID, d.code, d.title, hit.Score, d.snippet)
	}
	return step, b.String(), nil
}

type hitDetail struct {
	code, title, snippet string
}

func (s *Sandbox) loadHitDetails(ctx context.Context, hits []semanticHit) (map[string]hitDetail, error) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(hits)), ",")
	args := make([]any, len(hits))
	for i, hit := range hits {
		args[i] = hit.ID
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, COALESCE(code, ''), COALESCE(title, ''), COALESCE(overview, '')
FROM media WHERE id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	details := make(map[string]hitDetail, len(hits))
	for rows.Next() {
		var id, code, title, overview string
		if err := rows.Scan(&id, &code, &title, &overview); err != nil {
			return nil, err
		}
		snippet := documentText("", "", overview)
		if runes := []rune(snippet); len(runes) > semanticSnippetRunes {
			snippet = string(runes[:semanticSnippetRunes]) + "…"
		}
		if snippet == "" {
			snippet = "（没有简介）"
		}
		details[id] = hitDetail{code: code, title: strings.ReplaceAll(title, "|", "/"), snippet: strings.ReplaceAll(snippet, "|", "/")}
	}
	return details, rows.Err()
}

// queryIDs 跑模型给的圈范围查询，只取 id 列，最多 limit 个。防线和 Query 一样：只读连接、包成子查询。
func (s *Sandbox) queryIDs(ctx context.Context, query string, limit int) ([]string, error) {
	query = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(query), "; \t\r\n"))
	if query == "" {
		return nil, errors.New("SQL 为空")
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	aggregate := fmt.Sprintf("SELECT json_group_array(id) FROM (SELECT id FROM (SELECT * FROM (\n%s\n)) LIMIT %d)", query, limit)
	// 先在超时保护下跑一遍，原因见 Query。
	if _, err := s.db.ExecContext(ctx, aggregate); err != nil {
		return nil, s.idQueryError(ctx, err)
	}
	var payload string
	if err := s.db.QueryRowContext(ctx, aggregate).Scan(&payload); err != nil {
		return nil, s.idQueryError(ctx, err)
	}
	var ids []string
	if err := json.Unmarshal([]byte(payload), &ids); err != nil {
		return nil, fmt.Errorf("圈范围的结果读不出来，确认 id 列是 media.id：%w", err)
	}
	return ids, nil
}

func (s *Sandbox) idQueryError(ctx context.Context, err error) error {
	if strings.Contains(err.Error(), "no such column: id") {
		return errors.New("查询结果里要有 id 列（media.id）")
	}
	return s.queryError(ctx, err)
}
