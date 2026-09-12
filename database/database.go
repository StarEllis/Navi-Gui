package database

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const (
	DefaultBusyTimeout = 5 * time.Second
	DefaultBackupKeep  = 5
)

var ErrDatabaseTooNew = errors.New("database schema is newer than this application")

type MigrationStats map[string]int64

type Migration struct {
	Version int
	Name    string
	Apply   func(*gorm.DB) (MigrationStats, error)
}

type Options struct {
	ReadOnly        bool
	BackupDir       string
	BackupKeep      int
	BusyTimeout     time.Duration
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	Migrations      []Migration
}

type Manager struct {
	mu              sync.Mutex
	path            string
	backupDir       string
	backupKeep      int
	busyTimeout     time.Duration
	maxOpenConns    int
	maxIdleConns    int
	connMaxLifetime time.Duration
	readOnly        bool
	migrations      []Migration
	db              *gorm.DB
}

type SchemaMigration struct {
	Version   int       `gorm:"primaryKey"`
	Name      string    `gorm:"type:text;not null"`
	AppliedAt time.Time `gorm:"not null"`
}

func (SchemaMigration) TableName() string { return "schema_migrations" }

type SchemaMigrationAudit struct {
	ID        uint      `gorm:"primaryKey"`
	Version   int       `gorm:"index;not null"`
	Category  string    `gorm:"type:text;not null"`
	Count     int64     `gorm:"not null"`
	Details   string    `gorm:"type:text"`
	CreatedAt time.Time `gorm:"not null"`
}

func (SchemaMigrationAudit) TableName() string { return "schema_migration_audits" }

func DefaultOptions() Options {
	return Options{
		BackupKeep:   DefaultBackupKeep,
		BusyTimeout:  DefaultBusyTimeout,
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	}
}

func Open(path string, options Options) (*Manager, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("database path is empty")
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}
	options = normalizeOptions(options)
	_, statErr := os.Stat(absolutePath)
	newDatabase := os.IsNotExist(statErr)
	if statErr != nil && !newDatabase {
		return nil, fmt.Errorf("inspect database path: %w", statErr)
	}
	if !options.ReadOnly {
		if err := os.MkdirAll(filepath.Dir(absolutePath), 0o755); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}

	m := &Manager{
		path:            absolutePath,
		backupDir:       options.BackupDir,
		backupKeep:      options.BackupKeep,
		busyTimeout:     options.BusyTimeout,
		maxOpenConns:    options.MaxOpenConns,
		maxIdleConns:    options.MaxIdleConns,
		connMaxLifetime: options.ConnMaxLifetime,
		readOnly:        options.ReadOnly,
		migrations:      append([]Migration(nil), options.Migrations...),
	}
	if m.backupDir == "" {
		m.backupDir = absolutePath + ".backups"
	}
	if len(m.migrations) == 0 {
		m.migrations = DefaultMigrations()
	}
	if err := validateMigrations(m.migrations); err != nil {
		return nil, err
	}

	db, err := m.openConnection()
	if err != nil {
		return nil, err
	}
	m.db = db
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = m.closeConnection(false)
		}
	}()

	if err := m.validateOpenDatabase(); err != nil {
		return nil, err
	}
	version, err := currentSchemaVersion(m.db)
	if err != nil {
		return nil, err
	}
	supported := m.SupportedVersion()
	if version > supported && !m.readOnly {
		return nil, fmt.Errorf("%w: database=%d application=%d", ErrDatabaseTooNew, version, supported)
	}
	if err := validateVersionSequence(m.db, version); err != nil {
		return nil, err
	}
	if m.usesDefaultMigrations() {
		if err := validateNaviDatabaseIdentity(m.db, version, newDatabase); err != nil {
			return nil, err
		}
	}
	if m.readOnly {
		closeOnError = false
		return m, nil
	}
	if version < supported {
		// 刚建出来的空库没有任何东西可救，跳过备份；只有已有数据的库升级前才存一份。
		if !newDatabase {
			if _, err := m.createBackupLocked(version, supported, "migration"); err != nil {
				return nil, fmt.Errorf("migration backup failed; database was not modified: %w", err)
			}
		}
		if err := m.applyMigrations(version); err != nil {
			return nil, err
		}
	}
	if err := m.verifyRuntimeConfiguration(); err != nil {
		return nil, err
	}
	closeOnError = false
	return m, nil
}

