package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"navi-desktop/config"
	"navi-desktop/database"
	"navi-desktop/model"
	"navi-desktop/repository"
	"navi-desktop/service"
)

// App struct
type App struct {
	ctx              context.Context
	appCancel        context.CancelFunc
	db               *gorm.DB
	dbManager        *database.Manager
	repos            *repository.Repositories
	scanner          *service.ScannerService
	thumbnailWorker  *service.ThumbnailWorker
	artworkCache     *service.ArtworkCache
	logFile          *os.File
	removeMediaCache func(string) error
	avatarService    *service.GfriendsAvatarService
	logger           *zap.SugaredLogger
	remote           *remoteAccessState
	desktop          *desktopIntegration
	eventHub         *service.WSHub
	scanMu           sync.Mutex
	scanningLib      map[string]bool
	activeScans      map[string]*scanTask
	activeScanIDs    map[string]*scanTask
	lastScanTasks    map[string]ScanTaskInfo
	lastScanFailures map[string]ScanTaskInfo
	scanWG           sync.WaitGroup
	maintenanceWG    sync.WaitGroup
	postStartupOnce  sync.Once
	postStartupDelay time.Duration
	migrationHook    func()
	maintenanceHook  func(context.Context) error
	shuttingDown     bool
	shutdownOnce     sync.Once
	shutdownTimeout  time.Duration
}

const (
	ScanTaskPending    = "pending"
	ScanTaskRunning    = "running"
	ScanTaskCompleted  = "completed"
	ScanTaskIncomplete = "incomplete"
	ScanTaskFailed     = "failed"
	ScanTaskCanceled   = "canceled"
)

