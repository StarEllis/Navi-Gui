package service

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestActorAvatarStoreImportFileSurvivesSourceDeletion(t *testing.T) {
	cacheDir := t.TempDir()
	sourcePath := filepath.Join(cacheDir, "source.jpg")
	writeTestJPEG(t, sourcePath, 900, 1400)

	store := NewActorAvatarStore(cacheDir, nil)
	storedPath, err := store.ImportFile("有马美玖", sourcePath)
	if err != nil {
		t.Fatalf("import file: %v", err)
	}
	if err := os.Remove(sourcePath); err != nil {
		t.Fatalf("remove source: %v", err)
	}
	if !fileExists(storedPath) {
		t.Fatalf("expected the imported copy to outlive its source, missing %q", storedPath)
	}

	// The library owns it, and it sits outside the evictable artwork cache.
	if !store.Owns(storedPath) {
		t.Fatalf("store should own %q", storedPath)
	}
	if strings.Contains(filepath.ToSlash(storedPath), "/artwork/") {
		t.Fatalf("imported avatar must not live in the artwork cache: %s", storedPath)
	}
	if size := decodeImageSize(t, storedPath); size.X > 240 || size.Y > 240 {
		t.Fatalf("imported avatar not resized: %dx%d", size.X, size.Y)
	}
}

func TestActorAvatarStoreReplacingAvatarChangesPathAndDropsOldFile(t *testing.T) {
	cacheDir := t.TempDir()
	first := filepath.Join(cacheDir, "first.jpg")
	second := filepath.Join(cacheDir, "second.jpg")
	writeTestJPEG(t, first, 800, 800)
	writeTestJPEG(t, second, 400, 700)

	store := NewActorAvatarStore(cacheDir, nil)
	firstPath, err := store.ImportFile("有马美玖", first)
	if err != nil {
		t.Fatalf("import first: %v", err)
	}
	secondPath, err := store.ImportFile("有马美玖", second)
	if err != nil {
		t.Fatalf("import second: %v", err)
	}

	// A new path is what makes the UI show the new image instead of the cached one.
	if firstPath == secondPath {
		t.Fatal("replacing an avatar should produce a new path")
	}
	if fileExists(firstPath) {
		t.Fatalf("replaced avatar should be removed: %s", firstPath)
	}
	if !fileExists(secondPath) {
		t.Fatalf("expected the new avatar at %s", secondPath)
	}

	if err := store.Remove("有马美玖"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if fileExists(secondPath) {
		t.Fatalf("expected the avatar to be gone after removal")
	}
}

func TestActorAvatarStoreLookupIgnoresIDChurnAndNameVariants(t *testing.T) {
	cacheDir := t.TempDir()
	sourcePath := filepath.Join(cacheDir, "source.jpg")
	writeTestJPEG(t, sourcePath, 700, 700)

	store := NewActorAvatarStore(cacheDir, nil)
	storedPath, err := store.ImportFile("有马美玖", sourcePath)
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	// Keyed by name, so it is found again no matter what id the rebuilt library
	// hands the actor, and whichever spelling the new NFO carries.
	for _, name := range []string{"有马美玖", "有馬美玖", " 有马美玖 "} {
		if got := store.Lookup(name); got != storedPath {
			t.Fatalf("Lookup(%q) = %q, want %q", name, got, storedPath)
		}
	}
	if got := store.Lookup("白桃花"); got != "" {
		t.Fatalf("Lookup of an actor without an imported avatar = %q, want empty", got)
	}
}

func TestActorAvatarStoreAdoptMovesLegacyIDEntryOntoName(t *testing.T) {
	cacheDir := t.TempDir()
	sourcePath := filepath.Join(cacheDir, "source.jpg")
	writeTestJPEG(t, sourcePath, 500, 500)

	// An entry as the first release wrote them: filed under the person id.
	legacyDir := filepath.Join(cacheDir, "actors", "person-uuid-1")
	if err := os.MkdirAll(legacyDir, 0755); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(legacyDir, "abc123.jpg")
	if err := os.WriteFile(legacyPath, mustReadFile(t, sourcePath), 0644); err != nil {
		t.Fatal(err)
	}

	store := NewActorAvatarStore(cacheDir, nil)
	adopted, err := store.Adopt("person-uuid-1", "有马美玖")
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if adopted == "" || adopted != store.Lookup("有马美玖") {
		t.Fatalf("adopted = %q, lookup = %q", adopted, store.Lookup("有马美玖"))
	}
	if fileExists(legacyPath) {
		t.Fatalf("legacy entry should have moved, still at %s", legacyPath)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestActorAvatarStoreImportURLRejectsNonImage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html>not an image</html>")
	}))
	defer server.Close()

	store := NewActorAvatarStore(t.TempDir(), nil)
	if _, err := store.ImportURL("有马美玖", server.URL+"/actor"); err == nil {
		t.Fatal("expected a non-image URL to be rejected")
	}
	if _, err := store.ImportURL("有马美玖", "ftp://example.com/a.jpg"); err == nil {
		t.Fatal("expected a non-http URL to be rejected")
	}
}

func TestActorAvatarStoreImportURLStoresImage(t *testing.T) {
	cacheDir := t.TempDir()
	sourcePath := filepath.Join(cacheDir, "remote.jpg")
	writeTestJPEG(t, sourcePath, 600, 900)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		http.ServeFile(w, r, sourcePath)
	}))
	defer server.Close()

	store := NewActorAvatarStore(cacheDir, nil)
	storedPath, err := store.ImportURL("有马美玖", server.URL+"/actor.jpg")
	if err != nil {
		t.Fatalf("import URL: %v", err)
	}
	if !fileExists(storedPath) {
		t.Fatalf("expected stored avatar at %s", storedPath)
	}
}
