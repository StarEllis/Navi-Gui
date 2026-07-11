package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"navi-desktop/config"
	"navi-desktop/model"
	"navi-desktop/repository"
)

type overwriteSafetyFixture struct {
	db           *gorm.DB
	repos        *repository.Repositories
	scanner      *ScannerService
	library      model.Library
	root         string
	keptPath     string
	missingPath  string
	keptCache    string
	missingCache string
}

type recordedScanEvent struct {
	eventType string
	data      ScanProgressData
}

type recordingScanBroadcaster struct {
	events      []recordedScanEvent
	mediaEvents []MediaMetadataEventData
}

func (r *recordingScanBroadcaster) BroadcastEvent(eventType string, payload interface{}) {
	if eventType == EventMediaMetadataUpdated {
		if data, ok := payload.(*MediaMetadataEventData); ok && data != nil {
			r.mediaEvents = append(r.mediaEvents, *data)
		}
		return
	}
	data, ok := payload.(*ScanProgressData)
	if !ok || data == nil {
		return
	}
	r.events = append(r.events, recordedScanEvent{eventType: eventType, data: *data})
}

type commitCheckingBroadcaster struct {
	db                   *gorm.DB
	libraryID            string
	mediaEvents          int
	mediaBeforeCommit    bool
	metadataBeforeCommit bool
}

func (r *commitCheckingBroadcaster) BroadcastEvent(eventType string, payload interface{}) {
	if eventType != EventMediaMetadataUpdated {
		return
	}
	r.mediaEvents++
	var library model.Library
	if err := r.db.First(&library, "id = ?", r.libraryID).Error; err != nil || library.LastScan == nil {
		r.mediaBeforeCommit = true
	}
	data, ok := payload.(*MediaMetadataEventData)
	if !ok || data == nil {
		return
	}
	var media model.Media
	if err := r.db.First(&media, "id = ?", data.MediaID).Error; err != nil || NormalizeMetadataPhase(media.MetadataPhase) != MetadataPhaseFull {
		r.metadataBeforeCommit = true
	}
}

func (r *recordingScanBroadcaster) terminalEvents() []recordedScanEvent {
	var terminal []recordedScanEvent
	for _, event := range r.events {
		switch event.eventType {
		case EventScanCompleted, EventScanIncomplete, EventScanFailed, EventScanCanceled:
			terminal = append(terminal, event)
		}
	}
	return terminal
}

func assertSingleScanTerminal(t *testing.T, recorder *recordingScanBroadcaster, wantType string) recordedScanEvent {
	t.Helper()
	terminal := recorder.terminalEvents()
	if len(terminal) != 1 {
		t.Fatalf("expected one terminal event, got %+v", terminal)
	}
	if terminal[0].eventType != wantType {
		t.Fatalf("expected terminal %s, got %+v", wantType, terminal[0])
	}
	return terminal[0]
}