type ScanTaskInfo struct {
	TaskID       string     `json:"task_id"`
	LibraryID    string     `json:"library_id"`
	LibraryName  string     `json:"library_name"`
	Mode         string     `json:"mode"`
	Status       string     `json:"status"`
	FailureStage string     `json:"failure_stage,omitempty"`
	Error        string     `json:"error,omitempty"`
	Retryable    bool       `json:"retryable"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
}

type scanTask struct {
	info     ScanTaskInfo
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	terminal bool
}

func NewApp() *App {
	app := &App{
		remote:           newRemoteAccessState(),
		scanningLib:      make(map[string]bool),
		activeScans:      make(map[string]*scanTask),
		activeScanIDs:    make(map[string]*scanTask),
		lastScanTasks:    make(map[string]ScanTaskInfo),
		lastScanFailures: make(map[string]ScanTaskInfo),
	}
	app.desktop = newDesktopIntegration(app)
	return app
}

func (a *App) reserveArtworkPath(path string) (func(), bool) {
	if a == nil || a.artworkCache == nil || !a.artworkCache.IsCachedPath(path) {
		return func() {}, true
	}
	return a.artworkCache.Reserve(path)
}

func (a *App) tryBeginLibraryScan(libraryID string) bool {
	if a == nil {
		return false
	}
	libraryID = strings.TrimSpace(libraryID)
	if libraryID == "" {
		return false
	}
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	if a.shuttingDown {
		return false
	}
	if a.scanningLib == nil {
		a.scanningLib = make(map[string]bool)
	}
	if a.scanningLib[libraryID] {
		return false
	}
	a.scanningLib[libraryID] = true
	return true
}

func (a *App) finishLibraryScan(libraryID string) {
	if a == nil {
		return
	}
	libraryID = strings.TrimSpace(libraryID)
	if libraryID == "" {
		return
	}
	a.scanMu.Lock()
	delete(a.scanningLib, libraryID)
	a.scanMu.Unlock()
}

func (a *App) startup(ctx context.Context) {
	a.ctx, a.appCancel = context.WithCancel(ctx)

	// 1. 初始化控制台和持久化日志
	l, logFile, logErr := newApplicationLogger("navi.log")
	if logErr != nil {
		l, _ = zap.NewDevelopment()
	}
	a.logger = l.Sugar()
	a.logFile = logFile
	if logErr != nil {
		a.logger.Warnf("open persistent application log failed: %v", logErr)
	}

	// 2. 初始化带版本迁移、备份和连接级 PRAGMA 的 SQLite 持久层。
	dbManager, err := database.Open("navi.db", database.DefaultOptions())
	if err != nil {
		a.logger.Fatalf("连接或升级数据库失败: %v", err)
	}
	a.dbManager = dbManager
	a.db = dbManager.DB()

	// 3. 构建 Repositories 单例
	a.repos = repository.NewRepositories(a.db)

	// 4. 注入之前写好的最小化 Shim 层
	cfg := config.NewConfig()
	wsHub := service.NewWSHub(a.ctx)
	a.eventHub = wsHub
	a.artworkCache = service.NewArtworkCache(cfg.Cache.CacheDir, a.logger)
	a.avatarService = service.NewGfriendsAvatarService(service.GfriendsAvatarOptions{
		CacheDir:     cfg.Cache.CacheDir,
		ArtworkCache: a.artworkCache,
		Logger:       a.logger,
	})
	thumbnailSettingsProvider := func() service.ThumbnailSettings {
		settings, err := a.GetDesktopSettings()
		if err != nil || settings == nil {
			return service.DefaultThumbnailSettings()
		}
		return service.ThumbnailSettings{
			Enabled:            settings.EnableVideoThumbnail,
			PreviewCount:       settings.ThumbnailPreviewCount,
			MinDurationSeconds: settings.ThumbnailMinDurationSeconds,
		}
	}

	// 5. 将桌面级组件全部注入核心 Scanner
	gfriendsAvatarEnabled := func() bool {
		settings, err := a.GetDesktopSettings()
		if err != nil || settings == nil {
			return true
		}
		return settings.EnableGfriendsAvatars
	}
	a.scanner = service.NewScannerService(a.repos.Media, a.repos.Series, a.repos.Person, a.repos.MediaPerson, cfg, a.logger)
	a.scanner.SetWSHub(wsHub)
	a.scanner.SetThumbnailSettingsProvider(thumbnailSettingsProvider)
	a.scanner.SetArtworkCache(a.artworkCache)
	a.scanner.SetGfriendsAvatarService(a.avatarService, gfriendsAvatarEnabled)
	thumbSvc := a.scanner.ThumbnailService()
	thumbSvc.SetArtworkCache(a.artworkCache)
	a.thumbnailWorker = service.NewThumbnailWorker(a.repos.Media, thumbSvc, thumbnailSettingsProvider, a.logger, wsHub)
	// a.scanner.SetMatchRuleRepo(a.repos.MatchRule)

	a.startPostStartupServices(thumbSvc, thumbnailSettingsProvider)

	a.logger.Infof("Application backend started successfully! DB: %s", dbManager.Path())
}

func (a *App) startPostStartupServices(thumbSvc *service.ThumbnailService, thumbnailSettingsProvider func() service.ThumbnailSettings) {
	a.postStartupOnce.Do(func() {
		a.maintenanceWG.Add(1)
		go func() {
			defer a.maintenanceWG.Done()
			defer func() {
				if r := recover(); r != nil && a.logger != nil {
					a.logger.Warnf("post-startup services failed: %v", r)
				}
			}()

			// Let Wails finish creating and painting the WebView before maintenance work starts.
			delay := 500 * time.Millisecond
			if a.postStartupDelay != 0 {
				delay = a.postStartupDelay
			}
			if delay < 0 {
				delay = 0
			}
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-a.ctx.Done():
				return
			case <-timer.C:
			}

			if a.migrationHook != nil {
				a.migrationHook()
			} else {
				a.migrateThumbnailTasksV2(thumbSvc, thumbnailSettingsProvider())
			}
			if a.ctx.Err() != nil {
				return
			}
			maintenanceStart := time.Now()
			if a.maintenanceHook != nil {
				if err := a.maintenanceHook(a.ctx); err != nil && !errors.Is(err, context.Canceled) {
					a.logger.Warnf("post-startup maintenance failed: %v", err)
				}
			} else if a.artworkCache != nil {
				if _, err := a.artworkCache.Maintain(a.ctx); err != nil && !errors.Is(err, context.Canceled) {
					a.logger.Warnf("artwork cache maintenance failed: %v", err)
				}
				if err := a.artworkCache.CleanupOldTempsIfDue(a.ctx, 24*time.Hour, 24*time.Hour, 200); err != nil && !errors.Is(err, context.Canceled) {
					a.logger.Warnf("old cache temp cleanup failed: %v", err)
				}
				a.logger.Debugf("delayed cache maintenance completed in %s", time.Since(maintenanceStart))
			}
			if a.thumbnailWorker != nil {
				a.thumbnailWorker.Start()
			}

			if settings, err := a.GetDesktopSettings(); err == nil {
				if err := a.syncDesktopIntegration(settings); err != nil {
					a.logger.Warnf("sync desktop integration failed: %v", err)
				}
				if err := a.syncRemoteServices(settings); err != nil {
					a.logger.Warnf("start remote services failed: %v", err)
				}
			} else {
				a.logger.Warnf("load desktop settings for remote services failed: %v", err)
			}
		}()
	})
}

func (a *App) migrateThumbnailTasksV2(thumbSvc *service.ThumbnailService, settings service.ThumbnailSettings) {
	if a.db == nil || (a.ctx != nil && a.ctx.Err() != nil) {
		return
	}

	if a.db.Migrator().HasColumn(&model.Media{}, "thumbnail_policy") {
		if err := a.db.Migrator().DropColumn(&model.Media{}, "thumbnail_policy"); err != nil {
			a.logger.Warnf("drop thumbnail_policy column failed: %v", err)
		}
	}

	if settings == (service.ThumbnailSettings{}) {
		settings = service.DefaultThumbnailSettings()
	}
	if thumbSvc == nil {
		thumbSvc = service.NewThumbnailService(config.NewConfig(), a.logger)
	}
	sameMediaPath := func(left string, right string) bool {
		left = strings.TrimSpace(left)
		right = strings.TrimSpace(right)
		if left == "" || right == "" {
			return left == right
		}
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}

	type migrationStats struct {
		scanned   int64
		updated   int64
		pending   int64
		stale     int64
		generated int64
	}

	var stats migrationStats
	var batch []model.Media
	result := a.db.Model(&model.Media{}).
		Where("media_type = ? AND thumbnail_status = ? AND (thumbnail_fingerprint = '' OR thumbnail_fingerprint IS NULL)",
			"movie", service.ThumbnailStatusNone).
		Order("created_at ASC").
		FindInBatches(&batch, 200, func(tx *gorm.DB, batchNum int) error {
			for i := range batch {
				if a.ctx != nil {
					if err := a.ctx.Err(); err != nil {
						return err
					}
				}
				original := batch[i]
				media := batch[i]
				stats.scanned++

				status, fingerprint, err := service.ResolveThumbnailStateFromDisk(&media, thumbSvc, settings)
				if err != nil && !os.IsNotExist(err) {
					a.logger.Debugf("thumbnail v2 migration inspect failed: media=%s err=%v", media.ID, err)
				}

				if status == original.ThumbnailStatus &&
					strings.TrimSpace(fingerprint) == strings.TrimSpace(original.ThumbnailFingerprint) &&
					sameMediaPath(media.PosterPath, original.PosterPath) &&
					sameMediaPath(media.BackdropPath, original.BackdropPath) {
					continue
				}

				updates := map[string]interface{}{
					"thumbnail_status":      status,
					"thumbnail_fingerprint": fingerprint,
				}
				if !sameMediaPath(media.PosterPath, original.PosterPath) {
					updates["poster_path"] = media.PosterPath
				}
				if !sameMediaPath(media.BackdropPath, original.BackdropPath) {
					updates["backdrop_path"] = media.BackdropPath
				}

				if err := tx.Model(&model.Media{}).Where("id = ?", media.ID).Updates(updates).Error; err != nil {
					return err
				}

				stats.updated++
				switch status {
				case service.ThumbnailStatusPending:
					stats.pending++
				case service.ThumbnailStatusStale:
					stats.stale++
				case service.ThumbnailStatusGenerated:
					stats.generated++
				}
			}
			return nil
		})
	if result.Error != nil {
		if errors.Is(result.Error, context.Canceled) {
			return
		}
		a.logger.Warnf("thumbnail v2 migration failed: %v", result.Error)
		return
	}
	if stats.updated > 0 {
		a.logger.Infof("thumbnail v2 migration reconciled %d/%d media items (pending=%d stale=%d generated=%d)",
			stats.updated, stats.scanned, stats.pending, stats.stale, stats.generated)
	}
}

func (a *App) hydrateLibraryForClient(lib *model.Library) {
	if lib == nil {
		return
	}
	var count int64
	_ = a.db.Model(&model.Media{}).Where("library_id = ?", lib.ID).Count(&count).Error
	lib.MediaCount = int(count)
	lib.HydratePathConfig()
}

// -----------------------------------------------------
// Wails Bind 导出的暴露核心方法
// -----------------------------------------------------

func (a *App) GetLibraries() ([]model.Library, error) {
	libs, err := a.repos.Library.List()
	if err != nil {
		return nil, err
	}
	for i := range libs {
		a.hydrateLibraryForClient(&libs[i])
	}
	return libs, nil
}

func (a *App) CreateLibrary(lib *model.Library) (*model.Library, error) {
	if err := lib.ApplyPathConfig(); err != nil {
		return nil, err
	}
	if err := a.repos.Library.Create(lib); err != nil {
		return nil, err
	}
	lib.HydratePathConfig()
	return lib, nil
}

func (a *App) UpdateLibrary(lib *model.Library) error {
	if err := lib.ApplyPathConfig(); err != nil {
		return err
	}
	return a.repos.Library.Update(lib)
}

func normalizeScanMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "overwrite", "delete_update", "incremental":
		return strings.ToLower(strings.TrimSpace(mode))
	default:
		return "incremental"
	}
}

func (a *App) registerScanTask(lib *model.Library, mode string) (*scanTask, error) {
	if a == nil || lib == nil || strings.TrimSpace(lib.ID) == "" {
		return nil, fmt.Errorf("library is required")
	}
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	now := time.Now().UTC()
	task := &scanTask{
		info: ScanTaskInfo{
			TaskID:      uuid.NewString(),
			LibraryID:   lib.ID,
			LibraryName: lib.Name,
			Mode:        mode,
			Status:      ScanTaskPending,
			StartedAt:   now,
		},
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
	}

	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	if a.shuttingDown {
		cancel()
		return nil, fmt.Errorf("application is shutting down")
	}
	if a.scanningLib == nil {
		a.scanningLib = make(map[string]bool)
	}
	if a.scanningLib[lib.ID] {
		activeID := ""
		if active := a.activeScans[lib.ID]; active != nil {
			activeID = active.info.TaskID
		}
		cancel()
		if activeID != "" {
			return nil, fmt.Errorf("library %s is already scanning (task %s)", lib.ID, activeID)
		}
		return nil, fmt.Errorf("library %s is already scanning", lib.ID)
	}
	if a.activeScans == nil {
		a.activeScans = make(map[string]*scanTask)
	}
	if a.activeScanIDs == nil {
		a.activeScanIDs = make(map[string]*scanTask)
	}
	a.scanningLib[lib.ID] = true
	a.activeScans[lib.ID] = task
	a.activeScanIDs[task.info.TaskID] = task
	return task, nil
}

func (a *App) startScanWithOptions(lib *model.Library, task *scanTask, options service.ScanOptions) {
	if settings, err := a.GetDesktopSettings(); err == nil && settings != nil {
		options.UseEverything = settings.UseEverything
		options.EverythingAddr = settings.EverythingAddr
	}
	options.TaskID = task.info.TaskID
	options.Context = task.ctx
	options.SuppressTerminal = true

	a.scanMu.Lock()
	if a.shuttingDown || task.terminal {
		a.scanMu.Unlock()
		a.finishScanTask(lib, task, options, service.OverwriteScanResult{}, context.Canceled)
		return
	}
	a.scanWG.Add(1)
	a.scanMu.Unlock()
	if a.logger != nil {
		a.logger.Infof("scan task started: library=%s task=%s mode=%s roots=%d", lib.ID, task.info.TaskID, options.Mode, len(lib.RootPaths()))
	}
	go a.runScanTask(lib, task, options)
}

func (a *App) runScanTask(lib *model.Library, task *scanTask, options service.ScanOptions) {
	defer a.scanWG.Done()
	var result service.OverwriteScanResult
	var scanErr error
	defer func() {
		if recovered := recover(); recovered != nil {
			scanErr = fmt.Errorf("scan panic: %v", recovered)
			if a.logger != nil {
				a.logger.Errorf("scan panic: library=%s task=%s err=%v", lib.ID, task.info.TaskID, recovered)
			}
		}
		a.finishScanTask(lib, task, options, result, scanErr)
	}()

	a.scanMu.Lock()
	if !task.terminal {
		task.info.Status = ScanTaskRunning
	}
	a.scanMu.Unlock()

	if a.scanner == nil || a.repos == nil {
		scanErr = fmt.Errorf("scanner is unavailable")
		return
	}
	if err := options.Context.Err(); err != nil {
		scanErr = err
		return
	}
	if options.Mode == "overwrite" {
		before, err := a.repos.Media.ListIDAndPathByLibrary(lib.ID)
		if err != nil {
			scanErr = fmt.Errorf("load media before overwrite: %w", err)
			return
		}
		if err := a.repos.Media.DeleteByLibraryID(lib.ID); err != nil {
			scanErr = err
			return
		}
		if err := a.repos.Series.DeleteByLibraryID(lib.ID); err != nil {
			scanErr = err
			return
		}
		for _, record := range before {
			result.DeletedMediaIDs = append(result.DeletedMediaIDs, record.ID)
			if a.artworkCache != nil {
				if err := a.artworkCache.RemoveMedia(record.ID); err != nil {
					result.CacheCleanupWarnings = append(result.CacheCleanupWarnings, err)
				}
			}
		}
	}

	scanResult, err := a.scanner.ScanLibraryWithOptionsResult(lib, options)
	result.Scanned = scanResult.Scanned
	result.TotalTargets = scanResult.TotalTargets
	result.Cleaned = scanResult.Cleaned
	scanErr = err
	if scanErr != nil {
		if a.logger != nil && !errors.Is(scanErr, context.Canceled) {
			a.logger.Errorf("scan error: library=%s task=%s mode=%s err=%v", lib.ID, task.info.TaskID, options.Mode, scanErr)
		}
		return
	}

	result.LastScan = time.Now().UTC().Truncate(time.Second)
	lib.LastScan = &result.LastScan
	if err := a.repos.Library.Update(lib); err != nil {
		scanErr = fmt.Errorf("update library last scan: %w", err)
		return
	}
	for _, warning := range result.CacheCleanupWarnings {
		if a.logger != nil {
			a.logger.Warnf("scan cache cleanup warning: %v", warning)
		}
	}
	a.bumpRecommendationVersion()
	a.clearRecommendationGenres()
}

func (a *App) finishScanTask(lib *model.Library, task *scanTask, options service.ScanOptions, result service.OverwriteScanResult, scanErr error) {
	if a == nil || lib == nil || task == nil {
		return
	}
	status := ScanTaskCompleted
	if errors.Is(scanErr, context.Canceled) {
		status = ScanTaskCanceled
	} else if service.IsScanIncomplete(scanErr) {
		status = ScanTaskIncomplete
	} else if scanErr != nil {
		status = ScanTaskFailed
	}
	now := time.Now().UTC()

	a.scanMu.Lock()
	if task.terminal {
		a.scanMu.Unlock()
		return
	}
	task.terminal = true
	task.info.Status = status
	task.info.FinishedAt = &now
	task.info.Retryable = status == ScanTaskFailed || status == ScanTaskIncomplete
	if scanErr != nil && status != ScanTaskCanceled {
		task.info.Error = scanErr.Error()
		task.info.FailureStage = scanTaskFailureStage(scanErr)
	}
	delete(a.scanningLib, lib.ID)
	delete(a.activeScans, lib.ID)
	delete(a.activeScanIDs, task.info.TaskID)
	if a.lastScanTasks == nil {
		a.lastScanTasks = make(map[string]ScanTaskInfo)
	}
	a.lastScanTasks[lib.ID] = task.info
	if status == ScanTaskFailed || status == ScanTaskIncomplete {
		if a.lastScanFailures == nil {
			a.lastScanFailures = make(map[string]ScanTaskInfo)
		}
		a.lastScanFailures[lib.ID] = task.info
	} else if status == ScanTaskCompleted || status == ScanTaskCanceled {
		delete(a.lastScanFailures, lib.ID)
	}
	close(task.done)
	shuttingDown := a.shuttingDown
	a.scanMu.Unlock()
	task.cancel()
	if a.logger != nil {
		a.logger.Infof(
			"scan task finished: library=%s task=%s mode=%s status=%s scanned=%d total=%d cleaned=%d duration=%s failure_stage=%s err=%v",
			lib.ID,
			task.info.TaskID,
			options.Mode,
			status,
			result.Scanned,
			result.TotalTargets,
			result.Cleaned,
			now.Sub(task.info.StartedAt).Round(time.Millisecond),
			task.info.FailureStage,
			scanErr,
		)
	}

	if !shuttingDown && a.scanner != nil {
		a.scanner.BroadcastScanTerminal(lib, options, result.Scanned, result.TotalTargets, result.Cleaned, scanErr)
	}
}

func scanTaskFailureStage(err error) string {
	if err == nil {
		return ""
	}
	if service.IsScanIncomplete(err) {
		return "enumeration"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "ffprobe"), strings.Contains(message, "probe media"):
		return "ffprobe"
	case strings.Contains(message, "nfo"), strings.Contains(message, "metadata"):
		return "metadata"
	case strings.Contains(message, "strm"):
		return "strm"
	case strings.Contains(message, "transaction"), strings.Contains(message, "database"), strings.Contains(message, "last scan"):
		return "database"
	default:
		return "scan"
	}
}

func (a *App) ScanLibrary(libraryID string) (*ScanTaskInfo, error) {
	return a.ScanLibraryWithMode(libraryID, "incremental")
	/*
		lib, err := a.repos.Library.FindByID(libraryID)
		if err != nil {
			return err
		}
		// 利用本地并行协程进行扫描防卡死
		go func() {
			_, err := a.scanner.ScanLibraryWithOptions(lib, service.ScanOptions{
				Mode:         "incremental",
				Incremental:  true,
				CleanDeleted: false,
			})
			if err != nil {
				a.logger.Errorf("Scan error: %v", err)
				return
			}

			lastScanAt := time.Now().UTC().Truncate(time.Second)
			libPatch := &model.Library{
				ID:       lib.ID,
				LastScan: &lastScanAt,
			}
			if err := a.repos.Library.Update(libPatch); err != nil {
				a.logger.Errorf("update library last_scan failed: %v", err)
			}
		}()
		return nil
	*/
}

type StatsItem struct {
	Name        string `json:"name"`
	Count       int    `json:"count"`
	Image       string `json:"image"`
	FilterValue string `json:"filter_value"`
}

const desktopUserID = "desktop_user"

var genreTechnicalPattern = regexp.MustCompile(`^(?i)(\d{3,4}P|4K|8K|UHD|FHD|HD|SD|H264|H265|HEVC|AV1|HDR|60FPS|FPS)$`)
var genreCodePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{2,12}$`)

