package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// 刮削器普遍把同一批分类同时写进 <genre> 和 <tag> 两组，中间还夹着别的元素
// 和嵌套的 <fileinfo>。这是用户库里最常见的 NFO 布局。
const mirroredListNFO = `<?xml version="1.0" encoding="utf-8" standalone="yes"?>
<movie>
  <plot><![CDATA[剧情简介]]></plot>
  <title>STAR-424 Cosplay持续强烈高潮</title>
  <actor>
    <name>橘梨紗</name>
    <type>Actor</type>
  </actor>
  <rating>4.5</rating>
  <year>2013</year>
  <genre>HEVC</genre>
  <genre>4K</genre>
  <genre>STAR</genre>
  <genre>有码</genre>
  <genre>片商: SOD</genre>
  <studio>SOD</studio>
  <tag>4K</tag>
  <tag>HEVC</tag>
  <tag>STAR</tag>
  <tag>有码</tag>
  <tag>片商: SOD</tag>
  <fileinfo>
    <streamdetails>
      <video>
        <codec>hevc</codec>
        <width>3840</width>
      </video>
    </streamdetails>
  </fileinfo>
  <num>STAR-424</num>
  <javbusid>https://www.javbus.com/STAR-424</javbusid>
</movie>
`

func writeMirroredNFO(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "STAR-424-4K.nfo")
	if err := os.WriteFile(path, []byte(mirroredListNFO), 0o644); err != nil {
		t.Fatalf("准备样例失败: %v", err)
	}
	return path
}

func saveGenres(t *testing.T, path, genres string) error {
	t.Helper()
	content, _, exists, err := readNFOState(path)
	if err != nil {
		t.Fatalf("readNFOState: %v", err)
	}
	return NewNFOService(zap.NewNop().Sugar()).SaveEditorData(path, &NFOEditorData{
		NFOPath:           path,
		Genres:            genres,
		SourceFingerprint: nfoContentFingerprint(content, exists),
		UpdatedFields:     []string{"genres"},
	})
}

func TestSaveEditorDataAcceptsMirroredGenreAndTagLists(t *testing.T) {
	path := writeMirroredNFO(t)

	// 去掉「有码」、加上「无码破解」—— 用户在类别编辑器里最常做的操作。
	if err := saveGenres(t, path, "HEVC, 4K, STAR, 片商: SOD, 无码破解"); err != nil {
		t.Fatalf("SaveEditorData() error = %v", err)
	}

	data, _ := os.ReadFile(path)
	text := string(data)

	if strings.Contains(text, "<genre>有码</genre>") || strings.Contains(text, "<tag>有码</tag>") {
		t.Fatal("两组列表里的「有码」都应该被删掉")
	}
	if !strings.Contains(text, "<genre>无码破解</genre>") {
		t.Fatal("genre 组应该补上「无码破解」")
	}
	if !strings.Contains(text, "<tag>无码破解</tag>") {
		t.Fatal("tag 组也应该补上「无码破解」")
	}
}

// 之前的实现会把两组合并成一个列表改写，导致其中一组被整体抹掉。
func TestSaveEditorDataKeepsBothListsComplete(t *testing.T) {
	path := writeMirroredNFO(t)

	if err := saveGenres(t, path, "HEVC, 4K, STAR, 片商: SOD, 无码破解"); err != nil {
		t.Fatalf("SaveEditorData() error = %v", err)
	}

	data, _ := os.ReadFile(path)
	text := string(data)

	if got := strings.Count(text, "<genre>"); got != 5 {
		t.Fatalf("genre 应该有 5 条，实际 %d 条\n%s", got, text)
	}
	if got := strings.Count(text, "<tag>"); got != 5 {
		t.Fatalf("tag 应该有 5 条，实际 %d 条\n%s", got, text)
	}
}

func TestSaveEditorDataKeepsSurroundingElementsIntact(t *testing.T) {
	path := writeMirroredNFO(t)

	if err := saveGenres(t, path, "HEVC, 4K, STAR, 片商: SOD, 无码破解"); err != nil {
		t.Fatalf("SaveEditorData() error = %v", err)
	}

	data, _ := os.ReadFile(path)
	text := string(data)
	for _, fragment := range []string{
		"<studio>SOD</studio>",
		"<num>STAR-424</num>",
		"<javbusid>https://www.javbus.com/STAR-424</javbusid>",
		"<codec>hevc</codec>",
		"<width>3840</width>",
		"<plot><![CDATA[剧情简介]]></plot>",
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("夹在列表之间/之外的内容被破坏了: %s\n%s", fragment, text)
		}
	}
}

// 同一组内部出现重复值仍然是真正的歧义，必须继续拒绝。
func TestSaveEditorDataStillRejectsDuplicatesWithinOneList(t *testing.T) {
	duplicated := strings.Replace(mirroredListNFO,
		"  <genre>STAR</genre>\n", "  <genre>STAR</genre>\n  <genre>star</genre>\n", 1)
	path := filepath.Join(t.TempDir(), "dup.nfo")
	if err := os.WriteFile(path, []byte(duplicated), 0o644); err != nil {
		t.Fatal(err)
	}

	err := saveGenres(t, path, "HEVC, 4K, STAR")
	if err == nil {
		t.Fatal("同一组内的重复值应该被拒绝")
	}
	if !strings.Contains(err.Error(), "UnsupportedNFOLayout") {
		t.Fatalf("应该报 UnsupportedNFOLayout，实际: %v", err)
	}
}

// 只有 <genre> 没有 <tag> 的 NFO，不要凭空给人家造出一组 <tag>。
func TestSaveEditorDataDoesNotInventMissingTagList(t *testing.T) {
	genreOnly := `<?xml version="1.0" encoding="utf-8"?>
<movie>
  <num>ABC-123</num>
  <genre>4K</genre>
  <genre>有码</genre>
</movie>
`
	path := filepath.Join(t.TempDir(), "genre-only.nfo")
	if err := os.WriteFile(path, []byte(genreOnly), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := saveGenres(t, path, "4K, 无码破解"); err != nil {
		t.Fatalf("SaveEditorData() error = %v", err)
	}

	data, _ := os.ReadFile(path)
	text := string(data)
	if !strings.Contains(text, "<genre>无码破解</genre>") {
		t.Fatal("genre 组应该被更新")
	}
	if strings.Contains(text, "<tag>") {
		t.Fatalf("原本没有 tag 组就不该新建一组\n%s", text)
	}
}