func newOverwriteSafetyFixture(t *testing.T, libraryType string, online bool) *overwriteSafetyFixture {
	t.Helper()

	base := t.TempDir()
	root := filepath.Join(base, "library")
	if online {
		if err := os.MkdirAll(root, 0755); err != nil {
			t.Fatalf("create root: %v", err)
		}
	}

	var keptPath string
	seriesFolder := filepath.Join(root, "Show")
	switch libraryType {
	case "tvshow", "mixed":
		keptPath = filepath.Join(seriesFolder, "Show.S01E01.mkv")
		if online {
			if err := os.MkdirAll(seriesFolder, 0755); err != nil {
				t.Fatalf("create series folder: %v", err)
			}
			if err := os.WriteFile(keptPath, []byte("updated episode bytes"), 0644); err != nil {
				t.Fatalf("write kept episode: %v", err)
			}
			if err := os.WriteFile(filepath.Join(seriesFolder, "tvshow.nfo"), []byte("<tvshow><title>Refreshed Show</title></tvshow>"), 0644); err != nil {
				t.Fatalf("write series NFO: %v", err)
			}
			if libraryType == "mixed" {
				if err := os.WriteFile(filepath.Join(seriesFolder, "Show.S01E02.mkv"), []byte("second episode"), 0644); err != nil {
					t.Fatalf("write second episode: %v", err)
				}
			}
		}
	default:
		keptPath = filepath.Join(root, "kept.mp4")
		if online {
			if err := os.WriteFile(keptPath, []byte("updated movie bytes"), 0644); err != nil {
				t.Fatalf("write kept movie: %v", err)
			}
			if err := os.WriteFile(filepath.Join(root, "kept.nfo"), []byte("<movie><title>Refreshed Movie</title></movie>"), 0644); err != nil {
				t.Fatalf("write movie NFO: %v", err)
			}
		}
	}

	dbName := "overwrite_" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", dbName)), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.Exec("PRAGMA foreign_keys = OFF").Error; err != nil {
		t.Fatalf("disable foreign keys: %v", err)
	}
	if err := model.AutoMigrate(db); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	repos := repository.NewRepositories(db)

	library := model.Library{
		ID:               "overwrite-library",
		Name:             "Overwrite Safety",
		Path:             root,
		Type:             libraryType,
		EnableFileFilter: false,
	}
	user := model.User{ID: "overwrite-user", Username: "overwrite-user", Password: "hash"}
	series := model.Series{
		ID:           "overwrite-series",
		LibraryID:    library.ID,
		Title:        "Original Show",
		FolderPath:   seriesFolder,
		SeasonCount:  1,
		EpisodeCount: 2,
	}
	kept := model.Media{
		ID:        "media-kept",
		LibraryID: library.ID,
		Title:     "Original Title",
		FilePath:  keptPath,
		FileSize:  1,
		MediaType: "movie",
	}
	missing := model.Media{
		ID:         "media-missing",
		LibraryID:  library.ID,
		Title:      "Missing Title",
		FilePath:   filepath.Join(root, "missing.mkv"),
		FileSize:   1,
		MediaType:  "movie",
		SeriesID:   series.ID,
		SeasonNum:  1,
		EpisodeNum: 9,
	}
	if libraryType == "tvshow" || libraryType == "mixed" {
		kept.MediaType = "episode"
		kept.SeriesID = series.ID
		kept.SeasonNum = 1
		kept.EpisodeNum = 1
	}

	for _, value := range []interface{}{
		&user,
		&library,
		&series,
		&kept,
		&missing,
		&model.Favorite{ID: "favorite-kept", UserID: user.ID, MediaID: kept.ID},
		&model.Favorite{ID: "favorite-missing", UserID: user.ID, MediaID: missing.ID},
		&model.WatchHistory{ID: "history-kept", UserID: user.ID, MediaID: kept.ID},
		&model.WatchHistory{ID: "history-missing", UserID: user.ID, MediaID: missing.ID},
	} {
		if err := db.Create(value).Error; err != nil {
			t.Fatalf("seed %T: %v", value, err)
		}
	}
	// GORM applies model defaults to zero values during Create; the scanner
	// fixture intentionally keeps tiny synthetic media files.
	library.EnableFileFilter = false
	library.MinFileSize = 0

	cacheBase := filepath.Join(base, "cache")
	keptCache := filepath.Join(cacheBase, "artwork", "poster", "media-kept-old.jpg")
	missingCache := filepath.Join(cacheBase, "artwork", "poster", "media-missing-old.jpg")
	if err := os.MkdirAll(filepath.Dir(keptCache), 0755); err != nil {
		t.Fatalf("create cache dir: %v", err)
	}
	for _, path := range []string{keptCache, missingCache} {
		if err := os.WriteFile(path, []byte("cached"), 0644); err != nil {
			t.Fatalf("write cache marker: %v", err)
		}
	}

	logger := zap.NewNop().Sugar()
	cfg := config.NewConfig()
	scanner := &ScannerService{
		mediaRepo:       repos.Media,
		seriesRepo:      repos.Series,
		personRepo:      repos.Person,
		mediaPersonRepo: repos.MediaPerson,
		cfg:             cfg,
		logger:          logger,
		nfoService:      NewNFOService(logger),
		artworkCache:    NewArtworkCache(cacheBase, logger),
		sidecarCache:    make(map[string]directorySidecarCacheEntry),
		walkFileTree:    filepath.Walk,
		statFile:        os.Stat,
		probeMediaFile: func(string) ([]byte, error) {
			return []byte(`{"streams":[{"codec_type":"video","codec_name":"h264","width":1920,"height":1080}],"format":{"duration":"60"}}`), nil
		},
	}

	return &overwriteSafetyFixture{
		db:           db,
		repos:        repos,
		scanner:      scanner,
		library:      library,
		root:         root,
		keptPath:     keptPath,
		missingPath:  missing.FilePath,
		keptCache:    keptCache,
		missingCache: missingCache,
	}
}

