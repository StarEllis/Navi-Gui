package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"navi-desktop/config"
	"navi-desktop/model"
	"navi-desktop/repository"
)

type thumbnailWorkerFixture struct {
	db     *gorm.DB
	repo   *repository.MediaRepo
	worker *ThumbnailWorker
	root   string
}

type thumbnailEventRecorder struct {
	mu     sync.Mutex
	events map[string][]ThumbnailTaskEventData
}

func (r *thumbnailEventRecorder) BroadcastEvent(eventType string, data interface{}) {
	event, ok := data.(*ThumbnailTaskEventData)
	if !ok || event == nil {
		return
	}
	r.mu.Lock()
	r.events[eventType] = append(r.events[eventType], *event)
	r.mu.Unlock()
}

func (r *thumbnailEventRecorder) terminalCount(taskID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, eventType := range []string{EventThumbnailCompleted, EventThumbnailFailed, EventThumbnailCanceled} {
		for _, event := range r.events[eventType] {
			if event.TaskID == taskID {
				count++
			}
		}
	}
	return count
}

func newThumbnailWorkerFixture(t *testing.T) *thumbnailWorkerFixture {
	t.Helper()
	dbName := "thumbnail_" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", dbName)), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := model.AutoMigrate(db); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	repos := repository.NewRepositories(db)
	logger := zap.NewNop().Sugar()
	thumbSvc := NewThumbnailService(config.NewConfig(), logger)
	worker := NewThumbnailWorker(repos.Media, thumbSvc, func() ThumbnailSettings {
		return ThumbnailSettings{Enabled: true, PreviewCount: 1, MinDurationSeconds: 1}
	}, logger, nil)
	worker.pollInterval = time.Hour
	return &thumbnailWorkerFixture{
		db:     db,
		repo:   repos.Media,
		worker: worker,
		root:   t.TempDir(),
	}
}

func (f *thumbnailWorkerFixture) addMedia(t *testing.T, id string, status string) *model.Media {
	t.Helper()
	path := filepath.Join(f.root, id+".mp4")
	if err := os.WriteFile(path, []byte("video"), 0o600); err != nil {
		t.Fatalf("write media: %v", err)
	}
	media := &model.Media{
		ID:              id,
		LibraryID:       "thumbnail-library",
		Title:           id,
		FilePath:        path,
		FileSize:        5,
		MediaType:       "movie",
		Duration:        3600,
		ThumbnailStatus: status,
	}
	if err := f.db.Create(media).Error; err != nil {
		t.Fatalf("create media: %v", err)
	}
	return media
}

func TestThumbnailWorkerDeduplicatesRunningTask(t *testing.T) {
	fixture := newThumbnailWorkerFixture(t)
	media := fixture.addMedia(t, "dedupe", ThumbnailStatusPending)
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var calls atomic.Int32
	fixture.worker.executeGenerationFunc = func(context.Context, *model.Media, *directorySidecarFiles, ThumbnailSettings) (string, string) {
		calls.Add(1)
		once.Do(func() { close(started) })
		<-release
		return ThumbnailStatusGenerated, ""
	}

	firstDone := make(chan struct{})
	go func() {
		fixture.worker.processTask(context.Background(), media)
		close(firstDone)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first thumbnail task did not start")
	}
	secondDone := make(chan struct{})
	go func() {
		fixture.worker.processTask(context.Background(), media)
		close(secondDone)
	}()
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("duplicate thumbnail task did not return promptly")
	}
	close(release)
	<-firstDone
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected one generation, got %d", got)
	}
}

func TestThumbnailWorkerBatchSizeBoundsWork(t *testing.T) {
	fixture := newThumbnailWorkerFixture(t)
	fixture.worker.batchSize = 3
	for i := 0; i < 8; i++ {
		fixture.addMedia(t, fmt.Sprintf("batch-%02d", i), ThumbnailStatusPending)
	}
	var calls atomic.Int32
	fixture.worker.executeGenerationFunc = func(context.Context, *model.Media, *directorySidecarFiles, ThumbnailSettings) (string, string) {
		calls.Add(1)
		return ThumbnailStatusGenerated, ""
	}
	fixture.worker.processBatch(context.Background())
	if got := calls.Load(); got != 3 {
		t.Fatalf("expected bounded batch of 3, got %d", got)
	}
	var pending int64
	if err := fixture.db.Model(&model.Media{}).Where("thumbnail_status = ?", ThumbnailStatusPending).Count(&pending).Error; err != nil {
		t.Fatalf("count pending tasks: %v", err)
	}
	if pending != 5 {
		t.Fatalf("expected five tasks to remain pending, got %d", pending)
	}
}

