package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
	"navi-desktop/model"
	"navi-desktop/repository"
)

const (
	defaultThumbnailWorkerPollInterval = 5 * time.Second
	defaultThumbnailWorkerBatchSize    = 10
	defaultThumbnailWorkerLockTimeout  = 10 * time.Minute
	thumbnailRetryPromotionInterval    = time.Minute
)

type ThumbnailWorker struct {
	mediaRepo    *repository.MediaRepo
	thumbSvc     *ThumbnailService
	settingsFn   ThumbnailSettingsProvider
	logger       *zap.SugaredLogger
	wsHub        *WSHub
	workerID     string
	pollInterval time.Duration
	batchSize    int
	lockTimeout  time.Duration
	stopCh       chan struct{}
}

func NewThumbnailWorker(mediaRepo *repository.MediaRepo, thumbSvc *ThumbnailService, settingsFn ThumbnailSettingsProvider, logger *zap.SugaredLogger, wsHub *WSHub) *ThumbnailWorker {
	if logger == nil {
		base, _ := zap.NewDevelopment()
		logger = base.Sugar()
	}
	return &ThumbnailWorker{
		mediaRepo:    mediaRepo,
		thumbSvc:     thumbSvc,
		settingsFn:   settingsFn,
		logger:       logger,
		wsHub:        wsHub,
		workerID:     fmt.Sprintf("thumb-worker-%d", time.Now().UnixNano()),
		pollInterval: defaultThumbnailWorkerPollInterval,
		batchSize:    defaultThumbnailWorkerBatchSize,
		lockTimeout:  defaultThumbnailWorkerLockTimeout,
		stopCh:       make(chan struct{}),
	}
}

func (w *ThumbnailWorker) Start() {
	if w == nil || w.mediaRepo == nil || w.thumbSvc == nil {
		return
	}
	go w.loop()
}

func (w *ThumbnailWorker) Stop() {
	if w == nil {
		return
	}
	select {
	case <-w.stopCh:
	default:
		close(w.stopCh)
	}
}

func (w *ThumbnailWorker) loop() {
	pollTicker := time.NewTicker(w.pollInterval)
	retryTicker := time.NewTicker(thumbnailRetryPromotionInterval)
	defer pollTicker.Stop()
	defer retryTicker.Stop()

	w.promoteFailedTasks()
	w.processBatch()

	for {
		select {
		case <-w.stopCh:
			return
		case <-retryTicker.C:
			w.promoteFailedTasks()
		case <-pollTicker.C:
			w.processBatch()
		}
	}
}

func (w *ThumbnailWorker) promoteFailedTasks() {
	if w == nil || w.mediaRepo == nil {
		return
	}

	recovered, recoverErr := w.mediaRepo.RecoverStalledThumbnailTasks(w.lockTimeout)
	if recoverErr != nil {
		w.logger.Warnf("recover stalled thumbnail tasks failed: %v", recoverErr)
	} else if recovered > 0 {
		w.logger.Debugf("recovered stalled thumbnail tasks: %d", recovered)
	}

	count, err := w.mediaRepo.PromoteFailedThumbnailTasks()
	if err != nil {
		w.logger.Warnf("promote failed thumbnail tasks failed: %v", err)
		return
	}
	if count > 0 {
		w.logger.Debugf("promoted thumbnail tasks back to pending: %d", count)
	}
}

func (w *ThumbnailWorker) processBatch() {
	if w == nil || w.mediaRepo == nil {
		return
	}
	tasks, err := w.mediaRepo.FindRunnableThumbnailTasks(w.batchSize, w.lockTimeout)
	if err != nil {
		w.logger.Warnf("find runnable thumbnail tasks failed: %v", err)
		return
	}
	for i := range tasks {
		w.processTask(&tasks[i])
	}
}

func (w *ThumbnailWorker) processTask(task *model.Media) {
	if w == nil || task == nil || strings.TrimSpace(task.ID) == "" {
		return
	}

	locked, err := w.mediaRepo.LockThumbnailTask(task.ID, w.workerID, w.lockTimeout)
	if err != nil {
		w.logger.Warnf("lock thumbnail task failed: media=%s err=%v", task.ID, err)
		return
	}
	if !locked {
		return
	}

	media, err := w.mediaRepo.FindByID(task.ID)
	if err != nil || media == nil {
		w.logger.Warnf("reload thumbnail task media failed: media=%s err=%v", task.ID, err)
		return
	}

	info, statErr := os.Stat(media.FilePath)
	if statErr != nil || info.IsDir() {
		w.finishTask(media, ThumbnailStatusNone, "", statErr)
		return
	}

	applyFileTimes(media, info)
	sidecars := collectDirectorySidecarFiles(filepath.Dir(media.FilePath))
	if w.thumbSvc.syncPrimaryArtworkPaths(media, sidecars) {
		w.thumbSvc.syncGeneratedArtworkPaths(media)
	}

	settings := w.settings()
	resolveMedia := *media
	if normalizeThumbnailStatus(resolveMedia.ThumbnailStatus) == ThumbnailStatusProcessing {
		resolveMedia.ThumbnailStatus = task.ThumbnailStatus
	}
	status := w.thumbSvc.resolveThumbnailState(&resolveMedia, sidecars, settings)
	currentFingerprint := CurrentThumbnailFingerprint(media)

	if !ShouldWorkerProcess(&model.Media{
		ThumbnailStatus: status,
	}) {
		w.finishTask(media, status, currentFingerprint, nil)
		return
	}

	if w.checkAndMarkExisting(media, sidecars, settings) {
		w.finishTask(media, ThumbnailStatusGenerated, currentFingerprint, nil)
		return
	}

	finalStatus, errMsg := w.executeGeneration(media, sidecars, settings)
	switch finalStatus {
	case ThumbnailStatusGenerated:
		w.finishTask(media, ThumbnailStatusGenerated, currentFingerprint, nil)
	case ThumbnailStatusPartial:
		w.finishTask(media, ThumbnailStatusPartial, currentFingerprint, fmt.Errorf("%s", strings.TrimSpace(errMsg)))
	default:
		if strings.TrimSpace(errMsg) == "" {
			errMsg = "thumbnail generation failed"
		}
		w.finishTask(media, ThumbnailStatusFailed, currentFingerprint, fmt.Errorf("%s", errMsg))
	}
}