func assertOverwriteOriginalState(t *testing.T, fixture *overwriteSafetyFixture) {
	t.Helper()

	var kept model.Media
	if err := fixture.db.First(&kept, "id = ?", "media-kept").Error; err != nil {
		t.Fatalf("load kept media: %v", err)
	}
	if kept.Title != "Original Title" || kept.FileSize != 1 {
		t.Fatalf("kept media changed after failed overwrite: title=%q size=%d", kept.Title, kept.FileSize)
	}
	var library model.Library
	if err := fixture.db.First(&library, "id = ?", fixture.library.ID).Error; err != nil {
		t.Fatalf("load library after failed overwrite: %v", err)
	}
	if library.LastScan != nil {
		t.Fatalf("LastScan changed after failed overwrite: %v", library.LastScan)
	}
	for name, target := range map[string]interface{}{
		"media":         &model.Media{},
		"favorites":     &model.Favorite{},
		"watch history": &model.WatchHistory{},
	} {
		var count int64
		if err := fixture.db.Model(target).Count(&count).Error; err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		if count != 2 {
			t.Fatalf("expected two %s rows after rollback, got %d", name, count)
		}
	}
	var seriesCount int64
	if err := fixture.db.Model(&model.Series{}).Count(&seriesCount).Error; err != nil {
		t.Fatalf("count series: %v", err)
	}
	if seriesCount != 1 {
		t.Fatalf("expected series to survive rollback, got %d", seriesCount)
	}
	for _, path := range []string{fixture.keptCache, fixture.missingCache} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("cache changed before successful commit: %s: %v", path, err)
		}
	}
}

func seedOverwriteActor(t *testing.T, fixture *overwriteSafetyFixture, personID, name string) {
	t.Helper()
	if err := fixture.db.Create(&model.Person{ID: personID, Name: name}).Error; err != nil {
		t.Fatalf("seed actor person: %v", err)
	}
	if err := fixture.db.Create(&model.MediaPerson{
		ID:       "relation-" + personID,
		MediaID:  "media-kept",
		PersonID: personID,
		Role:     "actor",
	}).Error; err != nil {
		t.Fatalf("seed actor relation: %v", err)
	}
}

func mediaActorNames(t *testing.T, fixture *overwriteSafetyFixture) []string {
	t.Helper()
	var relations []model.MediaPerson
	if err := fixture.db.Preload("Person").Where("media_id = ?", "media-kept").Order("person_id ASC").Find(&relations).Error; err != nil {
		t.Fatalf("load actor relations: %v", err)
	}
	names := make([]string, 0, len(relations))
	for _, relation := range relations {
		names = append(names, relation.Person.Name)
	}
	return names
}

func TestOverwriteOfflineRootPreservesAllStateForEveryLibraryType(t *testing.T) {
	for _, libraryType := range []string{"movie", "tvshow", "mixed"} {
		t.Run(libraryType, func(t *testing.T) {
			fixture := newOverwriteSafetyFixture(t, libraryType, false)
			_, err := fixture.scanner.ScanLibraryOverwrite(fixture.db, &fixture.library, ScanOptions{})
			if !IsScanIncomplete(err) {
				t.Fatalf("expected incomplete offline overwrite, got %v", err)
			}
			assertOverwriteOriginalState(t, fixture)
		})
	}
}

func TestOverwriteLastScanFailureEmitsFailedOnlyAfterRollback(t *testing.T) {
	fixture := newOverwriteSafetyFixture(t, "movie", true)
	recorder := &recordingScanBroadcaster{}
	fixture.scanner.wsHub = recorder
	trigger := `
		CREATE TRIGGER fail_overwrite_last_scan
		BEFORE UPDATE ON libraries
		WHEN OLD.id = 'overwrite-library'
		BEGIN
			SELECT RAISE(ABORT, 'injected last scan failure');
		END`
	if err := fixture.db.Exec(trigger).Error; err != nil {
		t.Fatalf("create LastScan failure trigger: %v", err)
	}

	if _, err := fixture.scanner.ScanLibraryOverwrite(fixture.db, &fixture.library, ScanOptions{}); err == nil {
		t.Fatal("expected LastScan update failure")
	}
	assertOverwriteOriginalState(t, fixture)
	assertSingleScanTerminal(t, recorder, EventScanFailed)
	if len(recorder.mediaEvents) != 0 {
		t.Fatalf("metadata success events escaped rolled-back overwrite: %+v", recorder.mediaEvents)
	}
}

