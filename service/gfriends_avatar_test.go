package service

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"navi-desktop/model"
)

func TestGfriendsAvatarServiceMatchesAliasAndCachesActorAvatar(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/Filetree.json":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{
				"Information": {"TotalNum": "1", "TotalSize": "1", "Timestamp": "1"},
				"Content": {
					"StudioA": {
						"妃月由衣.jpg": "妃月るい.jpg?t=1700000000",
						"Hiduki Rui.jpg": "妃月るい.jpg?t=1700000000"
					}
				}
			}`)
		case strings.HasSuffix(r.URL.EscapedPath(), "/Content/StudioA/%E5%A6%83%E6%9C%88%E3%82%8B%E3%81%84.jpg"):
			w.Header().Set("Content-Type", "image/jpeg")
			sourcePath := filepath.Join(cacheDir, "source.jpg")
			writeTestJPEG(t, sourcePath, 800, 1200)
			http.ServeFile(w, r, sourcePath)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	artwork := NewArtworkCache(cacheDir, zap.NewNop().Sugar())
	service := NewGfriendsAvatarService(GfriendsAvatarOptions{
		CacheDir:     cacheDir,
		ArtworkCache: artwork,
		Logger:       zap.NewNop().Sugar(),
		IndexURLs:    []string{server.URL + "/Filetree.json"},
		ContentBases: []string{server.URL + "/Content/"},
	})

	if err := service.RefreshIndexIfNeeded(); err != nil {
		t.Fatalf("refresh index: %v", err)
	}
	candidate, ok := service.FindCandidate("妃月由衣")
	if !ok {
		t.Fatalf("expected alias match")
	}
	if candidate.FileName != "妃月るい.jpg" {
		t.Fatalf("expected canonical target file, got %q", candidate.FileName)
	}

	person := &model.Person{ID: "person-1", Name: "Hiduki Rui"}
	changed, err := service.EnsureActorAvatar(person)
	if err != nil {
		t.Fatalf("ensure actor avatar: %v", err)
	}
	if !changed {
		t.Fatalf("expected profile URL to change")
	}
	if person.ProfileURL == "" {
		t.Fatalf("expected cached profile URL")
	}
	if !strings.Contains(filepath.ToSlash(person.ProfileURL), "/artwork/actor/") {
		t.Fatalf("expected actor cache path, got %s", person.ProfileURL)
	}
	size := decodeImageSize(t, person.ProfileURL)
	if size.X > 240 || size.Y > 240 {
		t.Fatalf("actor image too large: %dx%d", size.X, size.Y)
	}
}

func TestGfriendsAvatarServiceNetworkFailureDoesNotBlockWhenNoIndexExists(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	service := NewGfriendsAvatarService(GfriendsAvatarOptions{
		CacheDir:     cacheDir,
		ArtworkCache: NewArtworkCache(cacheDir, nil),
		Logger:       zap.NewNop().Sugar(),
		IndexURLs:    []string{"http://127.0.0.1:1/Filetree.json"},
		ContentBases: []string{"http://127.0.0.1:1/Content/"},
	})

	person := &model.Person{ID: "person-2", Name: "Missing Actor"}
	changed, err := service.EnsureActorAvatar(person)
	if err != nil {
		t.Fatalf("network failure should not block avatar lookup: %v", err)
	}
	if changed {
		t.Fatalf("expected no change when index cannot be loaded")
	}
	if person.ProfileURL != "" {
		t.Fatalf("expected empty profile URL, got %q", person.ProfileURL)
	}
}

// newCountingAvatarService serves an index with one actor and answers every
// avatar request with status, counting the attempts.
func newCountingAvatarService(t *testing.T, status int) (*GfriendsAvatarService, *int32) {
	t.Helper()
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Filetree.json" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"Content":{"StudioA":{"有馬美玖.jpg":"有馬美玖.jpg"}}}`)
			return
		}
		atomic.AddInt32(&attempts, 1)
		http.Error(w, http.StatusText(status), status)
	}))
	t.Cleanup(server.Close)

	cacheDir := filepath.Join(t.TempDir(), "cache")
	return NewGfriendsAvatarService(GfriendsAvatarOptions{
		CacheDir:     cacheDir,
		ArtworkCache: NewArtworkCache(cacheDir, nil),
		IndexURLs:    []string{server.URL + "/Filetree.json"},
		ContentBases: []string{server.URL + "/Content/"},
	}), &attempts
}

func ensureAvatarTimes(t *testing.T, service *GfriendsAvatarService, times int) {
	t.Helper()
	for attempt := 0; attempt < times; attempt++ {
		changed, err := service.EnsureActorAvatar(&model.Person{ID: "person-1", Name: "有马美玖"})
		if err != nil || changed {
			t.Fatalf("attempt %d: changed=%v err=%v", attempt, changed, err)
		}
	}
}

