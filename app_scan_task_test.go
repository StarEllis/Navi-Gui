package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"navi-desktop/config"
	"navi-desktop/model"
	"navi-desktop/service"
)

func attachTestScanner(t *testing.T, app *App) {
	t.Helper()
	app.scanner = service.NewScannerService(app.repos.Media, app.repos.Series, app.repos.Person, app.repos.MediaPerson, config.NewConfig(), app.logger)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = app.scanner.Shutdown(ctx)
	})
}

func waitForScanTerminal(t *testing.T, app *App, libraryID, taskID string) *ScanTaskInfo {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		last := app.GetLastScanTask(libraryID)
		if last != nil && last.TaskID == taskID && last.FinishedAt != nil {
			return last
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("scan %s did not reach terminal state: %+v", taskID, app.GetLastScanTask(libraryID))
	return nil
}

func TestScanTaskRegistryRejectsDuplicateAndAllowsRestartAfterCancel(t *testing.T) {
	app := NewApp()
	app.ctx = context.Background()
	library := &model.Library{ID: "scan-library", Name: "Scan Library", Path: t.TempDir(), Type: "movie"}

	first, err := app.registerScanTask(library, "incremental")
	if err != nil {
		t.Fatalf("register first scan: %v", err)
	}
	if first.info.TaskID == "" {
		t.Fatal("first scan has no task ID")
	}
	if _, err := app.registerScanTask(library, "overwrite"); err == nil || !strings.Contains(err.Error(), first.info.TaskID) {
		t.Fatalf("duplicate scan did not identify active task: %v", err)
	}

	first.cancel()
	app.finishScanTask(library, first, service.ScanOptions{
		TaskID: first.info.TaskID,
		Mode:   first.info.Mode,
	}, service.OverwriteScanResult{}, context.Canceled)
	if got := app.GetLastScanTask(library.ID); got == nil || got.Status != ScanTaskCanceled {
		t.Fatalf("canceled task state = %+v", got)
	}
	if err := app.CancelScan(first.info.TaskID); err != nil {
		t.Fatalf("idempotent cancel returned an error: %v", err)
	}

	second, err := app.registerScanTask(library, "incremental")
	if err != nil {
		t.Fatalf("register scan after cancel: %v", err)
	}
	if second.info.TaskID == first.info.TaskID {
		t.Fatalf("restart reused task ID %q", second.info.TaskID)
	}
	second.cancel()
	app.finishScanTask(library, second, service.ScanOptions{TaskID: second.info.TaskID}, service.OverwriteScanResult{}, context.Canceled)
}

func TestScanLibraryWithModeReturnsExistingActiveTask(t *testing.T) {
	app := newTestApp(t)
	library := &model.Library{ID: "existing-scan", Name: "Existing Scan", Path: t.TempDir(), Type: "movie"}
	if err := app.repos.Library.Create(library); err != nil {
		t.Fatalf("create library: %v", err)
	}
	existing, err := app.registerScanTask(library, "incremental")
	if err != nil {
		t.Fatalf("register existing scan: %v", err)
	}

	returned, err := app.ScanLibraryWithMode(library.ID, "incremental")
	if err != nil {
		t.Fatalf("duplicate scan returned an error: %v", err)
	}
	if returned == nil || returned.TaskID != existing.info.TaskID {
		t.Fatalf("duplicate scan returned task %+v, want %s", returned, existing.info.TaskID)
	}

	existing.cancel()
	app.finishScanTask(library, existing, service.ScanOptions{TaskID: existing.info.TaskID}, service.OverwriteScanResult{}, context.Canceled)
}

func TestScanTaskEarlyFailureReleasesActiveStateOnce(t *testing.T) {
	app := NewApp()
	app.ctx = context.Background()
	library := &model.Library{ID: "failed-library", Name: "Failed Library", Path: t.TempDir(), Type: "movie"}
	task, err := app.registerScanTask(library, "incremental")
	if err != nil {
		t.Fatalf("register scan: %v", err)
	}
	app.scanWG.Add(1)
	go app.runScanTask(library, task, service.ScanOptions{
		TaskID:           task.info.TaskID,
		Mode:             "incremental",
		Context:          task.ctx,
		SuppressTerminal: true,
	})

	select {
	case <-task.done:
	case <-time.After(time.Second):
		t.Fatal("failed task did not release lifecycle state")
	}
	last := app.GetLastScanFailure(library.ID)
	if last == nil || last.Status != ScanTaskFailed || !last.Retryable {
		t.Fatalf("failed task record = %+v", last)
	}
	app.finishScanTask(library, task, service.ScanOptions{}, service.OverwriteScanResult{}, context.Canceled)
	if got := app.GetLastScanTask(library.ID); got == nil || got.Status != ScanTaskFailed {
		t.Fatalf("second terminal overwrote failed state: %+v", got)
	}
	if _, err := app.registerScanTask(library, "incremental"); err != nil {
		t.Fatalf("active state was not released after failure: %v", err)
	}
}

func TestAppShutdownIsIdempotent(t *testing.T) {
	app := NewApp()
	app.ctx, app.appCancel = context.WithCancel(context.Background())
	app.shutdown(context.Background())
	app.shutdown(context.Background())
	app.scanMu.Lock()
	shuttingDown := app.shuttingDown
	app.scanMu.Unlock()
	if !shuttingDown {
		t.Fatal("shutdown did not close task admission")
	}
}

func TestRetryFailedScanCreatesNewTaskID(t *testing.T) {
	app := newTestApp(t)
	library := &model.Library{ID: "retry-library", Name: "Retry Library", Path: t.TempDir(), Type: "movie"}
	if err := app.repos.Library.Create(library); err != nil {
		t.Fatalf("create library: %v", err)
	}
	app.lastScanFailures = map[string]ScanTaskInfo{
		library.ID: {
			TaskID:    "failed-task-id",
			LibraryID: library.ID,
			Mode:      "incremental",
			Status:    ScanTaskFailed,
			Retryable: true,
		},
	}

	retry, err := app.RetryFailedScan(library.ID)
	if err != nil {
		t.Fatalf("retry failed scan: %v", err)
	}
	if retry.TaskID == "" || retry.TaskID == "failed-task-id" {
		t.Fatalf("retry task ID = %q", retry.TaskID)
	}
	deadline := time.Now().Add(time.Second)
	for {
		last := app.GetLastScanTask(library.ID)
		if last != nil && last.TaskID == retry.TaskID && last.Status == ScanTaskFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("retried task did not reach failed terminal: %+v", last)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestIncompleteScanCanBeQueriedAndRetried(t *testing.T) {
	app := newTestApp(t)
	library := &model.Library{ID: "retry-incomplete", Name: "Retry Incomplete", Path: t.TempDir(), Type: "movie"}
	if err := app.repos.Library.Create(library); err != nil {
		t.Fatal(err)
	}
	task, err := app.registerScanTask(library, "incremental")
	if err != nil {
		t.Fatal(err)
	}
	app.finishScanTask(library, task, service.ScanOptions{TaskID: task.info.TaskID}, service.OverwriteScanResult{}, &service.ScanIncompleteError{Root: library.Path, Err: context.DeadlineExceeded})
	failure := app.GetLastScanFailure(library.ID)
	if failure == nil || failure.Status != ScanTaskIncomplete || !failure.Retryable {
		t.Fatalf("incomplete failure = %+v", failure)
	}
	retry, err := app.RetryFailedScan(library.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retry.TaskID == "" || retry.TaskID == task.info.TaskID {
		t.Fatalf("retry task ID = %q", retry.TaskID)
	}
}

func TestCanceledScanIsNotRetryable(t *testing.T) {
	app := NewApp()
	app.ctx = context.Background()
	library := &model.Library{ID: "retry-canceled", Name: "Retry Canceled", Path: t.TempDir(), Type: "movie"}
	task, err := app.registerScanTask(library, "incremental")
	if err != nil {
		t.Fatal(err)
	}
	app.finishScanTask(library, task, service.ScanOptions{TaskID: task.info.TaskID}, service.OverwriteScanResult{}, context.Canceled)
	last := app.GetLastScanTask(library.ID)
	if last == nil || last.Status != ScanTaskCanceled || last.Retryable {
		t.Fatalf("canceled task = %+v", last)
	}
	if failure := app.GetLastScanFailure(library.ID); failure != nil {
		t.Fatalf("canceled task remained retryable: %+v", failure)
	}
}

func TestOverwriteScanClearsLibraryAndCompletesDirectly(t *testing.T) {
	app := newTestApp(t)
	attachTestScanner(t, app)
	library := &model.Library{
		ID:               "direct-overwrite",
		Name:             "Direct Overwrite",
		Path:             t.TempDir(),
		Type:             "movie",
		EnableFileFilter: false,
	}
	if err := app.repos.Library.Create(library); err != nil {
		t.Fatalf("create library: %v", err)
	}
	if err := app.repos.Media.Create(&model.Media{
		ID:        "stale-media",
		LibraryID: library.ID,
		Title:     "Stale",
		FilePath:  filepath.Join(library.Path, "missing.mp4"),
		MediaType: "movie",
	}); err != nil {
		t.Fatalf("create stale media: %v", err)
	}

	task, err := app.ScanLibraryWithMode(library.ID, "overwrite")
	if err != nil {
		t.Fatalf("start overwrite scan: %v", err)
	}
	last := waitForScanTerminal(t, app, library.ID, task.TaskID)
	if last.Status != ScanTaskCompleted {
		t.Fatalf("overwrite status=%s error=%s", last.Status, last.Error)
	}
	media, err := app.repos.Media.ListByLibraryID(library.ID)
	if err != nil {
		t.Fatalf("list media after overwrite: %v", err)
	}
	if len(media) != 0 {
		t.Fatalf("overwrite retained stale media: %+v", media)
	}
	updated, err := app.repos.Library.FindByID(library.ID)
	if err != nil {
		t.Fatalf("reload library: %v", err)
	}
	if updated.LastScan == nil {
		t.Fatal("overwrite did not update last scan time")
	}
}

func TestIncompleteRetryReflectsCurrentRootState(t *testing.T) {
	t.Run("root recovered", func(t *testing.T) {
		app := newTestApp(t)
		attachTestScanner(t, app)
		root := filepath.Join(t.TempDir(), "restored-root")
		library := &model.Library{ID: "recovered-root", Name: "Recovered Root", Path: root, Type: "movie"}
		if err := app.repos.Library.Create(library); err != nil {
			t.Fatal(err)
		}
		app.lastScanFailures = make(map[string]ScanTaskInfo)
		app.lastScanFailures[library.ID] = ScanTaskInfo{TaskID: "old-incomplete", LibraryID: library.ID, Mode: "incremental", Status: ScanTaskIncomplete, Retryable: true}
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		retry, err := app.RetryFailedScan(library.ID)
		if err != nil {
			t.Fatal(err)
		}
		last := waitForScanTerminal(t, app, library.ID, retry.TaskID)
		if last.Status != ScanTaskCompleted {
			t.Fatalf("recovered-root retry status=%s error=%s", last.Status, last.Error)
		}
		if failure := app.GetLastScanFailure(library.ID); failure != nil {
			t.Fatalf("successful retry left failure=%+v", failure)
		}
	})

	t.Run("root still missing", func(t *testing.T) {
		app := newTestApp(t)
		attachTestScanner(t, app)
		root := filepath.Join(t.TempDir(), "missing-root")
		library := &model.Library{ID: "missing-root", Name: "Missing Root", Path: root, Type: "movie"}
		if err := app.repos.Library.Create(library); err != nil {
			t.Fatal(err)
		}
		app.lastScanFailures = make(map[string]ScanTaskInfo)
		app.lastScanFailures[library.ID] = ScanTaskInfo{TaskID: "old-incomplete", LibraryID: library.ID, Mode: "incremental", Status: ScanTaskIncomplete, Retryable: true}
		retry, err := app.RetryFailedScan(library.ID)
		if err != nil {
			t.Fatal(err)
		}
		last := waitForScanTerminal(t, app, library.ID, retry.TaskID)
		if last.Status != ScanTaskIncomplete || !last.Retryable {
			t.Fatalf("missing-root retry=%+v", last)
		}
	})
}
