package model

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/mozillazg/go-pinyin"
)

var mediaSearchSeparators = regexp.MustCompile(`[\s_\-./\\\[\](){}#+:;,|]+`)

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
		media.FilePath,
		media.NfoExtraFields,
		actorText,
	}
	if media.Year > 0 {
		parts = append(parts, strconv.Itoa(media.Year))
	}
	return strings.Join(parts, "\n")
}

func BuildMediaSearchFields(media *Media, actorText string) (text, fullPinyin, initials string) {
	source := BuildMediaSearchSource(media, actorText)
	text = NormalizeMediaSearchText(source)
	args := pinyin.NewArgs()
	args.Style = pinyin.Normal
	args.Heteronym = false

	var syllables []string
	var initialBuilder strings.Builder
	for _, r := range source {
		if !unicode.Is(unicode.Han, r) {
			continue
		}
		values := pinyin.SinglePinyin(r, args)
		if len(values) == 0 || values[0] == "" {
			continue
		}
		syllable := strings.ToLower(values[0])
		syllables = append(syllables, syllable)
		initialBuilder.WriteByte(syllable[0])
	}
	if len(syllables) > 0 {
		fullPinyin = strings.Join(syllables, " ") + " " + strings.Join(syllables, "")
	}
	return text, fullPinyin, initialBuilder.String()
}

func (m *Media) RefreshSearchFields(actorText string) {
	if m == nil {
		return
	}
	m.SearchText, m.SearchPinyin, m.SearchInitials = BuildMediaSearchFields(m, actorText)
}
