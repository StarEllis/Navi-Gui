package model

import (
	"strings"
	"testing"
)

func buildTestMedia() *Media {
	return &Media{
		Title:     "铃木一彻的午后",
		OrigTitle: "鈴木一徹",
		Code:      "ABC-123",
		Genres:    "巨乳,单体作品",
		FilePath:  `L:\艾薇\日本\ABC-123-U-4K.mp4`,
		Year:      2025,
	}
}

// hasUnit 判断某个拼音/首字母单元是否存在（单元之间以空格分隔）。
func hasUnit(index, unit string) bool {
	for _, value := range strings.Fields(index) {
		if value == unit {
			return true
		}
	}
	return false
}

// matchesAnchored 复刻 GetMediaList 的匹配规则：只认单元开头。
func matchesAnchored(index, token string) bool {
	for _, value := range strings.Fields(index) {
		if strings.HasPrefix(value, token) {
			return true
		}
	}
	return false
}

func TestBuildMediaSearchFieldsIndexesNameByPinyinAndInitials(t *testing.T) {
	media := buildTestMedia()
	_, pinyinIndex, initialsIndex := BuildMediaSearchFields(media, "铃木一彻")

	if !hasUnit(pinyinIndex, "lingmuyiche") {
		t.Fatalf("整段拼音应该建成一个单元，实际: %q", pinyinIndex)
	}
	if !hasUnit(initialsIndex, "lmyc") {
		t.Fatalf("首字母应该建成一个单元，实际: %q", initialsIndex)
	}
	if !hasUnit(pinyinIndex, "muyiche") {
		t.Fatalf("音节边界上的后缀也应该各存一份，实际: %q", pinyinIndex)
	}
}

// 这是这次修改要解决的核心问题：短 token 以前会在几 KB 的音节串里处处命中。
func TestShortLatinTokenNoLongerMatchesUnrelatedPinyin(t *testing.T) {
	media := buildTestMedia()
	_, pinyinIndex, initialsIndex := BuildMediaSearchFields(media, "铃木一彻")

	if matchesAnchored(pinyinIndex, "ai") {
		t.Fatalf("「铃木一彻」不该被 ai 命中，实际: %q", pinyinIndex)
	}
	if matchesAnchored(initialsIndex, "sky") {
		t.Fatalf("首字母不该被 sky 凭空命中，实际: %q", initialsIndex)
	}
}

func TestPinyinPrefixAndInitialsStillMatchTheName(t *testing.T) {
	media := buildTestMedia()
	_, pinyinIndex, initialsIndex := BuildMediaSearchFields(media, "铃木一彻")

	for _, token := range []string{"ling", "lingmu", "lingmuyiche", "muyiche"} {
		if !matchesAnchored(pinyinIndex, token) {
			t.Errorf("拼音 %q 应该能命中，实际: %q", token, pinyinIndex)
		}
	}
	for _, token := range []string{"lm", "lmyc"} {
		if !matchesAnchored(initialsIndex, token) {
			t.Errorf("首字母 %q 应该能命中，实际: %q", token, initialsIndex)
		}
	}
}

// 拼音索引只覆盖标题和演员名，路径、分类、NFO 附加字段不该进来。
func TestPhoneticIndexExcludesPathAndGenres(t *testing.T) {
	media := &Media{
		Title:    "午后",
		Genres:   "巨乳,单体作品",
		FilePath: `L:\艾薇\日本\ABC-123.mp4`,
	}
	_, pinyinIndex, _ := BuildMediaSearchFields(media, "")

	for _, unwanted := range []string{"juru", "aiwei", "ribbon", "danti"} {
		if strings.Contains(pinyinIndex, unwanted) {
			t.Errorf("拼音索引不该包含 %q，实际: %q", unwanted, pinyinIndex)
		}
	}
	if !hasUnit(pinyinIndex, "wuhou") {
		t.Fatalf("标题拼音应该在，实际: %q", pinyinIndex)
	}
}

// search_text 仍然覆盖全字段，汉字和番号靠它来搜。
func TestSearchTextStillCoversAllFields(t *testing.T) {
	media := buildTestMedia()
	text, _, _ := BuildMediaSearchFields(media, "铃木一彻")

	for _, expected := range []string{"abc 123", "铃木一彻", "巨乳", "2025"} {
		if !strings.Contains(text, expected) {
			t.Errorf("search_text 应该包含 %q，实际: %q", expected, text)
		}
	}
}

func TestPhoneticIndexStaysSmall(t *testing.T) {
	media := buildTestMedia()
	_, pinyinIndex, initialsIndex := BuildMediaSearchFields(media, "铃木一彻")

	// 旧实现对全字段每个汉字算拼音，平均 3072 字符；缩窄之后应该是几百的量级。
	if len(pinyinIndex) > 400 {
		t.Errorf("拼音索引过大 (%d)，说明源头没收窄: %q", len(pinyinIndex), pinyinIndex)
	}
	if len(initialsIndex) > 200 {
		t.Errorf("首字母索引过大 (%d): %q", len(initialsIndex), initialsIndex)
	}
}

