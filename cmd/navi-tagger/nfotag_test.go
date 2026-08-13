package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleNFO = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<movie>
  <plot><![CDATA[剧情 & 简介]]></plot>
  <num>MXGS-884</num>
  <title>MXGS-884 测试标题</title>
  <year>2016</year>
  <tag>HEVC</tag>
  <tag>4K</tag>
  <tag>中文字幕</tag>
  <tag>有码</tag>
  <tag>片商: MAXING</tag>
  <genre>HEVC</genre>
  <genre>4K</genre>
  <genre>中文字幕</genre>
  <genre>有码</genre>
  <genre>片商: MAXING</genre>
  <javbusid>https://www.javbus.com/MXGS-884</javbusid>
</movie>
`

func writeSample(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sample.nfo")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("准备样例失败: %v", err)
	}
	return path
}

func TestWriteStatusReplacesCensoredTagInBothLists(t *testing.T) {
	path := writeSample(t, sampleNFO)

	tags, err := writeStatusToNFO(path, StatusLeaked)
	if err != nil {
		t.Fatalf("writeStatusToNFO() error = %v", err)
	}
	for _, tag := range tags {
		if tag == StatusCensored {
			t.Fatal("有码应该已经被摘掉")
		}
	}
	if tags[len(tags)-1] != StatusLeaked {
		t.Fatalf("末尾应该是无码流出，实际 %q", tags[len(tags)-1])
	}

	data, _ := os.ReadFile(path)
	text := string(data)
	if strings.Contains(text, "<tag>有码</tag>") || strings.Contains(text, "<genre>有码</genre>") {
		t.Fatal("两份列表里的有码都应该被删掉")
	}
	if !strings.Contains(text, "<tag>无码流出</tag>") {
		t.Fatal("tag 列表里应该有无码流出")
	}
	if !strings.Contains(text, "<genre>无码流出</genre>") {
		t.Fatal("genre 列表里应该有无码流出")
	}
}

func TestWriteStatusKeepsUnrelatedContentIntact(t *testing.T) {
	path := writeSample(t, sampleNFO)

	if _, err := writeStatusToNFO(path, StatusCracked); err != nil {
		t.Fatalf("writeStatusToNFO() error = %v", err)
	}

	data, _ := os.ReadFile(path)
	text := string(data)
	for _, fragment := range []string{
		`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`,
		`<plot><![CDATA[剧情 & 简介]]></plot>`,
		`<num>MXGS-884</num>`,
		`<title>MXGS-884 测试标题</title>`,
		`<javbusid>https://www.javbus.com/MXGS-884</javbusid>`,
		`<tag>片商: MAXING</tag>`,
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("不该动的内容被改掉了: %s", fragment)
		}
	}
}

func TestWriteStatusBacksUpOriginalOnce(t *testing.T) {
	path := writeSample(t, sampleNFO)

	if _, err := writeStatusToNFO(path, StatusLeaked); err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}
	backup, err := os.ReadFile(path + ".navibak")
	if err != nil {
		t.Fatalf("应该留下备份: %v", err)
	}
	if string(backup) != sampleNFO {
		t.Fatal("备份内容应该是最原始的版本")
	}

	if _, err := writeStatusToNFO(path, StatusCracked); err != nil {
		t.Fatalf("二次写入失败: %v", err)
	}
	again, _ := os.ReadFile(path + ".navibak")
	if string(again) != sampleNFO {
		t.Fatal("备份不该被后续修改覆盖")
	}
}

func TestWriteStatusClearsAllStatusTags(t *testing.T) {
	path := writeSample(t, strings.Replace(sampleNFO,
		"<tag>有码</tag>", "<tag>有码</tag>\n  <tag>无码破解</tag>", 1))

	tags, err := writeStatusToNFO(path, StatusNone)
	if err != nil {
		t.Fatalf("writeStatusToNFO() error = %v", err)
	}
	for _, tag := range tags {
		if tag == StatusCensored || tag == StatusCracked || tag == StatusLeaked {
			t.Fatalf("码类标签应该全部清掉，还剩 %q", tag)
		}
	}
	if len(tags) != 4 {
		t.Fatalf("其余标签应该原样保留 4 个，实际 %d 个: %v", len(tags), tags)
	}
}