func TestOverwriteCommitFailureEmitsFailedWithoutCompleted(t *testing.T) {
	fixture := newOverwriteSafetyFixture(t, "movie", true)
	recorder := &recordingScanBroadcaster{}
	fixture.scanner.wsHub = recorder
	sqlDB, err := fixture.db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := fixture.db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}
	for _, statement := range []string{
		`CREATE TABLE overwrite_commit_parent (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE overwrite_commit_guard (
			id INTEGER PRIMARY KEY,
			parent_id INTEGER,
			FOREIGN KEY(parent_id) REFERENCES overwrite_commit_parent(id) DEFERRABLE INITIALLY DEFERRED
		)`,
	} {
		if err := fixture.db.Exec(statement).Error; err != nil {
			t.Fatalf("create commit failure fixture: %v", err)
		}
	}
	if err := fixture.db.Transaction(func(tx *gorm.DB) error {
		return tx.Exec("INSERT INTO overwrite_commit_guard(id, parent_id) VALUES (100, 999)").Error
	}); err == nil {
		t.Fatal("deferred foreign key fixture did not fail at commit")
	}
	trigger := `
		CREATE TRIGGER fail_overwrite_commit
		AFTER UPDATE ON libraries
		WHEN OLD.id = 'overwrite-library'
		BEGIN
			INSERT INTO overwrite_commit_guard(id, parent_id) VALUES (1, 999);
		END`
	if err := fixture.db.Exec(trigger).Error; err != nil {
		t.Fatalf("create commit failure trigger: %v", err)
	}

	if _, err := fixture.scanner.ScanLibraryOverwrite(fixture.db, &fixture.library, ScanOptions{}); err == nil {
		t.Fatal("expected transaction commit failure")
	}
	assertOverwriteOriginalState(t, fixture)
	assertSingleScanTerminal(t, recorder, EventScanFailed)
	if len(recorder.mediaEvents) != 0 {
		t.Fatalf("metadata success events escaped failed commit: %+v", recorder.mediaEvents)
	}
}

func TestOverwriteSuccessfulCommitEmitsCompletedOnce(t *testing.T) {
	fixture := newOverwriteSafetyFixture(t, "movie", true)
	recorder := &recordingScanBroadcaster{}
	fixture.scanner.wsHub = recorder

	if _, err := fixture.scanner.ScanLibraryOverwrite(fixture.db, &fixture.library, ScanOptions{}); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	assertSingleScanTerminal(t, recorder, EventScanCompleted)
	started := 0
	for _, event := range recorder.events {
		if event.eventType == EventScanStarted {
			started++
		}
	}
	if started != 1 {
		t.Fatalf("expected one scan start event, got %d", started)
	}
}

func TestOverwriteFilesystemAndProbeRunBeforeDatabaseTransaction(t *testing.T) {
	fixture := newOverwriteSafetyFixture(t, "movie", true)
	if err := fixture.db.Exec("CREATE TABLE overwrite_probe_markers (id INTEGER PRIMARY KEY AUTOINCREMENT, stage TEXT NOT NULL)").Error; err != nil {
		t.Fatalf("create probe marker table: %v", err)
	}
	sqlDB, err := fixture.db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(4)
	originalWalk := fixture.scanner.walkFileTree
	fixture.scanner.walkFileTree = func(root string, walkFn filepath.WalkFunc) error {
		if err := fixture.db.Exec("INSERT INTO overwrite_probe_markers(stage) VALUES ('walk')").Error; err != nil {
			return fmt.Errorf("walk could not write outside scan transaction: %w", err)
		}
		return originalWalk(root, walkFn)
	}
	fixture.scanner.probeMediaFile = func(string) ([]byte, error) {
		if err := fixture.db.Exec("INSERT INTO overwrite_probe_markers(stage) VALUES ('probe')").Error; err != nil {
			return nil, fmt.Errorf("probe could not write outside scan transaction: %w", err)
		}
		return []byte(`{"streams":[{"codec_type":"video","codec_name":"h264","width":1920,"height":1080}],"format":{"duration":"60"}}`), nil
	}

	if _, err := fixture.scanner.ScanLibraryOverwrite(fixture.db, &fixture.library, ScanOptions{}); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	for _, stage := range []string{"walk", "probe"} {
		var markerCount int64
		if err := fixture.db.Table("overwrite_probe_markers").Where("stage = ?", stage).Count(&markerCount).Error; err != nil {
			t.Fatalf("count %s markers: %v", stage, err)
		}
		if markerCount != 1 {
			t.Fatalf("expected one transaction-free %s operation, got %d", stage, markerCount)
		}
	}
}

