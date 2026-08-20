package service

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"navi-desktop/model"
)

func writeTestJPEG(t *testing.T, path string, width, height int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir image dir: %v", err)
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 120, A: 255})
		}
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create image: %v", err)
	}
	defer file.Close()
	if err := jpeg.Encode(file, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatalf("encode image: %v", err)
	}
}

func waitForArtworkCondition(t *testing.T, timeout time.Duration, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal(message)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestArtworkCacheDeterministicHitVersioningAndNoTreeWalk(t *testing.T) {
	cache := NewArtworkCache(filepath.Join(t.TempDir(), "cache"), nil)
	source := filepath.Join(t.TempDir(), "poster.jpg")
	writeTestJPEG(t, source, 800, 1200)
	media := &model.Media{ID: "deterministic", FilePath: filepath.Join(t.TempDir(), "movie.mkv")}
	_ = os.WriteFile(media.FilePath, []byte("video"), 0o600)
	first, err := cache.cacheImageFile("poster", media.ID, source, 400, 540)
	if err != nil {
		t.Fatal(err)
	}
	readDirs := 0
	cache.readDir = func(path string) ([]os.DirEntry, error) { readDirs++; return os.ReadDir(path) }
	second, err := cache.cacheImageFile("poster", media.ID, source, 400, 540)
	if err != nil || first != second {
		t.Fatalf("deterministic hit=%q/%q err=%v", first, second, err)
	}
	if readDirs != 0 {
		t.Fatalf("cache hit enumerated directories %d times", readDirs)
	}
	differentSize, err := cache.cacheImageFile("poster", media.ID, source, 200, 270)
	if err != nil || differentSize == first {
		t.Fatalf("different dimensions collided: %q", differentSize)
	}
	info, _ := os.Stat(source)
	_ = os.Chtimes(source, info.ModTime(), info.ModTime().Add(2*time.Second))
	changed, err := cache.cacheImageFile("poster", media.ID, source, 400, 540)
	if err != nil || changed == first {
		t.Fatalf("changed source reused stale cache: %q", changed)
	}
}

func TestArtworkCacheConcurrentGenerationAtomicFailureAndCleanup(t *testing.T) {
	cache := NewArtworkCache(filepath.Join(t.TempDir(), "cache"), nil)
	source := filepath.Join(t.TempDir(), "poster.jpg")
	writeTestJPEG(t, source, 800, 1200)
	var renames atomic.Int64
	cache.rename = func(old, new string) error { renames.Add(1); return os.Rename(old, new) }
	var wg sync.WaitGroup
	paths := make([]string, 20)
	for i := range paths {
		wg.Add(1)
		go func(i int) { defer wg.Done(); paths[i], _ = cache.cacheImageFile("poster", "same", source, 400, 540) }(i)
	}
	wg.Wait()
	if renames.Load() != 1 {
		t.Fatalf("actual generations=%d, want 1", renames.Load())
	}
	for _, path := range paths {
		if path != paths[0] {
			t.Fatalf("concurrent paths differ")
		}
	}
	if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(paths[0]), "*.part")); len(leftovers) != 0 {
		t.Fatalf("temporary files remain: %v", leftovers)
	}

	old := paths[0]
	cache.rename = func(string, string) error { return errors.New("rename failure") }
	other := filepath.Join(t.TempDir(), "other.jpg")
	writeTestJPEG(t, other, 800, 1200)
	if _, err := cache.cacheImageFile("poster", "same", other, 400, 540); err == nil {
		t.Fatal("expected rename failure")
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("old valid cache was removed: %v", err)
	}
}