func TestThumbnailCancellationCleansPartialFile(t *testing.T) {
	service := NewThumbnailService(config.NewConfig(), zap.NewNop().Sugar())
	started := make(chan struct{})
	service.captureFrameRunner = func(ctx context.Context, _ string, tempPath string, _ float64, _ int) error {
		if err := os.WriteFile(tempPath, []byte("partial"), 0o600); err != nil {
			return err
		}
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	output := filepath.Join(t.TempDir(), "frame.jpg")
	done := make(chan error, 1)
	go func() {
		done <- service.captureFrameContext(ctx, "movie.mp4", 10, output, 720)
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled capture, got %v", err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("partial output survived cancellation: %v", err)
	}
	parts, err := filepath.Glob(filepath.Join(filepath.Dir(output), "frame.part-*.jpg"))
	if err != nil {
		t.Fatalf("glob temp files: %v", err)
	}
	if len(parts) != 0 {
		t.Fatalf("temporary files survived cancellation: %v", parts)
	}
}

func TestThumbnailFailureCanBeRetriedWithNewTaskID(t *testing.T) {
	fixture := newThumbnailWorkerFixture(t)
	media := fixture.addMedia(t, "retry", ThumbnailStatusPending)
	fixture.worker.executeGenerationFunc = func(context.Context, *model.Media, *directorySidecarFiles, ThumbnailSettings) (string, string) {
		return ThumbnailStatusFailed, "injected generation failure"
	}
	fixture.worker.processTask(context.Background(), media)
	failure := fixture.worker.LastFailure(media.ID)
	if failure == nil || failure.TaskID == "" {
		t.Fatal("expected in-memory thumbnail failure record")
	}

	reloaded, err := fixture.repo.FindByID(media.ID)
	if err != nil {
		t.Fatalf("reload failed media: %v", err)
	}
	retry, err := fixture.worker.Retry(reloaded)
	if err != nil {
		t.Fatalf("retry failed thumbnail: %v", err)
	}
	if retry.TaskID == "" || retry.TaskID == failure.TaskID {
		t.Fatalf("retry did not create a new task ID: failure=%q retry=%q", failure.TaskID, retry.TaskID)
	}
	fixture.worker.executeGenerationFunc = func(context.Context, *model.Media, *directorySidecarFiles, ThumbnailSettings) (string, string) {
		return ThumbnailStatusGenerated, ""
	}
	reloaded, err = fixture.repo.FindByID(media.ID)
	if err != nil {
		t.Fatalf("reload retried media: %v", err)
	}
	fixture.worker.processTask(context.Background(), reloaded)
	completed, err := fixture.repo.FindByID(media.ID)
	if err != nil {
		t.Fatalf("reload completed media: %v", err)
	}
	if completed.ThumbnailStatus != ThumbnailStatusGenerated {
		t.Fatalf("retried thumbnail status = %q", completed.ThumbnailStatus)
	}
}

func TestThumbnailDeletedMediaTaskIsSkipped(t *testing.T) {
	fixture := newThumbnailWorkerFixture(t)
	media := fixture.addMedia(t, "deleted", ThumbnailStatusPending)
	if err := fixture.db.Delete(&model.Media{}, "id = ?", media.ID).Error; err != nil {
		t.Fatalf("delete media: %v", err)
	}
	fixture.worker.processTask(context.Background(), media)
	fixture.worker.mu.Lock()
	running := len(fixture.worker.running)
	fixture.worker.mu.Unlock()
	if running != 0 {
		t.Fatalf("deleted media left a dedupe marker: %d", running)
	}
}

func TestThumbnailShutdownCancelsWorkerAndIsIdempotent(t *testing.T) {
	fixture := newThumbnailWorkerFixture(t)
	media := fixture.addMedia(t, "shutdown", ThumbnailStatusPending)
	started := make(chan struct{})
	var once sync.Once
	fixture.worker.executeGenerationFunc = func(ctx context.Context, _ *model.Media, _ *directorySidecarFiles, _ ThumbnailSettings) (string, string) {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return ThumbnailStatusCanceled, ctx.Err().Error()
	}
	fixture.worker.Start()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("thumbnail worker did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := fixture.worker.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown worker: %v", err)
	}
	if err := fixture.worker.Shutdown(ctx); err != nil {
		t.Fatalf("repeat shutdown worker: %v", err)
	}
	reloaded, err := fixture.repo.FindByID(media.ID)
	if err != nil {
		t.Fatalf("reload canceled media: %v", err)
	}
	if reloaded.ThumbnailStatus != ThumbnailStatusCanceled {
		t.Fatalf("running task was not canceled: %q", reloaded.ThumbnailStatus)
	}
	if _, err := fixture.worker.Retry(reloaded); err == nil {
		t.Fatal("stopped worker accepted a new thumbnail task")
	}
}

