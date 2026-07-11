package database

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

var backupNow = time.Now

func (m *Manager) CreateBackup(sourceVersion, targetVersion int, reason string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.createBackupLocked(sourceVersion, targetVersion, reason)
}

func (m *Manager) createBackupLocked(sourceVersion, targetVersion int, reason string) (string, error) {
	if m.db == nil {
		return "", fmt.Errorf("database is closed")
	}
	if m.readOnly {
		return "", fmt.Errorf("cannot back up through a read-only manager")
	}
	if err := os.MkdirAll(m.backupDir, 0o755); err != nil {
		return "", fmt.Errorf("create backup directory: %w", err)
	}
	reason = sanitizeBackupReason(reason)
	stamp := backupNow().UTC().Format("20060102T150405.000000000Z")
	base := strings.TrimSuffix(filepath.Base(m.path), filepath.Ext(m.path))
	name := fmt.Sprintf("%s.%s.%s.v%d-to-v%d.db", base, stamp, reason, sourceVersion, targetVersion)
	finalPath := filepath.Join(m.backupDir, name)
	temporaryPath := finalPath + ".tmp"
	_ = os.Remove(temporaryPath)
	defer os.Remove(temporaryPath)

	statement := "VACUUM INTO '" + strings.ReplaceAll(temporaryPath, "'", "''") + "'"
	if err := m.db.Exec(statement).Error; err != nil {
		return "", fmt.Errorf("create consistent SQLite backup: %w", err)
	}
	file, err := os.OpenFile(temporaryPath, os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("open temporary backup for sync: %w", err)
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if syncErr != nil {
		return "", fmt.Errorf("sync temporary backup: %w", syncErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("close temporary backup: %w", closeErr)
	}
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		return "", fmt.Errorf("atomically finalize backup: %w", err)
	}
	if err := m.pruneBackupsLocked(); err != nil {
		return finalPath, fmt.Errorf("backup created but retention cleanup failed: %w", err)
	}
	return finalPath, nil
}

func sanitizeBackupReason(reason string) string {
	reason = strings.ToLower(strings.TrimSpace(reason))
	if reason == "" {
		return "manual"
	}
	var builder strings.Builder
	for _, r := range reason {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			builder.WriteRune(r)
		default:
			builder.WriteByte('-')
		}
	}
	return strings.Trim(builder.String(), "-")
}

func (m *Manager) pruneBackupsLocked() error {
	entries, err := os.ReadDir(m.backupDir)
	if err != nil {
		return err
	}
	type backupFile struct {
		path    string
		modTime time.Time
	}
	var backups []backupFile
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		backups = append(backups, backupFile{
			path:    filepath.Join(m.backupDir, entry.Name()),
			modTime: info.ModTime(),
		})
	}
	sort.Slice(backups, func(i, j int) bool {
		if backups[i].modTime.Equal(backups[j].modTime) {
			return backups[i].path > backups[j].path
		}
		return backups[i].modTime.After(backups[j].modTime)
	})
	if len(backups) <= m.backupKeep {
		return nil
	}
	for _, backup := range backups[m.backupKeep:] {
		if err := os.Remove(backup.path); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) LatestMigrationBackup() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entries, err := os.ReadDir(m.backupDir)
	if err != nil {
		return "", fmt.Errorf("list migration backups: %w", err)
	}
	type candidate struct {
		path    string
		modTime time.Time
	}
	var candidates []candidate
	for _, entry := range entries {
		if entry.IsDir() || !strings.Contains(entry.Name(), ".migration.") || !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return "", err
		}
		candidates = append(candidates, candidate{filepath.Join(m.backupDir, entry.Name()), info.ModTime()})
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("no migration backup is available")
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].modTime.Equal(candidates[j].modTime) {
			return candidates[i].path > candidates[j].path
		}
		return candidates[i].modTime.After(candidates[j].modTime)
	})
	return candidates[0].path, nil
}