func TestContainsHan(t *testing.T) {
	cases := map[string]bool{
		"铃木":     true,
		"lingmu": false,
		"123":    false,
		"4k":     false,
		"":       false,
	}
	for value, want := range cases {
		if got := ContainsHan(value); got != want {
			t.Errorf("ContainsHan(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestTokenizeMediaSearchQuerySplitsHanAndLatin(t *testing.T) {
	tokens := TokenizeMediaSearchQuery("4K 有码 ABC-123")
	want := []string{"4k", "有码", "abc", "123"}
	if len(tokens) != len(want) {
		t.Fatalf("切词结果 = %v, want %v", tokens, want)
	}
	for i := range want {
		if tokens[i] != want[i] {
			t.Fatalf("切词结果 = %v, want %v", tokens, want)
		}
	}
}

// 用户按「有码 / 无码」建了目录来分类，整条路径进索引的话，搜「有码」会命中
// 目录名而不是标签 —— 标签明明已经改成无码破解，搜出来还是它。
func TestSearchTextIndexesFileNameNotDirectories(t *testing.T) {
	media := &Media{
		Title:    "三上悠亚的作品",
		Genres:   "无码破解,4K",
		FilePath: `L:\艾薇\日本\归类完成\有码\三上悠亚\【SSIS-181-4K-C】\SSIS-181-U-4K.mp4`,
	}
	text, _, _ := BuildMediaSearchFields(media, "三上悠亚")

	for _, unwanted := range []string{"有码", "归类完成", "艾薇"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("目录名 %q 不该进 search_text，实际: %q", unwanted, text)
		}
	}
	for _, wanted := range []string{"ssis 181", "4k", "无码破解"} {
		if !strings.Contains(text, wanted) {
			t.Errorf("%q 应该还在 search_text 里，实际: %q", wanted, text)
		}
	}
}

// 文件名本身带的破解标记要保留，它是判断版本的重要线索。
func TestSearchTextKeepsFileNameMarkers(t *testing.T) {
	media := &Media{
		Title:    "示例",
		FilePath: `L:\归类完成\有码\www.98T.la@JUQ-919-U_chf3.mp4`,
	}
	text, _, _ := BuildMediaSearchFields(media, "")

	if strings.Contains(text, "有码") {
		t.Fatalf("目录名不该进索引，实际: %q", text)
	}
	for _, wanted := range []string{"juq 919", "98t"} {
		if !strings.Contains(text, wanted) {
			t.Errorf("文件名里的 %q 应该保留，实际: %q", wanted, text)
		}
	}
}

// NFO 附加字段里的封面链接不该进索引：链接里的长数字串会让短数字 token 处处命中。
func TestSearchTextExcludesNFOURLs(t *testing.T) {
	media := &Media{
		Title: "示例",
		Code:  "SERO-025",
		NfoExtraFields: `{"num":"SERO-025","outline":"露天混浴乱交",` +
			`"poster":"https://cdn.up-timely.com/image/30/content/80546/uGlklXHa6WaCn.jpg",` +
			`"thumb":"https://soski.tv/images/thumbnails/70025464.jpg"}`,
	}
	text, _, _ := BuildMediaSearchFields(media, "")

	for _, unwanted := range []string{"70025464", "cdn", "up timely", "soski", "jpg"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("链接里的 %q 不该进 search_text，实际: %q", unwanted, text)
		}
	}
	for _, wanted := range []string{"sero 025", "露天混浴乱交", "outline"} {
		if !strings.Contains(text, wanted) {
			t.Errorf("%q 应该还在 search_text 里，实际: %q", wanted, text)
		}
	}
}

func TestIsDigitsOnly(t *testing.T) {
	cases := map[string]bool{
		"025":     true,
		"2025":    true,
		"ssis":    false,
		"ssis950": false,
		"4k":      false,
		"三上":      false,
		"":        false,
	}
	for value, want := range cases {
		if got := IsDigitsOnly(value); got != want {
			t.Errorf("IsDigitsOnly(%q) = %v, want %v", value, got, want)
		}
	}
}

// 这是这次修改要解决的核心问题：搜番号尾号 025 以前会把整库 2025 年的片子翻出来。
// 匹配规则复刻 GetMediaList 对纯数字 token 的处理：只认单元开头。
func TestDigitTokenNoLongerMatchesReleaseYear(t *testing.T) {
	media := &Media{
		Title:                 "示例",
		Code:                  "MIDA-039",
		ReleaseDateNormalized: "2025-02-14",
		Year:                  2025,
		NfoExtraFields:        `{"thumb":"https://soski.tv/images/thumbnails/70025464.jpg"}`,
	}
	text, _, _ := BuildMediaSearchFields(media, "")

	if matchesAnchored(text, "025") {
		t.Fatalf("2025 年发行的片子不该被 025 命中，实际: %q", text)
	}
	// 年份本身还要能搜，锚定只是不让它被中段截取。
	if !matchesAnchored(text, "2025") {
		t.Fatalf("搜 2025 仍然应该命中发行年份，实际: %q", text)
	}
	if !matchesAnchored(text, "039") {
		t.Fatalf("番号尾号 039 应该命中，实际: %q", text)
	}
}

// 番号尾号正好是 025 的片子必须还能搜到 —— 连字符归一化成空格之后它就是单元开头。
func TestDigitTokenStillMatchesCodeSuffix(t *testing.T) {
	media := &Media{
		Title:                 "示例",
		Code:                  "SERO-025",
		ReleaseDateNormalized: "2018-06-13",
		Year:                  2018,
	}
	text, _, _ := BuildMediaSearchFields(media, "")

	if !matchesAnchored(text, "025") {
		t.Fatalf("SERO-025 应该被 025 命中，实际: %q", text)
	}
}
