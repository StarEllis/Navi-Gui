package model

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/mozillazg/go-pinyin"
)

var mediaSearchSeparators = regexp.MustCompile(`[\s_\-./\\\[\](){}#+:;,|]+`)

var mediaSearchURLPattern = regexp.MustCompile(`(?i)[a-z][a-z0-9+.\-]*://[^\s"',]+`)

// stripSearchSourceURLs 把 NFO 附加字段里的封面 / 预告片链接剔出索引。没人会拿
// CDN 路径搜片子，但链接里的长数字串会制造误命中 —— 搜「025」撞进
// thumbnails/70025464.jpg 就是一例。这和 buildPhoneticSegments 不收录路径、
// mediaFileName 只取文件名是同一个判断，之前只落实了一半。
func stripSearchSourceURLs(value string) string {
	if !strings.Contains(value, "://") {
		return value
	}
	return mediaSearchURLPattern.ReplaceAllString(value, " ")
}

// IsDigitsOnly 纯数字 token 需要单独对待：它们做任意位置子串匹配时会撞进
// 发行日期和文件名里的长数字串。
func IsDigitsOnly(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func NormalizeMediaSearchText(value string) string {
	value = strings.ToLower(NormalizeChineseVariants(strings.TrimSpace(value)))
	value = mediaSearchSeparators.ReplaceAllString(value, " ")
	return strings.Join(strings.Fields(value), " ")
}

func TokenizeMediaSearchQuery(value string) []string {
	normalized := NormalizeMediaSearchText(value)
	var tokens []string
	var current strings.Builder
	currentHan := false
	flush := func() {
		if current.Len() == 0 {
			return
		}
		tokens = append(tokens, current.String())
		current.Reset()
	}
	for _, r := range normalized {
		isHan := unicode.Is(unicode.Han, r)
		if !isHan && !unicode.IsLetter(r) && !unicode.IsNumber(r) {
			flush()
			continue
		}
		if current.Len() > 0 && currentHan != isHan {
			flush()
		}
		currentHan = isHan
		current.WriteRune(r)
	}
	flush()
	return tokens
}

// mediaFileName 只取文件名。用户常按「有码 / 无码 / 演员名」建目录来分类，
// 整条路径进索引的话，搜「有码」命中的是目录名而不是标签 —— 标签改对了也没用。
// 目录里的信息本来就通过标签和演员字段进了索引，另外还有专门的目录筛选。
func mediaFileName(path string) string {
	path = strings.TrimSpace(path)
	if index := strings.LastIndexAny(path, `/\`); index >= 0 {
		return path[index+1:]
	}
	return path
}

func BuildMediaSearchSource(media *Media, actorText string) string {
	if media == nil {
		return ""
	}
	parts := []string{
		media.Title,
		media.OrigTitle,
		media.EpisodeTitle,
		media.Code,
		media.Maker,
		media.Label,
		media.Studio,
		media.Genres,
		media.ReleaseDateNormalized,
		mediaFileName(media.FilePath),
		stripSearchSourceURLs(media.NfoExtraFields),
		actorText,
	}
	if media.Year > 0 {
		parts = append(parts, strconv.Itoa(media.Year))
	}
	return strings.Join(parts, "\n")
}

// ContainsHan 用来区分汉字 token 和拉丁 token：拼音两列都是纯 ASCII，
// 汉字 token 去查它们纯属白跑。
func ContainsHan(value string) bool {
	for _, r := range value {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

type phoneticSegment struct {
	text         string
	withSuffixes bool
}

// buildPhoneticSegments 收集需要做拼音索引的片段，只覆盖标题和演员名。
// 文件路径、分类标签、NFO 附加字段不进来 —— 没人会用拼音去搜路径，
// 把它们灌进来只会让索引膨胀到几 KB，短 token 一搜就命中全库。
func buildPhoneticSegments(media *Media, actorText string) []phoneticSegment {
	var segments []phoneticSegment
	// 标题只建整段单元。中文标题本来就能用 search_text 搜到，再给每个音节
	// 都补一份后缀单元，只会撑大索引并让 "ai" 这种短词到处命中。
	for _, part := range []string{media.Title, media.OrigTitle, media.EpisodeTitle} {
		for _, field := range strings.Fields(NormalizeMediaSearchText(part)) {
			segments = append(segments, phoneticSegment{text: field})
		}
	}
	// 人名允许从中间打起：只记得「一彻」也应该能搜到「铃木一彻」。
	for _, field := range strings.Fields(NormalizeMediaSearchText(actorText)) {
		segments = append(segments, phoneticSegment{text: field, withSuffixes: true})
	}
	return segments
}

func BuildMediaSearchFields(media *Media, actorText string) (text, fullPinyin, initials string) {
	if media == nil {
		return "", "", ""
	}
	text = NormalizeMediaSearchText(BuildMediaSearchSource(media, actorText))

	args := pinyin.NewArgs()
	args.Style = pinyin.Normal
	args.Heteronym = false

	var pinyinUnits, initialUnits []string
	for _, segment := range buildPhoneticSegments(media, actorText) {
		var syllables []string
		for _, r := range segment.text {
			if !unicode.Is(unicode.Han, r) {
				continue
			}
			values := pinyin.SinglePinyin(r, args)
			if len(values) == 0 || values[0] == "" {
				continue
			}
			syllables = append(syllables, strings.ToLower(values[0]))
		}

		// 人名在每个音节边界都留一份，从中段打起也能命中；查询时一律锚定
		// 单元开头，不再做任意位置的子串碰撞。
		starts := 1
		if segment.withSuffixes {
			starts = len(syllables)
		}
		for start := 0; start < starts; start++ {
			var unit, initial strings.Builder
			for _, syllable := range syllables[start:] {
				unit.WriteString(syllable)
				initial.WriteByte(syllable[0])
			}
			pinyinUnits = append(pinyinUnits, unit.String())
			if initial.Len() >= 2 {
				initialUnits = append(initialUnits, initial.String())
			}
		}
	}
	return text, strings.Join(dedupeUnits(pinyinUnits), " "), strings.Join(dedupeUnits(initialUnits), " ")
}

// dedupeUnits 去重：演员名往往同时出现在标题里，不去重会让索引白白翻倍。
func dedupeUnits(units []string) []string {
	seen := make(map[string]bool, len(units))
	result := make([]string, 0, len(units))
	for _, unit := range units {
		if unit == "" || seen[unit] {
			continue
		}
		seen[unit] = true
		result = append(result, unit)
	}
	return result
}

func (m *Media) RefreshSearchFields(actorText string) {
	if m == nil {
		return
	}
	m.SearchText, m.SearchPinyin, m.SearchInitials = BuildMediaSearchFields(m, actorText)
}
