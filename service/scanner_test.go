package service

import (
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"navi-desktop/model"
	"navi-desktop/repository"
)

func TestShouldRefreshExistingMovieMediaIncrementalUpdatesChangedSidecar(t *testing.T) {
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "sample.mp4")
	nfoPath := filepath.Join(dir, "sample.nfo")

	if err := os.WriteFile(mediaPath, []byte("video"), 0644); err != nil {
		t.Fatalf("write media: %v", err)
	}
	if err := os.WriteFile(nfoPath, []byte("<movie><title>old</title></movie>"), 0644); err != nil {
		t.Fatalf("write nfo: %v", err)
	}

	info, err := os.Stat(mediaPath)
	if err != nil {
		t.Fatalf("stat media: %v", err)
	}
	sidecars := collectDirectorySidecarFiles(dir)
	signature := repository.MediaFileSignature{
		VideoFingerprint:   buildVideoFingerprintFromInfo(info),
		SidecarFingerprint: buildSidecarFingerprint(mediaPath, sidecars),
	}

	if err := os.WriteFile(nfoPath, []byte("<movie><title>changed sidecar</title></movie>"), 0644); err != nil {
		t.Fatalf("rewrite nfo: %v", err)
	}
	nextSidecars := collectDirectorySidecarFiles(dir)

	shouldRefresh, refreshVideo := shouldRefreshExistingMovieMedia(
		ScanOptions{Incremental: true},
		signature,
		mediaPath,
		info,
		nextSidecars,
	)

	if !shouldRefresh {
		t.Fatal("expected incremental scan to refresh when the NFO sidecar changes")
	}
	if refreshVideo {
		t.Fatal("expected sidecar-only incremental refresh to skip video probing")
	}
}

func TestShouldRefreshExistingMovieMediaIncrementalSkipsVideoOnlyChange(t *testing.T) {
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "sample.mp4")

	if err := os.WriteFile(mediaPath, []byte("video"), 0644); err != nil {
		t.Fatalf("write media: %v", err)
	}

	info, err := os.Stat(mediaPath)
	if err != nil {
		t.Fatalf("stat media: %v", err)
	}
	sidecars := collectDirectorySidecarFiles(dir)
	signature := repository.MediaFileSignature{
		VideoFingerprint:   buildVideoFingerprintFromInfo(info),
		SidecarFingerprint: buildSidecarFingerprint(mediaPath, sidecars),
	}

	if err := os.WriteFile(mediaPath, []byte("video content changed"), 0644); err != nil {
		t.Fatalf("rewrite media: %v", err)
	}
	changedInfo, err := os.Stat(mediaPath)
	if err != nil {
		t.Fatalf("stat changed media: %v", err)
	}

	shouldRefresh, _ := shouldRefreshExistingMovieMedia(
		ScanOptions{Incremental: true},
		signature,
		mediaPath,
		changedInfo,
		sidecars,
	)

	if shouldRefresh {
		t.Fatal("expected incremental scan to keep skipping existing media when only the video fingerprint changes")
	}
}

type scannerSafetyFixture struct {
	db        *gorm.DB
	scanner   *ScannerService
	library   model.Library
	mediaPath string
}

