package main

import (
	"path/filepath"
	"testing"
	"time"

	"navi-desktop/model"
)

func TestInitialLibraryCallsWaitForStartup(t *testing.T) {
	app := newTestApp(t)
	app.startupReady = make(chan struct{})

	library := &model.Library{
		ID:   "startup-library",
		Name: "Startup Library",
		Path: t.TempDir(),
		Type: "movie",
	}
	if err := app.repos.Library.Create(library); err != nil {
		t.Fatalf("create library: %v", err)
	}
	media := &model.Media{
		ID:        "startup-media",
		LibraryID: library.ID,
		Title:     "Startup Media",
		FilePath:  filepath.Join(library.Path, "startup.mkv"),
		MediaType: "movie",
	}
	if err := app.repos.Media.Create(media); err != nil {
		t.Fatalf("create media: %v", err)
	}

	type librariesResult struct {
		libraries []model.Library
		err       error
	}
	type mediaResult struct {
		value interface{}
		err   error
	}
	librariesDone := make(chan librariesResult, 1)
	mediaDone := make(chan mediaResult, 1)
	started := make(chan struct{}, 2)

	go func() {
		started <- struct{}{}
		libraries, err := app.GetLibraries()
		librariesDone <- librariesResult{libraries: libraries, err: err}
	}()
	go func() {
		started <- struct{}{}
		value, err := app.GetMediaList(library.ID, 1, 20, "rating", "desc", "", "", "")
		mediaDone <- mediaResult{value: value, err: err}
	}()

	<-started
	<-started
	select {
	case result := <-librariesDone:
		t.Fatalf("GetLibraries returned before startup completed: %+v", result)
	case <-time.After(30 * time.Millisecond):
	}
	select {
	case result := <-mediaDone:
		t.Fatalf("GetMediaList returned before startup completed: %+v", result)
	case <-time.After(30 * time.Millisecond):
	}

	app.markStartupReady()
	select {
	case result := <-librariesDone:
		if result.err != nil {
			t.Fatalf("GetLibraries after startup: %v", result.err)
		}
		if len(result.libraries) != 1 || result.libraries[0].ID != library.ID {
			t.Fatalf("unexpected libraries result: %+v", result.libraries)
		}
	case <-time.After(time.Second):
		t.Fatal("GetLibraries did not resume after startup completed")
	}
	select {
	case result := <-mediaDone:
		if result.err != nil {
			t.Fatalf("GetMediaList after startup: %v", result.err)
		}
		page, ok := result.value.(map[string]interface{})
		if !ok || page["total"] != int64(1) {
			t.Fatalf("unexpected media result: %#v", result.value)
		}
	case <-time.After(time.Second):
		t.Fatal("GetMediaList did not resume after startup completed")
	}
}
