package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"navi-desktop/model"
	"navi-desktop/repository"

	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

func newTestApp(t *testing.T) *App {
	t.Helper()

	dbName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", dbName)), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := model.AutoMigrate(db); err != nil {
		t.Fatalf("migrate sqlite failed: %v", err)
	}
	for _, statement := range []string{
		"CREATE UNIQUE INDEX idx_libraries_path_key_active ON libraries(path_key) WHERE deleted_at IS NULL AND path_key <> ''",
		"CREATE UNIQUE INDEX idx_media_library_path_active ON media(library_id, path_key) WHERE deleted_at IS NULL AND path_key <> ''",
		"CREATE UNIQUE INDEX idx_series_library_folder_active ON series(library_id, folder_path_key) WHERE deleted_at IS NULL AND folder_path_key <> ''",
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("create sqlite index failed: %v", err)
		}
	}

	return &App{
		ctx:    context.Background(),
		db:     db,
		repos:  repository.NewRepositories(db),
		logger: zap.NewNop().Sugar(),
		remote: newRemoteAccessState(),
	}
}

func TestJellyfinAuthenticateAndListItems(t *testing.T) {
	app := newTestApp(t)
	mediaDir := t.TempDir()
	mediaPath := filepath.Join(mediaDir, "movie.mkv")
	if err := os.WriteFile(mediaPath, []byte("fake-video"), 0o644); err != nil {
		t.Fatalf("write media file failed: %v", err)
	}

	library := &model.Library{
		Name: "Movies",
		Path: mediaDir,
		Type: "movie",
	}
	if err := app.repos.Library.Create(library); err != nil {
		t.Fatalf("create library failed: %v", err)
	}

	media := &model.Media{
		LibraryID: library.ID,
		Title:     "Example Movie",
		FilePath:  mediaPath,
		MediaType: "movie",
		FileSize:  10,
		Duration:  120,
	}
	if err := app.repos.Media.Create(media); err != nil {
		t.Fatalf("create media failed: %v", err)
	}

	settings := &DesktopSettings{
		RemoteBindHost:     defaultRemoteBindHost,
		RemoteUsername:     "infuse",
		RemotePassword:     "secret",
		JellyfinServerName: "Test Sidecar",
	}
	handler := app.newJellyfinMux(settings)

	loginBody, _ := json.Marshal(map[string]string{
		"Username": "infuse",
		"Pw":       "secret",
	})
	loginReq := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", bytes.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	loginReq.Header.Set("X-Emby-Authorization", `MediaBrowser Client="Infuse", Device="iPhone", DeviceId="device-1", Version="8.0"`)
	loginResp := httptest.NewRecorder()
	handler.ServeHTTP(loginResp, loginReq)
	if loginResp.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", loginResp.Code, loginResp.Body.String())
	}

	var authResult struct {
		AccessToken string `json:"AccessToken"`
	}
	if err := json.Unmarshal(loginResp.Body.Bytes(), &authResult); err != nil {
		t.Fatalf("decode auth result failed: %v", err)
	}
	if authResult.AccessToken == "" {
		t.Fatalf("expected access token in auth result")
	}

	itemsReq := httptest.NewRequest(http.MethodGet, "/Items?recursive=true&includeItemTypes=Movie", nil)
	itemsReq.Header.Set("X-MediaBrowser-Token", authResult.AccessToken)
	itemsResp := httptest.NewRecorder()
	handler.ServeHTTP(itemsResp, itemsReq)
	if itemsResp.Code != http.StatusOK {
		t.Fatalf("items status = %d, body = %s", itemsResp.Code, itemsResp.Body.String())
	}

	var queryResult struct {
		Items []map[string]interface{} `json:"Items"`
	}
	if err := json.Unmarshal(itemsResp.Body.Bytes(), &queryResult); err != nil {
		t.Fatalf("decode items result failed: %v", err)
	}
	if len(queryResult.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(queryResult.Items))
	}
	if queryResult.Items[0]["Name"] != "Example Movie" {
		t.Fatalf("expected movie name Example Movie, got %#v", queryResult.Items[0]["Name"])
	}

	groupingReq := httptest.NewRequest(http.MethodGet, "/UserViews/GroupingOptions?userId="+jellyfinUserID, nil)
	groupingReq.Header.Set("X-MediaBrowser-Token", authResult.AccessToken)
	groupingResp := httptest.NewRecorder()
	handler.ServeHTTP(groupingResp, groupingReq)
	if groupingResp.Code != http.StatusOK {
		t.Fatalf("grouping options status = %d, body = %s", groupingResp.Code, groupingResp.Body.String())
	}

	var groupingResult []map[string]interface{}
	if err := json.Unmarshal(groupingResp.Body.Bytes(), &groupingResult); err != nil {
		t.Fatalf("decode grouping options failed: %v", err)
	}
	if len(groupingResult) != 0 {
		t.Fatalf("expected empty grouping options, got %d", len(groupingResult))
	}
}