func TestOverwriteMediaEventsObserveCommittedDatabaseState(t *testing.T) {
	fixture := newOverwriteSafetyFixture(t, "movie", true)
	recorder := &commitCheckingBroadcaster{db: fixture.db, libraryID: fixture.library.ID}
	fixture.scanner.wsHub = recorder

	if _, err := fixture.scanner.ScanLibraryOverwrite(fixture.db, &fixture.library, ScanOptions{}); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if recorder.mediaEvents == 0 {
		t.Fatal("expected at least one media metadata event")
	}
	if recorder.mediaBeforeCommit || recorder.metadataBeforeCommit {
		t.Fatalf("media event observed uncommitted state: lastScan=%v metadata=%v", recorder.mediaBeforeCommit, recorder.metadataBeforeCommit)
	}
}

func TestOverwriteIncompleteScanEmitsIncompleteOnly(t *testing.T) {
	fixture := newOverwriteSafetyFixture(t, "movie", false)
	recorder := &recordingScanBroadcaster{}
	fixture.scanner.wsHub = recorder

	if _, err := fixture.scanner.ScanLibraryOverwrite(fixture.db, &fixture.library, ScanOptions{}); !IsScanIncomplete(err) {
		t.Fatalf("expected incomplete scan, got %v", err)
	}
	assertSingleScanTerminal(t, recorder, EventScanIncomplete)
}

func TestOverwritePartialEnumerationRollsBack(t *testing.T) {
	fixture := newOverwriteSafetyFixture(t, "movie", true)
	fixture.scanner.walkFileTree = func(root string, walkFn filepath.WalkFunc) error {
		return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err := walkFn(path, info, err); err != nil {
				return err
			}
			if normalizeMediaPath(path) == normalizeMediaPath(fixture.keptPath) {
				return fs.ErrPermission
			}
			return nil
		})
	}

	_, err := fixture.scanner.ScanLibraryOverwrite(fixture.db, &fixture.library, ScanOptions{})
	if !IsScanIncomplete(err) {
		t.Fatalf("expected incomplete partial enumeration, got %v", err)
	}
	assertOverwriteOriginalState(t, fixture)
}

func TestOverwriteFailureDoesNotPoisonNextRefresh(t *testing.T) {
	fixture := newOverwriteSafetyFixture(t, "movie", true)
	failOnce := true
	fixture.scanner.walkFileTree = func(root string, walkFn filepath.WalkFunc) error {
		return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err := walkFn(path, info, err); err != nil {
				return err
			}
			if failOnce && normalizeMediaPath(path) == normalizeMediaPath(fixture.keptPath) {
				failOnce = false
				return fs.ErrPermission
			}
			return nil
		})
	}

	if _, err := fixture.scanner.ScanLibraryOverwrite(fixture.db, &fixture.library, ScanOptions{}); !IsScanIncomplete(err) {
		t.Fatalf("expected first refresh to fail incompletely, got %v", err)
	}
	assertOverwriteOriginalState(t, fixture)
	if _, err := fixture.scanner.ScanLibraryOverwrite(fixture.db, &fixture.library, ScanOptions{}); err != nil {
		t.Fatalf("second refresh did not recover: %v", err)
	}
}

func TestOverwriteCancellationMidScanRollsBack(t *testing.T) {
	fixture := newOverwriteSafetyFixture(t, "movie", true)
	recorder := &recordingScanBroadcaster{}
	fixture.scanner.wsHub = recorder
	ctx, cancel := context.WithCancel(context.Background())
	fixture.scanner.walkFileTree = func(root string, walkFn filepath.WalkFunc) error {
		return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err := walkFn(path, info, err); err != nil {
				return err
			}
			if normalizeMediaPath(path) == normalizeMediaPath(fixture.keptPath) {
				cancel()
			}
			return nil
		})
	}

	_, err := fixture.scanner.ScanLibraryOverwrite(fixture.db, &fixture.library, ScanOptions{Context: ctx})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled overwrite, got %v", err)
	}
	assertOverwriteOriginalState(t, fixture)
	terminal := assertSingleScanTerminal(t, recorder, EventScanCanceled)
	if terminal.data.Phase != "canceled" {
		t.Fatalf("expected canceled phase, got %+v", terminal.data)
	}
}

func TestOverwriteMetadataParseFailureRollsBack(t *testing.T) {
	fixture := newOverwriteSafetyFixture(t, "movie", true)
	seedOverwriteActor(t, fixture, "old-actor", "Old Actor")
	if err := os.WriteFile(filepath.Join(fixture.root, "kept.nfo"), []byte("<movie><title>broken"), 0644); err != nil {
		t.Fatalf("write invalid NFO: %v", err)
	}

	if _, err := fixture.scanner.ScanLibraryOverwrite(fixture.db, &fixture.library, ScanOptions{}); err == nil {
		t.Fatal("expected metadata parsing failure")
	}
	assertOverwriteOriginalState(t, fixture)
	if got := mediaActorNames(t, fixture); len(got) != 1 || got[0] != "Old Actor" {
		t.Fatalf("actor relations changed after NFO rollback: %v", got)
	}
}