// Restore replaces the current database only after the caller explicitly names
// a verified backup. The current database is backed up first, and every failed
// swap restores the original file before returning.
func (m *Manager) Restore(ctx context.Context, backupPath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.readOnly {
		return fmt.Errorf("cannot restore through a read-only manager")
	}
	if m.db == nil {
		return fmt.Errorf("database is closed")
	}
	absoluteBackup, err := filepath.Abs(backupPath)
	if err != nil {
		return fmt.Errorf("resolve restore backup: %w", err)
	}
	backupVersion, err := m.inspectBackup(ctx, absoluteBackup)
	if err != nil {
		return err
	}
	currentVersion, err := currentSchemaVersion(m.db)
	if err != nil {
		return err
	}
	restoreTemp := m.path + ".restore.tmp"
	rollbackPath := m.path + ".restore-current.tmp"
	_ = os.Remove(restoreTemp)
	_ = os.Remove(rollbackPath)
	defer os.Remove(restoreTemp)
	if err := copyAndSyncFile(absoluteBackup, restoreTemp); err != nil {
		return fmt.Errorf("prepare restore file: %w", err)
	}
	// Close the application pool before the safety backup. Existing writes must
	// finish before Close returns, and new writes through the old pool fail
	// instead of racing the restore.
	if err := m.closeConnection(true); err != nil {
		return fmt.Errorf("close database before restore: %w", err)
	}
	recoveryDB, err := m.openConnection()
	if err != nil {
		return fmt.Errorf("reopen current database for safety backup: %w", err)
	}
	m.db = recoveryDB
	if _, err := m.createBackupLocked(currentVersion, backupVersion, "restore-safety"); err != nil {
		return fmt.Errorf("restore safety backup failed; current database was not modified: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.closeConnection(true); err != nil {
		return fmt.Errorf("close safety-backup connection: %w", err)
	}
	for _, sidecar := range []string{m.path + "-wal", m.path + "-shm"} {
		if err := os.Remove(sidecar); err != nil && !os.IsNotExist(err) {
			_ = m.reopenAfterRestore()
			return fmt.Errorf("remove SQLite sidecar before restore: %w", err)
		}
	}

	if err := os.Rename(m.path, rollbackPath); err != nil {
		_ = m.reopenAfterRestore()
		return fmt.Errorf("preserve current database before restore: %w", err)
	}
	if err := os.Rename(restoreTemp, m.path); err != nil {
		_ = os.Rename(rollbackPath, m.path)
		_ = m.reopenAfterRestore()
		return fmt.Errorf("activate restored database: %w", err)
	}

	if err := m.reopenAfterRestore(); err != nil {
		_ = m.closeConnection(false)
		_ = os.Remove(m.path)
		_ = os.Rename(rollbackPath, m.path)
		reopenErr := m.reopenAfterRestore()
		return fmt.Errorf("restored database validation failed: %w (original reopen: %v)", err, reopenErr)
	}
	restoredVersion, err := currentSchemaVersion(m.db)
	if err == nil && restoredVersion < m.SupportedVersion() {
		err = m.applyMigrations(restoredVersion)
		if err == nil {
			restoredVersion, err = currentSchemaVersion(m.db)
		}
	}
	expectedVersion := m.SupportedVersion()
	if err != nil || restoredVersion != expectedVersion {
		validationErr := err
		if validationErr == nil {
			validationErr = fmt.Errorf("schema version=%d, expected %d", restoredVersion, expectedVersion)
		}
		_ = m.closeConnection(false)
		_ = os.Remove(m.path)
		_ = os.Rename(rollbackPath, m.path)
		reopenErr := m.reopenAfterRestore()
		return fmt.Errorf("restored database validation failed: %w (original reopen: %v)", validationErr, reopenErr)
	}
	if m.usesDefaultMigrations() {
		if err := validateNaviDatabaseIdentity(m.db, restoredVersion, false); err != nil {
			_ = m.closeConnection(false)
			_ = os.Remove(m.path)
			_ = os.Rename(rollbackPath, m.path)
			reopenErr := m.reopenAfterRestore()
			return fmt.Errorf("restored database identity validation failed: %w (original reopen: %v)", err, reopenErr)
		}
	}
	if err := os.Remove(rollbackPath); err != nil {
		return fmt.Errorf("restore succeeded but old database cleanup failed: %w", err)
	}
	return nil
}

func (m *Manager) reopenAfterRestore() error {
	db, err := m.openConnection()
	if err != nil {
		return err
	}
	m.db = db
	if err := m.validateOpenDatabase(); err != nil {
		return err
	}
	return m.verifyRuntimeConfiguration()
}

func (m *Manager) inspectBackup(ctx context.Context, path string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	db, err := gorm.Open(sqlite.Open(sqliteDSN(path, true, m.busyTimeout)), &gorm.Config{})
	if err != nil {
		return 0, fmt.Errorf("open restore backup: %w", err)
	}
	sqlDB, sqlErr := db.DB()
	if sqlErr != nil {
		return 0, fmt.Errorf("get restore backup connection: %w", sqlErr)
	}
	defer sqlDB.Close()
	var quickCheck string
	if err := db.Raw("PRAGMA quick_check").Scan(&quickCheck).Error; err != nil {
		return 0, fmt.Errorf("check restore backup: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(quickCheck), "ok") {
		return 0, fmt.Errorf("restore backup is corrupt: %s", quickCheck)
	}
	version, err := currentSchemaVersion(db)
	if err != nil {
		return 0, err
	}
	if version > m.SupportedVersion() {
		return 0, fmt.Errorf("%w: backup=%d application=%d", ErrDatabaseTooNew, version, m.SupportedVersion())
	}
	if err := validateVersionSequence(db, version); err != nil {
		return 0, err
	}
	if m.usesDefaultMigrations() {
		if err := validateNaviDatabaseIdentity(db, version, false); err != nil {
			return 0, err
		}
	}
	return version, nil
}

func copyAndSyncFile(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	syncErr := output.Sync()
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func (m *Manager) RestoreLatestMigrationBackup(ctx context.Context) error {
	path, err := m.LatestMigrationBackup()
	if err != nil {
		return err
	}
	return m.Restore(ctx, path)
}

// RestoreDatabase is the recovery entry point for a database that cannot pass
// normal application startup. It intentionally bypasses startup migrations,
// creates a safety backup through Restore, and then validates the recovered
// database at the current supported version.
func RestoreDatabase(ctx context.Context, databasePath, backupPath string, options Options) error {
	absolutePath, err := filepath.Abs(databasePath)
	if err != nil {
		return err
	}
	options = normalizeOptions(options)
	migrations := options.Migrations
	if len(migrations) == 0 {
		migrations = DefaultMigrations()
	}
	if err := validateMigrations(migrations); err != nil {
		return err
	}
	manager := &Manager{
		path:            absolutePath,
		backupDir:       options.BackupDir,
		backupKeep:      options.BackupKeep,
		busyTimeout:     options.BusyTimeout,
		maxOpenConns:    options.MaxOpenConns,
		maxIdleConns:    options.MaxIdleConns,
		connMaxLifetime: options.ConnMaxLifetime,
		migrations:      append([]Migration(nil), migrations...),
	}
	if manager.backupDir == "" {
		manager.backupDir = absolutePath + ".backups"
	}
	db, err := manager.openConnection()
	if err != nil {
		return err
	}
	manager.db = db
	defer manager.Close()
	return manager.Restore(ctx, backupPath)
}
