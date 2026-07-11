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
)

func newProbeTestScanner(t *testing.T, limit int, runner func(context.Context, string) ([]byte, error)) (*ScannerService, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(path, []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &ScannerService{
		statFile:              os.Stat,
		probeMediaFileContext: runner,
		probeGovernor:         newProcessGovernor(ctx, limit),
		probeScope:            newProbeScope(ctx),
		metadataWorkerCtx:     ctx,
	}, path
}

func TestProbeScopeSerialAndConcurrentRequestsStartOnce(t *testing.T) {
	var starts atomic.Int64
	s, path := newProbeTestScanner(t, 2, func(context.Context, string) ([]byte, error) {
		starts.Add(1)
		time.Sleep(20 * time.Millisecond)
		return []byte("result"), nil
	})
	for i := 0; i < 3; i++ {
		if _, err := s.runMediaProbe(path); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = s.runMediaProbe(path) }()
	}
	wg.Wait()
	if got := starts.Load(); got != 1 {
		t.Fatalf("actual process starts=%d, want 1", got)
	}
}

func TestProbeScopeFileVersionFailureRetryAndIsolation(t *testing.T) {
	var starts atomic.Int64
	fail := atomic.Bool{}
	fail.Store(true)
	s, path := newProbeTestScanner(t, 2, func(context.Context, string) ([]byte, error) {
		starts.Add(1)
		if fail.Swap(false) {
			return nil, errors.New("injected failure")
		}
		return []byte("ok"), nil
	})
	if _, err := s.runMediaProbe(path); err == nil {
		t.Fatal("expected injected failure")
	}
	if _, err := s.runMediaProbe(path); err != nil {
		t.Fatalf("failure was cached: %v", err)
	}
	if err := os.WriteFile(path, []byte("larger-video"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.runMediaProbe(path); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if err := os.Chtimes(path, info.ModTime(), info.ModTime().Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.runMediaProbe(path); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(filepath.Dir(path), "other.mkv")
	if err := os.WriteFile(other, []byte("larger-video"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.runMediaProbe(other); err != nil {
		t.Fatal(err)
	}
	if got := starts.Load(); got != 5 {
		t.Fatalf("starts=%d, want failure+retry+size+mtime+other = 5", got)
	}
}

func TestProbeGovernorLimitCancellationReleaseAndShutdown(t *testing.T) {
	var active, peak atomic.Int64
	block := make(chan struct{})
	s, path := newProbeTestScanner(t, 2, func(ctx context.Context, _ string) ([]byte, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for current > peak.Load() && !peak.CompareAndSwap(peak.Load(), current) {
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-block:
			return []byte("ok"), nil
		}
	})
	paths := []string{path}
	for i := 0; i < 7; i++ {
		p := filepath.Join(filepath.Dir(path), string(rune('a'+i))+".mkv")
		_ = os.WriteFile(p, []byte{byte(i)}, 0o600)
		paths = append(paths, p)
	}
	var wg sync.WaitGroup
	for _, p := range paths {
		wg.Add(1)
		go func(path string) { defer wg.Done(); _, _ = s.runMediaProbe(path) }(p)
	}
	deadline := time.Now().Add(time.Second)
	for active.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := peak.Load(); got > 2 {
		t.Fatalf("peak concurrency=%d, limit=2", got)
	}
	s.probeGovernor.shutdown()
	close(block)
	wg.Wait()
	if _, err := s.runMediaProbe(filepath.Join(filepath.Dir(path), "new.mkv")); !errors.Is(err, os.ErrNotExist) && !errors.Is(err, ErrProcessGovernorStopped) {
		t.Fatalf("shutdown request error=%v", err)
	}
}

func TestProbeWaiterCancellationDoesNotCancelSharedProbe(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	s, path := newProbeTestScanner(t, 1, func(ctx context.Context, _ string) ([]byte, error) {
		close(started)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return []byte("ok"), nil
		}
	})
	firstCtx, cancel := context.WithCancel(context.Background())
	s.scanContext = firstCtx
	first := make(chan error, 1)
	go func() { _, err := s.runMediaProbe(path); first <- err }()
	<-started
	s.scanContext = context.Background()
	second := make(chan error, 1)
	go func() { _, err := s.runMediaProbe(path); second <- err }()
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("first waiter error=%v", err)
	}
	close(release)
	if err := <-second; err != nil {
		t.Fatalf("shared probe was canceled: %v", err)
	}
}

func TestProbeGovernorMergesSameFileAcrossTaskScopes(t *testing.T) {
	ctx := context.Background()
	governor := newProcessGovernor(ctx, 2)
	path := filepath.Join(t.TempDir(), "shared.mkv")
	_ = os.WriteFile(path, []byte("video"), 0o600)
	var starts atomic.Int64
	runner := func(context.Context, string) ([]byte, error) {
		starts.Add(1)
		time.Sleep(20 * time.Millisecond)
		return []byte("ok"), nil
	}
	first := &ScannerService{statFile: os.Stat, probeMediaFileContext: runner, probeGovernor: governor, probeScope: newProbeScope(ctx), metadataWorkerCtx: ctx}
	second := &ScannerService{statFile: os.Stat, probeMediaFileContext: runner, probeGovernor: governor, probeScope: newProbeScope(ctx), metadataWorkerCtx: ctx}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = first.runMediaProbe(path) }()
	go func() { defer wg.Done(); _, _ = second.runMediaProbe(path) }()
	wg.Wait()
	if starts.Load() != 1 {
		t.Fatalf("cross-scope starts=%d, want 1", starts.Load())
	}
}

func BenchmarkProbeTask(b *testing.B) {
	for _, files := range []int{100, 1000} {
		b.Run(fmt.Sprintf("%d-media", files), func(b *testing.B) {
			dir := b.TempDir()
			paths := make([]string, files)
			for i := range paths {
				paths[i] = filepath.Join(dir, string(rune(i+1))+".mkv")
				_ = os.WriteFile(paths[i], []byte("x"), 0o600)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				ctx := context.Background()
				s := &ScannerService{statFile: os.Stat, probeMediaFileContext: func(context.Context, string) ([]byte, error) { return []byte("ok"), nil }, probeGovernor: newProcessGovernor(ctx, 2), probeScope: newProbeScope(ctx), metadataWorkerCtx: ctx}
				for _, path := range paths {
					_, _ = s.runMediaProbe(path)
				}
				s.probeScope.close()
			}
		})
	}
}

func BenchmarkProbeRepeatedRequests(b *testing.B) {
	for _, files := range []int{100, 1000} {
		dir := b.TempDir()
		paths := make([]string, files)
		for i := range paths {
			paths[i] = filepath.Join(dir, fmt.Sprintf("%04d.mkv", i))
			_ = os.WriteFile(paths[i], []byte("x"), 0o600)
		}
		b.Run(fmt.Sprintf("before-%d", files), func(b *testing.B) {
			var starts atomic.Int64
			b.ReportAllocs()
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				for _, path := range paths {
					for request := 0; request < 3; request++ {
						starts.Add(1)
						time.Sleep(100 * time.Microsecond)
						_ = path
					}
				}
			}
			b.ReportMetric(float64(starts.Load())/float64(b.N), "process_starts/op")
		})
		b.Run(fmt.Sprintf("after-%d", files), func(b *testing.B) {
			var starts atomic.Int64
			b.ReportAllocs()
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				ctx := context.Background()
				s := &ScannerService{statFile: os.Stat, probeMediaFileContext: func(context.Context, string) ([]byte, error) {
					starts.Add(1)
					time.Sleep(100 * time.Microsecond)
					return []byte("ok"), nil
				}, probeGovernor: newProcessGovernor(ctx, 2), probeScope: newProbeScope(ctx), metadataWorkerCtx: ctx}
				for _, path := range paths {
					for request := 0; request < 3; request++ {
						_, _ = s.runMediaProbe(path)
					}
				}
				s.probeScope.close()
			}
			b.ReportMetric(float64(starts.Load())/float64(b.N), "process_starts/op")
		})
	}
}