var allowedShortGenreTokens = map[string]bool{
	"VR": true,
	"3D": true,
}

func collectMediaIDs(mediaItems []model.Media) []string {
	seen := make(map[string]bool, len(mediaItems))
	mediaIDs := make([]string, 0, len(mediaItems))
	for _, item := range mediaItems {
		if item.ID == "" || seen[item.ID] {
			continue
		}
		seen[item.ID] = true
		mediaIDs = append(mediaIDs, item.ID)
	}
	return mediaIDs
}

func (a *App) loadMediaStateSets(mediaIDs []string) (map[string]bool, map[string]bool) {
	favoriteSet := make(map[string]bool, len(mediaIDs))
	watchedSet := make(map[string]bool, len(mediaIDs))
	if len(mediaIDs) == 0 {
		return favoriteSet, watchedSet
	}

	var favoriteIDs []string
	if err := a.db.Model(&model.Favorite{}).
		Where("user_id = ? AND media_id IN ?", desktopUserID, mediaIDs).
		Pluck("media_id", &favoriteIDs).Error; err != nil {
		a.logger.Warnf("load favorite states failed: %v", err)
	} else {
		for _, mediaID := range favoriteIDs {
			favoriteSet[mediaID] = true
		}
	}

	var watchedIDs []string
	if err := a.db.Model(&model.WatchHistory{}).
		Where("user_id = ? AND completed = ? AND media_id IN ?", desktopUserID, true, mediaIDs).
		Pluck("media_id", &watchedIDs).Error; err != nil {
		a.logger.Warnf("load watched states failed: %v", err)
	} else {
		for _, mediaID := range watchedIDs {
			watchedSet[mediaID] = true
		}
	}

	return favoriteSet, watchedSet
}

func (a *App) hydrateMediaState(media *model.Media) {
	if media == nil || media.ID == "" {
		return
	}
	favoriteSet, watchedSet := a.loadMediaStateSets([]string{media.ID})
	media.IsFavorite = favoriteSet[media.ID]
	media.IsWatched = watchedSet[media.ID]
}

func (a *App) hydrateMediaSliceStates(mediaItems []model.Media) {
	favoriteSet, watchedSet := a.loadMediaStateSets(collectMediaIDs(mediaItems))
	for i := range mediaItems {
		mediaItems[i].IsFavorite = favoriteSet[mediaItems[i].ID]
		mediaItems[i].IsWatched = watchedSet[mediaItems[i].ID]
	}
}

type mediaActorSearchRow struct {
	MediaID  string
	Name     string
	OrigName string
}

func (a *App) loadMediaActorTextMap(mediaIDs []string) map[string]string {
	actorTextByMediaID := make(map[string]string, len(mediaIDs))
	if len(mediaIDs) == 0 {
		return actorTextByMediaID
	}

	var rows []mediaActorSearchRow
	if err := a.db.Table("media_people").
		Select("media_people.media_id, people.name, people.orig_name").
		Joins("JOIN people ON people.id = media_people.person_id").
		Where("media_people.role = ? AND media_people.media_id IN ?", "actor", mediaIDs).
		Order("media_people.media_id ASC").
		Order("CASE WHEN media_people.sort_order <= 0 THEN 9999 ELSE media_people.sort_order END ASC").
		Order("media_people.created_at ASC").
		Scan(&rows).Error; err != nil {
		a.logger.Warnf("load media actor texts failed: %v", err)
		return actorTextByMediaID
	}

	namesByMediaID := make(map[string][]string, len(mediaIDs))
	seenByMediaID := make(map[string]map[string]bool, len(mediaIDs))
	for _, row := range rows {
		name := normalizeActorName(row.Name)
		if name == "" {
			name = normalizeActorName(row.OrigName)
		}
		key := actorKey(name)
		if key == "" {
			continue
		}

		seenNames := seenByMediaID[row.MediaID]
		if seenNames == nil {
			seenNames = make(map[string]bool)
			seenByMediaID[row.MediaID] = seenNames
		}
		if seenNames[key] {
			continue
		}

		seenNames[key] = true
		namesByMediaID[row.MediaID] = append(namesByMediaID[row.MediaID], name)
	}

	for mediaID, names := range namesByMediaID {
		if len(names) == 0 {
			continue
		}
		actorTextByMediaID[mediaID] = strings.Join(names, ", ")
	}

	return actorTextByMediaID
}

func buildMediaSearchText(media *model.Media, actorText string) string {
	if media == nil {
		return ""
	}

	parts := []string{
		media.Title,
		media.OrigTitle,
		media.Code,
		media.Maker,
		media.Label,
		media.Studio,
		media.Genres,
		media.ReleaseDateNormalized,
		media.FilePath,
		actorText,
	}

	if media.Year > 0 {
		parts = append(parts, fmt.Sprintf("%d", media.Year))
	}

	var builder strings.Builder
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString(part)
	}

	return builder.String()
}

func (a *App) hydrateMediaSearchText(media *model.Media) {
	if media == nil {
		return
	}
	media.SearchText = buildMediaSearchText(media, media.Actor)
}

func (a *App) hydrateMediaSliceSearchText(mediaItems []model.Media) {
	actorTextByMediaID := a.loadMediaActorTextMap(collectMediaIDs(mediaItems))
	for i := range mediaItems {
		if mediaItems[i].Actor == "" {
			mediaItems[i].Actor = actorTextByMediaID[mediaItems[i].ID]
		}
		mediaItems[i].SearchText = buildMediaSearchText(&mediaItems[i], mediaItems[i].Actor)
	}
}

func (a *App) buildLibraryActorKeySet(libraryID string) map[string]bool {
	var names []string
	actorKeys := make(map[string]bool)
	err := a.db.Table("people").
		Distinct("people.name").
		Joins("JOIN media_people ON media_people.person_id = people.id").
		Joins("JOIN media ON media.id = media_people.media_id").
		Where("media.library_id = ? AND media_people.role = ?", libraryID, "actor").
		Pluck("people.name", &names).Error
	if err != nil {
		a.logger.Warnf("load actor names for genre stats failed: %v", err)
		return actorKeys
	}

	for _, name := range names {
		if key := actorKey(normalizeActorName(name)); key != "" {
			actorKeys[key] = true
		}
	}
	return actorKeys
}

func normalizeGenreName(token string) string {
	token = strings.TrimSpace(token)
	token = strings.Trim(token, "/|")
	return strings.TrimSpace(token)
}

func isBrowsableGenre(token string, actorKeys map[string]bool) bool {
	token = normalizeGenreName(token)
	if token == "" {
		return false
	}
	if strings.Contains(token, ":") || strings.Contains(token, "：") {
		return false
	}
	if genreTechnicalPattern.MatchString(strings.ToUpper(token)) {
		return false
	}
	if genreCodePattern.MatchString(token) && !allowedShortGenreTokens[strings.ToUpper(token)] {
		return false
	}

	if actorKey := actorKey(normalizeActorName(token)); actorKey != "" && actorKeys[actorKey] {
		return false
	}
	return true
}

