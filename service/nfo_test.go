package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
	"navi-desktop/model"
)

const malformedURLNFO = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<movie>
  <plot><![CDATA[A & B]]></plot>
  <releasedate>2025-09-04</releasedate>
  <premiered>2025-09-04</premiered>
  <release>2025-09-04</release>
  <num>SONE-871</num>
  <title>SONE-871 title</title>
  <originaltitle>SONE-871 original</originaltitle>
  <actor>
    <name>Actor One</name>
  </actor>
  <rating>4.3</rating>
  <year>2025</year>
  <runtime>139</runtime>
  <studio>S1 NO.1 STYLE</studio>
  <maker>S1 NO.1 STYLE</maker>
  <label>S1 NO.1 STYLE</label>
  <genre>4K</genre>
  <tag>无码破解</tag>
  <dmmid>https://video.dmm.co.jp/av/content/?id=sone00871&i3_ref=search&i3_ord=1</dmmid>
</movie>
`

const numericTagNFO = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<movie>
  <title>SDNT-008 title</title>
  <num>SDNT-008</num>
  <year>2025</year>
  <dmmid>https://tv.dmm.co.jp/list/?content=1sdnt00008&i3_ref=search&i3_ord=1</dmmid>
  <7mmtvid>https://7mmtv.sx/zh/reducing-mosaic_content/15046/107SDNT-008.html</7mmtvid>
</movie>
`

const nestedSetSeriesNFO = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<movie>
  <title>MIAA-085 title</title>
  <num>MIAA-085</num>
  <set>
    <name>Super Deluxe Series</name>
  </set>
  <series>Super Deluxe Series</series>
