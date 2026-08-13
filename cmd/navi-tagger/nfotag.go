package main

import (
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// NFO 里 <tag> 和 <genre> 是两份内容相同的列表，改标签必须两边一起动，
// 否则刮削器和播放器读到的会不一致。
var (
	cdataPattern     = regexp.MustCompile(`(?s)^\s*<!\[CDATA\[(.*?)\]\]>\s*$`)
	elementPatterns  sync.Map
	statusTagLookup  = map[string]bool{StatusCensored: true, StatusCracked: true, StatusLeaked: true}
)

// elementPattern 不锚定行首，压缩成一行的 NFO 也能正确识别。
func elementPattern(element string) *regexp.Regexp {
	if cached, ok := elementPatterns.Load(element); ok {
		return cached.(*regexp.Regexp)
	}
	pattern := regexp.MustCompile(`([ \t]*)<` + element + `>(.*?)</` + element + `>[ \t]*\r?\n?`)
	elementPatterns.Store(element, pattern)
	return pattern
}

func firstElementText(text, name string) string {
	match := elementPattern(name).FindStringSubmatch(text)
	if match == nil {
		return ""
	}
	if inner := cdataPattern.FindStringSubmatch(match[2]); inner != nil {
		return strings.TrimSpace(inner[1])
	}
	return strings.TrimSpace(html.UnescapeString(match[2]))
}

func readTagList(text string) []string {
	tags := extractList(text, "tag")
	if len(tags) == 0 {
		tags = extractList(text, "genre")
	}
	return tags
}

func extractList(text, element string) []string {
	var values []string
	for _, match := range elementPattern(element).FindAllStringSubmatch(text, -1) {
		if value := strings.TrimSpace(html.UnescapeString(match[2])); value != "" {
			values = append(values, value)
		}
	}
	return values
}

// writeStatusToNFO 把码类标签换成 status（传空则只是把三个码类标签都摘掉），
// 其余内容一个字节都不动，返回改完之后的完整标签列表。
func writeStatusToNFO(nfoPath, status string) ([]string, error) {
	original, err := os.ReadFile(nfoPath)
	if err != nil {
		return nil, fmt.Errorf("读不到 NFO: %w", err)
	}
	text := string(original)

	current := readTagList(text)
	updated := make([]string, 0, len(current)+1)
	for _, tag := range current {
		if !statusTagLookup[tag] {
			updated = append(updated, tag)
		}
	}
	if status != StatusNone {
		updated = append(updated, status)
	}
	if sameList(current, updated) {
		return current, nil
	}

	rewritten := text
	for _, element := range []string{"tag", "genre"} {
		rewritten = applyStatusToElement(rewritten, element, status)
	}

	if err := validateXML(rewritten); err != nil {
		return nil, fmt.Errorf("改完之后 XML 不合法，已放弃写入: %w", err)
	}
	if err := backupOnce(nfoPath, original); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(nfoPath, []byte(rewritten)); err != nil {
		return nil, err
	}
	return updated, nil
}

type elementSpan struct {
	start, end int
	drop       bool
}

// applyStatusToElement 只删掉写错的码类标签、补上正确的那一个，
// 中间夹着的其他元素原样不动 —— 不整块替换，避免误伤。
func applyStatusToElement(text, element, status string) string {
	matches := elementPattern(element).FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		if status == StatusNone {
			return text
		}
		return insertBeforeClose(text, element, status)
	}

	indent := text[matches[0][2]:matches[0][3]]
	spans := make([]elementSpan, 0, len(matches))
	alreadyCorrect := false
	insertAfter := -1

	for i, match := range matches {
		value := strings.TrimSpace(html.UnescapeString(text[match[4]:match[5]]))
		drop := statusTagLookup[value] && value != status
		if value == status && status != StatusNone {
			alreadyCorrect = true
		}
		if !drop {
			insertAfter = i
		}
		spans = append(spans, elementSpan{start: match[0], end: match[1], drop: drop})
	}

	needInsert := status != StatusNone && !alreadyCorrect
	newline := "\n"
	if strings.Contains(text, "\r\n") {
		newline = "\r\n"
	}
	var line strings.Builder
	line.WriteString(indent)
	line.WriteString("<" + element + ">")
	xml.EscapeText(&line, []byte(status))
	line.WriteString("</" + element + ">")
	line.WriteString(newline)

	var out strings.Builder
	cursor := 0
	for i, span := range spans {
		out.WriteString(text[cursor:span.start])
		// 所有条目都被删掉时，新标签补在原来列表的位置上。
		if needInsert && insertAfter < 0 && i == 0 {
			out.WriteString(line.String())
		}
		if !span.drop {
			out.WriteString(text[span.start:span.end])
		}
		if needInsert && i == insertAfter {
			out.WriteString(line.String())
		}
		cursor = span.end
	}
	out.WriteString(text[cursor:])
	return out.String()
}

// insertBeforeClose 处理 NFO 里原本就没有这一类元素的情况。
func insertBeforeClose(text, element, value string) string {
	closing := strings.LastIndex(text, "</movie>")
	if closing < 0 {
		return text
	}
	newline := "\n"
	if strings.Contains(text, "\r\n") {
		newline = "\r\n"
	}

	var line strings.Builder
	line.WriteString("  <" + element + ">")
	xml.EscapeText(&line, []byte(value))
	line.WriteString("</" + element + ">")
	line.WriteString(newline)
	return text[:closing] + line.String() + text[closing:]
}

func sameList(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func validateXML(text string) error {
	decoder := xml.NewDecoder(strings.NewReader(text))
	decoder.Strict = false
	for {
		_, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// backupOnce 第一次改某个 NFO 时留一份原件，改错了可以直接改回来。
func backupOnce(nfoPath string, original []byte) error {
	backup := nfoPath + ".navibak"
	if _, err := os.Stat(backup); err == nil {
		return nil
	}
	if err := os.WriteFile(backup, original, 0o644); err != nil {
		return fmt.Errorf("备份原文件失败: %w", err)
	}
	return nil
}

func writeFileAtomic(path string, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".navitagger-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tempPath := temp.Name()

	if _, err := temp.Write(data); err != nil {
		temp.Close()
		os.Remove(tempPath)
		return fmt.Errorf("写入失败: %w", err)
	}
	if err := temp.Close(); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("写入失败: %w", err)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		os.Remove(tempPath)
		return fmt.Errorf("替换原文件失败: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("替换原文件失败: %w", err)
	}
	return nil
}