func TestArtworkCacheIndexRebuildCapacityAndTempCleanup(t *testing.T) {
	cache := NewArtworkCache(filepath.Join(t.TempDir(), "cache"), nil)
	now := time.Now()
	for i := 0; i < 5; i++ {
		path := filepath.Join(cache.mediaRoleDir("poster", fmt.Sprintf("m%d", i)), "item.jpg")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, make([]byte, 10), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(path, now.Add(time.Duration(i)*time.Minute), now.Add(time.Duration(i)*time.Minute))
	}
	stats, err := cache.RebuildIndex(context.Background())
	if err != nil || stats.Files != 5 || stats.Bytes != 50 {
		t.Fatalf("rebuild stats=%+v err=%v", stats, err)
	}
	reservedPath := filepath.Join(cache.mediaRoleDir("poster", "m0"), "item.jpg")
	release, ok := cache.Reserve(reservedPath)
	if !ok {
		t.Fatal("reserve cache entry")
	}
	stats, err = cache.Cleanup(context.Background(), 40, 20, 100, 10)
	if err != nil || stats.Bytes > 20 {
		t.Fatalf("cleanup stats=%+v err=%v", stats, err)
	}
	if _, err := os.Stat(reservedPath); err != nil {
		t.Fatalf("reserved cache was removed: %v", err)
	}
	release()
	oldTemp := filepath.Join(cache.root, ".navi-artwork-old.part")
	newTemp := filepath.Join(cache.root, ".navi-artwork-new.part")
	_ = os.WriteFile(oldTemp, []byte("x"), 0o600)
	_ = os.WriteFile(newTemp, []byte("x"), 0o600)
	_ = os.Chtimes(oldTemp, now.Add(-48*time.Hour), now.Add(-48*time.Hour))
	if _, err := cache.CleanupOldTemps(context.Background(), 24*time.Hour, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldTemp); !os.IsNotExist(err) {
		t.Fatalf("old temp remains: %v", err)
	}
	if _, err := os.Stat(newTemp); err != nil {
		t.Fatalf("new temp removed: %v", err)
	}
	if err := cache.flushDirty(); err != nil {
		t.Fatalf("flush rebuilt index: %v", err)
	}
	reloaded := NewArtworkCache(filepath.Dir(cache.root), nil)
	t.Cleanup(reloaded.Shutdown)
	if got := reloaded.Stats(); got.Files != stats.Files || got.Bytes != stats.Bytes {
		t.Fatalf("persisted index=%+v, want %+v", got, stats)
	}
}

func TestArtworkCacheShutdownContextIsBoundedAndIdempotent(t *testing.T) {
	cache := NewArtworkCache(filepath.Join(t.TempDir(), "cache"), nil)
	started := make(chan struct{})
	release := make(chan struct{})
	producerDone := make(chan error, 1)
	go func() {
		producerDone <- cache.generateOnce(filepath.Join(cache.root, "stuck.jpg"), func(context.Context) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := cache.ShutdownContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded shutdown error=%v", err)
	}
	if err := cache.generateOnce(filepath.Join(cache.root, "after.jpg"), func(context.Context) error { return nil }); !errors.Is(err, ErrProcessGovernorStopped) {
		t.Fatalf("new producer after shutdown error=%v", err)
	}
	cache.BeginShutdown()
	if err := cache.ShutdownContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("repeated deadline shutdown error=%v", err)
	}

	close(release)
	<-producerDone
	finishCtx, finishCancel := context.WithTimeout(context.Background(), time.Second)
	defer finishCancel()
	if err := cache.ShutdownContext(finishCtx); err != nil {
		t.Fatalf("shutdown after producer release: %v", err)
	}
}