func TestCleanOrphanedMediaAssociationsDelegatesToRepository(t *testing.T) {
	app := newTestApp(t)
	if err := app.db.Exec("PRAGMA foreign_keys = OFF").Error; err != nil {
		t.Fatalf("disable sqlite foreign keys failed: %v", err)
	}

	user := &model.User{ID: "cleanup-user", Username: "cleanup", Password: "hash"}
	library := &model.Library{ID: "cleanup-library", Name: "Cleanup", Path: "C:/cleanup"}
	media := &model.Media{ID: "cleanup-media", LibraryID: library.ID, Title: "Cleanup Media", FilePath: "C:/cleanup/media.mp4"}
	person := &model.Person{ID: "cleanup-person", Name: "Actor"}
	if err := app.db.Create(user).Error; err != nil {
		t.Fatalf("create user failed: %v", err)
	}
	if err := app.db.Create(library).Error; err != nil {
		t.Fatalf("create library failed: %v", err)
	}
	if err := app.db.Create(media).Error; err != nil {
		t.Fatalf("create media failed: %v", err)
	}
	if err := app.db.Create(person).Error; err != nil {
		t.Fatalf("create person failed: %v", err)
	}
	if err := app.db.Create(&model.MediaPerson{ID: "cleanup-media-person", MediaID: media.ID, PersonID: person.ID, Role: "actor"}).Error; err != nil {
		t.Fatalf("create media person failed: %v", err)
	}
	if err := app.db.Create(&model.WatchHistory{ID: "cleanup-watch", UserID: user.ID, MediaID: media.ID}).Error; err != nil {
		t.Fatalf("create watch history failed: %v", err)
	}
	if err := app.db.Unscoped().Delete(&model.Media{}, "id = ?", media.ID).Error; err != nil {
		t.Fatalf("force delete media failed: %v", err)
	}

	cleaned, err := app.CleanOrphanedMediaAssociations()
	if err != nil {
		t.Fatalf("clean orphaned media associations failed: %v", err)
	}
	if cleaned.MediaPeople != 1 || cleaned.WatchHistories != 1 || cleaned.People != 1 {
		t.Fatalf("unexpected cleanup result: %+v", cleaned)
	}
}

func TestAppLibraryScanStateRejectsDuplicateUntilFinished(t *testing.T) {
	app := NewApp()
	if !app.tryBeginLibraryScan("library-1") {
		t.Fatalf("expected first scan begin to succeed")
	}
	if app.tryBeginLibraryScan("library-1") {
		t.Fatalf("expected duplicate scan begin to be rejected")
	}
	if !app.tryBeginLibraryScan("library-2") {
		t.Fatalf("expected different library scan to be allowed")
	}
	app.finishLibraryScan("library-1")
	if !app.tryBeginLibraryScan("library-1") {
		t.Fatalf("expected scan begin after finish to succeed")
	}
}

func TestUpdateJellyfinPlaybackStateCoalescesDuplicateHistoryRows(t *testing.T) {
	app := newTestApp(t)
	library := &model.Library{ID: "history-library", Name: "History", Path: "C:/history"}
	media := &model.Media{ID: "history-media", LibraryID: library.ID, Title: "History Media", FilePath: "C:/history/media.mp4", Duration: 120}
	if err := app.db.Create(library).Error; err != nil {
		t.Fatalf("create library failed: %v", err)
	}
	if err := app.db.Create(media).Error; err != nil {
		t.Fatalf("create media failed: %v", err)
	}
	// Simulate a pre-versioned database created before the composite constraint.
	if err := app.db.Exec("DROP INDEX idx_watch_user_media").Error; err != nil {
		t.Fatalf("drop history unique index failed: %v", err)
	}
	if err := app.db.Create(&model.WatchHistory{ID: "history-old-1", UserID: desktopUserID, MediaID: media.ID, Position: 10}).Error; err != nil {
		t.Fatalf("create first duplicate history failed: %v", err)
	}
	if err := app.db.Create(&model.WatchHistory{ID: "history-old-2", UserID: desktopUserID, MediaID: media.ID, Position: 20}).Error; err != nil {
		t.Fatalf("create second duplicate history failed: %v", err)
	}

	if err := app.updateJellyfinPlaybackState(media.ID, int64(115)*jellyfinTicksPerSecond, true); err != nil {
		t.Fatalf("update playback state failed: %v", err)
	}

	var histories []model.WatchHistory
	if err := app.db.Where("user_id = ? AND media_id = ?", desktopUserID, media.ID).Find(&histories).Error; err != nil {
		t.Fatalf("query histories failed: %v", err)
	}
	if len(histories) != 1 {
		t.Fatalf("expected duplicate histories to be coalesced, got %d", len(histories))
	}
	if !histories[0].Completed || histories[0].Position != 115 {
		t.Fatalf("expected completed position 115, got %+v", histories[0])
	}
}

func TestWriteDesktopSettingsFileAtomicWritesJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	settings := &DesktopSettings{Theme: "dark", JellyfinPort: 18096, RemoteBindHost: "127.0.0.1"}
	if err := writeDesktopSettingsFile(path, settings); err != nil {
		t.Fatalf("write settings failed: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings failed: %v", err)
	}
	var got DesktopSettings
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("settings file is not valid json: %v", err)
	}
	if got.Theme != settings.Theme || got.JellyfinPort != settings.JellyfinPort || got.RemoteBindHost != settings.RemoteBindHost {
		t.Fatalf("unexpected settings payload: %+v", got)
	}

	settings.Theme = "light"
	if err := writeDesktopSettingsFile(path, settings); err != nil {
		t.Fatalf("rewrite existing settings failed: %v", err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read rewritten settings failed: %v", err)
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("rewritten settings file is not valid json: %v", err)
	}
	if got.Theme != "light" {
		t.Fatalf("expected rewritten theme, got %+v", got)
	}
}

func TestStartReplacementProcessReturnsErrorForMissingExecutable(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.exe")
	if err := startReplacementProcess(missing, nil); err == nil {
		t.Fatalf("expected missing executable to return an error")
	}
}
