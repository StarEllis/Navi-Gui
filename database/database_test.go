package database

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"navi-desktop/model"
	"navi-desktop/repository"
)

func openLegacyWithForeignKeys(t *testing.T, path string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+filepath.ToSlash(path)+"?_pragma=foreign_keys(1)"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := model.AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func openTestManager(t *testing.T, options Options) *Manager {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	manager, err := Open(path, options)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

func createCoreRows(t *testing.T, db *gorm.DB, suffix string) (model.User, model.Library, model.Media) {
	t.Helper()
	user := model.User{Username: "user-" + suffix, Password: "hash"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	library := model.Library{Name: "library-" + suffix, Path: "C:\\Media\\" + suffix}
	if err := repository.NewRepositories(db).Library.Create(&library); err != nil {
		t.Fatalf("create library: %v", err)
	}
	media := model.Media{
		LibraryID: library.ID,
		Title:     "media-" + suffix,
		FilePath:  filepath.Join(library.Path, "movie.mkv"),
	}
	if err := repository.NewRepositories(db).Media.Create(&media); err != nil {
		t.Fatalf("create media: %v", err)
	}
	return user, library, media
}

func TestNewDatabaseMigratesSequentiallyAndReopenIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.db")
	manager, err := Open(path, DefaultOptions())
	if err != nil {
		t.Fatalf("Open(new) error = %v", err)
	}
	version, err := manager.SchemaVersion()
	if err != nil || version != 3 {
		t.Fatalf("SchemaVersion() = %d, %v; want 3", version, err)
	}
	var versions []int
	if err := manager.DB().Model(&SchemaMigration{}).Order("version").Pluck("version", &versions).Error; err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(versions) != "[1 2 3]" {
		t.Fatalf("migration versions = %v", versions)
	}
	before, err := os.ReadDir(manager.BackupDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path, DefaultOptions())
	if err != nil {
		t.Fatalf("Open(existing) error = %v", err)
	}
	defer reopened.Close()
	after, err := os.ReadDir(reopened.BackupDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("idempotent reopen created another backup: before=%d after=%d", len(before), len(after))
	}
}

func TestMigrationFailureRollsBackAndDoesNotAdvanceVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "failed.db")
	migrations := []Migration{
		{Version: 1, Name: "one", Apply: func(tx *gorm.DB) (MigrationStats, error) {
			return nil, tx.Exec("CREATE TABLE marker (value INTEGER)").Error
		}},
		{Version: 2, Name: "two", Apply: func(tx *gorm.DB) (MigrationStats, error) {
			if err := tx.Exec("INSERT INTO marker(value) VALUES (2)").Error; err != nil {
				return nil, err
			}
			return nil, errors.New("injected migration failure")
		}},
	}
	_, err := Open(path, Options{Migrations: migrations})
	if err == nil || !contains(err.Error(), "rolled back") {
		t.Fatalf("Open() error = %v, want rollback diagnostic", err)
	}

	raw, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatalf("reopen failed database: %v", err)
	}
	sqlDB, _ := raw.DB()
	defer sqlDB.Close()
	version, err := currentSchemaVersion(raw)
	if err != nil || version != 1 {
		t.Fatalf("version after failure = %d, %v; want 1", version, err)
	}
	var count int64
	if err := raw.Table("marker").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed migration left %d rows", count)
	}
}

func TestHigherDatabaseVersionRejectsWriteOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "future.db")
	manager, err := Open(path, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.DB().Create(&SchemaMigration{Version: 4, Name: "future", AppliedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = Open(path, DefaultOptions())
	if !errors.Is(err, ErrDatabaseTooNew) {
		t.Fatalf("Open() error = %v, want ErrDatabaseTooNew", err)
	}
	readOnlyOptions := DefaultOptions()
	readOnlyOptions.ReadOnly = true
	readOnly, err := Open(path, readOnlyOptions)
	if err != nil {
		t.Fatalf("read-only open of future schema: %v", err)
	}
	defer readOnly.Close()
}

func TestBackupFailurePreventsMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "db.sqlite")
	blocked := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	options := DefaultOptions()
	options.BackupDir = blocked
	if _, err := Open(path, options); err == nil {
		t.Fatal("Open() succeeded with an unusable backup directory")
	}
	raw, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := raw.DB()
	defer sqlDB.Close()
	if raw.Migrator().HasTable(&SchemaMigration{}) {
		t.Fatal("migration metadata was created after backup failure")
	}
}