func (a *App) GetMediaList(libraryID string, page, size int, sortBy, sortOrder, keyword string, filterType, filterValue string) (interface{}, error) {
	// 扩展原生后端的检索逻辑，增加分类过滤支持
	query := a.db.Model(&model.Media{})
	if sortBy == "last_watched" {
		lastWatchSubQuery := a.db.Table("watch_histories").
			Select("media_id, MAX(updated_at) as last_watched_at").
			Where("user_id = ?", desktopUserID).
			Group("media_id")
		query = query.Joins("LEFT JOIN (?) AS last_watch ON last_watch.media_id = media.id", lastWatchSubQuery)
	}
	if sortBy == "favorite_at" {
		favoriteSubQuery := a.db.Table("favorites").
			Select("media_id, MAX(created_at) as favorite_at").
			Where("user_id = ?", desktopUserID).
			Group("media_id")
		query = query.Joins("LEFT JOIN (?) AS favorite_sort ON favorite_sort.media_id = media.id", favoriteSubQuery)
	}
	if libraryID != "" {
		query = query.Where("library_id = ?", libraryID)
	}
	for _, token := range model.TokenizeMediaSearchQuery(keyword) {
		keywordLike := "%" + token + "%"
		query = query.Where(
			"(media.search_text LIKE ? OR media.search_pinyin LIKE ? OR media.search_initials LIKE ?)",
			keywordLike, keywordLike, keywordLike,
		)
	}

	// 处理 4 种统计过滤
	switch filterType {
	case "directory":
		query = query.Where("file_path LIKE ?", filterValue+"%")
	case "actor":
		actorMatch := a.db.Table("media_people").
			Select("media_id").
			Joins("JOIN people ON people.id = media_people.person_id").
			Where("media_people.person_id = ? OR people.name = ?", filterValue, filterValue)
		query = query.Where("id IN (?)", actorMatch)
	case "genre", "tag":
		query = query.Where("genres LIKE ?", "%"+filterValue+"%")
	case "series":
		query = query.Where("series_id = ?", filterValue)
	case "media_type":
		query = query.Where("media_type = ?", filterValue)
	case "watched":
		// 使用 watch_histories 表关联：desktop_user 且 completed 为真
		query = query.Joins("JOIN watch_histories ON watch_histories.media_id = media.id").
			Where("watch_histories.user_id = ? AND watch_histories.completed = ?", desktopUserID, true)
	case "unwatched":
		watchedMatch := a.db.Table("watch_histories").
			Select("media_id").
			Where("user_id = ? AND completed = ?", desktopUserID, true)
		query = query.Where("media.id NOT IN (?)", watchedMatch)
	case "favorite":
		favoriteMatch := a.db.Table("favorites").
			Select("media_id").
			Where("user_id = ?", desktopUserID)
		if strings.EqualFold(filterValue, "false") {
			query = query.Where("media.id NOT IN (?)", favoriteMatch)
		} else {
			query = query.Where("media.id IN (?)", favoriteMatch)
		}
	}

	if size <= 0 {
		size = 120
	}
	if size > 200 {
		size = 200
	}
	if page < 1 {
		page = 1
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}
	if total > 0 {
		lastPage := int((total + int64(size) - 1) / int64(size))
		if page > lastPage {
			page = lastPage
		}
	}

	var media []model.Media
	// 简单的排序处理
	if sortBy == "created_at" || sortBy == "added_at" || sortBy == "" {
		a.backfillMediaNfoModTime(libraryID)
		a.backfillMediaFileCreatedAt(libraryID)
	}

	sortField := "COALESCE(media.nfo_mod_time, media.file_created_at, media.file_mod_time, media.created_at)"
	switch sortBy {
	case "release_date":
		sortField = "CASE WHEN media.release_date_normalized != '' THEN media.release_date_normalized ELSE printf('%04d-01-01', media.year) END"
	case "video_codec":
		sortField = "LOWER(media.video_codec)"
	case "last_watched":
		sortField = "COALESCE(last_watch.last_watched_at, '')"
	case "favorite_at":
		sortField = "COALESCE(favorite_sort.favorite_at, '')"
	case "rating":
		sortField = "media.rating"
	case "created_at", "added_at", "":
		sortField = "COALESCE(media.nfo_mod_time, media.file_created_at, media.file_mod_time, media.created_at)"
	default:
		sortField = "COALESCE(media.nfo_mod_time, media.file_created_at, media.file_mod_time, media.created_at)"
	}

	dir := "DESC"
	if strings.ToLower(sortOrder) == "asc" {
		dir = "ASC"
	}
	sortStr := sortField + " " + dir

	orderedQuery := query.Order(sortStr).Order("media.id ASC").Offset((page - 1) * size).Limit(size)

	err := orderedQuery.Find(&media).Error
	if err == nil {
		a.hydrateMediaSliceStates(media)
		a.hydrateMediaSliceSearchText(media)
	}
	return map[string]interface{}{
		"items":     media,
		"total":     total,
		"page":      page,
		"page_size": size,
	}, err
}

type mediaFileTimeBackfillRow struct {
	ID            string
	FilePath      string
	FileModTime   *time.Time
	FileCreatedAt *time.Time
}

func (a *App) backfillMediaFileCreatedAt(libraryID string) {
	query := a.db.Model(&model.Media{}).
		Select("id, file_path, file_mod_time, file_created_at").
		Where("file_created_at IS NULL")
	if libraryID != "" {
		query = query.Where("library_id = ?", libraryID)
	}

	var rows []mediaFileTimeBackfillRow
	if err := query.Find(&rows).Error; err != nil || len(rows) == 0 {
		return
	}

	for _, row := range rows {
		var fileCreatedAt *time.Time

		if info, err := os.Stat(row.FilePath); err == nil && !info.IsDir() {
			fileCreatedAt = service.ResolveFileCreatedTime(info)
		}

		if (fileCreatedAt == nil || fileCreatedAt.IsZero()) && row.FileModTime != nil && !row.FileModTime.IsZero() {
			fallback := row.FileModTime.UTC().Truncate(time.Second)
			fileCreatedAt = &fallback
		}

		if fileCreatedAt == nil || fileCreatedAt.IsZero() {
			continue
		}

		_ = a.db.Model(&model.Media{}).
			Where("id = ?", row.ID).
			Update("file_created_at", fileCreatedAt).
			Error
	}
}

type mediaNFOTimeBackfillRow struct {
	ID       string
	FilePath string
}

func (a *App) backfillMediaNfoModTime(libraryID string) {
	query := a.db.Model(&model.Media{}).
		Select("id, file_path").
		Where("nfo_mod_time IS NULL AND nfo_raw_xml != ''")
	if libraryID != "" {
		query = query.Where("library_id = ?", libraryID)
	}

	var rows []mediaNFOTimeBackfillRow
	if err := query.Find(&rows).Error; err != nil || len(rows) == 0 {
		return
	}

	nfoService := service.NewNFOService(a.logger)
	for _, row := range rows {
		nfoPath := ""
		if a.scanner != nil {
			nfoPath = a.scanner.FindNFOForMedia(row.FilePath)
		} else {
			nfoPath = nfoService.FindNFOForMedia(row.FilePath)
		}
		if nfoPath == "" {
			continue
		}

		info, err := os.Stat(nfoPath)
		if err != nil || info == nil || info.IsDir() {
			continue
		}

		nfoModTime := info.ModTime().UTC().Truncate(time.Second)
		_ = a.db.Model(&model.Media{}).
			Where("id = ?", row.ID).
			Update("nfo_mod_time", &nfoModTime).
			Error
	}
}

// GetDirectoryStats 获取目录聚合统计
func (a *App) GetDirectoryStats(libraryID string) ([]StatsItem, error) {
	var filePaths []string
	err := a.db.Model(&model.Media{}).Where("library_id = ?", libraryID).Pluck("file_path", &filePaths).Error
	if err != nil {
		return nil, err
	}

	counts := make(map[string]int)
	for _, p := range filePaths {
		dir := filepath.ToSlash(filepath.Dir(p))
		counts[dir]++
	}

	var results []StatsItem
	for dir, count := range counts {
		results = append(results, StatsItem{
			Name:        filepath.Base(dir),
			Count:       count,
			FilterValue: dir,
		})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Count > results[j].Count })
	return results, nil
}

// GetActorStats 获取演员聚合统计
func (a *App) GetActorStats(libraryID string) ([]StatsItem, error) {
	type Result struct {
		ID         string
		Name       string
		ProfileURL string
		Count      int
	}
	var rawResults []Result
	err := a.db.Raw(`
		SELECT p.id, p.name, p.profile_url, COUNT(mp.media_id) as count
		FROM people p
		JOIN media_people mp ON p.id = mp.person_id
		JOIN media m ON mp.media_id = m.id
		WHERE m.library_id = ? AND mp.role = 'actor'
		GROUP BY p.id, p.name, p.profile_url
		ORDER BY count DESC, p.name ASC
	`, libraryID).Scan(&rawResults).Error
	if err != nil {
		return nil, err
	}

	var results []StatsItem
	for _, r := range rawResults {
		results = append(results, StatsItem{
			Name:        r.Name,
			Count:       r.Count,
			Image:       r.ProfileURL,
			FilterValue: r.ID,
		})
	}
	return results, nil
}

// GetGenreStats 获取类别聚合统计
func (a *App) GetGenreStats(libraryID string) ([]StatsItem, error) {
	var genresStrs []string
	err := a.db.Model(&model.Media{}).Where("library_id = ?", libraryID).Pluck("genres", &genresStrs).Error
	if err != nil {
		return nil, err
	}

	actorKeys := a.buildLibraryActorKeySet(libraryID)
	counts := make(map[string]int)
	for _, s := range genresStrs {
		parts := strings.Split(s, ",")
		for _, g := range parts {
			g = normalizeGenreName(g)
			if isBrowsableGenre(g, actorKeys) {
				counts[g]++
			}
		}
	}

	var results []StatsItem
	for name, count := range counts {
		results = append(results, StatsItem{
			Name:        name,
			Count:       count,
			FilterValue: name,
		})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Count > results[j].Count })
	return results, nil
}

// GetSeriesStats 获取系列聚合统计
func (a *App) GetSeriesStats(libraryID string) ([]StatsItem, error) {
	tagRegex := regexp.MustCompile(`<[^>]*>`)

	type Result struct {
		ID         string
		Title      string
		PosterPath string
		Count      int
	}
	var rawResults []Result
	err := a.db.Raw(`
		SELECT s.id, s.title, s.poster_path, COUNT(m.id) as count
		FROM series s
		JOIN media m ON s.id = m.series_id
		WHERE s.library_id = ?
		GROUP BY s.id
		ORDER BY count DESC
	`, libraryID).Scan(&rawResults).Error
	if err != nil {
		return nil, err
	}

	var results []StatsItem
	for _, r := range rawResults {
		// 清洗 Title 中的 XML/HTML 标签
		cleanTitle := tagRegex.ReplaceAllString(r.Title, "")
		cleanTitle = strings.TrimSpace(cleanTitle)
		if cleanTitle == "" {
			continue
		}

		results = append(results, StatsItem{
			Name:        cleanTitle,
			Count:       r.Count,
			Image:       r.PosterPath,
			FilterValue: r.ID,
		})
	}
	return results, nil
}

func (a *App) GetMediaDetail(mediaID string) (*model.Media, error) {
	var media model.Media
	if err := a.db.Preload("Series").First(&media, "id = ?", mediaID).Error; err != nil {
		return nil, err
	}

	if shouldAutoCompleteDetailMetadata(&media) {
		a.scanner.EnqueueMetadataCompletion(media.ID, true)
	}

	artworkChanged := false
	if media.FilePath != "" {
		nfoService := service.NewNFOService(a.logger)
		if service.NeedsLocalMetadataRepair(&media) {
			nfoPath := ""
			if a.scanner != nil {
				nfoPath = a.scanner.FindNFOForMedia(media.FilePath)
			} else {
				nfoPath = nfoService.FindNFOForMedia(media.FilePath)
			}
			if nfoPath != "" {
				if err := nfoService.ParseMovieNFO(nfoPath, &media); err != nil {
					a.logger.Debugf("repair media local NFO failed: %s, err=%v", nfoPath, err)
				} else if err := a.repos.Media.Update(&media); err != nil {
					a.logger.Warnf("persist repaired media metadata failed: %s, err=%v", media.FilePath, err)
				}
			}
		}
		var poster, backdrop string
		if a.scanner != nil {
			poster, backdrop = a.scanner.FindLocalArtworkForMedia(media.FilePath)
		} else {
			poster, backdrop = nfoService.FindLocalImages(filepath.Dir(media.FilePath))
		}
		if poster != "" && (media.PosterPath == "" || strings.Contains(strings.ToLower(filepath.Base(media.PosterPath)), "fanart")) {
			if !sameAppPath(media.PosterPath, poster) {
				media.PosterPath = poster
				artworkChanged = true
			}
		}
		if backdrop != "" && (media.BackdropPath == "" || a.artworkCache == nil || !a.artworkCache.IsCachedPath(media.BackdropPath)) {
			if !sameAppPath(media.BackdropPath, backdrop) {
				media.BackdropPath = backdrop
				artworkChanged = true
			}
		}
	}
	if artworkChanged {
		if err := a.repos.Media.Update(&media); err != nil {
			a.logger.Warnf("persist cached artwork paths failed: media=%s err=%v", media.ID, err)
		}
	}
	actors, actorText := a.resolveMediaActors(&media)
	media.Actors = actors
	media.Actor = actorText
	a.hydrateMediaSearchText(&media)
	service.ApplyDerivedMediaFields(&media)
	a.hydrateMediaState(&media)
	a.cacheMediaArtworkInBackground(media)

	return &media, nil
}

