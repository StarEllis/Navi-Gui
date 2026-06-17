package service

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

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