</movie>
`

func writeTempNFO(t *testing.T, content string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "sample.nfo")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write temp nfo: %v", err)
	}
	return path
}

func TestParseMovieNFOToleratesBareAmpersandInURL(t *testing.T) {
	service := NewNFOService(zap.NewNop().Sugar())
	media := &model.Media{FilePath: `C:\videos\SONE-871.mp4`}

	nfoPath := writeTempNFO(t, malformedURLNFO)
	if err := service.ParseMovieNFO(nfoPath, media); err != nil {
		t.Fatalf("ParseMovieNFO returned error: %v", err)
	}

	if media.Title != "SONE-871 title" {
		t.Fatalf("expected title from NFO, got %q", media.Title)
	}
	if media.ReleaseDateNormalized != "2025-09-04" {
		t.Fatalf("expected normalized release date, got %q", media.ReleaseDateNormalized)
	}
	if media.Runtime != 139 {
		t.Fatalf("expected runtime 139, got %d", media.Runtime)
	}
	if media.Overview != "A & B" {
		t.Fatalf("expected CDATA plot to stay intact, got %q", media.Overview)
	}
	if media.Code != "SONE-871" {
		t.Fatalf("expected derived code SONE-871, got %q", media.Code)
	}
	if media.Maker != "S1 NO.1 STYLE" {
		t.Fatalf("expected maker from NFO, got %q", media.Maker)
	}
	if !strings.Contains(media.Genres, "4K") || !strings.Contains(media.Genres, "无码破解") {
		t.Fatalf("expected merged genres/tags, got %q", media.Genres)
	}
	if media.NfoRawXml == "" {
		t.Fatal("expected raw NFO XML to be retained")
	}
}

func TestParseMovieNFOToleratesNumericTagName(t *testing.T) {
	service := NewNFOService(zap.NewNop().Sugar())
	media := &model.Media{FilePath: `C:\videos\SDNT-008-C.mp4`}

	nfoPath := writeTempNFO(t, numericTagNFO)
	if err := service.ParseMovieNFO(nfoPath, media); err != nil {
		t.Fatalf("ParseMovieNFO returned error: %v", err)
	}

	if media.Title != "SDNT-008 title" {
		t.Fatalf("expected title from NFO, got %q", media.Title)
	}
	if media.Year != 2025 {
		t.Fatalf("expected year 2025, got %d", media.Year)
	}
}

func TestGetActorsFromNFOToleratesBareAmpersandInURL(t *testing.T) {
	service := NewNFOService(zap.NewNop().Sugar())
	nfoPath := writeTempNFO(t, malformedURLNFO)

	actors, directors, err := service.GetActorsFromNFO(nfoPath)
	if err != nil {
		t.Fatalf("GetActorsFromNFO returned error: %v", err)
	}
	if len(actors) != 1 || actors[0].Name != "Actor One" {
		t.Fatalf("expected actor from malformed NFO, got %+v", actors)
	}
	if len(directors) != 0 {
		t.Fatalf("expected no directors, got %+v", directors)
	}
}

func TestLoadEditorDataReadsNestedSetSeries(t *testing.T) {
	service := NewNFOService(zap.NewNop().Sugar())
	nfoPath := writeTempNFO(t, nestedSetSeriesNFO)

	data, err := service.LoadEditorData(nfoPath, &model.Media{FilePath: `C:\videos\MIAA-085.mp4`})
	if err != nil {
		t.Fatalf("LoadEditorData returned error: %v", err)
	}

	if data.Series != "Super Deluxe Series" {
		t.Fatalf("expected series from nested set, got %q", data.Series)
	}
}

func TestLoadEditorDataAcceptsUTF8BOM(t *testing.T) {
	service := NewNFOService(zap.NewNop().Sugar())
	content := append([]byte{0xEF, 0xBB, 0xBF}, []byte(nestedSetSeriesNFO)...)
	nfoPath := writeTempNFO(t, string(content))

	data, err := service.LoadEditorData(nfoPath, &model.Media{FilePath: `C:\videos\MIAA-085.mp4`})
	if err != nil {
		t.Fatalf("LoadEditorData returned error for UTF-8 BOM: %v", err)
	}
	if data.Series != "Super Deluxe Series" {
		t.Fatalf("expected series from BOM-prefixed NFO, got %q", data.Series)
	}
}

func TestParseNFOXMLDocumentStillRejectsTextOutsideRoot(t *testing.T) {
	for name, content := range map[string]string{
		"before": "unexpected<movie/>",
		"after":  "<movie/>unexpected",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseNFOXMLDocument([]byte(content), false)
			if err == nil || !strings.Contains(err.Error(), "text outside the root element") {
				t.Fatalf("expected outside-root text error, got %v", err)
			}
		})
	}
}

func TestSaveEditorDataWritesSeriesForEditorRoundTrip(t *testing.T) {
	service := NewNFOService(zap.NewNop().Sugar())
	nfoPath := writeTempNFO(t, nestedSetSeriesNFO)
	loaded := loadEditorForSave(t, service, nfoPath)

	if err := service.SaveEditorData(nfoPath, &NFOEditorData{
		NFOPath:           nfoPath,
		Title:             "MIAA-085 title",
		Code:              "MIAA-085",
		Series:            "Updated Series Name",
		SourceFingerprint: loaded.SourceFingerprint,
		UpdatedFields:     []string{"series"},
	}); err != nil {
		t.Fatalf("SaveEditorData returned error: %v", err)
	}

	content, err := os.ReadFile(nfoPath)
	if err != nil {
		t.Fatalf("read saved nfo: %v", err)
	}
	saved := string(content)
	if !strings.Contains(saved, "<name>Updated Series Name</name>") {
		t.Fatalf("expected nested set name in saved NFO, got %s", saved)
	}
	if !strings.Contains(saved, "<series>Updated Series Name</series>") {
		t.Fatalf("expected series tag in saved NFO, got %s", saved)
	}

	data, err := service.LoadEditorData(nfoPath, &model.Media{FilePath: `C:\videos\MIAA-085.mp4`})
	if err != nil {
		t.Fatalf("LoadEditorData after save returned error: %v", err)
	}
	if data.Series != "Updated Series Name" {
		t.Fatalf("expected saved series after reload, got %q", data.Series)
	}
}
