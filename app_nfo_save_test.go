package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveNFOEditorDataUnsupportedLayoutsLeaveDatabaseAndFileUnchanged(t *testing.T) {
	tests := []struct {
		name  string
		xml   string
		field string
		value string
	}{
		{name: "nested scalar", xml: `<movie><title>Movie<vendor:meta xmlns:vendor="urn:vendor">keep</vendor:meta></title></movie>`, field: "title", value: "Changed title"},
		{name: "duplicate variants", xml: `<movie><title lang="en">Movie</title><title lang="zh">Legacy</title></movie>`, field: "title", value: "Changed title"},
		{name: "prefixed namespace", xml: `<k:movie xmlns:k="urn:kodi"><k:title>Movie</k:title></k:movie>`, field: "title", value: "Changed title"},
		{name: "director variants", xml: `<movie><director lang="en">Alice</director><director lang="fr">Alice</director></movie>`, field: "director", value: "ALICE"},
		{name: "genre variants", xml: `<movie><genre source="one">Drama</genre><genre source="two">Drama</genre></movie>`, field: "genres", value: "DRAMA"},
		{name: "tag variants", xml: `<movie><tag source="one">Legacy</tag><tag source="two">Legacy</tag></movie>`, field: "genres", value: "LEGACY"},
		{name: "actor variants", xml: `<movie><actor><name>Alice</name><role>Lead</role></actor><actor><name>Alice</name><role>Guest</role></actor></movie>`, field: "actors", value: "ALICE"},
		{name: "mixed actors", xml: `<movie><actor><name>Alice</name></actor><actors><actor><name>Bob</name></actor></actors></movie>`, field: "actors", value: "ALICE / BOB"},
		{name: "multiple actor containers", xml: `<movie><actors><actor><name>Alice</name></actor></actors><actors><actor><name>Bob</name></actor></actors></movie>`, field: "actors", value: "ALICE / BOB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, mediaID, mediaPath := newDeleteLibraryAppFixture(t, filepath.Join(t.TempDir(), "cache"))
			nfoPath := strings.TrimSuffix(mediaPath, filepath.Ext(mediaPath)) + ".nfo"
			original := []byte(tt.xml)
			if err := os.WriteFile(nfoPath, original, 0644); err != nil {
				t.Fatalf("write NFO: %v", err)
			}
			data, err := app.GetNFOEditorData(mediaID)
			if err != nil {
				t.Fatalf("load editor data: %v", err)
			}
			switch tt.field {
			case "title":
				data.Title = tt.value
			case "director":
				data.Director = tt.value
			case "genres":
				data.Genres = tt.value
			case "actors":
				data.Actors = tt.value
			default:
				t.Fatalf("unsupported test field %q", tt.field)
			}
			data.UpdatedFields = []string{tt.field}

			err = app.SaveNFOEditorData(mediaID, data)
			if err == nil || !strings.Contains(err.Error(), "UnsupportedNFOLayout") {
				t.Fatalf("expected UnsupportedNFOLayout, got %v", err)
			}

			saved, readErr := os.ReadFile(nfoPath)
			if readErr != nil {
				t.Fatalf("read rejected NFO: %v", readErr)
			}
			if !bytes.Equal(saved, original) {
				t.Fatalf("rejected save changed NFO bytes: %q", saved)
			}
			matches, globErr := filepath.Glob(filepath.Join(filepath.Dir(nfoPath), "."+filepath.Base(nfoPath)+".tmp-*"))
			if globErr != nil {
				t.Fatalf("glob NFO temp files: %v", globErr)
			}
			if len(matches) != 0 {
				t.Fatalf("rejected save left temp files: %v", matches)
			}
			media, loadErr := app.loadMediaForNFO(mediaID)
			if loadErr != nil {
				t.Fatalf("reload media: %v", loadErr)
			}
			if media.Title != "Movie" {
				t.Fatalf("rejected save changed database title: %q", media.Title)
			}
			if media.NfoRawXml != "" || media.NfoModTime != nil {
				t.Fatalf("rejected save synchronized NFO state into database: raw=%q mod=%v", media.NfoRawXml, media.NfoModTime)
			}
		})
	}
}