func TestOverwriteFFprobeExecutionFailureRollsBackAndEmitsFailed(t *testing.T) {
	fixture := newOverwriteSafetyFixture(t, "movie", true)
	recorder := &recordingScanBroadcaster{}
	fixture.scanner.wsHub = recorder
	fixture.scanner.probeMediaFile = func(string) ([]byte, error) {
		return nil, errors.New("injected ffprobe execution failure")
	}
	if err := fixture.db.Model(&model.Media{}).Where("id = ?", "media-kept").Update("metadata_phase", MetadataPhaseQuick).Error; err != nil {
		t.Fatalf("mark original metadata quick: %v", err)
	}

	if _, err := fixture.scanner.ScanLibraryOverwrite(fixture.db, &fixture.library, ScanOptions{}); err == nil {
		t.Fatal("expected FFprobe execution failure")
	}
	assertOverwriteOriginalState(t, fixture)
	var kept model.Media
	if err := fixture.db.First(&kept, "id = ?", "media-kept").Error; err != nil {
		t.Fatalf("load kept media: %v", err)
	}
	if NormalizeMetadataPhase(kept.MetadataPhase) != MetadataPhaseQuick {
		t.Fatalf("probe failure marked media full: %s", kept.MetadataPhase)
	}
	assertSingleScanTerminal(t, recorder, EventScanFailed)
}

func TestOverwriteInvalidFFprobeJSONRollsBack(t *testing.T) {
	fixture := newOverwriteSafetyFixture(t, "movie", true)
	fixture.scanner.probeMediaFile = func(string) ([]byte, error) {
		return []byte(`{"streams":`), nil
	}

	if _, err := fixture.scanner.ScanLibraryOverwrite(fixture.db, &fixture.library, ScanOptions{}); err == nil {
		t.Fatal("expected invalid FFprobe JSON failure")
	}
	assertOverwriteOriginalState(t, fixture)
}

func TestOverwriteInvalidSTRMRollsBack(t *testing.T) {
	fixture := newOverwriteSafetyFixture(t, "movie", true)
	strmPath := filepath.Join(fixture.root, "kept.strm")
	if err := os.Remove(fixture.keptPath); err != nil {
		t.Fatalf("remove movie fixture: %v", err)
	}
	if err := os.Remove(filepath.Join(fixture.root, "kept.nfo")); err != nil {
		t.Fatalf("remove movie NFO fixture: %v", err)
	}
	if err := os.WriteFile(strmPath, []byte("https://"), 0644); err != nil {
		t.Fatalf("write invalid STRM: %v", err)
	}
	if err := fixture.db.Model(&model.Media{}).Where("id = ?", "media-kept").Updates(map[string]interface{}{
		"file_path": strmPath,
		"file_size": int64(1),
	}).Error; err != nil {
		t.Fatalf("point media at STRM: %v", err)
	}
	fixture.keptPath = strmPath

	if _, err := fixture.scanner.ScanLibraryOverwrite(fixture.db, &fixture.library, ScanOptions{}); err == nil {
		t.Fatal("expected invalid STRM parse failure")
	}
	assertOverwriteOriginalState(t, fixture)
}

func TestNonOverwriteMetadataProbeFailureRemainsBestEffort(t *testing.T) {
	fixture := newOverwriteSafetyFixture(t, "movie", true)
	fixture.scanner.probeMediaFile = func(string) ([]byte, error) {
		return nil, errors.New("injected best-effort probe failure")
	}
	if err := fixture.db.Model(&model.Media{}).Where("id = ?", "media-kept").Update("metadata_phase", MetadataPhaseQuick).Error; err != nil {
		t.Fatalf("mark media quick: %v", err)
	}

	if err := fixture.scanner.completeMediaMetadataByID("media-kept"); err != nil {
		t.Fatalf("ordinary metadata completion stopped on probe failure: %v", err)
	}
	var kept model.Media
	if err := fixture.db.First(&kept, "id = ?", "media-kept").Error; err != nil {
		t.Fatalf("load completed media: %v", err)
	}
	if NormalizeMetadataPhase(kept.MetadataPhase) != MetadataPhaseFull {
		t.Fatalf("ordinary best-effort completion behavior changed: %s", kept.MetadataPhase)
	}
}