func normalizeOptions(options Options) Options {
	defaults := DefaultOptions()
	if options.BackupKeep <= 0 {
		options.BackupKeep = defaults.BackupKeep
	}
	if options.BusyTimeout <= 0 {
		options.BusyTimeout = defaults.BusyTimeout
	}
	if options.MaxOpenConns <= 0 {
		options.MaxOpenConns = defaults.MaxOpenConns
	}
	if options.MaxIdleConns <= 0 {
		options.MaxIdleConns = defaults.MaxIdleConns
	}
	if options.MaxIdleConns > options.MaxOpenConns {
		options.MaxIdleConns = options.MaxOpenConns
	}
	return options
}

func validateMigrations(migrations []Migration) error {
	for i, migration := range migrations {
		expected := i + 1
		if migration.Version != expected {
			return fmt.Errorf("migration sequence invalid: expected version %d, got %d", expected, migration.Version)
		}
		if strings.TrimSpace(migration.Name) == "" || migration.Apply == nil {
			return fmt.Errorf("migration %d is incomplete", migration.Version)
		}
	}
	return nil
}

func (m *Manager) openConnection() (*gorm.DB, error) {
	dsn := sqliteDSN(m.path, m.readOnly, m.busyTimeout)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: false,
	})
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get sqlite connection pool: %w", err)
	}
	sqlDB.SetMaxOpenConns(m.maxOpenConns)
	sqlDB.SetMaxIdleConns(m.maxIdleConns)
	sqlDB.SetConnMaxLifetime(m.connMaxLifetime)
	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping sqlite database: %w", err)
	}
	return db, nil
}

func sqliteDSN(path string, readOnly bool, busyTimeout time.Duration) string {
	values := url.Values{}
	if readOnly {
		values.Set("mode", "ro")
	} else {
		values.Set("mode", "rwc")
		values.Add("_pragma", "journal_mode(WAL)")
		values.Add("_pragma", "synchronous(NORMAL)")
	}
	values.Add("_pragma", "foreign_keys(1)")
	values.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeout.Milliseconds()))
	return "file:" + filepath.ToSlash(path) + "?" + values.Encode()
}

