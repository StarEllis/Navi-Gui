package database

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gorm.io/gorm"
)

type requiredIndexDefinition struct {
	Name       string
	Table      string
	Unique     bool
	Columns    []string
	Collations []string
	Where      string
}

var requiredIndexDefinitions = []requiredIndexDefinition{
	{Name: "idx_favorite_user_media", Table: "favorites", Unique: true, Columns: []string{"user_id", "media_id"}, Collations: []string{"BINARY", "BINARY"}},
	{Name: "idx_watch_user_media", Table: "watch_histories", Unique: true, Columns: []string{"user_id", "media_id"}, Collations: []string{"BINARY", "BINARY"}},
	{Name: "idx_libraries_path_key_active", Table: "libraries", Unique: true, Columns: []string{"path_key"}, Collations: []string{"BINARY"}, Where: "deleted_at IS NULL AND path_key <> ''"},
	{Name: "idx_media_library_path_active", Table: "media", Unique: true, Columns: []string{"library_id", "path_key"}, Collations: []string{"BINARY", "BINARY"}, Where: "deleted_at IS NULL AND path_key <> ''"},
	{Name: "idx_series_library_folder_active", Table: "series", Unique: true, Columns: []string{"library_id", "folder_path_key"}, Collations: []string{"BINARY", "BINARY"}, Where: "deleted_at IS NULL AND folder_path_key <> ''"},
	{Name: "idx_watch_user_completed_updated", Table: "watch_histories", Columns: []string{"user_id", "completed", "updated_at"}, Collations: []string{"BINARY", "BINARY", "BINARY"}},
	{Name: "idx_media_deleted_created", Table: "media", Columns: []string{"deleted_at", "created_at"}, Collations: []string{"BINARY", "BINARY"}},
	{Name: "idx_media_library_type_deleted_created", Table: "media", Columns: []string{"library_id", "media_type", "deleted_at", "created_at"}, Collations: []string{"BINARY", "BINARY", "BINARY", "BINARY"}},
	{Name: "idx_media_series_deleted", Table: "media", Columns: []string{"series_id", "deleted_at"}, Collations: []string{"BINARY", "BINARY"}},
}

type sqliteIndexListRow struct {
	Name    string `gorm:"column:name"`
	Unique  int    `gorm:"column:unique"`
	Partial int    `gorm:"column:partial"`
}

type sqliteIndexXInfoRow struct {
	SeqNo int     `gorm:"column:seqno"`
	CID   int     `gorm:"column:cid"`
	Name  *string `gorm:"column:name"`
	Desc  int     `gorm:"column:desc"`
	Coll  string  `gorm:"column:coll"`
	Key   int     `gorm:"column:key"`
}

func quoteSQLiteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func createRequiredIndexSQL(def requiredIndexDefinition) string {
	columns := make([]string, 0, len(def.Columns))
	for _, column := range def.Columns {
		columns = append(columns, quoteSQLiteIdentifier(column))
	}
	unique := ""
	if def.Unique {
		unique = "UNIQUE "
	}
	statement := fmt.Sprintf("CREATE %sINDEX %s ON %s(%s)", unique, quoteSQLiteIdentifier(def.Name), quoteSQLiteIdentifier(def.Table), strings.Join(columns, ", "))
	if strings.TrimSpace(def.Where) != "" {
		statement += " WHERE " + def.Where
	}
	return statement
}

func normalizeSQLFragment(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), "")
}

func indexWhereClause(sql string) string {
	position := regexp.MustCompile(`(?i)\bwhere\b`).FindStringIndex(sql)
	if position == nil {
		return ""
	}
	return strings.TrimSpace(sql[position[1]:])
}

func validateRequiredIndex(db *gorm.DB, def requiredIndexDefinition) (bool, string, error) {
	var indexes []sqliteIndexListRow
	if err := db.Raw("PRAGMA index_list(" + quoteSQLiteIdentifier(def.Table) + ")").Scan(&indexes).Error; err != nil {
		return false, "", err
	}
	var found *sqliteIndexListRow
	for i := range indexes {
		if indexes[i].Name == def.Name {
			found = &indexes[i]
			break
		}
	}
	if found == nil {
		return false, "missing", nil
	}
	if (found.Unique == 1) != def.Unique {
		return false, "unique flag differs", nil
	}
	wantPartial := strings.TrimSpace(def.Where) != ""
	if (found.Partial == 1) != wantPartial {
		return false, "partial flag differs", nil
	}

	var details []sqliteIndexXInfoRow
	if err := db.Raw("PRAGMA index_xinfo(" + quoteSQLiteIdentifier(def.Name) + ")").Scan(&details).Error; err != nil {
		return false, "", err
	}
	var columns []sqliteIndexXInfoRow
	for _, detail := range details {
		if detail.Key == 1 {
			columns = append(columns, detail)
		}
	}
	sort.Slice(columns, func(i, j int) bool { return columns[i].SeqNo < columns[j].SeqNo })
	if len(columns) != len(def.Columns) {
		return false, fmt.Sprintf("column count=%d want=%d", len(columns), len(def.Columns)), nil
	}
	for i, column := range columns {
		if column.CID < 0 || column.Name == nil || *column.Name != def.Columns[i] {
			return false, fmt.Sprintf("column %d differs", i), nil
		}
		if column.Desc != 0 {
			return false, fmt.Sprintf("column %s sort order differs", def.Columns[i]), nil
		}
		if i < len(def.Collations) && !strings.EqualFold(strings.TrimSpace(column.Coll), def.Collations[i]) {
			return false, fmt.Sprintf("column %s collation differs", def.Columns[i]), nil
		}
	}

	var sql string
	if err := db.Raw("SELECT sql FROM sqlite_master WHERE type='index' AND name = ? AND tbl_name = ?", def.Name, def.Table).Scan(&sql).Error; err != nil {
		return false, "", err
	}
	if normalizeSQLFragment(indexWhereClause(sql)) != normalizeSQLFragment(def.Where) {
		return false, "WHERE clause differs", nil
	}
	return true, "", nil
}

func ensureRequiredIndexes(tx *gorm.DB) error {
	for _, def := range requiredIndexDefinitions {
		if !tx.Migrator().HasTable(def.Table) {
			continue
		}
		valid, reason, err := validateRequiredIndex(tx, def)
		if err != nil {
			return fmt.Errorf("inspect required index %s: %w", def.Name, err)
		}
		if !valid && reason != "missing" {
			if err := tx.Exec("DROP INDEX " + quoteSQLiteIdentifier(def.Name)).Error; err != nil {
				return fmt.Errorf("drop invalid required index %s: %w", def.Name, err)
			}
		}
		if !valid {
			if err := tx.Exec(createRequiredIndexSQL(def)).Error; err != nil {
				return fmt.Errorf("create required index %s: %w", def.Name, err)
			}
		}
		valid, reason, err = validateRequiredIndex(tx, def)
		if err != nil {
			return fmt.Errorf("verify required index %s: %w", def.Name, err)
		}
		if !valid {
			return fmt.Errorf("required index %s is invalid after migration: %s", def.Name, reason)
		}
	}
	return nil
}