func TestOverwriteExplicitEmptyActorListClearsOldRelations(t *testing.T) {
	fixture := newOverwriteSafetyFixture(t, "movie", true)
	seedOverwriteActor(t, fixture, "old-actor", "Old Actor")
	nfo := `<movie><title>Refreshed Movie</title><actors/></movie>`
	if err := os.WriteFile(filepath.Join(fixture.root, "kept.nfo"), []byte(nfo), 0644); err != nil {
		t.Fatalf("write empty actor NFO: %v", err)
	}

	if _, err := fixture.scanner.ScanLibraryOverwrite(fixture.db, &fixture.library, ScanOptions{}); err != nil {
		t.Fatalf("overwrite empty actors: %v", err)
	}
	if got := mediaActorNames(t, fixture); len(got) != 0 {
		t.Fatalf("old actor relations survived explicit empty list: %v", got)
	}
}

func TestOverwriteMissingActorFieldPreservesOldRelations(t *testing.T) {
	fixture := newOverwriteSafetyFixture(t, "movie", true)
	seedOverwriteActor(t, fixture, "old-actor", "Old Actor")
	nfo := `<movie><title>Refreshed Movie</title></movie>`
	if err := os.WriteFile(filepath.Join(fixture.root, "kept.nfo"), []byte(nfo), 0644); err != nil {
		t.Fatalf("write NFO without actors: %v", err)
	}

	if _, err := fixture.scanner.ScanLibraryOverwrite(fixture.db, &fixture.library, ScanOptions{}); err != nil {
		t.Fatalf("overwrite without actors: %v", err)
	}
	if got := mediaActorNames(t, fixture); len(got) != 1 || got[0] != "Old Actor" {
		t.Fatalf("missing actor field changed old relations: %v", got)
	}
}

func TestOverwriteDamagedEmptyActorPreservesOldRelations(t *testing.T) {
	fixture := newOverwriteSafetyFixture(t, "movie", true)
	seedOverwriteActor(t, fixture, "old-actor", "Old Actor")
	nfo := `<movie><title>Refreshed Movie</title><actor/></movie>`
	if err := os.WriteFile(filepath.Join(fixture.root, "kept.nfo"), []byte(nfo), 0644); err != nil {
		t.Fatalf("write damaged empty actor NFO: %v", err)
	}

	if _, err := fixture.scanner.ScanLibraryOverwrite(fixture.db, &fixture.library, ScanOptions{}); err != nil {
		t.Fatalf("overwrite damaged empty actor: %v", err)
	}
	if got := mediaActorNames(t, fixture); len(got) != 1 || got[0] != "Old Actor" {
		t.Fatalf("damaged empty actor changed old relations: %v", got)
	}
}

func TestOverwriteReplacesActorRelations(t *testing.T) {
	fixture := newOverwriteSafetyFixture(t, "movie", true)
	seedOverwriteActor(t, fixture, "old-actor", "Old Actor")
	nfo := `<movie><title>Refreshed Movie</title><actor><name>New Actor</name></actor></movie>`
	if err := os.WriteFile(filepath.Join(fixture.root, "kept.nfo"), []byte(nfo), 0644); err != nil {
		t.Fatalf("write replacement actor NFO: %v", err)
	}

	if _, err := fixture.scanner.ScanLibraryOverwrite(fixture.db, &fixture.library, ScanOptions{}); err != nil {
		t.Fatalf("overwrite actors: %v", err)
	}
	if got := mediaActorNames(t, fixture); len(got) != 1 || got[0] != "New Actor" {
		t.Fatalf("actor relations were not replaced: %v", got)
	}
}

func TestOverwriteActorInsertFailureRollsBackOldRelations(t *testing.T) {
	fixture := newOverwriteSafetyFixture(t, "movie", true)
	seedOverwriteActor(t, fixture, "old-actor", "Old Actor")
	nfo := `<movie><title>Refreshed Movie</title><actor><name>New Actor</name></actor></movie>`
	if err := os.WriteFile(filepath.Join(fixture.root, "kept.nfo"), []byte(nfo), 0644); err != nil {
		t.Fatalf("write replacement actor NFO: %v", err)
	}
	trigger := `
		CREATE TRIGGER fail_overwrite_actor_insert
		BEFORE INSERT ON media_people
		BEGIN
			SELECT RAISE(ABORT, 'injected actor relation failure');
		END`
	if err := fixture.db.Exec(trigger).Error; err != nil {
		t.Fatalf("create actor insert trigger: %v", err)
	}

	if _, err := fixture.scanner.ScanLibraryOverwrite(fixture.db, &fixture.library, ScanOptions{}); err == nil {
		t.Fatal("expected actor insertion failure")
	}
	assertOverwriteOriginalState(t, fixture)
	if got := mediaActorNames(t, fixture); len(got) != 1 || got[0] != "Old Actor" {
		t.Fatalf("old actor relation was not rolled back: %v", got)
	}
}