func TestGfriendsAvatarServiceDoesNotRetryMissingAvatarDuringCooldown(t *testing.T) {
	service, attempts := newCountingAvatarService(t, http.StatusNotFound)
	ensureAvatarTimes(t, service, 3)
	if got := atomic.LoadInt32(attempts); got != 1 {
		t.Fatalf("expected one download attempt, got %d", got)
	}
}

func TestGfriendsAvatarServiceRetriesAfterRateLimit(t *testing.T) {
	service, attempts := newCountingAvatarService(t, http.StatusTooManyRequests)
	ensureAvatarTimes(t, service, 2)
	// Rate limiting is about the source, not the actor: parking the actor for
	// the cooldown would outlast the limit by hours. Each call retries, so both
	// calls spend the full attempt budget.
	if got := atomic.LoadInt32(attempts); got != 2*gfriendsDownloadAttempts {
		t.Fatalf("expected %d attempts, got %d", 2*gfriendsDownloadAttempts, got)
	}
}

func TestGfriendsAvatarServiceRecoversWhenRetrySucceeds(t *testing.T) {
	var attempts int32
	cacheDir := filepath.Join(t.TempDir(), "cache")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Filetree.json" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"Content":{"StudioA":{"有馬美玖.jpg":"有馬美玖.jpg"}}}`)
			return
		}
		// The source refuses once, then answers — the shape of a throttled run.
		if atomic.AddInt32(&attempts, 1) == 1 {
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		sourcePath := filepath.Join(cacheDir, "source.jpg")
		writeTestJPEG(t, sourcePath, 800, 1200)
		http.ServeFile(w, r, sourcePath)
	}))
	defer server.Close()

	service := NewGfriendsAvatarService(GfriendsAvatarOptions{
		CacheDir:     cacheDir,
		ArtworkCache: NewArtworkCache(cacheDir, nil),
		IndexURLs:    []string{server.URL + "/Filetree.json"},
		ContentBases: []string{server.URL + "/Content/"},
	})

	person := &model.Person{ID: "person-1", Name: "有马美玖"}
	changed, err := service.EnsureActorAvatar(person)
	if err != nil || !changed {
		t.Fatalf("expected the retry to fetch the avatar: changed=%v err=%v", changed, err)
	}
	if person.ProfileURL == "" {
		t.Fatal("expected a cached profile URL")
	}
}