func newScannerSafetyFixture(t *testing.T, libraryType string, rootPath string) *scannerSafetyFixture {
	t.Helper()

	dbName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", dbName)), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.Exec("PRAGMA foreign_keys = OFF").Error; err != nil {
		t.Fatalf("disable sqlite foreign keys: %v", err)
	}
	if err := model.AutoMigrate(db); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}

	repos := repository.NewRepositories(db)
	library := model.Library{ID: "library-1", Name: "Safety", Path: rootPath, Type: libraryType}
	user := model.User{ID: "user-1", Username: "user", Password: "hash"}
	series := model.Series{
		ID:           "series-1",
		LibraryID:    library.ID,
		Title:        "Show",
		FolderPath:   filepath.Join(rootPath, "Show"),
		SeasonCount:  1,
		EpisodeCount: 1,
	}
	mediaPath := filepath.Join(rootPath, "Show", "Season 1", "S01E01.mkv")
	media := model.Media{
		ID:         "media-1",
		LibraryID:  library.ID,
		SeriesID:   series.ID,
		Title:      "Show",
		FilePath:   mediaPath,
		MediaType:  "episode",
		SeasonNum:  1,
		EpisodeNum: 1,
	}

	for _, value := range []interface{}{
		&user,
		&library,
		&series,
		&media,
		&model.Favorite{ID: "favorite-1", UserID: user.ID, MediaID: media.ID},
		&model.WatchHistory{ID: "watch-1", UserID: user.ID, MediaID: media.ID},
	} {
		if err := db.Create(value).Error; err != nil {
			t.Fatalf("seed %T: %v", value, err)
		}
	}

	logger := zap.NewNop().Sugar()
	return &scannerSafetyFixture{
		db: db,
		scanner: &ScannerService{
			mediaRepo:    repos.Media,
			seriesRepo:   repos.Series,
			logger:       logger,
			nfoService:   NewNFOService(logger),
			walkFileTree: filepath.Walk,
			statFile:     os.Stat,
		},
		library:   library,
		mediaPath: mediaPath,
	}
}

func assertScannerSafetyRows(t *testing.T, fixture *scannerSafetyFixture, want int64) {
	t.Helper()

	checks := []struct {
		name  string
		model interface{}
	}{
		{name: "media", model: &model.Media{}},
		{name: "favorite", model: &model.Favorite{}},
		{name: "watch history", model: &model.WatchHistory{}},
		{name: "series", model: &model.Series{}},
	}
	for _, check := range checks {
		var count int64
		if err := fixture.db.Model(check.model).Count(&count).Error; err != nil {
			t.Fatalf("count %s: %v", check.name, err)
		}
		if count != want {
			t.Fatalf("expected %s rows = %d, got %d", check.name, want, count)
		}
	}
}

func deleteUpdateOptions() ScanOptions {
	return ScanOptions{Mode: "delete_update"}
}

func TestDeleteUpdateMissingRootPreservesMediaAndUserState(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "offline")
	fixture := newScannerSafetyFixture(t, "movie", rootPath)

	_, err := fixture.scanner.ScanLibraryWithOptions(&fixture.library, deleteUpdateOptions())
	if !IsScanIncomplete(err) {
		t.Fatalf("expected incomplete scan, got %v", err)
	}
	assertScannerSafetyRows(t, fixture, 1)
}

func TestDeleteUpdatePartialTraversalPreservesMediaAndUserState(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rootPath, "Show"), 0755); err != nil {
		t.Fatalf("create show directory: %v", err)
	}
	fixture := newScannerSafetyFixture(t, "tvshow", rootPath)
	fixture.scanner.walkFileTree = func(root string, walkFn filepath.WalkFunc) error {
		info, err := os.Stat(root)
		if err != nil {
			return err
		}
		if err := walkFn(root, info, nil); err != nil {
			return err
		}
		return walkFn(filepath.Join(root, "blocked"), nil, fs.ErrPermission)
	}

	_, err := fixture.scanner.ScanLibraryWithOptions(&fixture.library, deleteUpdateOptions())
	if !IsScanIncomplete(err) {
		t.Fatalf("expected incomplete scan, got %v", err)
	}
	assertScannerSafetyRows(t, fixture, 1)
}

func TestDeleteUpdateMovieEnumeratesRootOnce(t *testing.T) {
	fixture := newScannerSafetyFixture(t, "movie", t.TempDir())
	walkCount := 0
	fixture.scanner.walkFileTree = func(root string, walkFn filepath.WalkFunc) error {
		walkCount++
		return filepath.Walk(root, walkFn)
	}

	_, err := fixture.scanner.ScanLibraryWithOptions(&fixture.library, deleteUpdateOptions())
	if err != nil {
		t.Fatalf("scan movie library: %v", err)
	}
	if walkCount != 1 {
		t.Fatalf("expected one full root enumeration, got %d", walkCount)
	}
	assertScannerSafetyRows(t, fixture, 0)
}