func TestWriteStatusHandlesNFOWithoutTagList(t *testing.T) {
	onlyGenre := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<movie>
  <num>ABC-123</num>
  <genre>4K</genre>
  <genre>有码</genre>
</movie>
`
	path := writeSample(t, onlyGenre)

	if _, err := writeStatusToNFO(path, StatusLeaked); err != nil {
		t.Fatalf("writeStatusToNFO() error = %v", err)
	}
	data, _ := os.ReadFile(path)
	text := string(data)
	if !strings.Contains(text, "<genre>无码流出</genre>") {
		t.Fatal("genre 列表应该被更新")
	}
	if !strings.Contains(text, "<tag>无码流出</tag>") {
		t.Fatal("原本没有 tag 列表时应该补一份出来")
	}
}

func TestWriteStatusIsIdempotent(t *testing.T) {
	path := writeSample(t, sampleNFO)

	if _, err := writeStatusToNFO(path, StatusLeaked); err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}
	first, _ := os.ReadFile(path)
	if _, err := writeStatusToNFO(path, StatusLeaked); err != nil {
		t.Fatalf("重复写入失败: %v", err)
	}
	second, _ := os.ReadFile(path)

	if string(first) != string(second) {
		t.Fatal("重复设置同一个标签不应该再改动文件")
	}
}

// 有些刮削器写出来的 NFO 是压缩成一行的，不能因为不在行首就认不出来。
func TestWriteStatusHandlesMinifiedNFO(t *testing.T) {
	minified := `<?xml version="1.0" encoding="UTF-8"?><movie><num>ABC-123</num>` +
		`<tag>4K</tag><tag>有码</tag><genre>4K</genre><genre>有码</genre></movie>`
	path := writeSample(t, minified)

	if _, err := writeStatusToNFO(path, StatusLeaked); err != nil {
		t.Fatalf("writeStatusToNFO() error = %v", err)
	}

	data, _ := os.ReadFile(path)
	text := string(data)
	if strings.Contains(text, "<tag>有码</tag>") || strings.Contains(text, "<genre>有码</genre>") {
		t.Fatal("有码应该被删掉，而不是留在原地")
	}
	if strings.Count(text, "<tag>无码流出</tag>") != 1 {
		t.Fatalf("无码流出应该只出现一次，实际 %d 次", strings.Count(text, "<tag>无码流出</tag>"))
	}
	if strings.Count(text, "<genre>无码流出</genre>") != 1 {
		t.Fatal("genre 里也应该只出现一次")
	}
	if !strings.Contains(text, "<num>ABC-123</num>") {
		t.Fatal("其他内容不该被动")
	}
}

// tag 和 genre 交替排列时，不能把夹在中间的元素连带删掉。
func TestWriteStatusKeepsInterleavedElements(t *testing.T) {
	interleaved := `<?xml version="1.0" encoding="UTF-8"?>
<movie>
  <tag>4K</tag>
  <genre>4K</genre>
  <tag>有码</tag>
  <genre>有码</genre>
  <tag>中文字幕</tag>
  <genre>中文字幕</genre>
</movie>
`
	path := writeSample(t, interleaved)

	if _, err := writeStatusToNFO(path, StatusCracked); err != nil {
		t.Fatalf("writeStatusToNFO() error = %v", err)
	}

	data, _ := os.ReadFile(path)
	text := string(data)
	for _, fragment := range []string{
		"<tag>4K</tag>", "<genre>4K</genre>",
		"<tag>中文字幕</tag>", "<genre>中文字幕</genre>",
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("夹在中间的元素被误删了: %s", fragment)
		}
	}
	if strings.Contains(text, "有码</tag>") || strings.Contains(text, "有码</genre>") {
		t.Fatal("有码应该被删干净")
	}
	if strings.Count(text, "<tag>无码破解</tag>") != 1 || strings.Count(text, "<genre>无码破解</genre>") != 1 {
		t.Fatal("新标签应该各补一条，不能重复")
	}
}

func TestReadTagListFallsBackToGenre(t *testing.T) {
	tags := readTagList(`<movie><genre>4K</genre><genre>有码</genre></movie>`)
	if len(tags) != 2 || tags[1] != "有码" {
		t.Fatalf("没有 tag 时应该读 genre，实际 %v", tags)
	}
}

func TestFirstElementTextUnwrapsCDATA(t *testing.T) {
	if got := firstElementText(sampleNFO, "plot"); got != "剧情 & 简介" {
		t.Fatalf("CDATA 应该被剥掉，实际 %q", got)
	}
	if got := firstElementText(sampleNFO, "num"); got != "MXGS-884" {
		t.Fatalf("num 解析错误，实际 %q", got)
	}
}

// 用户库里真实存在的文件名，破解版必须全部认出来，普通片一个都不能误伤。
func TestLooksCrackedOnRealFilenames(t *testing.T) {
	cracked := []string{
		"JUR-403-U-4K.mp4",
		"www.98T.la@JUQ-919-U_chf3_iris2.mp4",
		"www.98T.la@PRTD-010-UC_iris2.mkv",
		"佐佐波绫-美唇-200GANA-1581.restored.mp4",
		"MIDA-366-破解-C.mp4",
		"ipz-046-uncensored.mp4",
		"STZY-019-U-4K.mp4",
		"[marketingjl.com]@FTAV-004-U-4K.mp4",
	}
	for _, name := range cracked {
		if !looksCracked(name) {
			t.Errorf("应该认出是破解版: %s", name)
		}
	}

	normal := []string{
		"MXGS-884-C-4K.mp4",
		"EBOD-113-C-4K.mp4",
		"RKI-111-4K-cd7.mp4",
		"SONE-978-4K.mp4",
		"AOZ-309z.mp4",
		"www.98T.la@ABP-353@BVPP4X(STD)_iris2_watermusk.mp4",
		"SOME-001-UHD.mp4",
		"MOVIE-ULTRA-4K.mp4",
	}
	for _, name := range normal {
		if looksCracked(name) {
			t.Errorf("不该误判成破解版: %s", name)
		}
	}
}
