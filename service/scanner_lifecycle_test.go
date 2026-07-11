package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
	"navi-desktop/model"
)

func TestEverythingEnumerationCancellationStopsRequestWithoutWalkFallback(t *testing.T) {
	root := t.TempDir()
	requestStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	walks := 0
	scanner := &ScannerService{
		logger:   zap.NewNop().Sugar(),
		statFile: os.Stat,
		walkFileTree: func(string, filepath.WalkFunc) error {
			walks++
			return nil
		},
		scanContext: ctx,
	}
	library := &model.Library{ID: "everything-cancel", Name: "Everything", Path: root, Type: "movie"}
	done := make(chan error, 1)
	go func() {
		_, err := scanner.listMovieEntries(library, ScanOptions{
			Context:        ctx,
			UseEverything:  true,
			EverythingAddr: server.URL,
		}, newScanRootResult(root))
		done <- err
	}()

	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("Everything request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context cancellation, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Everything request did not stop after cancellation")
	}
	if walks != 0 {
		t.Fatalf("canceled Everything request fell back to walk %d times", walks)
	}
}

func TestOverwriteCancellationDuringNFOPreparationRollsBackAndEmitsCanceled(t *testing.T) {
	fixture := newOverwriteSafetyFixture(t, "movie", true)
	recorder := &recordingScanBroadcaster{}
	fixture.scanner.wsHub = recorder
	ctx, cancel := context.WithCancel(context.Background())
	fixture.scanner.readFile = func(path string) ([]byte, error) {
		data, err := os.ReadFile(path)
		if err == nil && filepath.Ext(path) == ".nfo" {
			cancel()
		}
		return data, err
	}

	_, err := fixture.scanner.ScanLibraryOverwrite(fixture.db, &fixture.library, ScanOptions{
		TaskID:  "nfo-cancel-task",
		Context: ctx,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled NFO preparation, got %v", err)
	}
	assertOverwriteOriginalState(t, fixture)
	terminal := assertSingleScanTerminal(t, recorder, EventScanCanceled)
	if terminal.data.TaskID != "nfo-cancel-task" {
		t.Fatalf("unexpected terminal task ID: %+v", terminal.data)
	}
}

func TestFFprobeCommandContextCancellationTerminatesProcess(t *testing.T) {
	if os.Getenv("NAVI_TEST_FFPROBE_HELPER") == "1" {
		marker := os.Getenv("NAVI_TEST_FFPROBE_MARKER")
		_ = os.WriteFile(marker, []byte("started"), 0o600)
		for {
			time.Sleep(time.Second)
		}
	}

	marker := filepath.Join(t.TempDir(), "started")
	ctx, cancel := context.WithCancel(context.Background())
	cmd, cleanup := newBackgroundCommand(ctx, 0, os.Args[0], "-test.run=TestFFprobeCommandContextCancellationTerminatesProcess")
	defer cleanup()
	cmd.Env = append(os.Environ(),
		"NAVI_TEST_FFPROBE_HELPER=1",
		"NAVI_TEST_FFPROBE_MARKER="+marker,
	)
	done := make(chan error, 1)
	go func() {
		_, err := cmd.Output()
		done <- err
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper process did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected canceled process to return an error")
		}
		if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
			t.Fatalf("helper process did not exit: %+v", cmd.ProcessState)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled FFprobe helper process was not terminated")
	}
}

func TestSuccessfulScanEventsCarryOneTaskID(t *testing.T) {
	fixture := newOverwriteSafetyFixture(t, "movie", true)
	recorder := &recordingScanBroadcaster{}
	fixture.scanner.wsHub = recorder

	if _, err := fixture.scanner.ScanLibraryOverwrite(fixture.db, &fixture.library, ScanOptions{TaskID: "scan-task-1"}); err != nil {
		t.Fatalf("overwrite scan: %v", err)
	}
	assertSingleScanTerminal(t, recorder, EventScanCompleted)
	for _, event := range recorder.events {
		if event.data.TaskID != "scan-task-1" {
			t.Fatalf("event %s has stale task ID %q", event.eventType, event.data.TaskID)
		}
	}
}

func TestScannerShutdownIsIdempotent(t *testing.T) {
	scanner := NewScannerService(nil, nil, nil, nil, nil, zap.NewNop().Sugar())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := scanner.Shutdown(ctx); err != nil {
		t.Fatalf("first shutdown: %v", err)
	}
	if err := scanner.Shutdown(ctx); err != nil {
		t.Fatalf("second shutdown: %v", err)
	}
	if scanner.EnqueueMetadataCompletion("after-shutdown", false) {
		t.Fatal("scanner accepted metadata work after shutdown")
	}
}
