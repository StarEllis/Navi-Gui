package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"navi-desktop/model"
	"navi-desktop/service"
)

type blockingImageResponseWriter struct {
	header  http.Header
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *blockingImageResponseWriter) Header() http.Header { return w.header }
func (w *blockingImageResponseWriter) WriteHeader(int)     {}
func (w *blockingImageResponseWriter) Write(data []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	<-w.release
	return len(data), nil
}

func assertImageResponseHoldsCacheReservation(t *testing.T, cache *service.ArtworkCache, path string, serve func(http.ResponseWriter)) {
	t.Helper()
	w := &blockingImageResponseWriter{header: make(http.Header), started: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	go func() { serve(w); close(done) }()
	select {
	case <-w.started:
	case <-time.After(time.Second):
		t.Fatal("image response did not start")
	}
	if _, err := cache.Cleanup(context.Background(), 0, 0, 0, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("cache was removed during response: %v", err)
	}
	close(w.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("image response did not finish")
	}
	if _, err := cache.Cleanup(context.Background(), 0, 0, 0, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("released cache was not evicted: %v", err)
	}
}

func TestLocalAndJellyfinImageResponsesHoldArtworkReservation(t *testing.T) {
	t.Run("local", func(t *testing.T) {
		cacheBase := filepath.Join(t.TempDir(), "cache")
		cache := service.NewArtworkCache(cacheBase, nil)
		t.Cleanup(cache.Shutdown)
		path := filepath.Join(cacheBase, "artwork", "poster", "local", "item.jpg")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("image"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := cache.RebuildIndex(context.Background()); err != nil {
			t.Fatal(err)
		}
		handler := &LocalFileHandler{reserve: cache.Reserve}
		request := httptest.NewRequest("GET", "/local/"+url.PathEscape(path), nil)
		assertImageResponseHoldsCacheReservation(t, cache, path, func(w http.ResponseWriter) { handler.ServeHTTP(w, request) })
	})

	t.Run("jellyfin", func(t *testing.T) {
		app := newTestApp(t)
		seedJellyfinMovies(t, app, 1)
		cacheBase := filepath.Join(t.TempDir(), "cache")
		cache := service.NewArtworkCache(cacheBase, nil)
		t.Cleanup(cache.Shutdown)
		app.artworkCache = cache
		path := filepath.Join(cacheBase, "artwork", "poster", "media-000000", "item.jpg")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("image"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := cache.RebuildIndex(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := app.db.Model(&model.Media{}).Where("id = ?", "media-000000").Update("poster_path", path).Error; err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest("GET", "/Items/media:media-000000/Images/Primary", nil)
		request.SetPathValue("itemId", "media:media-000000")
		request.SetPathValue("imageType", "Primary")
		handler := app.handleJellyfinImage(&DesktopSettings{})
		assertImageResponseHoldsCacheReservation(t, cache, path, func(w http.ResponseWriter) { handler.ServeHTTP(w, request) })
	})
}