func (m *Manager) validateOpenDatabase() error {
	var result string
	if err := m.db.Raw("PRAGMA quick_check").Scan(&result).Error; err != nil {
		return fmt.Errorf("sqlite quick_check failed: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(result), "ok") {
		return fmt.Errorf("sqlite quick_check reported corruption: %s", result)
	}
	return nil
}

func (m *Manager) verifyRuntimeConfiguration() error {
	expectations := map[string]string{
		"foreign_keys": "1",
		"journal_mode": "wal",
		"synchronous":  "1",
	}
	for name, want := range expectations {
		var value string
		if err := m.db.Raw("PRAGMA " + name).Scan(&value).Error; err != nil {
			return fmt.Errorf("read PRAGMA %s: %w", name, err)
		}
		if !strings.EqualFold(strings.TrimSpace(value), want) {
			return fmt.Errorf("critical PRAGMA %s=%s, expected %s", name, value, want)
		}
	}
	var timeout int64
	if err := m.db.Raw("PRAGMA busy_timeout").Scan(&timeout).Error; err != nil {
		return fmt.Errorf("read PRAGMA busy_timeout: %w", err)
	}
	if timeout != m.busyTimeout.Milliseconds() {
		return fmt.Errorf("critical PRAGMA busy_timeout=%d, expected %d", timeout, m.busyTimeout.Milliseconds())
	}
	return nil
}

func currentSchemaVersion(db *gorm.DB) (int, error) {
	if !db.Migrator().HasTable(&SchemaMigration{}) {
		return 0, nil
	}
	var version sql.NullInt64
	if err := db.Model(&SchemaMigration{}).Select("MAX(version)").Scan(&version).Error; err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	if !version.Valid {
		return 0, nil
	}
	return int(version.Int64), nil
}

func validateVersionSequence(db *gorm.DB, current int) error {
	if current == 0 || !db.Migrator().HasTable(&SchemaMigration{}) {
		return nil
	}
	var versions []int
	if err := db.Model(&SchemaMigration{}).Order("version ASC").Pluck("version", &versions).Error; err != nil {
		return fmt.Errorf("read migration sequence: %w", err)
	}
	if len(versions) != current {
		return fmt.Errorf("schema migration history has gaps: current=%d entries=%v", current, versions)
	}
	for i, version := range versions {
		if version != i+1 {
			return fmt.Errorf("schema migration history skipped version %d: entries=%v", i+1, versions)
		}
	}
	return nil
}

func validateKnownLegacySchema(db *gorm.DB, allowEmpty bool) error {
	var names []string
	if err := db.Raw("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name NOT IN ('schema_migrations','schema_migration_audits')").Scan(&names).Error; err != nil {
		return fmt.Errorf("inspect legacy schema: %w", err)
	}
	if len(names) == 0 {
		if allowEmpty {
			return nil
		}
		return fmt.Errorf("unsupported legacy database: no Navi tables were found")
	}
	required := []string{"libraries", "media", "series"}
	for _, table := range required {
		if !db.Migrator().HasTable(table) {
			return fmt.Errorf("unsupported legacy database: found tables but required table %q is missing; back up the database and use a compatible older application first", table)
		}
	}
	requiredColumns := map[string][]string{
		"libraries": {"id", "path"},
		"media":     {"id", "library_id", "file_path"},
		"series":    {"id", "library_id", "folder_path"},
	}
	for table, columns := range requiredColumns {
		for _, column := range columns {
			if !db.Migrator().HasColumn(table, column) {
				return fmt.Errorf("unsupported legacy database: %s.%s is missing; refusing to guess an unknown schema", table, column)
			}
		}
	}
	return nil
}

func validateNaviDatabaseIdentity(db *gorm.DB, version int, allowEmptyLegacy bool) error {
	if version == 0 {
		return validateKnownLegacySchema(db, allowEmptyLegacy)
	}
	metadataColumns := map[string][]string{
		"schema_migrations":       {"version", "name", "applied_at"},
		"schema_migration_audits": {"version", "category", "count", "created_at"},
	}
	for table, columns := range metadataColumns {
		if !db.Migrator().HasTable(table) {
			return fmt.Errorf("invalid Navi database: migration table %s is missing", table)
		}
		for _, column := range columns {
			if !db.Migrator().HasColumn(table, column) {
				return fmt.Errorf("invalid Navi database: %s.%s is missing", table, column)
			}
		}
	}
	coreColumns := map[string][]string{
		"users":     {"id", "username"},
		"libraries": {"id", "path"},
		"media":     {"id", "library_id", "file_path"},
		"series":    {"id", "library_id", "folder_path"},
	}
	if version >= 2 {
		coreColumns["libraries"] = append(coreColumns["libraries"], "path_key")
		coreColumns["media"] = append(coreColumns["media"], "path_key")
		coreColumns["series"] = append(coreColumns["series"], "folder_path_key")
	}
	for table, columns := range coreColumns {
		if !db.Migrator().HasTable(table) {
			return fmt.Errorf("invalid Navi database: core table %s is missing", table)
		}
		for _, column := range columns {
			if !db.Migrator().HasColumn(table, column) {
				return fmt.Errorf("invalid Navi database: %s.%s is missing", table, column)
			}
		}
	}
	return nil
}

func (m *Manager) usesDefaultMigrations() bool {
	defaults := DefaultMigrations()
	if len(m.migrations) != len(defaults) {
		return false
	}
	for i := range defaults {
		if m.migrations[i].Version != defaults[i].Version || m.migrations[i].Name != defaults[i].Name {
			return false
		}
	}
	return true
}

func (m *Manager) applyMigrations(current int) error {
	for _, migration := range m.migrations {
		if migration.Version <= current {
			continue
		}
		err := m.db.Transaction(func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&SchemaMigration{}, &SchemaMigrationAudit{}); err != nil {
				return fmt.Errorf("create migration metadata: %w", err)
			}
			stats, err := migration.Apply(tx)
			if err != nil {
				return err
			}
			keys := make([]string, 0, len(stats))
			for category := range stats {
				keys = append(keys, category)
			}
			sort.Strings(keys)
			for _, category := range keys {
				audit := SchemaMigrationAudit{
					Version:   migration.Version,
					Category:  category,
					Count:     stats[category],
					CreatedAt: time.Now().UTC(),
				}
				if err := tx.Create(&audit).Error; err != nil {
					return fmt.Errorf("record migration audit %s: %w", category, err)
				}
			}
			record := SchemaMigration{
				Version:   migration.Version,
				Name:      migration.Name,
				AppliedAt: time.Now().UTC(),
			}
			if err := tx.Create(&record).Error; err != nil {
				return fmt.Errorf("record schema version %d: %w", migration.Version, err)
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("database migration %d (%s) failed and was rolled back: %w", migration.Version, migration.Name, err)
		}
	}
	return nil
}

func (m *Manager) DB() *gorm.DB {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.db
}

func (m *Manager) Path() string      { return m.path }
func (m *Manager) BackupDir() string { return m.backupDir }

func (m *Manager) SupportedVersion() int {
	if len(m.migrations) == 0 {
		return 0
	}
	return m.migrations[len(m.migrations)-1].Version
}

func (m *Manager) SchemaVersion() (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.db == nil {
		return 0, fmt.Errorf("database is closed")
	}
	return currentSchemaVersion(m.db)
}

type CheckpointResult struct {
	Busy         int `json:"busy"`
	LogFrames    int `json:"log_frames"`
	Checkpointed int `json:"checkpointed_frames"`
}

func (m *Manager) Checkpoint(mode string) (CheckpointResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.checkpointLocked(mode)
}

func (m *Manager) checkpointLocked(mode string) (CheckpointResult, error) {
	var result CheckpointResult
	if m.db == nil || m.readOnly {
		return result, nil
	}
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case "PASSIVE", "FULL", "RESTART", "TRUNCATE":
		mode = strings.ToUpper(strings.TrimSpace(mode))
	default:
		mode = "PASSIVE"
	}
	row := m.db.Raw("PRAGMA wal_checkpoint(" + mode + ")").Row()
	if err := row.Scan(&result.Busy, &result.LogFrames, &result.Checkpointed); err != nil {
		return result, fmt.Errorf("WAL checkpoint %s: %w", mode, err)
	}
	return result, nil
}

func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closeConnection(true)
}

func (m *Manager) closeConnection(checkpoint bool) error {
	if m.db == nil {
		return nil
	}
	var checkpointErr error
	if checkpoint && !m.readOnly {
		checkpointErr = m.checkpointNoLock("TRUNCATE")
	}
	sqlDB, err := m.db.DB()
	if err == nil {
		err = sqlDB.Close()
	}
	m.db = nil
	return errors.Join(checkpointErr, err)
}

func (m *Manager) checkpointNoLock(mode string) error {
	if m.db == nil || m.readOnly {
		return nil
	}
	var result CheckpointResult
	row := m.db.Raw("PRAGMA wal_checkpoint(" + mode + ")").Row()
	if err := row.Scan(&result.Busy, &result.LogFrames, &result.Checkpointed); err != nil {
		return fmt.Errorf("WAL checkpoint %s: %w", mode, err)
	}
	if result.Busy != 0 {
		return fmt.Errorf("WAL checkpoint %s remained busy", mode)
	}
	return nil
}