func TestThumbnailRetryReservationExcludesPollerAndKeepsTaskID(t *testing.T) {
	fixture := newThumbnailWorkerFixture(t)
	media := fixture.addMedia(t, "retry-reserved", ThumbnailStatusFailed)
	pending := make(chan struct{})
	release := make(chan struct{})
	fixture.worker.retryThumbnailTaskFn = func(mediaID string) (bool, error) {
		retried, err := fixture.repo.RetryThumbnailTask(mediaID)
		close(pending)
		<-release
		return retried, err
	}
	var calls atomic.Int32
	fixture.worker.executeGenerationFunc = func(context.Context, *model.Media, *directorySidecarFiles, ThumbnailSettings) (string, string) {
		calls.Add(1)
		return ThumbnailStatusGenerated, ""
	}
	recorder := &thumbnailEventRecorder{events: make(map[string][]ThumbnailTaskEventData)}
	fixture.worker.wsHub = recorder

	result := make(chan *ThumbnailTaskEventData, 1)
	errs := make(chan error, 1)
	go func() {
		event, err := fixture.worker.Retry(media)
		result <- event
		errs <- err
	}()
	<-pending
	fixture.worker.processBatch(context.Background())
	if calls.Load() != 0 {
		t.Fatal("poller processed a reserved retry")
	}
	close(release)
	event := <-result
	if err := <-errs; err != nil || event == nil {
		t.Fatalf("Retry() event=%v err=%v", event, err)
	}
	reloaded, err := fixture.repo.FindByID(media.ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.worker.processTask(context.Background(), reloaded)
	if calls.Load() != 1 {
		t.Fatalf("generation calls=%d", calls.Load())
	}
	if count := recorder.terminalCount(event.TaskID); count != 1 {
		t.Fatalf("terminal events for %s=%d", event.TaskID, count)
	}
}

func TestThumbnailRetryShutdownDoesNotLeavePending(t *testing.T) {
	fixture := newThumbnailWorkerFixture(t)
	media := fixture.addMedia(t, "retry-shutdown", ThumbnailStatusFailed)
	pending := make(chan struct{})
	release := make(chan struct{})
	fixture.worker.retryThumbnailTaskFn = func(mediaID string) (bool, error) {
		retried, err := fixture.repo.RetryThumbnailTask(mediaID)
		close(pending)
		<-release
		return retried, err
	}
	done := make(chan error, 1)
	go func() {
		_, err := fixture.worker.Retry(media)
		done <- err
	}()
	<-pending
	fixture.worker.Stop()
	close(release)
	if err := <-done; err == nil {
		t.Fatal("Retry() succeeded during shutdown")
	}
	reloaded, err := fixture.repo.FindByID(media.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.ThumbnailStatus != ThumbnailStatusCanceled {
		t.Fatalf("status after retry/shutdown=%q", reloaded.ThumbnailStatus)
	}
	fixture.worker.mu.Lock()
	reservations, queued := len(fixture.worker.retryReservations), len(fixture.worker.queued)
	fixture.worker.mu.Unlock()
	if reservations != 0 || queued != 0 {
		t.Fatalf("retry state remains: reservations=%d queued=%d", reservations, queued)
	}
}

func TestThumbnailRetryFailuresReleaseReservation(t *testing.T) {
	fixture := newThumbnailWorkerFixture(t)
	media := fixture.addMedia(t, "retry-release", ThumbnailStatusFailed)
	fixture.worker.retryThumbnailTaskFn = func(string) (bool, error) {
		return false, errors.New("injected database failure")
	}
	if _, err := fixture.worker.Retry(media); err == nil {
		t.Fatal("Retry() succeeded after database failure")
	}
	fixture.worker.mu.Lock()
	reservations := len(fixture.worker.retryReservations)
	fixture.worker.mu.Unlock()
	if reservations != 0 {
		t.Fatalf("reservation remains after database failure: %d", reservations)
	}
	fixture.worker.retryThumbnailTaskFn = nil
	if _, err := fixture.worker.Retry(media); err != nil {
		t.Fatalf("retry after database failure: %v", err)
	}
}

func TestThumbnailRetryQueueFailureReleasesState(t *testing.T) {
	fixture := newThumbnailWorkerFixture(t)
	media := fixture.addMedia(t, "retry-queue", ThumbnailStatusFailed)
	fixture.worker.wakeRetryWorkerFn = func() bool { return false }
	if _, err := fixture.worker.Retry(media); err == nil {
		t.Fatal("Retry() succeeded with unavailable queue")
	}
	fixture.worker.mu.Lock()
	reservations, queued := len(fixture.worker.retryReservations), len(fixture.worker.queued)
	fixture.worker.mu.Unlock()
	if reservations != 0 || queued != 0 {
		t.Fatalf("retry state remains: reservations=%d queued=%d", reservations, queued)
	}
	reloaded, err := fixture.repo.FindByID(media.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.ThumbnailStatus != ThumbnailStatusCanceled {
		t.Fatalf("queue failure status=%q", reloaded.ThumbnailStatus)
	}
	if err := fixture.repo.UpdateThumbnailStatus(media.ID, map[string]interface{}{"thumbnail_status": ThumbnailStatusFailed}); err != nil {
		t.Fatal(err)
	}
	fixture.worker.wakeRetryWorkerFn = nil
	reloaded.ThumbnailStatus = ThumbnailStatusFailed
	if _, err := fixture.worker.Retry(reloaded); err != nil {
		t.Fatalf("retry after queue failure: %v", err)
	}
}