func TestNonOverwriteExplicitEmptyActorsPreserveExistingRelations(t *testing.T) {
	fixture := newOverwriteSafetyFixture(t, "movie", true)
	seedOverwriteActor(t, fixture, "old-actor", "Old Actor")
	nfo := `<movie><title>Refreshed Movie</title><actors/></movie>`
	if err := os.WriteFile(filepath.Join(fixture.root, "kept.nfo"), []byte(nfo), 0644); err != nil {
		t.Fatalf("write empty actor NFO: %v", err)
	}

	fixture.scanner.SyncActorsForMedia(&model.Media{ID: "media-kept", FilePath: fixture.keptPath})
	if got := mediaActorNames(t, fixture); len(got) != 1 || got[0] != "Old Actor" {
		t.Fatalf("ordinary actor sync behavior changed: %v", got)
	}
}

func TestOverwriteDatabaseWriteFailureRollsBack(t *testing.T) {
	fixture := newOverwriteSafetyFixture(t, "movie", true)
	trigger := `
		CREATE TRIGGER fail_overwrite_media_update
		BEFORE UPDATE ON media
		WHEN OLD.id = 'media-kept'
		BEGIN
			SELECT RAISE(ABORT, 'injected overwrite write failure');
		END`
	if err := fixture.db.Exec(trigger).Error; err != nil {
		t.Fatalf("create update failure trigger: %v", err)
	}

	if _, err := fixture.scanner.ScanLibraryOverwrite(fixture.db, &fixture.library, ScanOptions{}); err == nil {
		t.Fatal("expected database write failure")
	}
	assertOverwriteOriginalState(t, fixture)
}

func TestOverwriteSuccessRefreshesInPlaceAndCleansConfirmedMissingForEveryLibraryType(t *testing.T) {
	for _, libraryType := range []string{"movie", "tvshow", "mixed"} {
		t.Run(libraryType, func(t *testing.T) {
			fixture := newOverwriteSafetyFixture(t, libraryType, true)
			result, err := fixture.scanner.ScanLibraryOverwrite(fixture.db, &fixture.library, ScanOptions{})
			if err != nil {
				t.Fatalf("overwrite %s: %v", libraryType, err)
			}
			if len(result.DeletedMediaIDs) != 1 || result.DeletedMediaIDs[0] != "media-missing" {
				t.Fatalf("unexpected deleted IDs: %v", result.DeletedMediaIDs)
			}

			var kept model.Media
			if err := fixture.db.First(&kept, "id = ?", "media-kept").Error; err != nil {
				t.Fatalf("load kept media: %v", err)
			}
			if kept.ID != "media-kept" || kept.FileSize <= 1 {
				t.Fatalf("existing media was not refreshed in place: id=%s size=%d", kept.ID, kept.FileSize)
			}
			if libraryType == "movie" && kept.Title != "Refreshed Movie" {
				t.Fatalf("movie metadata was not reread: %q", kept.Title)
			}

			var keptFavorites, keptHistory int64
			if err := fixture.db.Model(&model.Favorite{}).Where("media_id = ?", kept.ID).Count(&keptFavorites).Error; err != nil {
				t.Fatalf("count kept favorites: %v", err)
			}
			if err := fixture.db.Model(&model.WatchHistory{}).Where("media_id = ?", kept.ID).Count(&keptHistory).Error; err != nil {
				t.Fatalf("count kept history: %v", err)
			}
			if keptFavorites != 1 || keptHistory != 1 {
				t.Fatalf("kept user state changed: favorites=%d history=%d", keptFavorites, keptHistory)
			}
			for _, target := range []interface{}{&model.Favorite{}, &model.WatchHistory{}} {
				var missingCount int64
				if err := fixture.db.Model(target).Where("media_id = ?", "media-missing").Count(&missingCount).Error; err != nil {
					t.Fatalf("count missing association %T: %v", target, err)
				}
				if missingCount != 0 {
					t.Fatalf("missing media association %T was not cleaned", target)
				}
			}
			if _, err := os.Stat(fixture.keptCache); err != nil {
				t.Fatalf("surviving media cache was removed: %v", err)
			}
			if _, err := os.Stat(fixture.missingCache); !os.IsNotExist(err) {
				t.Fatalf("missing media cache was not removed after commit: %v", err)
			}
		})
	}
}
