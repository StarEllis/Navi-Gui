package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"navi-desktop/model"
	"navi-desktop/repository"
)

const (
	defaultThumbnailWorkerPollInterval = 5 * time.Second
	defaultThumbnailWorkerBatchSize    = 10
	defaultThumbnailWorkerLockTimeout  = 10 * time.Minute
	thumbnailRetryPromotionInterval    = time.Minute
	thumbnailTaskType                  = "thumbnail"
)

type ThumbnailWorker struct {
	mediaRepo    *repository.MediaRepo
	thumbSvc     *ThumbnailService
	settingsFn   ThumbnailSettingsProvider
	logger       *zap.SugaredLogger
	wsHub        eventBroadcaster
	workerID     string
	pollInterval time.Duration
	batchSize    int
	lockTimeout  time.Duration

	ctx                   context.Context
	cancel                context.CancelFunc
	wakeCh                chan struct{}
	wg                    sync.WaitGroup
	mu                    sync.Mutex
	started               bool
	stopped               bool
	running               map[string]string
	queued                map[string]string
	retryReservations     map[string]thumbnailRetryReservation
	retryTerminals        map[string]bool
	failures              map[string]ThumbnailTaskEventData
	retryThumbnailTaskFn  func(string) (bool, error)
	wakeRetryWorkerFn     func() bool
	executeGenerationFunc func(context.Context, *model.Media, *directorySidecarFiles, ThumbnailSettings) (string, string)
}

type thumbnailRetryReservation struct {
	taskID string
	media  model.Media
	phase  string
}

func NewThumbnailWorker(mediaRepo *repository.MediaRepo, thumbSvc *ThumbnailService, settingsFn ThumbnailSettingsProvider, logger *zap.SugaredLogger, wsHub *WSHub) *ThumbnailWorker {
	if logger == nil {
		base, _ := zap.NewDevelopment()
		logger = base.Sugar()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &ThumbnailWorker{
		mediaRepo:         mediaRepo,
		thumbSvc:          thumbSvc,
		settingsFn:        settingsFn,
		logger:            logger,
		wsHub:             wsHub,
		workerID:          fmt.Sprintf("thumb-worker-%d", time.Now().UnixNano()),
		pollInterval:      defaultThumbnailWorkerPollInterval,
		batchSize:         defaultThumbnailWorkerBatchSize,
		lockTimeout:       defaultThumbnailWorkerLockTimeout,
		ctx:               ctx,
		cancel:            cancel,
		wakeCh:            make(chan struct{}, 1),
		running:           make(map[string]string),
		queued:            make(map[string]string),
		retryReservations: make(map[string]thumbnailRetryReservation),
		retryTerminals:    make(map[string]bool),
		failures:          make(map[string]ThumbnailTaskEventData),
	}
}

func (w *ThumbnailWorker) Start() {
	if w == nil || w.mediaRepo == nil || w.thumbSvc == nil {
		return
	}
	w.mu.Lock()
	if w.started || w.stopped {
		w.mu.Unlock()
		return
	}
	w.started = true
	w.wg.Add(1)
	w.mu.Unlock()
	go func() {
		defer w.wg.Done()
		w.loop()
	}()
}

func (w *ThumbnailWorker) Stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	var reservations []thumbnailRetryReservation
	if !w.stopped {
		w.stopped = true
		w.cancel()
		for key, reservation := range w.retryReservations {
			reservations = append(reservations, reservation)
			delete(w.retryReservations, key)
			delete(w.queued, key)
		}
	}
	w.mu.Unlock()
	for i := range reservations {
		w.cancelRetryReservation(&reservations[i], "thumbnail worker stopped before retry started")
	}
}

func (w *ThumbnailWorker) Shutdown(ctx context.Context) error {
	if w == nil {
		return nil
	}
	w.Stop()
	if ctx == nil {
		ctx = context.Background()
	}
	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		if w.thumbSvc != nil {
			w.thumbSvc.Shutdown()
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *ThumbnailWorker) Wake() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	stopped := w.stopped
	w.mu.Unlock()
	if stopped {
		return false
	}
	select {
	case w.wakeCh <- struct{}{}:
	default:
	}
	return true
}

func (w *ThumbnailWorker) loop() {
	pollTicker := time.NewTicker(w.pollInterval)
	retryTicker := time.NewTicker(thumbnailRetryPromotionInterval)
	defer pollTicker.Stop()
	defer retryTicker.Stop()

	w.promoteFailedTasks()
	w.processBatch(w.ctx)
	for {
		select {
		case <-w.ctx.Done():
			return
		case <-w.wakeCh:
			w.processBatch(w.ctx)
		case <-retryTicker.C:
			w.promoteFailedTasks()
		case <-pollTicker.C:
			w.processBatch(w.ctx)
		}
	}
}