func shouldAutoCompleteDetailMetadata(media *model.Media) bool {
	if media == nil || service.NormalizeMetadataPhase(media.MetadataPhase) != service.MetadataPhaseQuick {
		return false
	}
	if strings.TrimSpace(media.FilePath) == "" {
		return false
	}
	return media.Duration <= 0 && media.Runtime <= 0
}

func sameAppPath(left string, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" || right == "" {
		return false
	}
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}

type MediaDetailBundle struct {
	Detail   *model.Media `json:"detail"`
	Files    []string     `json:"files"`
	Previews []string     `json:"previews"`
}

func (a *App) getMediaFilesByPath(filePath string) []string {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return nil
	}

	if a.scanner != nil {
		if files := a.scanner.ListMediaFiles(filePath); len(files) > 0 {
			return files
		}
	}

	dir := filepath.Dir(filePath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var files []string
	exts := map[string]bool{".mp4": true, ".mkv": true, ".avi": true, ".mov": true, ".wmv": true, ".flv": true, ".webm": true, ".ts": true, ".strm": true}

	for _, entry := range entries {
		if !entry.IsDir() {
			if exts[strings.ToLower(filepath.Ext(entry.Name()))] {
				files = append(files, filepath.Join(dir, entry.Name()))
			}
		}
	}
	sort.Strings(files)
	return files
}

func (a *App) getMediaPreviewsByPath(filePath string) []string {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return nil
	}

	if a.scanner != nil {
		if previews := a.scanner.CollectMediaPreviews(filePath); len(previews) > 0 {
			return previews
		}
	}

	dir := filepath.Dir(filePath)
	requirePrefix := countVideoFilesInDirectory(dir) > 1

	var candidates []previewCandidate
	order := 0
	addCandidate := func(path string, priority int) {
		candidates = append(candidates, previewCandidate{
			path:     path,
			priority: priority,
			groupKey: previewGroupKey(path, dir),
			order:    order,
		})
		order++
	}

	subDirs := []struct {
		name     string
		priority int
	}{
		{name: "extrafanart", priority: 0},
		{name: "behind the scenes", priority: 1},
	}
	for _, sub := range subDirs {
		subPath := filepath.Join(dir, sub.name)
		if entries, err := os.ReadDir(subPath); err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				path := filepath.Join(subPath, entry.Name())
				if isPreviewImage(path) && previewBelongsToMediaFile(entry.Name(), filePath, requirePrefix) {
					addCandidate(path, sub.priority)
				}
			}
		}
	}

	if entries, err := os.ReadDir(dir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			if !isPreviewImage(path) {
				continue
			}
			if !previewBelongsToMediaFile(entry.Name(), filePath, requirePrefix) {
				continue
			}
			if priority, ok := previewPriority(entry.Name()); ok {
				addCandidate(path, priority)
			}
		}
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].priority != candidates[j].priority {
			return candidates[i].priority < candidates[j].priority
		}
		leftName := strings.ToLower(filepath.Base(candidates[i].path))
		rightName := strings.ToLower(filepath.Base(candidates[j].path))
		if leftName != rightName {
			return leftName < rightName
		}
		return candidates[i].order < candidates[j].order
	})

	var previews []string
	seenPaths := make(map[string]bool)
	seenGroups := make(map[string]bool)
	for _, candidate := range candidates {
		if seenPaths[candidate.path] {
			continue
		}
		if candidate.groupKey != "" && seenGroups[candidate.groupKey] {
			continue
		}
		seenPaths[candidate.path] = true
		if candidate.groupKey != "" {
			seenGroups[candidate.groupKey] = true
		}
		previews = append(previews, candidate.path)
	}

	return previews
}

func (a *App) getMediaPreviews(media *model.Media) []string {
	if media == nil {
		return nil
	}
	if a.scanner != nil {
		if previews := a.scanner.CachedMediaPreviews(media.ID); len(previews) > 0 {
			return previews
		}
	}

	previews := a.getMediaPreviewsByPath(media.FilePath)
	if len(previews) == 0 || a.scanner == nil {
		return previews
	}
	a.cacheMediaPreviewsInBackground(*media, previews)
	return previews
}

func (a *App) cacheMediaArtworkInBackground(media model.Media) {
	if a == nil || a.scanner == nil || strings.TrimSpace(media.ID) == "" || strings.TrimSpace(media.FilePath) == "" {
		return
	}
	go func() {
		updated := media
		if a.scanner.CacheMediaArtworkForMedia(&updated) {
			if err := a.repos.Media.Update(&updated); err != nil {
				a.logger.Warnf("persist background cached artwork paths failed: media=%s err=%v", updated.ID, err)
			}
		}
	}()
}

func (a *App) cacheMediaPreviewsInBackground(media model.Media, previews []string) {
	if a == nil || a.scanner == nil || strings.TrimSpace(media.ID) == "" || len(previews) == 0 {
		return
	}
	sourcePaths := append([]string(nil), previews...)
	go func() {
		a.scanner.CacheMediaPreviews(&media, sourcePaths, len(sourcePaths))
	}()
}

func (a *App) GetMediaDetailBundle(mediaID string) (*MediaDetailBundle, error) {
	detail, err := a.GetMediaDetail(mediaID)
	if err != nil {
		return nil, err
	}

	return &MediaDetailBundle{
		Detail:   detail,
		Files:    a.getMediaFilesByPath(detail.FilePath),
		Previews: a.getMediaPreviews(detail),
	}, nil
}

type DesktopSettings struct {
	// 播放设置
	PlayerPath        string `json:"player_path"`
	UseExternalPlayer bool   `json:"use_external_player"`
	LoopPlayback      bool   `json:"loop_playback"`
	ReadFileInfo      bool   `json:"read_file_info"`

	// 外观设置
	Theme             string `json:"theme"`
	PosterRadius      int    `json:"poster_radius"`
	BackdropBlur      int    `json:"backdrop_blur"`
	MinWindowWidth    int    `json:"min_window_width"`
	ShowSubtitleTag   bool   `json:"show_subtitle_tag"`
	ShowResolutionTag bool   `json:"show_resolution_tag"`
	ShowCountTag      bool   `json:"show_count_tag"`
	ShowGenreInList   bool   `json:"show_genre_in_list"`
	ShowSeriesInList  bool   `json:"show_series_in_list"`
	StaticLoading     bool   `json:"static_loading"`

	// 快捷设置
	Hotkey       string `json:"hotkey"`
	MinToTray    bool   `json:"min_to_tray"`
	MaxNoTaskbar bool   `json:"max_no_taskbar"`
	ShowPrompt   bool   `json:"show_prompt"`
	StartWithOS  bool   `json:"start_with_os"`

	// 扫描设置
	SkipNoNfo                   bool   `json:"skip_no_nfo"`
	GetResolution               bool   `json:"get_resolution"`
	UseEverything               bool   `json:"use_everything"`
	EverythingAddr              string `json:"everything_addr"`
	ScanFromVideoDir            bool   `json:"scan_from_video_dir"`
	EnableVideoThumbnail        bool   `json:"enable_video_thumbnail"`
	EnableGfriendsAvatars       bool   `json:"enable_gfriends_avatars"`
	ThumbnailPreviewCount       int    `json:"thumbnail_preview_count"`
	ThumbnailMinDurationSeconds int    `json:"thumbnail_min_duration_seconds"`
	RemoteBindHost              string `json:"remote_bind_host"`
	RemoteUsername              string `json:"remote_username"`
	RemotePassword              string `json:"remote_password"`
	JellyfinEnabled             bool   `json:"jellyfin_enabled"`
	JellyfinPort                int    `json:"jellyfin_port"`
	JellyfinServerName          string `json:"jellyfin_server_name"`

	// Emby 设置
	EmbyEnabled bool   `json:"emby_enabled"`
	EmbyUser    string `json:"emby_user"`
	EmbyURL     string `json:"emby_url"`
	EmbyAPIKey  string `json:"emby_api_key"`
}

var settingsPath = "settings.json"

func (a *App) GetDesktopSettings() (*DesktopSettings, error) {
	var settings DesktopSettings
	var data []byte
	data, err := os.ReadFile(settingsPath)
	if err == nil {
		json.Unmarshal(data, &settings)
	}
	// 设置默认值，确保旧配置文件兼容
	if settings.Theme == "" {
		settings.Theme = "dark"
	}
	if settings.PosterRadius == 0 {
		settings.PosterRadius = 4
	}
	if settings.MinWindowWidth == 0 {
		settings.MinWindowWidth = 1024
	}
	if strings.TrimSpace(settings.EverythingAddr) == "" || strings.EqualFold(strings.TrimSpace(settings.EverythingAddr), "http://127.0.0.1:80") {
		settings.EverythingAddr = "http://127.0.0.1:8077"
	}
	thumbnailDefaults := service.DefaultThumbnailSettings()
	if err != nil || !bytes.Contains(data, []byte(`"enable_video_thumbnail"`)) {
		settings.EnableVideoThumbnail = thumbnailDefaults.Enabled
	}
	if err != nil || !bytes.Contains(data, []byte(`"enable_gfriends_avatars"`)) {
		settings.EnableGfriendsAvatars = true
	}
	if settings.ThumbnailPreviewCount <= 0 {
		settings.ThumbnailPreviewCount = thumbnailDefaults.PreviewCount
	}
	if settings.ThumbnailMinDurationSeconds <= 0 {
		settings.ThumbnailMinDurationSeconds = thumbnailDefaults.MinDurationSeconds
	}
	if strings.TrimSpace(settings.RemoteBindHost) == "" {
		settings.RemoteBindHost = defaultRemoteBindHost
	}
	if settings.JellyfinPort <= 0 {
		settings.JellyfinPort = defaultJellyfinPort
	}
	if strings.TrimSpace(settings.JellyfinServerName) == "" {
		settings.JellyfinServerName = defaultJellyfinServerName
	}
	return &settings, nil
}

