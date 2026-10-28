package assistant

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const (
	defaultQueryTimeout = 10 * time.Second
	defaultMaxRows      = 50
	// 单格超过这个长度就截断：简介、路径偶尔很长，整段塞回模型既费钱又没用。
	maxCellRunes = 500
)

// Sandbox 对 navi.db 开一条独立的只读连接，专门给模型跑查询。
//
// 防线全在数据库这一层，不做关键词黑名单（正则既拦不全，又会误伤字面量）：
//   - mode=ro + query_only：SQLite 拒绝一切写语句；
//   - 语句被包进 SELECT * FROM (...)：子查询语法只接受 SELECT / WITH / VALUES，
//     ATTACH、PRAGMA 赋值、VACUUM、多条语句在解析阶段就失败。
type Sandbox struct {
	db      *sql.DB
	timeout time.Duration
	maxRows int
}

type QueryResult struct {
	Columns   []string `json:"columns"`
	Rows      [][]any  `json:"rows"`
	Truncated bool     `json:"truncated"`
}

func OpenSandbox(dbPath string) (*Sandbox, error) {
	values := url.Values{}
	values.Set("mode", "ro")
	values.Add("_pragma", "query_only(1)")
	values.Add("_pragma", "busy_timeout(5000)")
	dsn := "file:" + filepath.ToSlash(dbPath) + "?" + values.Encode()

	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("open read-only database: %w", err)
	}
	db, err := gdb.DB()
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(2)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping read-only database: %w", err)
	}
	return &Sandbox{db: db, timeout: defaultQueryTimeout, maxRows: defaultMaxRows}, nil
}

func (s *Sandbox) Close() error {
	return s.db.Close()
}

// Query 执行模型给的一条只读查询，最多返回 maxRows 行。
func (s *Sandbox) Query(ctx context.Context, query string) (*QueryResult, error) {
	query = strings.TrimSpace(query)
	query = strings.TrimSpace(strings.TrimRight(query, "; \t\r\n"))
	if query == "" {
		return nil, fmt.Errorf("SQL 为空")
	}

	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	// 前后换行：末尾的 -- 注释不会吞掉右括号。
	wrapped := "SELECT * FROM (\n" + query + "\n)"

	// 驱动的 QueryContext 只编译不执行，这里只为拿列名（语法错误也在这一步暴露）。
	rows, err := s.db.QueryContext(ctx, wrapped)
	if err != nil {
		return nil, s.queryError(ctx, err)
	}
	columns, err := rows.Columns()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}

	// glebarez/go-sqlite 只在 Exec 的那一步 step 上响应 ctx 超时，rows.Next 里跑飞的查询
	// 打断不了。所以把整个结果聚合成一行 JSON：全部计算都落在第一步里，
	// 先用 Exec 在超时保护下跑一遍，没超时再真正取数（同一条语句，耗时已被证明有上限）。
	quoted := make([]string, len(columns))
	for i, column := range columns {
		quoted[i] = `"` + strings.ReplaceAll(column, `"`, `""`) + `"`
	}
	aggregate := fmt.Sprintf(
		"SELECT json_group_array(json_array(%s)) FROM (SELECT * FROM (%s) LIMIT %d)",
		strings.Join(quoted, ", "), wrapped, s.maxRows+1,
	)
	if _, err := s.db.ExecContext(ctx, aggregate); err != nil {
		return nil, s.queryError(ctx, err)
	}
	var payload string
	if err := s.db.QueryRowContext(ctx, aggregate).Scan(&payload); err != nil {
		return nil, s.queryError(ctx, err)
	}

	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.UseNumber()
	var rowsData [][]any
	if err := decoder.Decode(&rowsData); err != nil {
		return nil, fmt.Errorf("decode query result: %w", err)
	}
	result := &QueryResult{Columns: columns, Rows: rowsData}
	if len(result.Rows) > s.maxRows {
		result.Rows = result.Rows[:s.maxRows]
		result.Truncated = true
	}
	for _, row := range result.Rows {
		for i, value := range row {
			if text, ok := value.(string); ok {
				row[i] = truncateRunes(text)
			}
		}
	}
	return result, nil
}

func (s *Sandbox) queryError(ctx context.Context, err error) error {
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("查询超过 %s 被中止，请缩小范围或换用带索引的条件", s.timeout)
	}
	return err
}

func truncateRunes(text string) string {
	if utf8.RuneCountInString(text) <= maxCellRunes {
		return text
	}
	runes := []rune(text)
	return string(runes[:maxCellRunes]) + "…(截断)"
}