func TestArtworkCacheShutdownContextCancelsResponsiveProducer(t *testing.T) {
	cache := NewArtworkCache(filepath.Join(t.TempDir(), "cache"), nil)
	started := make(chan struct{})
	producerDone := make(chan error, 1)
	go func() {
		producerDone <- cache.generateOnce(filepath.Join(cache.root, "responsive.jpg"), func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := cache.ShutdownContext(ctx); err != nil {
		t.Fatalf("responsive shutdown: %v", err)
	}
	if err := <-producerDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("responsive producer error=%v", err)
	}
}

func TestArtworkTempCleanupRunsMultipleBatchesBeforeMarker(t *testing.T) {
	cache := NewArtworkCache(filepath.Join(t.TempDir(), "cache"), nil)
	t.Cleanup(cache.Shutdown)
	if err := os.MkdirAll(cache.root, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	for index := 0; index < 450; index++ {
		path := filepath.Join(cache.root, fmt.Sprintf(".navi-old-%03d.part", index))
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	beforeWalks := cache.walks.Load()
	if err := cache.CleanupOldTempsIfDue(context.Background(), 24*time.Hour, 24*time.Hour, 200); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(cache.root, ".temp-cleanup")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("partial cleanup wrote completion marker: %v", err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for {
		matches, err := filepath.Glob(filepath.Join(cache.root, "*.part"))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("temp cleanup left %d files", len(matches))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if cache.walks.Load()-beforeWalks < 3 {
		t.Fatalf("cleanup walks=%d want at least 3 batches", cache.walks.Load()-beforeWalks)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("completed cleanup marker missing: %v", err)
	}
}

func TestArtworkTempCleanupPreservesNewAndReservedFilesAndReportsFailures(t *testing.T) {
	cache := NewArtworkCache(filepath.Join(t.TempDir(), "cache"), nil)
	t.Cleanup(cache.Shutdown)
	if err := os.MkdirAll(cache.root, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	reserved := filepath.Join(cache.root, ".navi-reserved.part")
	newFile := filepath.Join(cache.root, ".navi-new.part")
	failing := filepath.Join(cache.root, ".navi-failing.part")
	for _, path := range []string{reserved, newFile, failing} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{reserved, failing} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	release, ok := cache.Reserve(reserved)
	if !ok {
		t.Fatal("reserve temp file")
	}
	defer release()
	originalRemove := cache.remove
	cache.remove = func(path string) error {
		if samePath(path, failing) {
			return errors.New("injected delete failure")
		}
		return originalRemove(path)
	}
	result, err := cache.CleanupOldTemps(context.Background(), 24*time.Hour, 200)
	if err == nil || result.Completed || !result.Remaining || result.StopReason != "delete_failed" {
		t.Fatalf("cleanup result=%+v err=%v", result, err)
	}
	for _, path := range []string{reserved, newFile, failing} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("preserved file %s: %v", path, err)
		}
	}
}

func TestArtworkTempCleanupShutdownStopsScheduledBatches(t *testing.T) {
	cache := NewArtworkCache(filepath.Join(t.TempDir(), "cache"), nil)
	if err := os.MkdirAll(cache.root, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	for index := 0; index < 450; index++ {
		path := filepath.Join(cache.root, fmt.Sprintf(".navi-stop-%03d.part", index))
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(path, old, old)
	}
	if err := cache.CleanupOldTempsIfDue(context.Background(), 24*time.Hour, 24*time.Hour, 200); err != nil {
		t.Fatal(err)
	}
	cache.BeginShutdown()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := cache.ShutdownContext(ctx); err != nil {
		t.Fatal(err)
	}
	walks := cache.walks.Load()
	time.Sleep(150 * time.Millisecond)
	if cache.walks.Load() != walks {
		t.Fatalf("scheduled cleanup continued after shutdown: before=%d after=%d", walks, cache.walks.Load())
	}
}

func TestArtworkCacheCleanupFailureDoesNotBreakReads(t *testing.T) {
	cache := NewArtworkCache(filepath.Join(t.TempDir(), "cache"), nil)
	path := filepath.Join(cache.mediaRoleDir("preview", "keep"), "item.jpg")
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte("x"), 0o600)
	_, _ = cache.RebuildIndex(context.Background())
	cache.remove = func(string) error { return errors.New("injected remove failure") }
	if _, err := cache.Cleanup(context.Background(), 0, 0, 0, 10); err == nil {
		t.Fatal("expected cleanup warning error")
	}
	if got := cache.CachedMediaPreviews("keep"); len(got) != 1 {
		t.Fatalf("read failed after cleanup warning: %v", got)
	}
	if release, ok := cache.Reserve(path); !ok {
		t.Fatal("failed eviction did not clear evicting state")
	} else {
		release()
	}
}

func TestArtworkCacheReservationAndEvictingAreAtomic(t *testing.T) {
	cache := NewArtworkCache(filepath.Join(t.TempDir(), "cache"), nil)
	t.Cleanup(cache.Shutdown)
	path := filepath.Join(cache.mediaRoleDir("poster", "race"), "item.jpg")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	cache.recordFile(path)

	release, ok := cache.Reserve(path)
	if !ok {
		t.Fatal("reserve existing cache")
	}
	if removed, err := cache.evictPath(path); err != nil || removed {
		t.Fatalf("reserved path removed=%t err=%v", removed, err)
	}
	release()

	removeStarted := make(chan struct{})
	allowRemove := make(chan struct{})
	cache.remove = func(target string) error { close(removeStarted); <-allowRemove; return os.Remove(target) }
	done := make(chan error, 1)
	go func() { _, err := cache.evictPath(path); done <- err }()
	<-removeStarted
	if release, ok := cache.Reserve(path); ok {
		release()
		t.Fatal("evicting path accepted a new reservation")
	}
	close(allowRemove)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("evicted path remains: %v", err)
	}
}

func TestArtworkCacheRemoveMediaDefersReservedPathDeletion(t *testing.T) {
	cache := NewArtworkCache(filepath.Join(t.TempDir(), "cache"), nil)
	t.Cleanup(cache.Shutdown)
	mediaID := "reserved-delete"
	path := filepath.Join(cache.mediaRoleDir("poster", mediaID), "generated.jpg")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("generated"), 0o600); err != nil {
		t.Fatal(err)
	}
	cache.recordFile(path)

	release, ok := cache.Reserve(path)
	if !ok {
		t.Fatal("reserve generated artwork")
	}
	if err := cache.RemoveMedia(mediaID); err != nil {
		t.Fatalf("remove media: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("reserved file was removed before release: %v", err)
	}
	if releaseAgain, ok := cache.Reserve(path); ok {
		releaseAgain()
		t.Fatal("media artwork pending deletion accepted a new reservation")
	}
	release()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("released media artwork remains: %v", err)
	}
}

func TestArtworkCacheRemoveMediaDeletesInflightGeneration(t *testing.T) {
	cache := NewArtworkCache(filepath.Join(t.TempDir(), "cache"), nil)
	t.Cleanup(cache.Shutdown)
	mediaID := "inflight-delete"
	path := filepath.Join(cache.mediaRoleDir("poster", mediaID), "generated.jpg")
	started := make(chan struct{})
	finish := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		done <- cache.generateOnce(path, func(context.Context) error {
			close(started)
			<-finish
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte("generated"), 0o600); err != nil {
				return err
			}
			cache.recordFile(path)
			return nil
		})
	}()
	<-started
	if err := cache.RemoveMedia(mediaID); err != nil {
		t.Fatalf("remove media: %v", err)
	}
	close(finish)
	if err := <-done; err != nil {
		t.Fatalf("finish generation: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("in-flight generated artwork remains after media removal: %v", err)
	}
}

func TestArtworkIndexDebouncesRecordsAndRetriesFailure(t *testing.T) {
	cache := NewArtworkCache(filepath.Join(t.TempDir(), "cache"), nil)
	t.Cleanup(cache.Shutdown)
	cache.flushDebounce = 200 * time.Millisecond
	if _, err := cache.RebuildIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := cache.flushDirty(); err != nil {
		t.Fatal(err)
	}

	var attempts atomic.Int64
	fail := atomic.Bool{}
	fail.Store(true)
	originalWriter := cache.writeIndexSnapshot
	cache.indexWriter = func(entries []artworkIndexEntry) error {
		attempts.Add(1)
		if fail.Load() {
			return errors.New("injected index write failure")
		}
		return originalWriter(entries)
	}
	paths := make([]string, 100)
	for i := 0; i < 100; i++ {
		path := filepath.Join(cache.mediaRoleDir("poster", fmt.Sprintf("batch-%03d", i)), "item.jpg")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		paths[i] = path
	}
	for _, path := range paths {
		cache.recordFile(path)
	}
	waitForArtworkCondition(t, time.Second, func() bool { return attempts.Load() >= 1 }, "index flusher did not run")
	cache.mu.Lock()
	dirtyAfterFailure := cache.dirty
	cache.mu.Unlock()
	if !dirtyAfterFailure {
		t.Fatal("failed index write cleared dirty state")
	}
	if got := attempts.Load(); got > 2 {
		t.Fatalf("100 recordFile calls produced %d full index writes before retry", got)
	}

	fail.Store(false)
	select {
	case cache.flushCh <- struct{}{}:
	default:
	}
	waitForArtworkCondition(t, 2*time.Second, func() bool {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		return !cache.dirty && cache.persistedVersion == cache.indexVersion
	}, "dirty index did not retry successfully")
	if attempts.Load() > 3 {
		t.Fatalf("debounced flusher attempts=%d", attempts.Load())
	}
}

func TestArtworkIndexOlderSnapshotCannotClearNewDirtyVersion(t *testing.T) {
	cache := NewArtworkCache(filepath.Join(t.TempDir(), "cache"), nil)
	t.Cleanup(cache.Shutdown)
	if _, err := cache.RebuildIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := cache.flushDirty(); err != nil {
		t.Fatal(err)
	}
	firstPath := filepath.Join(cache.mediaRoleDir("poster", "first"), "item.jpg")
	secondPath := filepath.Join(cache.mediaRoleDir("poster", "second"), "item.jpg")
	for _, path := range []string{firstPath, secondPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	started, release := make(chan struct{}), make(chan struct{})
	var writes atomic.Int64
	originalWriter := cache.writeIndexSnapshot
	cache.indexWriter = func(entries []artworkIndexEntry) error {
		if writes.Add(1) == 1 {
			close(started)
			<-release
		}
		return originalWriter(entries)
	}
	cache.recordFile(firstPath)
	flushDone := make(chan error, 1)
	go func() { flushDone <- cache.flushDirty() }()
	<-started
	cache.recordFile(secondPath)
	close(release)
	if err := <-flushDone; err != nil {
		t.Fatal(err)
	}
	cache.mu.Lock()
	dirty := cache.dirty
	cache.mu.Unlock()
	if !dirty {
		t.Fatal("older successful snapshot cleared a newer dirty version")
	}
	if err := cache.flushDirty(); err != nil {
		t.Fatal(err)
	}
	cache.mu.Lock()
	clean := !cache.dirty && cache.persistedVersion == cache.indexVersion
	cache.mu.Unlock()
	if !clean || writes.Load() != 2 {
		t.Fatalf("versioned flush clean=%t writes=%d", clean, writes.Load())
	}
}

func TestArtworkStartupReconcilesFileCommittedBeforeLastFlush(t *testing.T) {
	base := filepath.Join(t.TempDir(), "cache")
	cache := NewArtworkCache(base, nil)
	first := filepath.Join(cache.mediaRoleDir("poster", "first"), "item.jpg")
	if err := os.MkdirAll(filepath.Dir(first), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(first, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.RebuildIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := cache.flushDirty(); err != nil {
		t.Fatal(err)
	}
	if err := cache.prepareFileCommit(context.Background()); err != nil {
		t.Fatal(err)
	}
	crashFile := filepath.Join(cache.mediaRoleDir("poster", "crash"), "item.jpg")
	if err := os.MkdirAll(filepath.Dir(crashFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(crashFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cache.Shutdown()

	reloaded := NewArtworkCache(base, nil)
	t.Cleanup(reloaded.Shutdown)
	reloaded.mu.Lock()
	needsReconcile := reloaded.reconcileNeeded
	reloaded.mu.Unlock()
	if !needsReconcile {
		t.Fatal("startup ignored reconciliation marker")
	}
	stats, err := reloaded.Maintain(context.Background())
	if err != nil || stats.Files != 2 {
		t.Fatalf("reconciled stats=%+v err=%v", stats, err)
	}
}

func TestArtworkCacheShutdownWaitsForProducerAndPreventsCommit(t *testing.T) {
	cache := NewArtworkCache(filepath.Join(t.TempDir(), "cache"), nil)
	output := filepath.Join(cache.mediaRoleDir("poster", "shutdown"), "item.jpg")
	started := make(chan struct{})
	releaseProducer := make(chan struct{})
	renames := atomic.Int64{}
	cache.rename = func(old, new string) error { renames.Add(1); return os.Rename(old, new) }
	producerDone := make(chan error, 1)
	go func() {
		producerDone <- cache.generateOnce(output, func(ctx context.Context) error {
			close(started)
			<-releaseProducer
			if err := ctx.Err(); err != nil {
				return err
			}
			return os.WriteFile(output, []byte("committed"), 0o600)
		})
	}()
	<-started
	shutdownDone := make(chan struct{})
	go func() { cache.Shutdown(); close(shutdownDone) }()
	select {
	case <-shutdownDone:
		t.Fatal("Shutdown returned before producer exited")
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseProducer)
	if err := <-producerDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("producer error=%v", err)
	}
	select {
	case <-shutdownDone:
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not finish")
	}
	if renames.Load() != 0 {
		t.Fatalf("rename occurred after shutdown: %d", renames.Load())
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("output committed during shutdown: %v", err)
	}
	if stats := cache.Stats(); stats.Files != 0 {
		t.Fatalf("shutdown producer registered index: %+v", stats)
	}
}

func TestArtworkMaintainContinuesBeyondSingleBatch(t *testing.T) {
	cache := NewArtworkCache(filepath.Join(t.TempDir(), "cache"), nil)
	t.Cleanup(cache.Shutdown)
	cache.highBytes, cache.lowBytes = 1<<62, 1<<62
	cache.maxFiles, cache.cleanupBatch = 10, 500
	for i := 0; i < 1200; i++ {
		path := filepath.Join(cache.mediaRoleDir("poster", fmt.Sprintf("m-%04d", i)), "item.jpg")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := cache.RebuildIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForArtworkCondition(t, 5*time.Second, func() bool { return cache.Stats().Files <= 10 }, "cleanup worker did not continue past the first 500-file batch")
}

func TestArtworkCacheRemovesNewAndIndexedLegacyFormatsTogether(t *testing.T) {
	cache := NewArtworkCache(filepath.Join(t.TempDir(), "cache"), nil)
	t.Cleanup(cache.Shutdown)
	mediaID := "mixed-format"
	newPath := filepath.Join(cache.mediaRoleDir("poster", mediaID), "new.jpg")
	legacyPath := filepath.Join(cache.roleDir("poster"), mediaID+"-legacy.jpg")
	for _, path := range []string{newPath, legacyPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := cache.RebuildIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := cache.RemoveMedia(mediaID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{newPath, legacyPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("cache path remains after media deletion: %s err=%v", path, err)
		}
	}
}

func BenchmarkArtworkCacheHitLookup(b *testing.B) {
	cache := NewArtworkCache(filepath.Join(b.TempDir(), "cache"), nil)
	mediaID := "target"
	mediaDir := cache.mediaRoleDir("preview", mediaID)
	_ = os.MkdirAll(mediaDir, 0o755)
	_ = os.WriteFile(filepath.Join(mediaDir, "hit.jpg"), []byte("x"), 0o600)
	legacyDir := cache.roleDir("preview")
	for i := 0; i < 1000; i++ {
		_ = os.WriteFile(filepath.Join(legacyDir, fmt.Sprintf("unrelated-%04d.jpg", i)), []byte("x"), 0o600)
	}
	b.Run("before-role-directory-enumeration", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_, _ = os.ReadDir(legacyDir)
		}
	})
	b.Run("after-media-directory-lookup", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = cache.CachedMediaPreviews(mediaID)
		}
	})
}

func decodeImageSize(t *testing.T, path string) image.Point {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open cached image: %v", err)
	}
	defer file.Close()
	cfg, _, err := image.DecodeConfig(file)
	if err != nil {
		t.Fatalf("decode cached image: %v", err)
	}
	return image.Point{X: cfg.Width, Y: cfg.Height}
}

func TestArtworkCacheCachesBoundedMediaArtworkAndKeepsItAfterSourceDelete(t *testing.T) {
	mediaDir := t.TempDir()
	cacheDir := filepath.Join(t.TempDir(), "cache")
	mediaPath := filepath.Join(mediaDir, "ABC-123.mp4")
	if err := os.WriteFile(mediaPath, []byte("video"), 0644); err != nil {
		t.Fatalf("write media: %v", err)
	}
	posterSource := filepath.Join(mediaDir, "ABC-123-poster.jpg")
	fanartSource := filepath.Join(mediaDir, "ABC-123-fanart.jpg")
	writeTestJPEG(t, posterSource, 1200, 1800)
	writeTestJPEG(t, fanartSource, 2400, 1350)

	cache := NewArtworkCache(cacheDir, nil)
	media := &model.Media{ID: "media-1", FilePath: mediaPath}
	sidecars := collectDirectorySidecarFiles(mediaDir)
	posterPath, fanartPath, changed, err := cache.CacheMediaArtwork(media, sidecars)
	if err != nil {
		t.Fatalf("cache media artwork: %v", err)
	}
	if !changed {
		t.Fatalf("expected artwork paths to change")
	}
	if posterPath == "" || fanartPath == "" {
		t.Fatalf("expected cached poster and fanart paths, got %q / %q", posterPath, fanartPath)
	}
	if !strings.Contains(filepath.ToSlash(posterPath), "/artwork/poster/") {
		t.Fatalf("poster path should be inside poster cache, got %s", posterPath)
	}
	if !strings.Contains(filepath.ToSlash(fanartPath), "/artwork/fanart/") {
		t.Fatalf("fanart path should be inside fanart cache, got %s", fanartPath)
	}
	posterSize := decodeImageSize(t, posterPath)
	if posterSize.X > 400 || posterSize.Y > 540 {
		t.Fatalf("poster too large: %dx%d", posterSize.X, posterSize.Y)
	}
	fanartSize := decodeImageSize(t, fanartPath)
	if fanartSize.X > 1280 || fanartSize.Y > 720 {
		t.Fatalf("fanart too large: %dx%d", fanartSize.X, fanartSize.Y)
	}

	if err := os.Remove(posterSource); err != nil {
		t.Fatalf("remove poster source: %v", err)
	}
	if err := os.Remove(fanartSource); err != nil {
		t.Fatalf("remove fanart source: %v", err)
	}
	if _, err := os.Stat(posterPath); err != nil {
		t.Fatalf("cached poster should remain after source delete: %v", err)
	}
	if _, err := os.Stat(fanartPath); err != nil {
		t.Fatalf("cached fanart should remain after source delete: %v", err)
	}
}

func TestArtworkCacheCachesPreviewsAndCanRemoveOnlyMediaArtwork(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	previewSource := filepath.Join(t.TempDir(), "extrafanart", "ABC-123-preview-01.jpg")
	writeTestJPEG(t, previewSource, 1920, 1080)

	cache := NewArtworkCache(cacheDir, nil)
	media := &model.Media{ID: "media-2", FilePath: filepath.Join(t.TempDir(), "ABC-123.mp4")}
	previewPaths, err := cache.CacheMediaPreviews(media, []string{previewSource}, 4)
	if err != nil {
		t.Fatalf("cache previews: %v", err)
	}
	if len(previewPaths) != 1 {
		t.Fatalf("expected one cached preview, got %d", len(previewPaths))
	}
	if !strings.Contains(filepath.ToSlash(previewPaths[0]), "/artwork/preview/") {
		t.Fatalf("preview path should be inside preview cache, got %s", previewPaths[0])
	}
	size := decodeImageSize(t, previewPaths[0])
	if size.X > 1280 || size.Y > 720 {
		t.Fatalf("preview too large: %dx%d", size.X, size.Y)
	}

	if cached := cache.CachedMediaPreviewsForSources(media.ID, []string{previewSource}); len(cached) != 1 {
		t.Fatalf("expected the cached copy to be reused for the same source, got %v", cached)
	}
	otherSource := filepath.Join(t.TempDir(), "extrafanart", "ABC-123-preview-02.jpg")
	writeTestJPEG(t, otherSource, 1920, 1080)
	if cached := cache.CachedMediaPreviewsForSources(media.ID, []string{previewSource, otherSource}); cached != nil {
		t.Fatalf("expected a new source to invalidate the cached set, got %v", cached)
	}
	// sidecar 缓存出来的副本不算"已经生成过预览图"。
	if generated := cache.GeneratedMediaPreviews(media.ID); len(generated) != 0 {
		t.Fatalf("expected no generated previews, got %v", generated)
	}

	if err := cache.RemoveMedia(media.ID); err != nil {
		t.Fatalf("remove media cache: %v", err)
	}
	if _, err := os.Stat(previewPaths[0]); !os.IsNotExist(err) {
		t.Fatalf("expected media preview cache to be removed, stat err=%v", err)
	}
}