func (a *App) UpdateDesktopSettings(settings *DesktopSettings) error {
	if err := a.validateRemoteSettings(settings); err != nil {
		return err
	}

	previous, _ := a.GetDesktopSettings()
	rollback := func(originalErr error) error {
		if previous == nil {
			return originalErr
		}
		if rollbackErr := a.restoreDesktopSettings(previous); rollbackErr != nil {
			return fmt.Errorf("%w; rollback failed: %w", originalErr, rollbackErr)
		}
		return originalErr
	}

	if err := writeDesktopSettingsFile(settingsPath, settings); err != nil {
		return err
	}
	if err := a.syncDesktopIntegration(settings); err != nil {
		return rollback(err)
	}
	if err := a.syncRemoteServices(settings); err != nil {
		return rollback(err)
	}
	return nil
}

func (a *App) restoreDesktopSettings(settings *DesktopSettings) error {
	if settings == nil {
		return nil
	}
	return errors.Join(
		writeDesktopSettingsFile(settingsPath, settings),
		a.syncDesktopIntegration(settings),
		a.syncRemoteServices(settings),
	)
}

func writeDesktopSettingsFile(path string, settings *DesktopSettings) error {
	if settings == nil {
		return fmt.Errorf("settings is nil")
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("settings path is empty")
	}
	dir := filepath.Dir(path)
	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := replaceFile(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func replaceFile(source string, target string) error {
	if err := os.Rename(source, target); err == nil {
		return nil
	} else if _, statErr := os.Stat(target); statErr != nil {
		return err
	}

	backup := fmt.Sprintf("%s.%d.bak", target, time.Now().UnixNano())
	if err := os.Rename(target, backup); err != nil {
		return err
	}
	if err := os.Rename(source, target); err != nil {
		_ = os.Rename(backup, target)
		return err
	}
	_ = os.Remove(backup)
	return nil
}

// RestartApp 尝试重启软件
func (a *App) RestartApp() {
	a.logger.Infof("Software restart requested...")
	// 极简且保守实现
	// 在开发模式下，通常最好手动重启，避免干扰 wails dev 的热重载
	executable, err := os.Executable()
	if err != nil {
		a.logger.Errorf("Failed to get executable: %v", err)
		return
	}

	if err := startReplacementProcess(executable, os.Args[1:]); err != nil {
		a.logger.Errorf("Failed to restart: %v", err)
		return
	}
	os.Exit(0)
}

func startReplacementProcess(executable string, args []string) error {
	if strings.TrimSpace(executable) == "" {
		return fmt.Errorf("empty executable path")
	}
	cmd := exec.Command(executable, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Start()
}

func startDetachedCommand(cmd *exec.Cmd) error {
	if cmd == nil {
		return fmt.Errorf("launch command is nil")
	}
	configureDetachedCommand(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	if cmd.Process != nil {
		_ = cmd.Process.Release()
	}
	return nil
}

func buildOpenFileCommand(filePath string, playerPath string) *exec.Cmd {
	if strings.TrimSpace(playerPath) != "" {
		cmd := exec.Command(playerPath, filePath)
		cmd.Dir = filepath.Dir(filePath)
		return cmd
	}

	switch runtime.GOOS {
	case "windows":
		cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", filepath.Clean(filePath))
		cmd.Dir = filepath.Dir(filePath)
		return cmd
	case "darwin":
		return exec.Command("open", filePath)
	default:
		return exec.Command("xdg-open", filePath)
	}
}

func preferredPlayerPath(settings *DesktopSettings, forceExternal bool) string {
	if settings == nil {
		return ""
	}
	if forceExternal || settings.UseExternalPlayer {
		return strings.TrimSpace(settings.PlayerPath)
	}
	return ""
}

func (a *App) ensureWatched(mediaID string) error {
	if strings.TrimSpace(mediaID) == "" {
		return nil
	}

	var wh model.WatchHistory
	err := a.db.Where("media_id = ? AND user_id = ?", mediaID, desktopUserID).First(&wh).Error
	if err == nil {
		if wh.Completed {
			return a.db.Model(&wh).Update("updated_at", time.Now()).Error
		}
		wh.Completed = true
		return a.db.Save(&wh).Error
	}
	if err != gorm.ErrRecordNotFound {
		return err
	}

	newWh := model.WatchHistory{
		UserID:    desktopUserID,
		MediaID:   mediaID,
		Completed: true,
	}
	return a.db.Create(&newWh).Error
}

func (a *App) markWatchedByFilePath(filePath string) {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return
	}

	var media model.Media
	if err := a.db.Where("file_path = ?", filePath).First(&media).Error; err != nil {
		return
	}
	if err := a.ensureWatched(media.ID); err != nil {
		a.logger.Warnf("mark watched by file path failed: %v", err)
	}
}

// PlayWithExternalPlayer 调用系统原生的外部播放器或配置播放器打开媒体
func (a *App) PlayWithExternalPlayer(mediaID string) error {
	media, err := a.repos.Media.FindByID(mediaID)
	if err != nil {
		return err
	}
	settings, _ := a.GetDesktopSettings()
	if err := startDetachedCommand(buildOpenFileCommand(media.FilePath, preferredPlayerPath(settings, true))); err != nil {
		return err
	}
	if err := a.ensureWatched(media.ID); err != nil {
		a.logger.Warnf("mark watched after external play failed: %v", err)
	}
	return nil
}

// OpenMediaFolder 调用本地系统打开文件所属的原生文件管理器
func (a *App) OpenMediaFolder(mediaID string) error {
	media, err := a.repos.Media.FindByID(mediaID)
	if err != nil {
		return err
	}
	dir := filepath.Dir(media.FilePath)
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer", dir)
	case "darwin":
		cmd = exec.Command("open", dir)
	default:
		cmd = exec.Command("xdg-open", dir)
	}
	return cmd.Start()
}

func (a *App) ToggleFavorite(mediaID string) error {
	var fav model.Favorite
	err := a.db.Where("media_id = ? AND user_id = ?", mediaID, desktopUserID).First(&fav).Error
	if err == nil {
		return a.db.Delete(&fav).Error
	}
	newFav := model.Favorite{
		UserID:  desktopUserID,
		MediaID: mediaID,
	}
	return a.db.Create(&newFav).Error
}

// ToggleWatched 切换已看状态
func (a *App) ToggleWatched(mediaID string) error {
	var wh model.WatchHistory
	err := a.db.Where("media_id = ? AND user_id = ?", mediaID, desktopUserID).First(&wh).Error
	if err == nil {
		wh.Completed = !wh.Completed
		return a.db.Save(&wh).Error
	}
	newWh := model.WatchHistory{
		UserID:    desktopUserID,
		MediaID:   mediaID,
		Completed: true,
	}
	return a.db.Create(&newWh).Error
}

// DeleteMedia 只从数据库删除记录，不移动文件
func (a *App) DeleteMedia(mediaID string) error {
	if a.artworkCache != nil {
		if err := a.artworkCache.RemoveMedia(mediaID); err != nil {
			a.logger.Warnf("remove media artwork cache failed: media=%s err=%v", mediaID, err)
		}
	}
	return a.repos.Media.DeleteByID(mediaID)
}

func (a *App) CleanOrphanedMediaAssociations() (repository.OrphanedMediaCleanupResult, error) {
	if a.repos == nil || a.repos.Media == nil {
		return repository.OrphanedMediaCleanupResult{}, fmt.Errorf("media repository is not initialized")
	}

	result, err := a.repos.Media.CleanOrphanedMediaAssociations()
	if err != nil {
		if a.logger != nil {
			a.logger.Errorf("clean orphaned media associations failed: %v", err)
		}
		return result, err
	}
	if a.logger != nil && result.Total() > 0 {
		a.logger.Infof("cleaned orphaned media associations: %+v", result)
	}
	return result, nil
}

// OpenNFO 尝试找到并打开关联的 .nfo 文件
func (a *App) OpenNFO(mediaID string) error {
	media, err := a.repos.Media.FindByID(mediaID)
	if err != nil {
		return err
	}

	// 1. 同名 nfo
	nfoPath := strings.TrimSuffix(media.FilePath, filepath.Ext(media.FilePath)) + ".nfo"
	if _, err := os.Stat(nfoPath); os.IsNotExist(err) {
		// 2. 目录下的 movie.nfo
		dir := filepath.Dir(media.FilePath)
		nfoPath = filepath.Join(dir, "movie.nfo")
	}

	if _, err := os.Stat(nfoPath); os.IsNotExist(err) {
		return fmt.Errorf("找不到对应的 .nfo 文件")
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", filepath.Clean(nfoPath))
	case "darwin":
		cmd = exec.Command("open", nfoPath)
	default:
		cmd = exec.Command("xdg-open", nfoPath)
	}
	return startDetachedCommand(cmd)
}

func (a *App) loadMediaForNFO(mediaID string) (*model.Media, error) {
	var media model.Media
	if err := a.db.Preload("Series").First(&media, "id = ?", mediaID).Error; err != nil {
		return nil, err
	}
	return &media, nil
}

func (a *App) resolveMediaNFOPath(media *model.Media) string {
	nfoService := service.NewNFOService(a.logger)
	if nfoPath := nfoService.FindNFOForMedia(media.FilePath); nfoPath != "" {
		return nfoPath
	}
	return strings.TrimSuffix(media.FilePath, filepath.Ext(media.FilePath)) + ".nfo"
}

func (a *App) GetNFOEditorData(mediaID string) (*service.NFOEditorData, error) {
	media, err := a.loadMediaForNFO(mediaID)
	if err != nil {
		return nil, err
	}

	nfoService := service.NewNFOService(a.logger)
	return nfoService.LoadEditorData(a.resolveMediaNFOPath(media), media)
}

func (a *App) syncMediaFromNFO(mediaID string, nfoPath string) error {
	media, err := a.loadMediaForNFO(mediaID)
	if err != nil {
		return fmt.Errorf("load media after NFO save: %w", err)
	}

	nfoService := service.NewNFOService(a.logger)
	updated := *media
	if err := nfoService.ParseMovieNFO(nfoPath, &updated); err != nil {
		return fmt.Errorf("parse saved NFO: %w", err)
	}

	var poster, backdrop string
	if a.scanner != nil {
		poster, backdrop = a.scanner.FindLocalArtworkForMedia(media.FilePath)
	} else {
		poster, backdrop = nfoService.FindLocalImages(filepath.Dir(media.FilePath))
	}
	if poster != "" || backdrop != "" {
		if poster != "" {
			updated.PosterPath = poster
		}
		if backdrop != "" {
			updated.BackdropPath = backdrop
		}
	}
	if a.scanner != nil {
		a.scanner.CacheMediaArtworkForMedia(&updated)
	}

	service.ApplyDerivedMediaFields(&updated)

	updates := map[string]interface{}{
		"title":                   updated.Title,
		"orig_title":              updated.OrigTitle,
		"year":                    updated.Year,
		"overview":                updated.Overview,
		"rating":                  updated.Rating,
		"runtime":                 updated.Runtime,
		"genres":                  updated.Genres,
		"studio":                  updated.Studio,
		"maker":                   updated.Maker,
		"label":                   updated.Label,
		"code":                    updated.Code,
		"code_prefix":             updated.CodePrefix,
		"metadata_score":          updated.MetadataScore,
		"poster_path":             updated.PosterPath,
		"backdrop_path":           updated.BackdropPath,
		"nfo_extra_fields":        updated.NfoExtraFields,
		"nfo_raw_xml":             updated.NfoRawXml,
		"nfo_mod_time":            updated.NfoModTime,
		"release_date_normalized": updated.ReleaseDateNormalized,
	}
	if err := a.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Media{}).Where("id = ?", mediaID).Updates(updates).Error; err != nil {
			return fmt.Errorf("update media after NFO save: %w", err)
		}
		if a.scanner != nil {
			if err := a.scanner.SyncActorsForMediaStrictWithDB(&updated, tx); err != nil {
				return fmt.Errorf("sync actors after NFO save: %w", err)
			}
		}
		if err := repository.RefreshMediaSearchIndex(tx, mediaID); err != nil {
			return fmt.Errorf("refresh media search index after NFO save: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}
	return nil
}

func (a *App) SaveNFOEditorData(mediaID string, data *service.NFOEditorData) error {
	media, err := a.loadMediaForNFO(mediaID)
	if err != nil {
		return err
	}
	if data == nil {
		return fmt.Errorf("empty nfo editor data")
	}

	nfoPath := strings.TrimSpace(data.NFOPath)
	if nfoPath == "" {
		nfoPath = a.resolveMediaNFOPath(media)
	}

	nfoService := service.NewNFOService(a.logger)
	if err := nfoService.SaveEditorData(nfoPath, data); err != nil {
		return err
	}

	if err := a.syncMediaFromNFO(mediaID, nfoPath); err != nil {
		return err
	}
	a.bumpRecommendationVersion()
	a.invalidateRecommendationGenres(mediaID)
	return nil
}

// GetMediaFiles 获取当前目录下的所有视频文件
func (a *App) GetMediaFiles(mediaID string) ([]string, error) {
	media, err := a.repos.Media.FindByID(mediaID)
	if err != nil {
		return nil, err
	}
	return a.getMediaFilesByPath(media.FilePath), nil
}

type resolvedActor struct {
	actor          model.MediaActor
	sortOrder      int
	sourcePriority int
	order          int
}

func (a *App) resolveMediaActors(media *model.Media) ([]model.MediaActor, string) {
	resolved := make(map[string]*resolvedActor)
	linkByPersonID := make(map[string]bool)
	orderCounter := 0

	register := func(name, personID string, sortOrder, sourcePriority int) {
		cleaned := normalizeActorName(name)
		if cleaned == "" {
			return
		}

		key := actorKey(cleaned)
		if key == "" {
			return
		}

		if sortOrder < 0 {
			sortOrder = 9999
		}

		if personID != "" {
			linkByPersonID[personID] = true
		}

		existing, ok := resolved[key]
		if !ok {
			resolved[key] = &resolvedActor{
				actor: model.MediaActor{
					ID:   personID,
					Name: cleaned,
				},
				sortOrder:      sortOrder,
				sourcePriority: sourcePriority,
				order:          orderCounter,
			}
			orderCounter++
			return
		}

		if existing.actor.ID == "" && personID != "" {
			existing.actor.ID = personID
		}
		if sortOrder < existing.sortOrder {
			existing.sortOrder = sortOrder
		}
		if sourcePriority < existing.sourcePriority || (sourcePriority == existing.sourcePriority && isBetterActorName(cleaned, existing.actor.Name)) {
			existing.actor.Name = cleaned
			existing.sourcePriority = sourcePriority
		}
	}

	var mediaPeople []model.MediaPerson
	if rows, err := a.repos.MediaPerson.ListByMediaID(media.ID); err == nil {
		mediaPeople = rows
		for _, mediaPerson := range mediaPeople {
			if strings.EqualFold(mediaPerson.Role, "actor") && mediaPerson.PersonID != "" {
				linkByPersonID[mediaPerson.PersonID] = true
			}
		}
	}

	nfoService := service.NewNFOService(a.logger)
	nfoPath := ""
	if a.scanner != nil {
		nfoPath = a.scanner.FindNFOForMedia(media.FilePath)
	} else {
		nfoPath = nfoService.FindNFOForMedia(media.FilePath)
	}
	hasNFOActors := false
	if nfoPath != "" {
		if nfoActors, _, err := nfoService.GetActorsFromNFO(nfoPath); err == nil {
			for idx, nfoActor := range nfoActors {
				sortOrder := nfoActor.SortOrder
				if sortOrder == 0 {
					sortOrder = idx
				}
				before := len(resolved)
				register(nfoActor.Name, "", sortOrder, 0)
				if len(resolved) > before {
					hasNFOActors = true
				}
			}
		}
	}
	if !hasNFOActors {
		for idx, mediaPerson := range mediaPeople {
			if !strings.EqualFold(mediaPerson.Role, "actor") {
				continue
			}
			sortOrder := mediaPerson.SortOrder
			if sortOrder == 0 {
				sortOrder = idx
			}
			register(mediaPerson.Person.Name, mediaPerson.PersonID, sortOrder, 1)
		}
	}

	if len(resolved) == 0 {
		return nil, ""
	}

	items := make([]*resolvedActor, 0, len(resolved))
	for _, item := range resolved {
		if item.actor.ID == "" {
			person, err := a.repos.Person.FindOrCreate(item.actor.Name, 0)
			if err == nil && person != nil {
				item.actor.ID = person.ID
				if !linkByPersonID[person.ID] {
					_ = a.repos.MediaPerson.Create(&model.MediaPerson{
						MediaID:   media.ID,
						PersonID:  person.ID,
						Role:      "actor",
						SortOrder: item.sortOrder,
					})
					linkByPersonID[person.ID] = true
				}
			}
		}
		items = append(items, item)
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].sortOrder != items[j].sortOrder {
			return items[i].sortOrder < items[j].sortOrder
		}
		if items[i].sourcePriority != items[j].sourcePriority {
			return items[i].sourcePriority < items[j].sourcePriority
		}
		return items[i].order < items[j].order
	})

	actors := make([]model.MediaActor, 0, len(items))
	names := make([]string, 0, len(items))
	for _, item := range items {
		actors = append(actors, item.actor)
		names = append(names, item.actor.Name)
	}

	return actors, strings.Join(names, ", ")
}

func normalizeActorName(name string) string {
	name = strings.TrimSpace(name)
	name = regexp.MustCompile(`[?？]+$`).ReplaceAllString(name, "")
	name = regexp.MustCompile(`[\(\[]\d+[\)\]]$`).ReplaceAllString(name, "")

	var builder strings.Builder
	lastWasSpace := false
	for _, r := range name {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			builder.WriteRune(r)
			lastWasSpace = false
		case strings.ContainsRune("-.&'·・", r):
			builder.WriteRune(r)
			lastWasSpace = false
		case unicode.IsSpace(r) || r == '_' || r == '/' || r == '\\':
			if builder.Len() > 0 && !lastWasSpace {
				builder.WriteRune(' ')
				lastWasSpace = true
			}
		}
	}

	cleaned := strings.TrimSpace(builder.String())
	cleaned = strings.Trim(cleaned, "-.&'·・ ")
	if isUnknownActorName(cleaned) {
		return ""
	}
	return cleaned
}