func (w *ThumbnailWorker) promoteFailedTasks() {
	if w == nil || w.mediaRepo == nil || w.ctx.Err() != nil {
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

func (w *ThumbnailWorker) processBatch(ctx context.Context) {
	if w == nil || w.mediaRepo == nil || ctx.Err() != nil {
		return
	}
	tasks, err := w.mediaRepo.FindRunnableThumbnailTasks(w.batchSize, w.lockTimeout)
	if err != nil {
		w.logger.Warnf("find runnable thumbnail tasks failed: %v", err)
		return
	}
	for i := range tasks {
		if ctx.Err() != nil {
			return
		}
		key := thumbnailTaskKey(&tasks[i])
		w.mu.Lock()
		_, reserved := w.retryReservations[key]
		w.mu.Unlock()
		if !reserved {
			w.processTask(ctx, &tasks[i])
		}
	}
}

func thumbnailTaskKey(media *model.Media) string {
	if media == nil {
		return ""
	}
	return strings.Join([]string{
		strings.TrimSpace(media.ID),
		nfoPathKey(media.FilePath),
		thumbnailTaskType,
	}, "|")
}

func (w *ThumbnailWorker) beginTask(media *model.Media) (string, bool, bool) {
	key := thumbnailTaskKey(media)
	if key == "" {
		return "", false, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return "", false, false
	}
	if _, exists := w.running[key]; exists {
		return "", false, false
	}
	if reservation, exists := w.retryReservations[key]; exists && reservation.phase == "reserved" {
		return "", false, false
	}
	taskID, queued := w.queued[key]
	delete(w.queued, key)
	delete(w.retryReservations, key)
	if taskID == "" {
		taskID = uuid.NewString()
	}
	w.running[key] = taskID
	return taskID, queued, true
}

func (w *ThumbnailWorker) releaseTask(media *model.Media) {
	key := thumbnailTaskKey(media)
	w.mu.Lock()
	delete(w.running, key)
	w.mu.Unlock()
}

func (w *ThumbnailWorker) processTask(ctx context.Context, task *model.Media) {
	if w == nil || task == nil || strings.TrimSpace(task.ID) == "" || ctx.Err() != nil {
		return
	}
	taskID, pendingAlreadySent, accepted := w.beginTask(task)
	if !accepted {
		return
	}
	defer w.releaseTask(task)

	locked, err := w.mediaRepo.LockThumbnailTask(task.ID, w.workerID, w.lockTimeout)
	if err != nil {
		w.logger.Warnf("lock thumbnail task failed: media=%s err=%v", task.ID, err)
		return
	}
	if !locked {
		return
	}
	if !pendingAlreadySent {
		w.broadcastThumbnailEvent(EventThumbnailPending, taskID, task, ThumbnailStatusPending, "queued", "", false)
	}
	w.broadcastThumbnailEvent(EventThumbnailRunning, taskID, task, ThumbnailStatusProcessing, "generation", "", false)

	if err := ctx.Err(); err != nil {
		w.finishTask(taskID, task, ThumbnailStatusCanceled, "", err)
		return
	}
	media, err := w.mediaRepo.FindByID(task.ID)
	if err != nil || media == nil {
		if err == nil {
			err = fmt.Errorf("media no longer exists")
		}
		w.finishTask(taskID, task, ThumbnailStatusCanceled, "", err)
		return
	}
	if !samePath(task.FilePath, media.FilePath) {
		w.finishTask(taskID, media, ThumbnailStatusCanceled, "", fmt.Errorf("media path changed before thumbnail generation"))
		return
	}

	info, statErr := os.Stat(media.FilePath)
	if statErr != nil || info.IsDir() {
		w.finishTask(taskID, media, ThumbnailStatusCanceled, "", statErr)
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
	if !ShouldWorkerProcess(&model.Media{ThumbnailStatus: status}) {
		w.finishTask(taskID, media, status, currentFingerprint, nil)
		return
	}
	if w.checkAndMarkExisting(media, sidecars, settings) {
		w.finishTask(taskID, media, ThumbnailStatusGenerated, currentFingerprint, nil)
		return
	}

	finalStatus, errMsg := w.executeGeneration(ctx, media, sidecars, settings)
	if ctxErr := ctx.Err(); ctxErr != nil {
		w.finishTask(taskID, media, ThumbnailStatusCanceled, currentFingerprint, ctxErr)
		return
	}
	switch finalStatus {
	case ThumbnailStatusGenerated:
		w.finishTask(taskID, media, ThumbnailStatusGenerated, currentFingerprint, nil)
	case ThumbnailStatusPartial:
		w.finishTask(taskID, media, ThumbnailStatusPartial, currentFingerprint, errors.New(strings.TrimSpace(errMsg)))
	default:
		if strings.TrimSpace(errMsg) == "" {
			errMsg = "thumbnail generation failed"
		}
		w.finishTask(taskID, media, ThumbnailStatusFailed, currentFingerprint, errors.New(errMsg))
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

func (w *ThumbnailWorker) executeGeneration(ctx context.Context, media *model.Media, sidecars *directorySidecarFiles, settings ThumbnailSettings) (string, string) {
	if w == nil || w.thumbSvc == nil || media == nil {
		return ThumbnailStatusFailed, "thumbnail service unavailable"
	}
	if w.executeGenerationFunc != nil {
		return w.executeGenerationFunc(ctx, media, sidecars, settings)
	}
	var warnings []string
	if _, err := w.thumbSvc.EnsurePrimaryArtworkContext(ctx, media, sidecars, settings); err != nil {
		if errors.Is(err, context.Canceled) {
			return ThumbnailStatusCanceled, err.Error()
		}
		warnings = append(warnings, err.Error())
	}
	w.thumbSvc.syncGeneratedArtworkPaths(media)
	if _, err := w.thumbSvc.GeneratePreviewsContext(ctx, media, sidecars, settings); err != nil {
		if errors.Is(err, context.Canceled) {
			return ThumbnailStatusCanceled, err.Error()
		}
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

func (w *ThumbnailWorker) finishTask(taskID string, media *model.Media, status string, fingerprint string, taskErr error) {
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
	if taskErr != nil && (status == ThumbnailStatusFailed || status == ThumbnailStatusPartial) {
		retryCount := media.ThumbnailRetryCount + 1
		nextAttempt := now.Add(retryDelay(media.ThumbnailRetryCount))
		updates["thumbnail_retry_count"] = retryCount
		updates["thumbnail_next_attempt"] = &nextAttempt
		updates["thumbnail_error"] = strings.TrimSpace(taskErr.Error())
	} else {
		updates["thumbnail_retry_count"] = 0
		updates["thumbnail_next_attempt"] = nil
		updates["thumbnail_error"] = ""
	}
	if updateErr := w.mediaRepo.UpdateThumbnailStatus(media.ID, updates); updateErr != nil {
		w.logger.Warnf("update thumbnail task status failed: media=%s err=%v", media.ID, updateErr)
		return
	}

	eventType := EventThumbnailCompleted
	retryable := false
	message := ""
	switch status {
	case ThumbnailStatusCanceled:
		eventType = EventThumbnailCanceled
		if taskErr != nil {
			message = taskErr.Error()
		}
	case ThumbnailStatusFailed, ThumbnailStatusPartial:
		eventType = EventThumbnailFailed
		retryable = true
		if taskErr != nil {
			message = taskErr.Error()
		}
	}
	w.mu.Lock()
	if eventType == EventThumbnailFailed {
		w.failures[media.ID] = ThumbnailTaskEventData{
			TaskID:    taskID,
			MediaID:   media.ID,
			LibraryID: media.LibraryID,
			Path:      media.FilePath,
			Type:      thumbnailTaskType,
			Status:    status,
			Phase:     "generation",
			Message:   message,
			Retryable: true,
		}
	} else if eventType == EventThumbnailCompleted {
		delete(w.failures, media.ID)
	}
	w.mu.Unlock()
	w.broadcastThumbnailEvent(eventType, taskID, media, status, "generation", message, retryable)
	w.broadcastUpdate(media, status)
}

func (w *ThumbnailWorker) Retry(media *model.Media) (*ThumbnailTaskEventData, error) {
	if w == nil || w.mediaRepo == nil || media == nil || strings.TrimSpace(media.ID) == "" {
		return nil, fmt.Errorf("media is required")
	}
	if info, err := os.Stat(media.FilePath); err != nil || info.IsDir() {
		if err == nil {
			err = fmt.Errorf("media path is not a file")
		}
		return nil, err
	}
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return nil, fmt.Errorf("thumbnail worker is stopped")
	}
	key := thumbnailTaskKey(media)
	if _, running := w.running[key]; running {
		w.mu.Unlock()
		return nil, fmt.Errorf("thumbnail task is already running")
	}
	if _, queued := w.queued[key]; queued {
		w.mu.Unlock()
		return nil, fmt.Errorf("thumbnail task is already pending")
	}
	if _, reserved := w.retryReservations[key]; reserved {
		w.mu.Unlock()
		return nil, fmt.Errorf("thumbnail task retry is already reserved")
	}
	taskID := uuid.NewString()
	reservation := thumbnailRetryReservation{taskID: taskID, media: *media, phase: "reserved"}
	w.retryReservations[key] = reservation
	w.mu.Unlock()

	retried, err := w.retryThumbnailTask(media.ID)
	if err != nil {
		w.releaseRetryReservation(key, taskID)
		return nil, err
	}
	if !retried {
		w.releaseRetryReservation(key, taskID)
		return nil, fmt.Errorf("thumbnail task is not retryable")
	}
	w.mu.Lock()
	if w.stopped {
		delete(w.retryReservations, key)
		w.mu.Unlock()
		w.cancelRetryReservation(&reservation, "thumbnail worker stopped during retry")
		return nil, fmt.Errorf("thumbnail worker is stopped")
	}
	current, reserved := w.retryReservations[key]
	if !reserved || current.taskID != taskID {
		w.mu.Unlock()
		w.cancelRetryReservation(&reservation, "thumbnail retry reservation was lost")
		return nil, fmt.Errorf("thumbnail retry reservation was lost")
	}
	current.phase = "queued"
	w.retryReservations[key] = current
	w.queued[key] = taskID
	delete(w.failures, media.ID)
	w.mu.Unlock()
	event := &ThumbnailTaskEventData{
		TaskID:    taskID,
		MediaID:   media.ID,
		LibraryID: media.LibraryID,
		Path:      media.FilePath,
		Type:      thumbnailTaskType,
		Status:    ThumbnailStatusPending,
		Phase:     "queued",
	}
	if w.wsHub != nil {
		w.wsHub.BroadcastEvent(EventThumbnailPending, event)
	}
	if !w.wakeRetryWorker() {
		w.releaseRetryReservation(key, taskID)
		w.cancelRetryReservation(&reservation, "thumbnail retry queue is unavailable")
		return nil, fmt.Errorf("thumbnail retry queue is unavailable")
	}
	return event, nil
}

func (w *ThumbnailWorker) retryThumbnailTask(mediaID string) (bool, error) {
	if w.retryThumbnailTaskFn != nil {
		return w.retryThumbnailTaskFn(mediaID)
	}
	return w.mediaRepo.RetryThumbnailTask(mediaID)
}

func (w *ThumbnailWorker) wakeRetryWorker() bool {
	if w.wakeRetryWorkerFn != nil {
		return w.wakeRetryWorkerFn()
	}
	return w.Wake()
}

func (w *ThumbnailWorker) releaseRetryReservation(key, taskID string) {
	w.mu.Lock()
	if reservation, ok := w.retryReservations[key]; ok && reservation.taskID == taskID {
		delete(w.retryReservations, key)
		delete(w.queued, key)
	}
	w.mu.Unlock()
}

func (w *ThumbnailWorker) cancelRetryReservation(reservation *thumbnailRetryReservation, message string) {
	if w == nil || w.mediaRepo == nil || reservation == nil {
		return
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := w.mediaRepo.UpdateThumbnailStatus(reservation.media.ID, map[string]interface{}{
		"thumbnail_status":       ThumbnailStatusCanceled,
		"thumbnail_locked_at":    nil,
		"thumbnail_locked_by":    "",
		"thumbnail_next_attempt": nil,
		"thumbnail_error":        message,
		"thumbnail_updated_at":   &now,
	}); err != nil {
		w.logger.Warnf("cancel thumbnail retry reservation failed: media=%s err=%v", reservation.media.ID, err)
		return
	}
	w.mu.Lock()
	alreadyTerminal := w.retryTerminals[reservation.taskID]
	w.retryTerminals[reservation.taskID] = true
	w.mu.Unlock()
	if alreadyTerminal {
		return
	}
	w.broadcastThumbnailEvent(EventThumbnailCanceled, reservation.taskID, &reservation.media, ThumbnailStatusCanceled, "queued", message, false)
}

func (w *ThumbnailWorker) LastFailure(mediaID string) *ThumbnailTaskEventData {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	event, ok := w.failures[strings.TrimSpace(mediaID)]
	if !ok {
		return nil
	}
	copy := event
	return &copy
}

func (w *ThumbnailWorker) broadcastThumbnailEvent(eventType string, taskID string, media *model.Media, status string, phase string, message string, retryable bool) {
	if w == nil || w.wsHub == nil || media == nil {
		return
	}
	w.wsHub.BroadcastEvent(eventType, &ThumbnailTaskEventData{
		TaskID:    taskID,
		MediaID:   media.ID,
		LibraryID: media.LibraryID,
		Path:      media.FilePath,
		Type:      thumbnailTaskType,
		Status:    status,
		Phase:     phase,
		Message:   message,
		Retryable: retryable,
	})
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