func (w *ThumbnailWorker) checkAndMarkExisting(media *model.Media, sidecars *directorySidecarFiles, settings ThumbnailSettings) bool {
	if w == nil || media == nil {
		return false
	}
	if w.thumbSvc != nil {
		_ = w.thumbSvc.syncPrimaryArtworkPaths(media, sidecars)
		w.thumbSvc.syncGeneratedArtworkPaths(media)
	} else {
		syncGeneratedArtworkPaths(media)
	}
	return HasAllThumbnailAssets(media, sidecars, settings)
}

func (w *ThumbnailWorker) executeGeneration(media *model.Media, sidecars *directorySidecarFiles, settings ThumbnailSettings) (string, string) {
	if w == nil || w.thumbSvc == nil || media == nil {
		return ThumbnailStatusFailed, "thumbnail service unavailable"
	}

	var warnings []string
	if _, err := w.thumbSvc.EnsurePrimaryArtwork(media, sidecars, settings); err != nil {
		warnings = append(warnings, err.Error())
	}
	w.thumbSvc.syncGeneratedArtworkPaths(media)

	if _, err := w.thumbSvc.GeneratePreviews(media, sidecars, settings); err != nil {
		warnings = append(warnings, err.Error())
	}

	if HasAllThumbnailAssets(media, sidecars, settings) {
		return ThumbnailStatusGenerated, strings.Join(warnings, "; ")
	}
	if HasThumbnailArtwork(media, sidecars) || w.thumbSvc.countThumbnailPreviewImages(media, sidecars) > 0 {
		return ThumbnailStatusPartial, strings.Join(warnings, "; ")
	}
	return ThumbnailStatusFailed, strings.Join(warnings, "; ")
}

func (w *ThumbnailWorker) settings() ThumbnailSettings {
	if w != nil && w.settingsFn != nil {
		return normalizeThumbnailSettings(w.settingsFn())
	}
	return DefaultThumbnailSettings()
}

func (w *ThumbnailWorker) finishTask(media *model.Media, status string, fingerprint string, err error) {
	if w == nil || w.mediaRepo == nil || media == nil {
		return
	}

	now := time.Now().UTC().Truncate(time.Second)
	status = normalizeThumbnailStatus(status)

	updates := map[string]interface{}{
		"thumbnail_status":      status,
		"thumbnail_fingerprint": fingerprint,
		"thumbnail_locked_at":   nil,
		"thumbnail_locked_by":   "",
		"thumbnail_updated_at":  &now,
		"poster_path":           media.PosterPath,
		"backdrop_path":         media.BackdropPath,
	}

	if err != nil && (status == ThumbnailStatusFailed || status == ThumbnailStatusPartial) {
		retryCount := media.ThumbnailRetryCount + 1
		nextAttempt := now.Add(retryDelay(media.ThumbnailRetryCount))
		updates["thumbnail_retry_count"] = retryCount
		updates["thumbnail_next_attempt"] = &nextAttempt
		updates["thumbnail_error"] = strings.TrimSpace(err.Error())
	} else {
		updates["thumbnail_retry_count"] = 0
		updates["thumbnail_next_attempt"] = nil
		updates["thumbnail_error"] = ""
	}

	if updateErr := w.mediaRepo.UpdateThumbnailStatus(media.ID, updates); updateErr != nil {
		w.logger.Warnf("update thumbnail task status failed: media=%s err=%v", media.ID, updateErr)
		return
	}

	w.broadcastUpdate(media, status)
}

func (w *ThumbnailWorker) broadcastUpdate(media *model.Media, status string) {
	if w == nil || w.wsHub == nil || media == nil {
		return
	}
	w.wsHub.BroadcastEvent(EventMediaMetadataUpdated, &MediaMetadataEventData{
		MediaID:       media.ID,
		LibraryID:     media.LibraryID,
		MetadataPhase: NormalizeMetadataPhase(media.MetadataPhase),
		Message:       fmt.Sprintf("thumbnail %s", status),
	})
}

func retryDelay(retryCount int) time.Duration {
	switch retryCount {
	case 0:
		return 5 * time.Minute
	case 1:
		return 30 * time.Minute
	case 2:
		return 2 * time.Hour
	case 3:
		return 6 * time.Hour
	default:
		return 24 * time.Hour
	}
}