func actorKey(name string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func isUnknownActorName(name string) bool {
	if name == "" {
		return true
	}
	normalized := strings.ToLower(strings.TrimSpace(name))
	switch normalized {
	case "unknown", "unk", "n/a", "na", "none", "演员", "未知", "佚名":
		return true
	}
	return false
}

func actorNameScore(name string) int {
	score := utf8.RuneCountInString(name) * 4
	if strings.Contains(name, " ") {
		score += 2
	}
	if regexp.MustCompile(`[?？_<>\[\]\{\}\|]`).MatchString(name) {
		score -= 12
	}
	if isUnknownActorName(name) {
		score -= 100
	}
	return score
}

func isBetterActorName(candidate, current string) bool {
	return actorNameScore(candidate) > actorNameScore(current)
}

type previewCandidate struct {
	path     string
	priority int
	groupKey string
	order    int
}

var previewImageExts = map[string]bool{".jpg": true, ".png": true, ".jpeg": true, ".webp": true}
var previewVideoExts = map[string]bool{".mp4": true, ".mkv": true, ".avi": true, ".mov": true, ".wmv": true, ".flv": true, ".webm": true, ".m4v": true, ".ts": true, ".strm": true}

func isPreviewImage(path string) bool {
	return previewImageExts[strings.ToLower(filepath.Ext(path))]
}

func countVideoFilesInDirectory(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if previewVideoExts[strings.ToLower(filepath.Ext(entry.Name()))] {
			count++
		}
	}
	return count
}

func normalizePreviewOwnershipStem(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer("_", "-", ".", "-", " ", "-").Replace(value)
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == '-'
	})
	return strings.Join(parts, "-")
}

func previewBelongsToMediaFile(name string, mediaFilePath string, requirePrefix bool) bool {
	if !requirePrefix {
		return true
	}
	mediaStem := normalizePreviewOwnershipStem(strings.TrimSuffix(filepath.Base(mediaFilePath), filepath.Ext(mediaFilePath)))
	imageStem := normalizePreviewOwnershipStem(strings.TrimSuffix(name, filepath.Ext(name)))
	if mediaStem == "" || imageStem == "" {
		return false
	}
	return strings.HasPrefix(imageStem, mediaStem+"-")
}

func previewPriority(name string) (int, bool) {
	lower := strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
	switch {
	case strings.Contains(lower, "thumb"):
		return 2, true
	case strings.Contains(lower, "fanart") || strings.Contains(lower, "backdrop"):
		return 3, true
	case strings.Contains(lower, "poster") || strings.Contains(lower, "cover") || strings.Contains(lower, "folder"):
		return 4, true
	default:
		return 0, false
	}
}

