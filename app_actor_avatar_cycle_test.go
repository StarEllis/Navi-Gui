package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
	"navi-desktop/model"
	"navi-desktop/service"
)

// newAvatarCycleApp serves one actor with three avatars spread over two studios,
// which is the shape that makes "换一张" meaningful.
func newAvatarCycleApp(t *testing.T) (*App, model.Person) {
	t.Helper()
	cacheDir := filepath.Join(t.TempDir(), "cache")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Filetree.json" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"Content":{
				"0-StudioA":{"actor.jpg":"actor.jpg"},
				"1-StudioB":{"actor.jpg":"actor.jpg","actor-1.jpg":"actor-1.jpg"}
			}}`)
			return
		}
		if strings.Contains(r.URL.Path, "/Content/") {
			// 每个片商返回不同像素的图，这样缓存文件名（按内容哈希）也不同，
			// 能验证确实换了图，而不是原地打转。
			sourcePath := filepath.Join(cacheDir, strings.ReplaceAll(r.URL.Path, "/", "_")+".jpg")
			writeActorTestJPEG(t, sourcePath)
			http.ServeFile(w, r, sourcePath)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	app := newTestApp(t)
	app.artworkCache = service.NewArtworkCache(cacheDir, zap.NewNop().Sugar())
	app.avatarStore = service.NewActorAvatarStore(cacheDir, zap.NewNop().Sugar())
	app.avatarService = service.NewGfriendsAvatarService(service.GfriendsAvatarOptions{
		CacheDir:     cacheDir,
		ArtworkCache: app.artworkCache,
		Logger:       zap.NewNop().Sugar(),
		IndexURLs:    []string{server.URL + "/Filetree.json"},
		ContentBases: []string{server.URL + "/Content/"},
	})

	person := model.Person{ID: "person-cycle", Name: "actor"}
	if err := app.db.Create(&person).Error; err != nil {
		t.Fatalf("seed person: %v", err)
	}
	return app, person
}

func TestCycleActorAvatarWalksEveryCandidateAndWrapsAround(t *testing.T) {
	app, person := newAvatarCycleApp(t)

	seen := make([]string, 0, 4)
	for step := 1; step <= 4; step++ {
		result, err := app.CycleActorAvatar(person.ID)
		if err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
		if result.Total != 3 {
			t.Fatalf("step %d: total = %d, want 3", step, result.Total)
		}
		wantIndex := step
		if step == 4 {
			// 转完一圈回到第一张。
			wantIndex = 1
		}
		if result.Index != wantIndex {
			t.Fatalf("step %d: index = %d, want %d", step, result.Index, wantIndex)
		}

		var stored model.Person
		if err := app.db.First(&stored, "id = ?", person.ID).Error; err != nil {
			t.Fatalf("step %d: reload person: %v", step, err)
		}
		if stored.ProfileURL != result.ProfileURL {
			t.Fatalf("step %d: profile_url = %q, want %q", step, stored.ProfileURL, result.ProfileURL)
		}
		if strings.TrimSpace(stored.AvatarSource) == "" {
			t.Fatalf("step %d: avatar_source should record which candidate is in use", step)
		}
		seen = append(seen, stored.AvatarSource)
	}

	if seen[0] == seen[1] || seen[1] == seen[2] {
		t.Fatalf("consecutive steps reused the same candidate: %v", seen)
	}
	if seen[3] != seen[0] {
		t.Fatalf("wrap-around landed on %q, want the first candidate %q", seen[3], seen[0])
	}
}

func TestCycleActorAvatarReportsWhenThereIsNothingToCycle(t *testing.T) {
	app, _ := newAvatarCycleApp(t)

	lonely := model.Person{ID: "person-unknown", Name: "Gabbie Carter"}
	if err := app.db.Create(&lonely).Error; err != nil {
		t.Fatalf("seed person: %v", err)
	}
	if _, err := app.CycleActorAvatar(lonely.ID); err == nil ||
		!strings.Contains(err.Error(), "没有收录") {
		t.Fatalf("unknown actor error = %v, want a 没有收录 message", err)
	}

	if _, err := app.CycleActorAvatar("missing-person"); err == nil {
		t.Fatal("expected an error for an unknown person id")
	}
}
