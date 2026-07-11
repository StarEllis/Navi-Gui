package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"navi-desktop/config"
)

func TestFFmpegSameKeyOnceAndConcurrencyLimit(t *testing.T) {
	cfg := &config.Config{App: config.AppConfig{FFmpegPath: "test", FFmpegConcurrency: 2}}
	svc := NewThumbnailService(cfg, nil)
	var starts, active, peak atomic.Int64
	svc.captureFrameRunner = func(ctx context.Context, _, output string, _ float64, _ int) error {
		starts.Add(1)
		current := active.Add(1)
		defer active.Add(-1)
		for {
			old := peak.Load()
			if current <= old || peak.CompareAndSwap(old, current) {
				break
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
		return os.WriteFile(output, []byte("frame"), 0o600)
	}
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = svc.captureFrameContext(context.Background(), "movie", 1, filepath.Join(dir, "same.jpg"), 720)
		}()
	}
	wg.Wait()
	if starts.Load() != 1 {
		t.Fatalf("same-key starts=%d", starts.Load())
	}
	starts.Store(0)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = svc.captureFrameContext(context.Background(), "movie", 1, filepath.Join(dir, fmt.Sprintf("%d.jpg", i)), 720)
		}(i)
	}
	wg.Wait()
	if peak.Load() > 2 {
		t.Fatalf("peak FFmpeg concurrency=%d", peak.Load())
	}
}

func TestFFmpegWaitingCancellationFailureReleaseAndShutdown(t *testing.T) {
	cfg := &config.Config{App: config.AppConfig{FFmpegPath: "test", FFmpegConcurrency: 1}}
	svc := NewThumbnailService(cfg, nil)
	block := make(chan struct{})
	svc.captureFrameRunner = func(ctx context.Context, _, output string, _ float64, _ int) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-block:
			return errors.New("injected failure")
		}
	}
	dir := t.TempDir()
	first := make(chan error, 1)
	go func() {
		first <- svc.captureFrameContext(context.Background(), "movie", 1, filepath.Join(dir, "first.jpg"), 720)
	}()
	deadline := time.Now().Add(time.Second)
	for svc.ffmpegGovernor.active.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithCancel(context.Background())
	second := make(chan error, 1)
	go func() { second <- svc.captureFrameContext(ctx, "movie", 1, filepath.Join(dir, "second.jpg"), 720) }()
	cancel()
	select {
	case err := <-second:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting cancellation=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiting cancellation blocked")
	}
	close(block)
	<-first
	if err := svc.captureFrameContext(context.Background(), "movie", 1, filepath.Join(dir, "third.jpg"), 720); err == nil {
		t.Fatal("expected injected failure after slot release")
	}
	svc.Shutdown()
	if err := svc.captureFrameContext(context.Background(), "movie", 1, filepath.Join(dir, "after.jpg"), 720); !errors.Is(err, ErrProcessGovernorStopped) {
		t.Fatalf("shutdown request=%v", err)
	}
}

func TestArtworkCacheShutdownWaitsForFFmpegCacheProducer(t *testing.T) {
	cacheBase := filepath.Join(t.TempDir(), "cache")
	cache := NewArtworkCache(cacheBase, nil)
	svc := NewThumbnailService(&config.Config{App: config.AppConfig{FFmpegPath: "test", FFmpegConcurrency: 1}}, nil)
	t.Cleanup(svc.Shutdown)
	svc.SetArtworkCache(cache)
	output := filepath.Join(cacheBase, "artwork", "poster", "media", "frame.jpg")
	started, releaseRunner := make(chan struct{}), make(chan struct{})
	svc.captureFrameRunner = func(_ context.Context, _, temp string, _ float64, _ int) error {
		close(started)
		<-releaseRunner
		return os.WriteFile(temp, []byte("frame"), 0o600)
	}
	captureDone := make(chan error, 1)
	go func() { captureDone <- svc.captureFrameContext(context.Background(), "movie", 1, output, 720) }()
	<-started
	shutdownDone := make(chan struct{})
	go func() { cache.Shutdown(); close(shutdownDone) }()
	select {
	case <-shutdownDone:
		t.Fatal("cache shutdown returned before FFmpeg producer exited")
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseRunner)
	if err := <-captureDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("capture error=%v", err)
	}
	select {
	case <-shutdownDone:
	case <-time.After(time.Second):
		t.Fatal("cache shutdown did not finish")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("final cache committed during shutdown: %v", err)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(output), "*.part-*")); len(leftovers) != 0 {
		t.Fatalf("temporary FFmpeg files remain: %v", leftovers)
	}
	if stats := cache.Stats(); stats.Files != 0 {
		t.Fatalf("shutdown FFmpeg producer registered cache: %+v", stats)
	}
}