func previewGroupKey(path, rootDir string) string {
	name := strings.ToLower(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	name = strings.NewReplacer("_", "-", ".", "-", " ", "-").Replace(name)
	tokens := strings.FieldsFunc(name, func(r rune) bool {
		return r == '-'
	})

	var kept []string
	removedCategory := false
	for _, token := range tokens {
		switch token {
		case "", "poster", "fanart", "thumb", "cover", "folder", "backdrop", "preview", "landscape", "image", "images":
			if token != "" {
				removedCategory = true
			}
			continue
		default:
			kept = append(kept, token)
		}
	}

	if len(kept) == 0 && removedCategory && filepath.Clean(filepath.Dir(path)) == filepath.Clean(rootDir) {
		return "primary"
	}
	if len(kept) == 0 {
		return name
	}
	return strings.Join(kept, "-")
}

// GetMediaPreviews 收集本地预览图片（由高到低优先级：extrafanart > BTS > thumb > fanart > poster）
func (a *App) GetMediaPreviews(mediaID string) ([]string, error) {
	media, err := a.repos.Media.FindByID(mediaID)
	if err != nil {
		return nil, err
	}
	return a.getMediaPreviews(media), nil
}

// PlayFile 播放指定绝对路径的文件
func (a *App) PlayFile(filePath string) error {
	totalStart := time.Now()
	targetPath := strings.TrimSpace(filePath)
	if targetPath == "" {
		return fmt.Errorf("empty file path")
	}

	settingsStart := time.Now()
	settings, _ := a.GetDesktopSettings()
	settingsCost := time.Since(settingsStart)

	playerPath := preferredPlayerPath(settings, false)
	cmd := buildOpenFileCommand(targetPath, playerPath)

	startCostStart := time.Now()
	err := startDetachedCommand(cmd)
	startCost := time.Since(startCostStart)
	if err != nil {
		appendPlayLatencyLog("FAIL path=%q player=%q settings=%s start=%s total=%s err=%v",
			targetPath, playerPath, settingsCost, startCost, time.Since(totalStart), err)
		return err
	}

	watchedStart := time.Now()
	a.markWatchedByFilePath(targetPath)
	watchedCost := time.Since(watchedStart)

	appendPlayLatencyLog("OK path=%q player=%q settings=%s start=%s mark_watched=%s total=%s",
		targetPath, playerPath, settingsCost, startCost, watchedCost, time.Since(totalStart))

	return nil
}

func appendPlayLatencyLog(format string, args ...interface{}) {
	file, err := os.OpenFile("play_latency.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	defer file.Close()

	line := fmt.Sprintf(format, args...)
	_, _ = fmt.Fprintf(file, "%s %s\n", time.Now().Format(time.RFC3339Nano), line)
}

func (a *App) PlayRandomLibraryMedia(libraryID string) (string, error) {
	var media model.Media
	err := a.db.
		Where("library_id = ? AND file_path <> ''", libraryID).
		Order("RANDOM()").
		Limit(1).
		First(&media).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return "", fmt.Errorf("当前媒体库没有可播放文件")
		}
		return "", err
	}

	if err := a.PlayFile(media.FilePath); err != nil {
		return "", err
	}

	return filepath.Base(media.FilePath), nil
}

type DeleteLibraryResult struct {
	Deleted bool   `json:"deleted"`
	Warning string `json:"warning,omitempty"`
}

// DeleteLibrary 删除一个媒体库及其关联的所有媒体记录
func (a *App) DeleteLibrary(libraryID string) (*DeleteLibraryResult, error) {
	result, err := a.repos.DeleteLibraryAtomic(libraryID)
	if err != nil {
		return nil, err
	}

	var cacheErrs []error
	if a.artworkCache != nil || a.removeMediaCache != nil {
		for _, mediaID := range result.MediaIDs {
			if err := a.removeLibraryMediaCache(mediaID); err != nil {
				a.logger.Warnf("library deleted but artwork cache cleanup failed: media=%s err=%v", mediaID, err)
				cacheErrs = append(cacheErrs, fmt.Errorf("media %s: %w", mediaID, err))
			}
		}
	}
	if len(cacheErrs) > 0 {
		return &DeleteLibraryResult{
			Deleted: true,
			Warning: fmt.Sprintf("cache cleanup incomplete: %v", errors.Join(cacheErrs...)),
		}, nil
	}
	return &DeleteLibraryResult{Deleted: true}, nil
}

func (a *App) removeLibraryMediaCache(mediaID string) error {
	if a.removeMediaCache != nil {
		return a.removeMediaCache(mediaID)
	}
	if a.artworkCache == nil {
		return nil
	}
	return a.artworkCache.RemoveMedia(mediaID)
}

// ScanLibraryWithMode 带刷新模式的扫描入口
// mode: "incremental" | "overwrite" | "delete_update"
func (a *App) ScanLibraryWithMode(libraryID string, mode string) (*ScanTaskInfo, error) {
	lib, err := a.repos.Library.FindByID(libraryID)
	if err != nil {
		return nil, err
	}
	mode = normalizeScanMode(mode)
	a.logger.Infof("ScanLibraryWithMode: id=%s mode=%s", libraryID, mode)
	task, err := a.registerScanTask(lib, mode)
	if err != nil {
		a.scanMu.Lock()
		active := a.activeScans[lib.ID]
		if active != nil {
			info := active.info
			a.scanMu.Unlock()
			if a.logger != nil {
				a.logger.Infof("scan request joined active task: library=%s task=%s requested_mode=%s active_mode=%s", lib.ID, info.TaskID, mode, info.Mode)
			}
			return &info, nil
		}
		a.scanMu.Unlock()
		return nil, err
	}

	options := service.ScanOptions{
		Mode:        mode,
		Incremental: mode == "incremental",
		// Delete/update mode now handles deletes and sidecar changes inside the
		// main sync pass instead of running a separate full-library existence sweep.
		CleanDeleted: false,
	}
	a.startScanWithOptions(lib, task, options)
	info := task.info
	return &info, nil
}

func (a *App) CancelScan(taskID string) error {
	if a == nil {
		return nil
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return fmt.Errorf("task ID is required")
	}
	a.scanMu.Lock()
	task := a.activeScanIDs[taskID]
	if task == nil {
		for _, previous := range a.lastScanTasks {
			if previous.TaskID == taskID {
				a.scanMu.Unlock()
				return nil
			}
		}
		a.scanMu.Unlock()
		return fmt.Errorf("scan task %s is not running", taskID)
	}
	cancel := task.cancel
	a.scanMu.Unlock()
	cancel()
	return nil
}

func (a *App) CancelLibraryScan(libraryID string) error {
	if a == nil {
		return nil
	}
	a.scanMu.Lock()
	task := a.activeScans[strings.TrimSpace(libraryID)]
	a.scanMu.Unlock()
	if task == nil {
		return nil
	}
	return a.CancelScan(task.info.TaskID)
}

func (a *App) GetLastScanTask(libraryID string) *ScanTaskInfo {
	if a == nil {
		return nil
	}
	libraryID = strings.TrimSpace(libraryID)
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	if task := a.activeScans[libraryID]; task != nil {
		info := task.info
		return &info
	}
	info, ok := a.lastScanTasks[libraryID]
	if !ok {
		return nil
	}
	return &info
}

func (a *App) GetLastScanFailure(libraryID string) *ScanTaskInfo {
	if a == nil {
		return nil
	}
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	info, ok := a.lastScanFailures[strings.TrimSpace(libraryID)]
	if !ok {
		return nil
	}
	return &info
}

func (a *App) RetryFailedScan(libraryID string) (*ScanTaskInfo, error) {
	if a == nil {
		return nil, fmt.Errorf("application is unavailable")
	}
	libraryID = strings.TrimSpace(libraryID)
	a.scanMu.Lock()
	failed, ok := a.lastScanFailures[libraryID]
	a.scanMu.Unlock()
	if !ok || !failed.Retryable || (failed.Status != ScanTaskFailed && failed.Status != ScanTaskIncomplete) {
		return nil, fmt.Errorf("library %s has no retryable failed scan", libraryID)
	}
	return a.ScanLibraryWithMode(libraryID, failed.Mode)
}

func (a *App) GetThumbnailFailure(mediaID string) (*service.ThumbnailTaskEventData, error) {
	if a == nil || a.repos == nil || a.repos.Media == nil {
		return nil, fmt.Errorf("media repository is unavailable")
	}
	mediaID = strings.TrimSpace(mediaID)
	if a.thumbnailWorker != nil {
		if failure := a.thumbnailWorker.LastFailure(mediaID); failure != nil {
			return failure, nil
		}
	}
	media, err := a.repos.Media.FindByID(mediaID)
	if err != nil {
		return nil, err
	}
	status := strings.ToLower(strings.TrimSpace(media.ThumbnailStatus))
	if status != service.ThumbnailStatusFailed && status != service.ThumbnailStatusPartial {
		return nil, nil
	}
	return &service.ThumbnailTaskEventData{
		MediaID:   media.ID,
		LibraryID: media.LibraryID,
		Path:      media.FilePath,
		Type:      "thumbnail",
		Status:    status,
		Phase:     "generation",
		Message:   media.ThumbnailError,
		Retryable: true,
	}, nil
}

func (a *App) RetryThumbnailTask(mediaID string) (*service.ThumbnailTaskEventData, error) {
	if a == nil || a.thumbnailWorker == nil || a.repos == nil || a.repos.Media == nil {
		return nil, fmt.Errorf("thumbnail worker is unavailable")
	}
	media, err := a.repos.Media.FindByID(strings.TrimSpace(mediaID))
	if err != nil {
		return nil, err
	}
	return a.thumbnailWorker.Retry(media)
}

// SelectDirectory 调起真正的桌面级弹窗选择媒体库路径
func (a *App) SelectDirectory() (string, error) {
	return wailsRuntime.OpenDirectoryDialog(a.ctx, wailsRuntime.OpenDialogOptions{
		Title: "请选择库文件夹",
	})
}

func (a *App) SelectProgram() (string, error) {
	defaultDirectory := ""
	if settings, err := a.GetDesktopSettings(); err == nil {
		if currentPath := strings.TrimSpace(settings.PlayerPath); currentPath != "" {
			candidateDir := filepath.Dir(currentPath)
			if info, statErr := os.Stat(candidateDir); statErr == nil && info.IsDir() {
				defaultDirectory = candidateDir
			}
		}
	}

	filters := []wailsRuntime.FileFilter{
		{
			DisplayName: "All Files (*.*)",
			Pattern:     "*.*",
		},
	}
	if runtime.GOOS == "windows" {
		filters = append([]wailsRuntime.FileFilter{
			{
				DisplayName: "Programs (*.exe)",
				Pattern:     "*.exe",
			},
		}, filters...)
	}

	return wailsRuntime.OpenFileDialog(a.ctx, wailsRuntime.OpenDialogOptions{
		Title:            "Select Player Program",
		DefaultDirectory: defaultDirectory,
		Filters:          filters,
	})
}