func TestWALConsistentBackupContainsLatestRows(t *testing.T) {
	manager := openTestManager(t, DefaultOptions())
	_, _, media := createCoreRows(t, manager.DB(), "wal")
	if info, err := os.Stat(manager.Path() + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("test did not create pending WAL content: size=%v err=%v", func() int64 {
			if info == nil {
				return 0
			}
			return info.Size()
		}(), err)
	}
	backup, err := manager.CreateBackup(3, 3, "wal-test")
	if err != nil {
		t.Fatalf("CreateBackup() error = %v", err)
	}
	backupDB, err := gorm.Open(sqlite.Open(sqliteDSN(backup, true, DefaultBusyTimeout)), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := backupDB.DB()
	defer sqlDB.Close()
	var count int64
	if err := backupDB.Model(&model.Media{}).Where("id = ?", media.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("backup media count = %d, want 1", count)
	}
}

func TestBackupRetentionAndTemporaryCleanup(t *testing.T) {
	options := DefaultOptions()
	options.BackupKeep = 2
	manager := openTestManager(t, options)

	originalNow := backupNow
	defer func() { backupNow = originalNow }()
	index := 0
	backupNow = func() time.Time {
		index++
		return time.Date(2026, 7, 11, 0, 0, index, 0, time.UTC)
	}
	for i := 0; i < 4; i++ {
		if _, err := manager.CreateBackup(3, 3, fmt.Sprintf("keep-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(manager.BackupDir())
	if err != nil {
		t.Fatal(err)
	}
	var files int
	for _, entry := range entries {
		if !entry.IsDir() {
			files++
		}
	}
	if files != 2 {
		t.Fatalf("retained backup files = %d, want 2", files)
	}

	fixed := time.Date(2026, 7, 12, 1, 2, 3, 4, time.UTC)
	backupNow = func() time.Time { return fixed }
	base := stringsTrimExt(filepath.Base(manager.Path()))
	final := filepath.Join(manager.BackupDir(), fmt.Sprintf("%s.%s.rename-failure.v3-to-v3.db", base, fixed.Format("20060102T150405.000000000Z")))
	if err := os.Mkdir(final, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CreateBackup(3, 3, "rename-failure"); err == nil {
		t.Fatal("CreateBackup() succeeded when final target was a directory")
	}
	if _, err := os.Stat(final + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temporary backup remains after failure: %v", err)
	}
}

func TestRestoreSuccessAndFailurePreserveCurrentDatabase(t *testing.T) {
	manager := openTestManager(t, DefaultOptions())
	_, first, _ := createCoreRows(t, manager.DB(), "first")
	backup, err := manager.CreateBackup(3, 3, "restore-source")
	if err != nil {
		t.Fatal(err)
	}
	_, second, _ := createCoreRows(t, manager.DB(), "second")

	if err := manager.Restore(context.Background(), backup); err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	var count int64
	if err := manager.DB().Model(&model.Library{}).Where("id IN ?", []string{first.ID, second.ID}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("library count after restore = %d, want 1", count)
	}

	corrupt := filepath.Join(t.TempDir(), "corrupt.db")
	if err := os.WriteFile(corrupt, []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Restore(context.Background(), corrupt); err == nil {
		t.Fatal("Restore(corrupt) succeeded")
	}
	if err := manager.DB().Model(&model.Library{}).Where("id = ?", first.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("current database changed after failed restore: count=%d err=%v", count, err)
	}
}

func TestLegacyDuplicateDataMigratesDeterministically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := model.AutoMigrate(legacy); err != nil {
		t.Fatal(err)
	}
	for _, index := range []string{"idx_favorite_user_media", "idx_watch_user_media"} {
		if err := legacy.Exec("DROP INDEX IF EXISTS " + index).Error; err != nil {
			t.Fatal(err)
		}
	}
	user := model.User{Username: "legacy-user", Password: "hash"}
	if err := legacy.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	library := model.Library{Name: "legacy-library", Path: "C:\\Media\\legacy"}
	if err := legacy.Create(&library).Error; err != nil {
		t.Fatal(err)
	}
	media := model.Media{LibraryID: library.ID, Title: "legacy-media", FilePath: "C:\\Media\\legacy\\movie.mkv"}
	if err := legacy.Create(&media).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	favorites := []model.Favorite{
		{ID: "fav-old", UserID: user.ID, MediaID: media.ID, CreatedAt: now.Add(-time.Hour)},
		{ID: "fav-new", UserID: user.ID, MediaID: media.ID, CreatedAt: now},
	}
	histories := []model.WatchHistory{
		{ID: "history-old", UserID: user.ID, MediaID: media.ID, Position: 1, UpdatedAt: now.Add(-time.Hour), CreatedAt: now.Add(-time.Hour)},
		{ID: "history-new", UserID: user.ID, MediaID: media.ID, Position: 99, UpdatedAt: now, CreatedAt: now},
	}
	if err := legacy.Create(&favorites).Error; err != nil {
		t.Fatal(err)
	}
	if err := legacy.Create(&histories).Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := legacy.DB()
	_ = sqlDB.Close()

	manager, err := Open(path, DefaultOptions())
	if err != nil {
		t.Fatalf("Open(legacy) error = %v", err)
	}
	defer manager.Close()
	var favorite model.Favorite
	if err := manager.DB().First(&favorite).Error; err != nil {
		t.Fatal(err)
	}
	if favorite.ID != "fav-old" {
		t.Fatalf("favorite survivor = %s, want fav-old", favorite.ID)
	}
	var history model.WatchHistory
	if err := manager.DB().First(&history).Error; err != nil {
		t.Fatal(err)
	}
	if history.ID != "history-new" || history.Position != 99 {
		t.Fatalf("history survivor = %#v", history)
	}
	var mediaCount int64
	if err := manager.DB().Model(&model.Media{}).Where("id = ?", media.ID).Count(&mediaCount).Error; err != nil || mediaCount != 1 {
		t.Fatalf("media was not preserved: count=%d err=%v", mediaCount, err)
	}
	if library.ID == "" {
		t.Fatal("legacy library unexpectedly empty")
	}
}

func TestRepositoryUniquenessUpsertAndWindowsPathIdentity(t *testing.T) {
	manager := openTestManager(t, DefaultOptions())
	repos := repository.NewRepositories(manager.DB())
	user, library, media := createCoreRows(t, manager.DB(), "unique")

	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			errs <- repos.Favorite.Add(&model.Favorite{UserID: user.ID, MediaID: media.ID})
		}()
		go func(position int) {
			defer wg.Done()
			errs <- repos.WatchHistory.Upsert(&model.WatchHistory{
				UserID: user.ID, MediaID: media.ID, Position: float64(position),
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent upsert: %v", err)
		}
	}
	for name, target := range map[string]interface{}{
		"favorites":       &model.Favorite{},
		"watch_histories": &model.WatchHistory{},
	} {
		var count int64
		if err := manager.DB().Model(target).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("%s count=%d err=%v", name, count, err)
		}
	}

	duplicate := model.Media{LibraryID: library.ID, Title: "duplicate", FilePath: stringsSwapCaseAndSeparators(media.FilePath)}
	if err := repos.Media.Create(&duplicate); err != nil {
		t.Fatal(err)
	}
	if duplicate.ID != media.ID {
		t.Fatalf("path-equivalent media ID = %s, want %s", duplicate.ID, media.ID)
	}
	otherLibrary := model.Library{Name: "other", Path: "C:\\Other"}
	if err := repos.Library.Create(&otherLibrary); err != nil {
		t.Fatal(err)
	}
	otherMedia := model.Media{LibraryID: otherLibrary.ID, Title: "same physical path", FilePath: media.FilePath}
	if err := repos.Media.Create(&otherMedia); err != nil {
		t.Fatal(err)
	}
	if otherMedia.ID == media.ID {
		t.Fatal("media identity leaked across libraries")
	}
	found, err := repos.Media.FindByFilePathInLibrary(otherLibrary.ID, stringsSwapCaseAndSeparators(media.FilePath))
	if err != nil || found.ID != otherMedia.ID {
		t.Fatalf("library-scoped path lookup=%v err=%v", found, err)
	}

	series := model.Series{LibraryID: library.ID, Title: "series", FolderPath: "C:\\TV\\Show"}
	if err := repos.Series.Create(&series); err != nil {
		t.Fatal(err)
	}
	if err := repos.Series.Delete(series.ID); err != nil {
		t.Fatal(err)
	}
	recreated := model.Series{LibraryID: library.ID, Title: "series again", FolderPath: "c:/tv/show"}
	if err := repos.Series.Create(&recreated); err != nil {
		t.Fatalf("recreate soft-deleted series path: %v", err)
	}
	if recreated.ID == series.ID {
		t.Fatal("soft-deleted series row was reused")
	}
}

func TestSQLiteConfigurationHealthAndShutdownCheckpoint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.db")
	manager, err := Open(path, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	db := manager.DB()
	for pragma, want := range map[string]string{
		"foreign_keys": "1",
		"journal_mode": "wal",
		"busy_timeout": "5000",
		"synchronous":  "1",
	} {
		var got string
		if err := db.Raw("PRAGMA " + pragma).Scan(&got).Error; err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("PRAGMA %s=%s, want %s", pragma, got, want)
		}
	}
	sqlDB, _ := db.DB()
	if sqlDB.Stats().MaxOpenConnections != 1 {
		t.Fatalf("MaxOpenConnections=%d", sqlDB.Stats().MaxOpenConnections)
	}

	if err := db.Exec("DROP INDEX idx_media_library_path_active").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("PRAGMA foreign_keys=OFF").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO favorites(id,user_id,media_id,created_at) VALUES('orphan','missing-user','missing-media',CURRENT_TIMESTAMP)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("PRAGMA foreign_keys=ON").Error; err != nil {
		t.Fatal(err)
	}
	report := manager.HealthCheck(context.Background(), true)
	if report.Status != "error" || len(report.MissingIndexes) == 0 || len(report.OrphanCounts) == 0 || len(report.ForeignKeyViolations) == 0 {
		t.Fatalf("health report did not detect damage: %#v", report)
	}
	if report.Checkpoint != nil {
		t.Fatal("read-only health check performed a checkpoint")
	}

	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path + "-wal"); err == nil && info.Size() != 0 {
		t.Fatalf("WAL size after close = %d", info.Size())
	}
}

func TestCorruptDatabaseIsRejectedWithoutRepair(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.db")
	manager, err := Open(path, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("broken sqlite header"), 0); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if _, err := Open(path, DefaultOptions()); err == nil {
		t.Fatal("Open(corrupt) succeeded")
	}
}

func TestRestoreDatabaseRecoversAfterMigrationFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "recover.db")
	failing := []Migration{
		{Version: 1, Name: "one", Apply: func(tx *gorm.DB) (MigrationStats, error) {
			return nil, tx.Exec("CREATE TABLE recovery_marker (value INTEGER)").Error
		}},
		{Version: 2, Name: "two", Apply: func(tx *gorm.DB) (MigrationStats, error) {
			return nil, errors.New("injected failure")
		}},
	}
	options := DefaultOptions()
	options.Migrations = failing
	if _, err := Open(path, options); err == nil {
		t.Fatal("failing migration unexpectedly succeeded")
	}
	entries, err := os.ReadDir(path + ".backups")
	if err != nil || len(entries) == 0 {
		t.Fatalf("migration backup missing: entries=%v err=%v", entries, err)
	}
	backup := filepath.Join(path+".backups", entries[0].Name())
	fixed := []Migration{
		failing[0],
		{Version: 2, Name: "two", Apply: func(tx *gorm.DB) (MigrationStats, error) {
			return MigrationStats{"recovered": 1}, tx.Exec("INSERT INTO recovery_marker(value) VALUES (2)").Error
		}},
	}
	recoveryOptions := DefaultOptions()
	recoveryOptions.Migrations = fixed
	if err := RestoreDatabase(context.Background(), path, backup, recoveryOptions); err != nil {
		t.Fatalf("RestoreDatabase() error = %v", err)
	}
	raw, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := raw.DB()
	defer sqlDB.Close()
	version, err := currentSchemaVersion(raw)
	if err != nil || version != 2 {
		t.Fatalf("recovered version=%d err=%v", version, err)
	}
	var count int64
	if err := raw.Table("recovery_marker").Where("value = 2").Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("recovery marker count=%d err=%v", count, err)
	}
}

