package service

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
	"navi-desktop/model"
)

func loadEditorForSave(t *testing.T, service *NFOService, path string) *NFOEditorData {
	t.Helper()
	data, err := service.LoadEditorData(path, &model.Media{FilePath: strings.TrimSuffix(path, filepath.Ext(path)) + ".mp4"})
	if err != nil {
		t.Fatalf("load editor data: %v", err)
	}
	return data
}

func assertFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read NFO: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("NFO bytes changed\nwant: %q\n got: %q", want, got)
	}
}

func assertNoNFOTempFiles(t *testing.T, path string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*"))
	if err != nil {
		t.Fatalf("glob temporary NFO files: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary NFO files were not cleaned: %v", matches)
	}
}

func TestSaveEditorDataPreservesUnknownXMLAndActorDetails(t *testing.T) {
	service := NewNFOService(zap.NewNop().Sugar())
	original := []byte("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\r\n" +
		"<movie source=\"other-app\">\r\n" +
		"  <title lang=\"zh\">Old title</title>\r\n" +
		"  <set><name>Original collection</name><overview>keep set details</overview></set>\r\n" +
		"  <genre>Drama</genre><tag>Legacy tag</tag><studio>Original studio</studio>\r\n" +
		"  <premiered>2020-01-02</premiered><release>2020-01-03</release>\r\n" +
		"  <uniqueid type=\"tmdb\" default=\"true\">12345</uniqueid>\r\n" +
		"  <provider name=\"vendor\" id=\"provider-7\"/>\r\n" +
		"  <ratings><rating name=\"imdb\"><value>8.5</value></rating></ratings>\r\n" +
		"  <custom-field provider=\"vendor\"><nested>keep &amp; safe</nested></custom-field>\r\n" +
		"  <actor id=\"person-1\"><name>Alice</name><role>Lead</role><thumb aspect=\"poster\">alice.jpg</thumb><order>7</order><custom>keep</custom></actor>\r\n" +
		"</movie>\r\n")
	path := writeTempNFO(t, string(original))
	data := loadEditorForSave(t, service, path)
	data.Title = "新标题 & <特别>"
	data.UpdatedFields = []string{"title"}

	if err := service.SaveEditorData(path, data); err != nil {
		t.Fatalf("save editor data: %v", err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved NFO: %v", err)
	}
	for _, preserved := range []string{
		`<movie source="other-app">`,
		`<set><name>Original collection</name><overview>keep set details</overview></set>`,
		`<genre>Drama</genre><tag>Legacy tag</tag><studio>Original studio</studio>`,
		`<premiered>2020-01-02</premiered><release>2020-01-03</release>`,
		`<uniqueid type="tmdb" default="true">12345</uniqueid>`,
		`<provider name="vendor" id="provider-7"/>`,
		`<ratings><rating name="imdb"><value>8.5</value></rating></ratings>`,
		`<custom-field provider="vendor"><nested>keep &amp; safe</nested></custom-field>`,
		`<actor id="person-1"><name>Alice</name><role>Lead</role><thumb aspect="poster">alice.jpg</thumb><order>7</order><custom>keep</custom></actor>`,
	} {
		if !bytes.Contains(saved, []byte(preserved)) {
			t.Fatalf("saved NFO lost preserved XML %q: %s", preserved, saved)
		}
	}
	if !bytes.Contains(saved, []byte(`<title lang="zh">新标题 &amp; &lt;特别&gt;</title>`)) {
		t.Fatalf("title was not updated safely: %s", saved)
	}
	if bytes.Contains(bytes.ReplaceAll(saved, []byte("\r\n"), nil), []byte("\n")) {
		t.Fatalf("CRLF formatting was not preserved: %q", saved)
	}
}

func TestSaveEditorDataDoesNotTreatNamespacedExtensionsAsStandardFields(t *testing.T) {
	service := NewNFOService(zap.NewNop().Sugar())
	original := `<movie xmlns:vendor="urn:vendor">
  <title>Standard title</title>
  <vendor:title vendor:source="external">Extension title</vendor:title>
  <vendor:actor><vendor:name>Extension actor</vendor:name></vendor:actor>
</movie>
`
	path := writeTempNFO(t, original)
	data := loadEditorForSave(t, service, path)
	data.Title = "Updated standard title"
	data.UpdatedFields = []string{"title"}

	if err := service.SaveEditorData(path, data); err != nil {
		t.Fatalf("save namespaced NFO: %v", err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read namespaced NFO: %v", err)
	}
	if !bytes.Contains(saved, []byte(`<vendor:title vendor:source="external">Extension title</vendor:title>`)) {
		t.Fatalf("namespaced extension title was modified: %s", saved)
	}
	if !bytes.Contains(saved, []byte(`<title>Updated standard title</title>`)) {
		t.Fatalf("standard title was not updated: %s", saved)
	}
	editorData, err := service.LoadEditorData(path, &model.Media{Title: "Database title"})
	if err != nil {
		t.Fatalf("load namespaced NFO for editing: %v", err)
	}
	if editorData.Title != "Updated standard title" {
		t.Fatalf("namespaced extension overrode standard editor title: %q", editorData.Title)
	}

	metadata, err := service.GetActorMetadataFromNFO(path)
	if err != nil {
		t.Fatalf("read namespaced actor metadata: %v", err)
	}
	if metadata.ActorsPresent || len(metadata.Actors) != 0 {
		t.Fatalf("namespaced actor extension became authoritative: %+v", metadata)
	}
}

func TestSaveEditorDataPreservesDetailsForUnchangedActors(t *testing.T) {
	service := NewNFOService(zap.NewNop().Sugar())
	original := `<movie>
  <title>Movie</title>
  <actor source="legacy"><name>Alice</name><role>Lead</role><thumb>alice.jpg</thumb><order>4</order><unknown>keep</unknown></actor>
</movie>
`
	path := writeTempNFO(t, original)
	data := loadEditorForSave(t, service, path)
	data.Actors = "Alice / Bob"
	data.UpdatedFields = []string{"actors"}

	if err := service.SaveEditorData(path, data); err != nil {
		t.Fatalf("save actors: %v", err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved NFO: %v", err)
	}
	if !bytes.Contains(saved, []byte(`<actor source="legacy"><name>Alice</name><role>Lead</role><thumb>alice.jpg</thumb><order>4</order><unknown>keep</unknown></actor>`)) {
		t.Fatalf("existing actor details were lost: %s", saved)
	}
	if !bytes.Contains(saved, []byte(`<actor><name>Bob</name></actor>`)) {
		t.Fatalf("new actor was not added: %s", saved)
	}
}

func TestSaveEditorDataPreservesNestedActorContainerDetails(t *testing.T) {
	service := NewNFOService(zap.NewNop().Sugar())
	original := `<movie>
  <title>Movie</title>
  <actors provider="legacy">
    <actor id="alice"><name>Alice</name><role>Lead</role><thumb>alice.jpg</thumb><unknown>keep</unknown></actor>
    <container-extension>keep container</container-extension>
  </actors>
</movie>
`
	path := writeTempNFO(t, original)
	data := loadEditorForSave(t, service, path)
	data.Actors = "Alice / Bob"
	data.UpdatedFields = []string{"actors"}

	if err := service.SaveEditorData(path, data); err != nil {
		t.Fatalf("save nested actors: %v", err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read nested actor NFO: %v", err)
	}
	for _, preserved := range []string{
		`<actors provider="legacy">`,
		`<actor id="alice"><name>Alice</name><role>Lead</role><thumb>alice.jpg</thumb><unknown>keep</unknown></actor>`,
		`<container-extension>keep container</container-extension>`,
	} {
		if !bytes.Contains(saved, []byte(preserved)) {
			t.Fatalf("nested actor XML %q was not preserved: %s", preserved, saved)
		}
	}
	if !bytes.Contains(saved, []byte(`<actor><name>Bob</name></actor>`)) {
		t.Fatalf("new nested actor was not added: %s", saved)
	}
}

func TestSaveEditorDataDistinguishesMissingAndExplicitEmptyFields(t *testing.T) {
	service := NewNFOService(zap.NewNop().Sugar())
	path := writeTempNFO(t, `<movie><title>Old</title><plot>Keep me</plot></movie>`)
	data := loadEditorForSave(t, service, path)
	data.Title = "New"
	data.Plot = ""
	data.UpdatedFields = []string{"title"}
	if err := service.SaveEditorData(path, data); err != nil {
		t.Fatalf("save with missing plot: %v", err)
	}
	saved, _ := os.ReadFile(path)
	if !bytes.Contains(saved, []byte(`<plot>Keep me</plot>`)) {
		t.Fatalf("field omitted from patch was changed: %s", saved)
	}

	data = loadEditorForSave(t, service, path)
	data.Plot = ""
	data.UpdatedFields = []string{"plot"}
	if err := service.SaveEditorData(path, data); err != nil {
		t.Fatalf("save explicit empty plot: %v", err)
	}
	saved, _ = os.ReadFile(path)
	if bytes.Contains(saved, []byte(`Keep me`)) || !bytes.Contains(saved, []byte(`<plot></plot>`)) {
		t.Fatalf("explicit empty plot did not clear the value: %s", saved)
	}
}

func TestLoadEditorDataKeepsExplicitEmptyValues(t *testing.T) {
	service := NewNFOService(zap.NewNop().Sugar())
	path := writeTempNFO(t, `<movie><title/><num/><publisher/><maker/><genre/><actors/><plot/><runtime/><rating/></movie>`)
	media := &model.Media{
		FilePath:              filepath.Join(t.TempDir(), "OLD-001.mp4"),
		Title:                 "Old title",
		Code:                  "OLD-001",
		Label:                 "Old publisher",
		Maker:                 "Old maker",
		Genres:                "Old genre",
		Actor:                 "Old actor",
		Overview:              "Old plot",
		Runtime:               100,
		Rating:                8,
		ReleaseDateNormalized: "2020-01-01",
	}
	data, err := service.LoadEditorData(path, media)
	if err != nil {
		t.Fatalf("load explicit empty editor data: %v", err)
	}
	for name, value := range map[string]string{
		"title": data.Title, "code": data.Code, "publisher": data.Publisher,
		"maker": data.Maker, "genres": data.Genres, "actors": data.Actors,
		"plot": data.Plot, "runtime": data.Runtime, "rating": data.Rating,
	} {
		if value != "" {
			t.Fatalf("explicit empty %s fell back to %q", name, value)
		}
	}
}

func TestLoadEditorDataCanonicalEmptyOverridesAliases(t *testing.T) {
	service := NewNFOService(zap.NewNop().Sugar())
	path := writeTempNFO(t, `<movie>
  <releasedate/><premiered>2020-01-02</premiered>
  <plot/><outline>Old outline</outline>
  <maker/><studio>Old studio</studio>
  <publisher/><label>Old label</label>
</movie>`)
	data, err := service.LoadEditorData(path, &model.Media{
		ReleaseDateNormalized: "2019-01-01",
		Overview:              "Database plot",
		Maker:                 "Database maker",
		Label:                 "Database label",
	})
	if err != nil {
		t.Fatalf("load editor data: %v", err)
	}
	for name, value := range map[string]string{
		"release date": data.ReleaseDate,
		"plot":         data.Plot,
		"maker":        data.Maker,
		"publisher":    data.Publisher,
	} {
		if value != "" {
			t.Fatalf("canonical empty %s was replaced by alias %q", name, value)
		}
	}
}

func TestActorPresenceRequiresValidActorOrExplicitEmptyCollection(t *testing.T) {
	service := NewNFOService(zap.NewNop().Sugar())
	tests := []struct {
		name    string
		xml     string
		present bool
		actors  int
	}{
		{name: "missing", xml: `<movie><title>Movie</title></movie>`},
		{name: "explicit empty collection", xml: `<movie><title>Movie</title><actors/></movie>`, present: true},
		{name: "damaged empty actor", xml: `<movie><title>Movie</title><actor/></movie>`},
		{name: "valid actor", xml: `<movie><title>Movie</title><actor><name>Alice</name></actor></movie>`, present: true, actors: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metadata, err := service.GetActorMetadataFromNFO(writeTempNFO(t, tt.xml))
			if err != nil {
				t.Fatalf("get actor metadata: %v", err)
			}
			if metadata.ActorsPresent != tt.present || len(metadata.Actors) != tt.actors {
				t.Fatalf("unexpected actor presence: %+v", metadata)
			}
		})
	}
}

func TestInvalidXMLDoesNotClearExistingMetadata(t *testing.T) {
	service := NewNFOService(zap.NewNop().Sugar())
	path := writeTempNFO(t, `<movie><title></title><actor><name>Alice</name></actor>`)
	media := &model.Media{Title: "Existing", Overview: "Existing plot", Rating: 7.5}
	if err := service.ParseMovieNFO(path, media); err == nil {
		t.Fatal("expected invalid XML parse error")
	}
	if media.Title != "Existing" || media.Overview != "Existing plot" || media.Rating != 7.5 {
		t.Fatalf("invalid XML changed media fields: %+v", media)
	}
	if _, err := service.GetActorMetadataFromNFO(path); err == nil {
		t.Fatal("expected invalid XML actor parse error")
	}
}

func TestParseMovieNFOMatchesMainMissingAndEmptyBehavior(t *testing.T) {
	service := NewNFOService(zap.NewNop().Sugar())
	media := &model.Media{Title: "Existing", Overview: "Existing plot", Rating: 8, Genres: "Existing genre", Studio: "Existing studio"}
	missingPath := writeTempNFO(t, `<movie><tagline>Only supplied field</tagline></movie>`)
	if err := service.ParseMovieNFO(missingPath, media); err != nil {
		t.Fatalf("parse NFO with missing fields: %v", err)
	}
	if media.Title != "Existing" || media.Overview != "Existing plot" || media.Rating != 8 || media.Genres != "Existing genre" || media.Studio != "Existing studio" {
		t.Fatalf("missing fields did not preserve existing values: %+v", media)
	}

	emptyPath := writeTempNFO(t, `<movie><title/><plot/><rating/><genre/><studio/></movie>`)
	if err := service.ParseMovieNFO(emptyPath, media); err != nil {
		t.Fatalf("parse NFO with explicit empty fields: %v", err)
	}
	if media.Title != "Existing" || media.Overview != "Existing plot" || media.Rating != 8 || media.Genres != "" || media.Studio != "Existing studio" {
		t.Fatalf("explicit empty fields did not match main parser behavior: %+v", media)
	}
}

func TestParseMovieNFOKeepsExistingDerivedFields(t *testing.T) {
	service := NewNFOService(zap.NewNop().Sugar())
	media := &model.Media{
		FilePath: "C:/media/OLD-001.mp4",
		Code:     "OLD-001",
		Maker:    "Old maker",
		Label:    "Old publisher",
	}
	path := writeTempNFO(t, `<movie><num>NEW-002</num><maker>New maker</maker><publisher>New publisher</publisher></movie>`)
	if err := service.ParseMovieNFO(path, media); err != nil {
		t.Fatalf("parse updated derived fields: %v", err)
	}
	if media.Code != "OLD-001" || media.CodePrefix != "OLD" || media.Maker != "Old maker" || media.Label != "Old publisher" {
		t.Fatalf("scan parser replaced existing derived fields: %+v", media)
	}

	emptyPath := writeTempNFO(t, `<movie><num/><maker/><publisher/></movie>`)
	if err := service.ParseMovieNFO(emptyPath, media); err != nil {
		t.Fatalf("parse cleared derived fields: %v", err)
	}
	if media.Code != "OLD-001" || media.CodePrefix != "OLD" || media.Maker != "Old maker" || media.Label != "Old publisher" {
		t.Fatalf("explicit empty derived fields replaced existing values: %+v", media)
	}
}

func TestSaveEditorDataCreatesMissingNFOWithoutChangedFields(t *testing.T) {
	service := NewNFOService(zap.NewNop().Sugar())
	path := filepath.Join(t.TempDir(), "new.nfo")
	data := &NFOEditorData{
		NFOPath:           path,
		Title:             "New movie",
		Code:              "NEW-001",
		Maker:             "New maker",
		Actors:            "Alice",
		Plot:              "New plot",
		SourceFingerprint: missingNFOFingerprint,
	}
	if err := service.SaveEditorData(path, data); err != nil {
		t.Fatalf("create missing NFO: %v", err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read created NFO: %v", err)
	}
	for _, expected := range []string{
		`<title>New movie</title>`, `<num>NEW-001</num>`, `<maker>New maker</maker>`,
		`<actor><name>Alice</name></actor>`, `<plot>New plot</plot>`,
	} {
		if !bytes.Contains(saved, []byte(expected)) {
			t.Fatalf("created NFO is missing %q: %s", expected, saved)
		}
	}
	assertNoNFOTempFiles(t, path)
}

func TestSaveEditorDataWritesCaseOnlyListChanges(t *testing.T) {
	service := NewNFOService(zap.NewNop().Sugar())
	path := writeTempNFO(t, `<movie>
  <director source="legacy">alice director</director>
  <genre source="legacy">drama</genre>
  <actor source="legacy"><name>alice actor</name><role>Lead</role><thumb>alice.jpg</thumb></actor>
</movie>`)
	data := loadEditorForSave(t, service, path)
	data.Director = "Alice Director"
	data.Genres = "Drama"
	data.Actors = "Alice Actor"
	data.UpdatedFields = []string{"director", "genres", "actors"}

	if err := service.SaveEditorData(path, data); err != nil {
		t.Fatalf("save case-only list changes: %v", err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved NFO: %v", err)
	}
	for _, expected := range []string{
		`<director source="legacy">Alice Director</director>`,
		`<genre source="legacy">Drama</genre>`,
		`<actor source="legacy"><name>Alice Actor</name><role>Lead</role><thumb>alice.jpg</thumb></actor>`,
	} {
		if !bytes.Contains(saved, []byte(expected)) {
			t.Fatalf("case-only change or extension metadata was lost for %q: %s", expected, saved)
		}
	}
}

func TestSaveEditorDataExpandsSelfClosingMovieRoot(t *testing.T) {
	service := NewNFOService(zap.NewNop().Sugar())
	path := writeTempNFO(t, `<movie/>`)
	data := loadEditorForSave(t, service, path)
	data.Title = "New title"
	data.UpdatedFields = []string{"title"}

	if err := service.SaveEditorData(path, data); err != nil {
		t.Fatalf("save self-closing movie root: %v", err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved NFO: %v", err)
	}
	if !bytes.Contains(saved, []byte(`<movie><title>New title</title></movie>`)) {
		t.Fatalf("new field was not inserted inside expanded root: %s", saved)
	}
	if err := service.validateNFOContent(saved); err != nil {
		t.Fatalf("expanded NFO is invalid: %v", err)
	}
	assertNoNFOTempFiles(t, path)
}

func TestSaveEditorDataRejectsUnsupportedLayoutsWithoutChangingFile(t *testing.T) {
	tests := []struct {
		name     string
		original string
	}{
		{
			name:     "scalar has nested element",
			original: `<movie><title>Old<vendor:meta xmlns:vendor="urn:vendor">keep</vendor:meta></title></movie>`,
		},
		{
			name:     "duplicate language variants",
			original: `<movie><title lang="en">Old</title><title lang="zh">Legacy</title></movie>`,
		},
		{
			name:     "prefixed root namespace",
			original: `<k:movie xmlns:k="urn:kodi"><k:custom>keep</k:custom></k:movie>`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := NewNFOService(zap.NewNop().Sugar())
			original := []byte(tt.original)
			path := writeTempNFO(t, tt.original)
			data := loadEditorForSave(t, service, path)
			data.Title = "New title"
			data.UpdatedFields = []string{"title"}

			err := service.SaveEditorData(path, data)
			if err == nil || !strings.Contains(err.Error(), "UnsupportedNFOLayout") {
				t.Fatalf("expected UnsupportedNFOLayout, got %v", err)
			}
			assertFileBytes(t, path, original)
			assertNoNFOTempFiles(t, path)
		})
	}
}

func TestSaveEditorDataRejectsAmbiguousListAndActorLayouts(t *testing.T) {
	tests := []struct {
		name     string
		original string
		field    string
		value    string
	}{
		{
			name:     "duplicate director language variants",
			original: `<movie><director lang="en">Alice</director><director lang="fr">Alice</director></movie>`,
			field:    "director",
			value:    "ALICE",
		},
		{
			name:     "duplicate genre source variants",
			original: `<movie><genre source="one">Drama</genre><genre source="two">Drama</genre></movie>`,
			field:    "genres",
			value:    "DRAMA",
		},
		{
			name:     "duplicate tag variants",
			original: `<movie><tag source="one">Legacy</tag><tag source="two">Legacy</tag></movie>`,
			field:    "genres",
			value:    "LEGACY",
		},
		{
			name: "duplicate actor metadata variants",
			original: `<movie>
  <actor id="lead"><name>Alice</name><role>Lead</role><thumb>lead.jpg</thumb></actor>
  <actor id="guest"><name>Alice</name><role>Guest</role><thumb>guest.jpg</thumb></actor>
</movie>`,
			field: "actors",
			value: "ALICE",
		},
		{
			name: "mixed top-level and contained actors",
			original: `<movie>
  <actor><name>Alice</name><role>Lead</role></actor>
  <actors><actor><name>Bob</name><role>Guest</role></actor></actors>
</movie>`,
			field: "actors",
			value: "ALICE / BOB",
		},
		{
			name: "multiple actor containers",
			original: `<movie>
  <actors source="one"><actor><name>Alice</name></actor></actors>
  <actors source="two"><actor><name>Bob</name></actor></actors>
</movie>`,
			field: "actors",
			value: "ALICE / BOB",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := NewNFOService(zap.NewNop().Sugar())
			original := []byte(tt.original)
			path := writeTempNFO(t, tt.original)
			data := loadEditorForSave(t, service, path)
			switch tt.field {
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

			err := service.SaveEditorData(path, data)
			if err == nil || !strings.Contains(err.Error(), "UnsupportedNFOLayout") {
				t.Fatalf("expected UnsupportedNFOLayout, got %v", err)
			}
			assertFileBytes(t, path, original)
			assertNoNFOTempFiles(t, path)
		})
	}
}

func TestSaveEditorDataFailuresLeaveOriginalUntouched(t *testing.T) {
	original := []byte(`<movie><title>Original</title><custom>keep</custom></movie>`)
	tests := []struct {
		name   string
		inject func(*NFOService)
	}{
		{
			name: "temporary write failure",
			inject: func(service *NFOService) {
				service.writeNFOtemp = func(_ *os.File, data []byte) (int, error) {
					return len(data) / 2, io.ErrShortWrite
				}
			},
		},
		{
			name: "replacement failure",
			inject: func(service *NFOService) {
				service.replaceNFO = func(_, _ string) error { return errors.New("injected replacement failure") }
			},
		},
		{
			name: "XML validation failure",
			inject: func(service *NFOService) {
				service.validateNFO = func([]byte) error { return errors.New("injected validation failure") }
			},
		},
		{
			name: "directory permission denied",
			inject: func(service *NFOService) {
				service.createNFOtemp = func(_, _ string) (*os.File, error) { return nil, fs.ErrPermission }
			},
		},
		{
			name: "file occupied",
			inject: func(service *NFOService) {
				service.replaceNFO = func(_, _ string) error { return fs.ErrPermission }
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := NewNFOService(zap.NewNop().Sugar())
			path := writeTempNFO(t, string(original))
			data := loadEditorForSave(t, service, path)
			data.Title = "Changed"
			data.UpdatedFields = []string{"title"}
			tt.inject(service)

			if err := service.SaveEditorData(path, data); err == nil {
				t.Fatal("expected save failure")
			}
			assertFileBytes(t, path, original)
			assertNoNFOTempFiles(t, path)
		})
	}
}

func TestSaveEditorDataRejectsReadOnlyFile(t *testing.T) {
	service := NewNFOService(zap.NewNop().Sugar())
	original := []byte(`<movie><title>Original</title></movie>`)
	path := writeTempNFO(t, string(original))
	data := loadEditorForSave(t, service, path)
	data.Title = "Changed"
	data.UpdatedFields = []string{"title"}
	if err := os.Chmod(path, 0444); err != nil {
		t.Fatalf("make NFO read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0644) })

	if err := service.SaveEditorData(path, data); err == nil {
		t.Fatal("expected read-only save failure")
	}
	assertFileBytes(t, path, original)
}

func TestSaveEditorDataDetectsExternalModification(t *testing.T) {
	service := NewNFOService(zap.NewNop().Sugar())
	path := writeTempNFO(t, `<movie><title>Original</title></movie>`)
	data := loadEditorForSave(t, service, path)
	external := []byte(`<movie><title>External edit</title></movie>`)
	if err := os.WriteFile(path, external, 0644); err != nil {
		t.Fatalf("write external modification: %v", err)
	}
	data.Title = "Editor edit"
	data.UpdatedFields = []string{"title"}

	err := service.SaveEditorData(path, data)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "conflict") {
		t.Fatalf("expected understandable conflict error, got %v", err)
	}
	assertFileBytes(t, path, external)
}

func TestSaveEditorDataDetectsModificationDuringTemporaryWrite(t *testing.T) {
	service := NewNFOService(zap.NewNop().Sugar())
	path := writeTempNFO(t, `<movie><title>Original</title></movie>`)
	data := loadEditorForSave(t, service, path)
	external := []byte(`<movie><title>External during save</title></movie>`)
	service.writeNFOtemp = func(file *os.File, content []byte) (int, error) {
		if err := os.WriteFile(path, external, 0644); err != nil {
			return 0, err
		}
		return file.Write(content)
	}
	data.Title = "Editor edit"
	data.UpdatedFields = []string{"title"}

	err := service.SaveEditorData(path, data)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "conflict") {
		t.Fatalf("expected second conflict check, got %v", err)
	}
	assertFileBytes(t, path, external)
	assertNoNFOTempFiles(t, path)
}

func TestSaveEditorDataRejectsInvalidSourceEncoding(t *testing.T) {
	service := NewNFOService(zap.NewNop().Sugar())
	original := []byte("<movie><title>bad \xff encoding</title></movie>")
	dir := t.TempDir()
	path := filepath.Join(dir, "invalid.nfo")
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatalf("write invalid encoding NFO: %v", err)
	}
	data := &NFOEditorData{
		Title:             "Changed",
		SourceFingerprint: nfoContentFingerprint(original, true),
		UpdatedFields:     []string{"title"},
	}
	if err := service.SaveEditorData(path, data); err == nil {
		t.Fatal("expected invalid source encoding error")
	}
	assertFileBytes(t, path, original)
}

func TestSaveEditorDataPreservesUnicodeSpecialCharactersAndLF(t *testing.T) {
	service := NewNFOService(zap.NewNop().Sugar())
	path := writeTempNFO(t, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<movie>\n  <title>Old</title>\n</movie>\n")
	data := loadEditorForSave(t, service, path)
	data.Title = "电影 & café <最终版>"
	data.UpdatedFields = []string{"title"}
	if err := service.SaveEditorData(path, data); err != nil {
		t.Fatalf("save Unicode NFO: %v", err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved NFO: %v", err)
	}
	if bytes.Contains(saved, []byte("\r\n")) {
		t.Fatalf("LF input was changed to CRLF: %q", saved)
	}
	var movie NFOMovie
	if err := xmlUnmarshalForTest(saved, &movie); err != nil {
		t.Fatalf("saved Unicode XML is invalid: %v", err)
	}
	if movie.Title != data.Title {
		t.Fatalf("Unicode title did not round-trip: %q", movie.Title)
	}
	assertNoNFOTempFiles(t, path)
}

func xmlUnmarshalForTest(data []byte, target interface{}) error {
	return NewNFOService(zap.NewNop().Sugar()).unmarshalNFOXML(data, target, "test.nfo")
}
