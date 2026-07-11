package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"navi-desktop/service"
)

func TestPostStartupMaintenanceIsDelayedOrderedAndNonBlocking(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app := NewApp()
	app.ctx = ctx
	app.logger = zap.NewNop().Sugar()
	app.postStartupDelay = -1
	migrated := make(chan struct{})
	release := make(chan struct{})
	app.migrationHook = func() { close(migrated) }
	app.maintenanceHook = func(context.Context) error {
		select {
		case <-migrated:
		case <-time.After(time.Second):
			t.Error("maintenance started before migration")
		}
		<-release
		return nil
	}
	returned := make(chan struct{})
	go func() { app.startPostStartupServices(nil, service.DefaultThumbnailSettings); close(returned) }()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("post-startup scheduling blocked startup")
	}
	select {
	case <-migrated:
	case <-time.After(time.Second):
		t.Fatal("migration did not run")
	}
	close(release)
	done := make(chan struct{})
	go func() { app.maintenanceWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("maintenance did not finish")
	}
}

func TestPostStartupShutdownCancelsMaintenanceAndStartIsIdempotent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	app := NewApp()
	app.ctx = ctx
	app.logger = zap.NewNop().Sugar()
	app.postStartupDelay = -1
	var starts atomic.Int64
	app.migrationHook = func() {}
	app.maintenanceHook = func(ctx context.Context) error { starts.Add(1); <-ctx.Done(); return ctx.Err() }
	app.startPostStartupServices(nil, service.DefaultThumbnailSettings)
	app.startPostStartupServices(nil, service.DefaultThumbnailSettings)
	deadline := time.Now().Add(time.Second)
	for starts.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	done := make(chan struct{})
	go func() { app.maintenanceWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel delayed maintenance")
	}
	if starts.Load() != 1 {
		t.Fatalf("maintenance workers=%d, want 1", starts.Load())
	}
}

func TestPostStartupMaintenanceFailureDoesNotBecomeStartupFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app := NewApp()
	app.ctx = ctx
	app.logger = zap.NewNop().Sugar()
	app.postStartupDelay = -1
	app.migrationHook = func() {}
	app.maintenanceHook = func(context.Context) error { return errors.New("injected warning") }
	app.startPostStartupServices(nil, service.DefaultThumbnailSettings)
	app.maintenanceWG.Wait()
}