func TestLegacyPathDuplicatesMergeAndAudit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "path-duplicates.db")
	legacy, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := model.AutoMigrate(legacy); err != nil {
		t.Fatal(err)
	}
	user := model.User{Username: "path-user", Password: "hash"}
	library := model.Library{Name: "path-library", Path: "C:\\Movies"}
	if err := legacy.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := legacy.Create(&library).Error; err != nil {
		t.Fatal(err)
	}
	older := time.Now().UTC().Add(-time.Hour)
	newer := time.Now().UTC()
	first := model.Media{ID: "media-old", LibraryID: library.ID, Title: "old", FilePath: "C:\\Movies\\Film.mkv", CreatedAt: older, UpdatedAt: older}
	second := model.Media{ID: "media-new", LibraryID: library.ID, Title: "new", FilePath: "c:/movies/FILM.MKV", CreatedAt: newer, UpdatedAt: newer}
	if err := legacy.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	if err := legacy.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	if err := legacy.Create(&model.Favorite{UserID: user.ID, MediaID: first.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := legacy.Create(&model.Favorite{UserID: user.ID, MediaID: second.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := legacy.Create(&model.WatchHistory{UserID: user.ID, MediaID: first.ID, Position: 10, UpdatedAt: older}).Error; err != nil {
		t.Fatal(err)
	}
	if err := legacy.Create(&model.WatchHistory{UserID: user.ID, MediaID: second.ID, Position: 20, UpdatedAt: newer}).Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := legacy.DB()
	_ = sqlDB.Close()

	manager, err := Open(path, DefaultOptions())
	if err != nil {
		t.Fatalf("Open(legacy duplicates) error = %v", err)
	}
	defer manager.Close()
	var mediaCount, favoriteCount, historyCount int64
	_ = manager.DB().Model(&model.Media{}).Count(&mediaCount).Error
	_ = manager.DB().Model(&model.Favorite{}).Count(&favoriteCount).Error
	_ = manager.DB().Model(&model.WatchHistory{}).Count(&historyCount).Error
	if mediaCount != 1 || favoriteCount != 1 || historyCount != 1 {
		t.Fatalf("merged counts media=%d favorite=%d history=%d", mediaCount, favoriteCount, historyCount)
	}
	var survivor model.Media
	if err := manager.DB().First(&survivor).Error; err != nil || survivor.ID != second.ID {
		t.Fatalf("media survivor=%s err=%v", survivor.ID, err)
	}
	var audit SchemaMigrationAudit
	if err := manager.DB().Where("version = ? AND category = ?", 2, "media_merged").First(&audit).Error; err != nil || audit.Count != 1 {
		t.Fatalf("media merge audit=%#v err=%v", audit, err)
	}
}

func TestForeignKeyMigrationCleansChildrenBeforeOrphanParents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fk-legacy.db")
	legacy := openLegacyWithForeignKeys(t, path)
	user := model.User{ID: "fk-user", Username: "fk-user", Password: "hash"}
	library := model.Library{ID: "fk-library", Name: "fk-library", Path: "C:\\FK"}
	series := model.Series{ID: "fk-series", LibraryID: library.ID, Title: "series", FolderPath: "C:\\FK\\series"}
	media := model.Media{ID: "fk-media", LibraryID: library.ID, SeriesID: series.ID, Title: "media", FilePath: "C:\\FK\\series\\one.mkv"}
	person := model.Person{ID: "fk-person", Name: "person"}
	for _, value := range []interface{}{&user, &library, &series, &media, &person} {
		if err := legacy.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []interface{}{
		&model.Favorite{ID: "fk-favorite", UserID: user.ID, MediaID: media.ID},
		&model.WatchHistory{ID: "fk-history", UserID: user.ID, MediaID: media.ID},
		&model.MediaPerson{ID: "fk-cast", MediaID: media.ID, SeriesID: series.ID, PersonID: person.ID, Role: "actor"},
	} {
		if err := legacy.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := legacy.Exec("PRAGMA foreign_keys=OFF").Error; err != nil {
		t.Fatal(err)
	}
	if err := legacy.Exec("DELETE FROM libraries WHERE id = ?", library.ID).Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := legacy.DB()
	_ = sqlDB.Close()

	manager, err := Open(path, DefaultOptions())
	if err != nil {
		t.Fatalf("foreign_keys=ON migration failed: %v", err)
	}
	defer manager.Close()
	for table, target := range map[string]interface{}{
		"media":           &model.Media{},
		"series":          &model.Series{},
		"favorites":       &model.Favorite{},
		"watch_histories": &model.WatchHistory{},
		"media_people":    &model.MediaPerson{},
		"people":          &model.Person{},
	} {
		var count int64
		if err := manager.DB().Model(target).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
	var foreignKeys string
	if err := manager.DB().Raw("PRAGMA foreign_keys").Scan(&foreignKeys).Error; err != nil || foreignKeys != "1" {
		t.Fatalf("foreign_keys=%s err=%v", foreignKeys, err)
	}
	var audits int64
	if err := manager.DB().Model(&SchemaMigrationAudit{}).Where("category LIKE 'doomed_%' OR category LIKE 'orphan_%'").Count(&audits).Error; err != nil || audits == 0 {
		t.Fatalf("cleanup audits=%d err=%v", audits, err)
	}
}

func TestCleanupFailureRollsBackRowsAndVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cleanup-rollback.db")
	raw, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Exec("CREATE TABLE libraries(id TEXT PRIMARY KEY, deleted_at DATETIME); CREATE TABLE media(id TEXT PRIMARY KEY, library_id TEXT NOT NULL); INSERT INTO media(id,library_id) VALUES('orphan','missing')").Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := raw.DB()
	_ = sqlDB.Close()
	migrations := []Migration{{Version: 1, Name: "cleanup", Apply: func(tx *gorm.DB) (MigrationStats, error) {
		stats, err := cleanKnownOrphans(tx)
		if err != nil {
			return nil, err
		}
		return stats, errors.New("injected cleanup failure")
	}}}
	if _, err := Open(path, Options{Migrations: migrations}); err == nil {
		t.Fatal("cleanup migration unexpectedly succeeded")
	}
	raw, err = gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { db, _ := raw.DB(); _ = db.Close() }()
	var count int64
	if err := raw.Table("media").Where("id='orphan'").Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("orphan row after rollback=%d err=%v", count, err)
	}
	version, err := currentSchemaVersion(raw)
	if err != nil || version != 0 {
		t.Fatalf("version after rollback=%d err=%v", version, err)
	}
}

func TestRestoreRejectsNonNaviCandidatesWithoutTouchingCurrent(t *testing.T) {
	manager := openTestManager(t, DefaultOptions())
	_, library, _ := createCoreRows(t, manager.DB(), "restore-identity")
	if _, err := manager.Checkpoint("TRUNCATE"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(manager.Path())
	if err != nil {
		t.Fatal(err)
	}
	for name, setup := range map[string]func(*gorm.DB) error{
		"empty": func(*gorm.DB) error { return nil },
		"other-app": func(db *gorm.DB) error {
			return db.Exec("CREATE TABLE invoices(id INTEGER PRIMARY KEY, total INTEGER)").Error
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := filepath.Join(t.TempDir(), name+".db")
			db, err := gorm.Open(sqlite.Open(candidate), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			if err := setup(db); err != nil {
				t.Fatal(err)
			}
			sqlDB, _ := db.DB()
			_ = sqlDB.Close()
			if err := manager.Restore(context.Background(), candidate); err == nil {
				t.Fatal("non-Navi candidate was accepted")
			}
			after, err := os.ReadFile(manager.Path())
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("current database bytes changed: equal=%v err=%v", bytes.Equal(before, after), err)
			}
			var count int64
			if err := manager.DB().Model(&model.Library{}).Where("id = ?", library.ID).Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("current connection changed: count=%d err=%v", count, err)
			}
		})
	}
}

func TestRestoreAcceptsRecognizedVersionlessNaviDatabase(t *testing.T) {
	manager := openTestManager(t, DefaultOptions())
	candidate := filepath.Join(t.TempDir(), "legacy-navi.db")
	legacy := openLegacyWithForeignKeys(t, candidate)
	library := model.Library{ID: "legacy-restore-library", Name: "legacy", Path: "C:\\Legacy"}
	if err := legacy.Create(&library).Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := legacy.DB()
	_ = sqlDB.Close()
	if err := manager.Restore(context.Background(), candidate); err != nil {
		t.Fatalf("restore recognized legacy Navi: %v", err)
	}
	var count int64
	if err := manager.DB().Model(&model.Library{}).Where("id = ?", library.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("restored legacy library count=%d err=%v", count, err)
	}
}

func TestRestoreRejectsNewerNaviDatabase(t *testing.T) {
	manager := openTestManager(t, DefaultOptions())
	candidatePath := filepath.Join(t.TempDir(), "future-restore.db")
	candidate, err := Open(candidatePath, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := candidate.DB().Create(&SchemaMigration{Version: 4, Name: "future", AppliedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := candidate.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Restore(context.Background(), candidatePath); !errors.Is(err, ErrDatabaseTooNew) {
		t.Fatalf("Restore(future) error=%v", err)
	}
}

func TestRequiredIndexDefinitionsAreValidatedAndRepaired(t *testing.T) {
	manager := openTestManager(t, DefaultOptions())
	db := manager.DB()
	definition := requiredIndexDefinitions[3]
	if valid, reason, err := validateRequiredIndex(db, definition); err != nil || !valid {
		t.Fatalf("correct index invalid: valid=%v reason=%s err=%v", valid, reason, err)
	}
	cases := map[string]string{
		"non-unique":     "CREATE INDEX idx_media_library_path_active ON media(library_id, path_key) WHERE deleted_at IS NULL AND path_key <> ''",
		"wrong-order":    "CREATE UNIQUE INDEX idx_media_library_path_active ON media(path_key, library_id) WHERE deleted_at IS NULL AND path_key <> ''",
		"missing-column": "CREATE UNIQUE INDEX idx_media_library_path_active ON media(library_id) WHERE deleted_at IS NULL AND path_key <> ''",
		"missing-where":  "CREATE UNIQUE INDEX idx_media_library_path_active ON media(library_id, path_key)",
		"wrong-where":    "CREATE UNIQUE INDEX idx_media_library_path_active ON media(library_id, path_key) WHERE deleted_at IS NOT NULL AND path_key <> ''",
	}
	for name, statement := range cases {
		t.Run(name, func(t *testing.T) {
			if err := db.Exec("DROP INDEX idx_media_library_path_active").Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Exec(statement).Error; err != nil {
				t.Fatal(err)
			}
			if report := manager.HealthCheck(context.Background(), true); report.Status != "error" || len(report.MissingIndexes) == 0 {
				t.Fatalf("invalid index not reported: %#v", report)
			}
			if err := db.Transaction(ensureRequiredIndexes); err != nil {
				t.Fatalf("repair invalid index: %v", err)
			}
			if valid, reason, err := validateRequiredIndex(db, definition); err != nil || !valid {
				t.Fatalf("repaired index invalid: valid=%v reason=%s err=%v", valid, reason, err)
			}
		})
	}
}

func TestDuplicateSeriesMergeMovesAssociationsBeforeDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "series-duplicates.db")
	legacy := openLegacyWithForeignKeys(t, path)
	library := model.Library{ID: "series-library", Name: "series-library", Path: "C:\\TV"}
	if err := legacy.Create(&library).Error; err != nil {
		t.Fatal(err)
	}
	older := time.Now().UTC().Add(-time.Hour)
	newer := time.Now().UTC()
	oldSeries := model.Series{ID: "series-old", LibraryID: library.ID, Title: "old", FolderPath: "C:\\TV\\Show", CreatedAt: older, UpdatedAt: older}
	newSeries := model.Series{ID: "series-new", LibraryID: library.ID, Title: "new", FolderPath: "c:/tv/SHOW", CreatedAt: newer, UpdatedAt: newer}
	if err := legacy.Create(&oldSeries).Error; err != nil {
		t.Fatal(err)
	}
	if err := legacy.Create(&newSeries).Error; err != nil {
		t.Fatal(err)
	}
	media := model.Media{ID: "series-episode", LibraryID: library.ID, SeriesID: oldSeries.ID, Title: "episode", FilePath: "C:\\TV\\Show\\S01E01.mkv"}
	if err := legacy.Create(&media).Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := legacy.DB()
	_ = sqlDB.Close()

	manager, err := Open(path, DefaultOptions())
	if err != nil {
		t.Fatalf("merge duplicate series: %v", err)
	}
	defer manager.Close()
	var reloaded model.Media
	if err := manager.DB().First(&reloaded, "id = ?", media.ID).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.SeriesID != newSeries.ID {
		t.Fatalf("media series_id=%s want=%s", reloaded.SeriesID, newSeries.ID)
	}
	var count int64
	if err := manager.DB().Model(&model.Series{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("series count=%d err=%v", count, err)
	}
	var audit SchemaMigrationAudit
	if err := manager.DB().Where("version = ? AND category = ?", 2, "series_merged").First(&audit).Error; err != nil || audit.Count != 1 {
		t.Fatalf("series merge audit=%#v err=%v", audit, err)
	}
}
func contains(value, fragment string) bool {
	return len(fragment) == 0 || (len(value) >= len(fragment) && stringsContains(value, fragment))
}

func stringsContains(value, fragment string) bool {
	for i := 0; i+len(fragment) <= len(value); i++ {
		if value[i:i+len(fragment)] == fragment {
			return true
		}
	}
	return false
}

func stringsTrimExt(value string) string {
	return value[:len(value)-len(filepath.Ext(value))]
}

func stringsSwapCaseAndSeparators(value string) string {
	result := []rune(value)
	for i, r := range result {
		switch {
		case r == '\\':
			result[i] = '/'
		case r >= 'a' && r <= 'z':
			result[i] = r - ('a' - 'A')
		case r >= 'A' && r <= 'Z':
			result[i] = r + ('a' - 'A')
		}
	}
	return string(result)
}
