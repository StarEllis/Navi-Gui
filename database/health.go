package database

import (
	"context"
	"fmt"
	"os"
	"strings"

	"gorm.io/gorm"
)

type ForeignKeyViolation struct {
	Table  string `json:"table" gorm:"column:table"`
	RowID  int64  `json:"row_id" gorm:"column:rowid"`
	Parent string `json:"parent" gorm:"column:parent"`
	FKID   int    `json:"fk_id" gorm:"column:fkid"`
}

type HealthReport struct {
	Healthy              bool                  `json:"healthy"`
	Status               string                `json:"status"`
	SchemaVersion        int                   `json:"schema_version"`
	SupportedVersion     int                   `json:"supported_version"`
	QuickCheck           []string              `json:"quick_check"`
	MissingTables        []string              `json:"missing_tables"`
	MissingColumns       []string              `json:"missing_columns"`
	MissingIndexes       []string              `json:"missing_indexes"`
	ForeignKeyViolations []ForeignKeyViolation `json:"foreign_key_violations"`
	OrphanCounts         map[string]int64      `json:"orphan_counts"`
	WALSizeBytes         int64                 `json:"wal_size_bytes"`
	Checkpoint           *CheckpointResult     `json:"checkpoint,omitempty"`
	Recommendations      []string              `json:"recommendations"`
	Errors               []string              `json:"errors"`
}

var requiredTables = []string{
	"schema_migrations",
	"schema_migration_audits",
	"users",
	"libraries",
	"series",
	"media",
	"people",
	"media_people",
	"favorites",
	"watch_histories",
	"scrape_tasks",
}

var requiredColumns = map[string][]string{
	"media": {"search_text", "search_pinyin", "search_initials"},
}

func (m *Manager) HealthCheck(ctx context.Context, readOnly bool) HealthReport {
	m.mu.Lock()
	defer m.mu.Unlock()
	report := HealthReport{
		Status:           "healthy",
		SupportedVersion: m.SupportedVersion(),
		OrphanCounts:     map[string]int64{},
	}
	if m.db == nil {
		report.Errors = append(report.Errors, "database is closed")
		return finalizeHealth(report)
	}
	if err := ctx.Err(); err != nil {
		report.Errors = append(report.Errors, err.Error())
		return finalizeHealth(report)
	}

	if err := m.db.Raw("PRAGMA quick_check").Scan(&report.QuickCheck).Error; err != nil {
		report.Errors = append(report.Errors, "quick_check: "+err.Error())
	} else {
		for _, result := range report.QuickCheck {
			if !strings.EqualFold(strings.TrimSpace(result), "ok") {
				report.Errors = append(report.Errors, "integrity: "+result)
			}
		}
	}
	version, err := currentSchemaVersion(m.db)
	if err != nil {
		report.Errors = append(report.Errors, err.Error())
	} else {
		report.SchemaVersion = version
		if version > report.SupportedVersion {
			report.Errors = append(report.Errors, fmt.Sprintf("schema version %d is newer than supported version %d", version, report.SupportedVersion))
		}
	}

	for _, table := range requiredTables {
		if !m.db.Migrator().HasTable(table) {
			report.MissingTables = append(report.MissingTables, table)
		}
	}
	for table, columns := range requiredColumns {
		if !m.db.Migrator().HasTable(table) {
			continue
		}
		for _, column := range columns {
			if !m.db.Migrator().HasColumn(table, column) {
				report.MissingColumns = append(report.MissingColumns, table+"."+column)
			}
		}
	}
	for _, index := range requiredIndexDefinitions {
		valid, reason, err := validateRequiredIndex(m.db, index)
		if err != nil {
			report.Errors = append(report.Errors, "inspect index "+index.Name+": "+err.Error())
		} else if !valid {
			report.MissingIndexes = append(report.MissingIndexes, index.Name+": "+reason)
		}
	}

	if err := m.db.Raw("PRAGMA foreign_key_check").Scan(&report.ForeignKeyViolations).Error; err != nil {
		report.Errors = append(report.Errors, "foreign_key_check: "+err.Error())
	}
	for _, rule := range orphanRules {
		if !hasTableAndColumn(m.db, rule.child, rule.column) || !m.db.Migrator().HasTable(rule.parent) {
			continue
		}
		parentFilter := ""
		if m.db.Migrator().HasColumn(rule.parent, "deleted_at") {
			parentFilter = " WHERE deleted_at IS NULL"
		}
		condition := fmt.Sprintf(
			"%s IS NOT NULL AND %s <> '' AND %s NOT IN (SELECT id FROM %s%s)",
			rule.column, rule.column, rule.column, rule.parent, parentFilter,
		)
		var count int64
		if err := m.db.Table(rule.child).Where(condition).Count(&count).Error; err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("orphan check %s.%s: %v", rule.child, rule.column, err))
		} else if count > 0 {
			report.OrphanCounts[rule.child+"."+rule.column] = count
		}
	}
	if m.db.Migrator().HasTable("people") && m.db.Migrator().HasTable("media_people") {
		var count int64
		if err := m.db.Table("people").Where("id NOT IN (SELECT DISTINCT person_id FROM media_people WHERE person_id <> '')").Count(&count).Error; err == nil && count > 0 {
			report.OrphanCounts["people.unreferenced"] = count
		}
	}

	if info, err := os.Stat(m.path + "-wal"); err == nil {
		report.WALSizeBytes = info.Size()
	} else if !os.IsNotExist(err) {
		report.Errors = append(report.Errors, "inspect WAL: "+err.Error())
	}
	if !readOnly && !m.readOnly {
		if checkpoint, err := m.checkpointLocked("PASSIVE"); err != nil {
			report.Errors = append(report.Errors, "WAL checkpoint: "+err.Error())
		} else {
			report.Checkpoint = &checkpoint
		}
	}
	return finalizeHealth(report)
}

func finalizeHealth(report HealthReport) HealthReport {
	switch {
	case len(report.Errors) > 0,
		len(report.MissingTables) > 0,
		len(report.MissingColumns) > 0,
		len(report.MissingIndexes) > 0,
		len(report.ForeignKeyViolations) > 0:
		report.Status = "error"
		report.Healthy = false
	case len(report.OrphanCounts) > 0, report.WALSizeBytes > 64<<20:
		report.Status = "warning"
		report.Healthy = false
	default:
		report.Status = "healthy"
		report.Healthy = true
	}
	if len(report.MissingTables) > 0 {
		report.Recommendations = append(report.Recommendations, "restore a verified migration backup or reinstall a compatible application; do not recreate tables manually")
	}
	if len(report.MissingColumns) > 0 {
		report.Recommendations = append(report.Recommendations, "run the supported database migration path to restore required columns")
	}
	if len(report.MissingIndexes) > 0 {
		report.Recommendations = append(report.Recommendations, "run the supported database migration path to recreate required indexes")
	}
	if len(report.ForeignKeyViolations) > 0 || len(report.OrphanCounts) > 0 {
		report.Recommendations = append(report.Recommendations, "back up the database and run the explicit integrity migration or maintenance command")
	}
	if report.WALSizeBytes > 64<<20 {
		report.Recommendations = append(report.Recommendations, "close active scans and run a WAL checkpoint")
	}
	if len(report.Errors) > 0 {
		report.Recommendations = append(report.Recommendations, "preserve the database and its backups before attempting recovery")
	}
	return report
}

func HasRequiredIndex(db *gorm.DB, name string) bool {
	for _, definition := range requiredIndexDefinitions {
		if definition.Name == name {
			valid, _, err := validateRequiredIndex(db, definition)
			return err == nil && valid
		}
	}
	return false
}