func TestDeleteUpdateEverythingDoesNotWalkRoot(t *testing.T) {
	fixture := newScannerSafetyFixture(t, "movie", t.TempDir())
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"totalResults":0,"results":[]}`))
	}))
	defer server.Close()

	walkCount := 0
	fixture.scanner.walkFileTree = func(root string, walkFn filepath.WalkFunc) error {
		walkCount++
		return filepath.Walk(root, walkFn)
	}

	_, err := fixture.scanner.ScanLibraryWithOptions(&fixture.library, ScanOptions{
		Mode:           "delete_update",
		UseEverything:  true,
		EverythingAddr: server.URL,
	})
	if err != nil {
		t.Fatalf("scan movie library with Everything: %v", err)
	}
	if walkCount != 0 {
		t.Fatalf("expected Everything scan to avoid filepath.Walk, got %d calls", walkCount)
	}
	if requestCount != 1 {
		t.Fatalf("expected one Everything enumeration request, got %d", requestCount)
	}
	assertScannerSafetyRows(t, fixture, 0)
}

func TestDeleteUpdateOfflineSecondRootPreservesAllRoots(t *testing.T) {
	rootPath := t.TempDir()
	fixture := newScannerSafetyFixture(t, "movie", rootPath)
	fixture.library.FolderPaths = []string{rootPath, filepath.Join(t.TempDir(), "offline")}
	if err := fixture.library.ApplyPathConfig(); err != nil {
		t.Fatalf("apply multi-root config: %v", err)
	}

	_, err := fixture.scanner.ScanLibraryWithOptions(&fixture.library, deleteUpdateOptions())
	if !IsScanIncomplete(err) {
		t.Fatalf("expected incomplete scan, got %v", err)
	}
	assertScannerSafetyRows(t, fixture, 1)
}

func TestDeleteUpdateAmbiguousStatPreservesMediaAndUserState(t *testing.T) {
	fixture := newScannerSafetyFixture(t, "tvshow", t.TempDir())
	fixture.scanner.statFile = func(path string) (os.FileInfo, error) {
		if normalizeMediaPath(path) == normalizeMediaPath(fixture.mediaPath) {
			return nil, fs.ErrPermission
		}
		return os.Stat(path)
	}

	_, err := fixture.scanner.ScanLibraryWithOptions(&fixture.library, deleteUpdateOptions())
	if !IsScanIncomplete(err) {
		t.Fatalf("expected incomplete scan, got %v", err)
	}
	assertScannerSafetyRows(t, fixture, 1)
}

func TestDeleteUpdateAllLibraryTypesRemoveMissingEpisodesAndAssociations(t *testing.T) {
	for _, libraryType := range []string{"movie", "tvshow", "mixed"} {
		t.Run(libraryType, func(t *testing.T) {
			fixture := newScannerSafetyFixture(t, libraryType, t.TempDir())

			_, err := fixture.scanner.ScanLibraryWithOptions(&fixture.library, deleteUpdateOptions())
			if err != nil {
				t.Fatalf("scan %s library: %v", libraryType, err)
			}
			assertScannerSafetyRows(t, fixture, 0)
		})
	}
}

func TestScanTerminalEventDistinguishesIncompleteFromFailure(t *testing.T) {
	if got := scanTerminalEvent(&ScanIncompleteError{Root: "root", Err: fs.ErrPermission}); got != EventScanIncomplete {
		t.Fatalf("expected incomplete event, got %s", got)
	}
	if got := scanTerminalEvent(fmt.Errorf("database failed")); got != EventScanFailed {
		t.Fatalf("expected failed event, got %s", got)
	}
	if got := scanTerminalEvent(nil); got != EventScanCompleted {
		t.Fatalf("expected completed event, got %s", got)
	}
}