func TestGfriendsAvatarServiceRefreshIndexOnlyDownloadsOnceConcurrently(t *testing.T) {
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"Content": {"StudioA": {"Actor.jpg": "Actor.jpg"}}}`)
	}))
	defer server.Close()

	service := NewGfriendsAvatarService(GfriendsAvatarOptions{
		CacheDir:     filepath.Join(t.TempDir(), "cache"),
		ArtworkCache: NewArtworkCache(filepath.Join(t.TempDir(), "artwork"), nil),
		Logger:       zap.NewNop().Sugar(),
		IndexURLs:    []string{server.URL + "/Filetree.json"},
		ContentBases: []string{server.URL + "/Content/"},
	})

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := service.RefreshIndexIfNeeded(); err != nil {
				t.Errorf("refresh index failed: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("expected one index download, got %d", got)
	}
}

func TestGfriendsAvatarServiceMatchesChineseAndJapaneseNameVariants(t *testing.T) {
	service := NewGfriendsAvatarService(GfriendsAvatarOptions{})
	if err := service.loadIndex([]byte(`{
		"Content": {
			"StudioA": {
				"有馬美玖.jpg": "有馬美玖.jpg",
				"仲村みう.jpg": "仲村みう.jpg",
				"桃乃木かな.jpg": "桃乃木かな.jpg",
				"水卜さくら.jpg": "水卜さくら.jpg",
				"さつき芽衣.jpg": "さつき芽衣.jpg",
				"白峰ミウ.jpg": "白峰ミウ.jpg"
			}
		}
	}`), time.Now()); err != nil {
		t.Fatalf("load index: %v", err)
	}

	tests := map[string]string{
		"有马美玖":  "有馬美玖.jpg",
		"仲村美羽":  "仲村みう.jpg",
		"桃乃木香奈": "桃乃木かな.jpg",
		"水卜樱":   "水卜さくら.jpg",
		"沙月芽衣":  "さつき芽衣.jpg",
		"白峰美羽":  "白峰ミウ.jpg",
	}
	for name, expectedFile := range tests {
		candidate, ok := service.FindCandidate(name)
		if !ok {
			t.Fatalf("expected %q to match", name)
		}
		if candidate.FileName != expectedFile {
			t.Fatalf("candidate for %q = %q, want %q", name, candidate.FileName, expectedFile)
		}
	}
}

func TestGfriendsAvatarServiceMatchesChineseNamesOfKanaStageNames(t *testing.T) {
	service := NewGfriendsAvatarService(GfriendsAvatarOptions{})
	if err := service.loadIndex([]byte(`{
		"Content": {
			"StudioA": {
				"天海つばさ.jpg": "天海つばさ.jpg",
				"深田えいみ.jpg": "深田えいみ.jpg",
				"鈴村あいり.jpg": "鈴村あいり.jpg",
				"野々浦暖.jpg": "野々浦暖.jpg"
			}
		}
	}`), time.Now()); err != nil {
		t.Fatalf("load index: %v", err)
	}

	tests := map[string]string{
		"天海翼":  "天海つばさ.jpg",
		"深田咏美": "深田えいみ.jpg",
		"铃村爱里": "鈴村あいり.jpg",
		"野野浦暖": "野々浦暖.jpg",
	}
	for name, expectedFile := range tests {
		candidate, ok := service.FindCandidate(name)
		if !ok {
			t.Fatalf("expected %q to match", name)
		}
		if candidate.FileName != expectedFile {
			t.Fatalf("candidate for %q = %q, want %q", name, candidate.FileName, expectedFile)
		}
	}
}

func TestGfriendsAvatarServiceAliasesNeverShadowIndexedNames(t *testing.T) {
	service := NewGfriendsAvatarService(GfriendsAvatarOptions{})
	// 田中瞳 is both an indexed gfriends name and an alias of Hitomi.
	if err := service.loadIndex([]byte(`{
		"Content": {
			"StudioA": {
				"田中瞳.jpg": "田中瞳.jpg",
				"Hitomi.jpg": "Hitomi.jpg"
			}
		}
	}`), time.Now()); err != nil {
		t.Fatalf("load index: %v", err)
	}

	candidate, ok := service.FindCandidate("田中瞳")
	if !ok {
		t.Fatal("expected 田中瞳 to match")
	}
	if candidate.FileName != "田中瞳.jpg" {
		t.Fatalf("candidate for 田中瞳 = %q, want the indexed 田中瞳.jpg", candidate.FileName)
	}
}

func TestGfriendsAvatarServicePrefersLaterHigherQualityStudio(t *testing.T) {
	service := NewGfriendsAvatarService(GfriendsAvatarOptions{})
	if err := service.loadIndex([]byte(`{
		"Content": {
			"9-Javrave": {"桃乃木かな.jpg": "桃乃木かな.jpg"},
			"0-Hand-Storage": {"桃乃木かな.jpg": "桃乃木かな.jpg"}
		}
	}`), time.Now()); err != nil {
		t.Fatalf("load index: %v", err)
	}

	candidate, ok := service.FindCandidate("桃乃木香奈")
	if !ok {
		t.Fatal("expected 桃乃木香奈 to match")
	}
	if candidate.Studio != "0-Hand-Storage" {
		t.Fatalf("studio = %q, want highest-quality 0-Hand-Storage", candidate.Studio)
	}
}

func TestGfriendsAvatarServiceRejectsNonImageDownload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "not an image")
	}))
	defer server.Close()

	service := NewGfriendsAvatarService(GfriendsAvatarOptions{Client: server.Client()})
	if _, err := service.downloadURL(server.URL + "/avatar.txt"); err == nil {
		t.Fatalf("expected non-image download to be rejected")
	}
}

func TestGfriendsAvatarServiceRejectsOversizedDownload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		fmt.Fprint(w, strings.Repeat("x", maxGfriendsAvatarBytes+1))
	}))
	defer server.Close()

	service := NewGfriendsAvatarService(GfriendsAvatarOptions{Client: server.Client()})
	if _, err := service.downloadURL(server.URL + "/avatar.jpg"); err == nil {
		t.Fatalf("expected oversized download to be rejected")
	}
}

func TestGfriendsAvatarServiceAllowsAvatarAboveTwoMiB(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		fmt.Fprint(w, strings.Repeat("x", (2<<20)+1))
	}))
	defer server.Close()

	service := NewGfriendsAvatarService(GfriendsAvatarOptions{Client: server.Client()})
	data, err := service.downloadURL(server.URL + "/avatar.jpg")
	if err != nil {
		t.Fatalf("expected avatar above two MiB to be allowed: %v", err)
	}
	if len(data) != (2<<20)+1 {
		t.Fatalf("downloaded %d bytes", len(data))
	}
}
