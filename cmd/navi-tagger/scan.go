package main

import (
	"crypto/sha1"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// 三个码类标签互斥：一部片子只可能是其中一种。
const (
	StatusCensored = "有码"
	StatusCracked  = "无码破解"
	StatusLeaked   = "无码流出"
	StatusNone     = ""
)

func isKnownStatus(status string) bool {
	switch status {
	case StatusCensored, StatusCracked, StatusLeaked, StatusNone:
		return true
	}
	return false
}

type Item struct {
	ID        string   `json:"id"`
	NFOPath   string   `json:"nfo_path"`
	Video     string   `json:"video"`
	Poster    string   `json:"-"`
	Title     string   `json:"title"`
	Code      string   `json:"code"`
	Tags      []string `json:"tags"`
	Status    string   `json:"status"`
	Subbed    bool     `json:"subbed"`
	Guess     string   `json:"guess"`
	HasPoster bool     `json:"has_poster"`
}

var videoExtensions = map[string]bool{
	".mp4": true, ".mkv": true, ".avi": true, ".wmv": true, ".ts": true,
	".mov": true, ".m4v": true, ".rmvb": true, ".flv": true, ".iso": true,
	".mpg": true, ".mpeg": true, ".webm": true, ".m2ts": true,
}

func scanRoots(roots []string) []*Item {
	var items []*Item
	seen := map[string]bool{}

	for _, root := range roots {
		filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return nil // 打不开的目录跳过，不要中断整次扫描
			}
			if entry.IsDir() || !strings.EqualFold(filepath.Ext(path), ".nfo") {
				return nil
			}
			if seen[strings.ToLower(path)] {
				return nil
			}
			seen[strings.ToLower(path)] = true
			if item := loadItem(path); item != nil {
				items = append(items, item)
			}
			return nil
		})
	}
	return items
}

func loadItem(nfoPath string) *Item {
	data, err := os.ReadFile(nfoPath)
	if err != nil {
		return nil
	}
	text := string(data)

	dir := filepath.Dir(nfoPath)
	stem := strings.TrimSuffix(filepath.Base(nfoPath), filepath.Ext(nfoPath))

	item := &Item{
		ID:      hashID(nfoPath),
		NFOPath: nfoPath,
		Title:   firstElementText(text, "title"),
		Code:    firstElementText(text, "num"),
		Tags:    readTagList(text),
		Video:   findSibling(dir, stem, videoExtensions),
		Poster:  findPoster(dir, stem),
	}
	if item.Code == "" {
		item.Code = stem
	}
	if item.Title == "" {
		item.Title = stem
	}
	item.HasPoster = item.Poster != ""

	for _, tag := range item.Tags {
		switch tag {
		case StatusCensored, StatusCracked, StatusLeaked:
			item.Status = tag
		case "中文字幕":
			item.Subbed = true
		}
	}

	// 文件名能认出破解版的，给个建议；流出没有可靠的文件名标记，只能靠人眼看封面。
	nameSource := item.Video
	if nameSource == "" {
		nameSource = nfoPath
	}
	if looksCracked(nameSource) && item.Status != StatusCracked {
		item.Guess = StatusCracked
	}
	return item
}

func hashID(path string) string {
	sum := sha1.Sum([]byte(strings.ToLower(path)))
	return hex.EncodeToString(sum[:10])
}

func findSibling(dir, stem string, extensions map[string]bool) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	lowerStem := strings.ToLower(stem)
	var fallback string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !extensions[strings.ToLower(filepath.Ext(name))] {
			continue
		}
		if strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name))) == lowerStem {
			return filepath.Join(dir, name)
		}
		if fallback == "" {
			fallback = filepath.Join(dir, name)
		}
	}
	return fallback
}

// findPoster 优先找竖版封面，角标（字幕/流出/破解）就印在上面。
func findPoster(dir, stem string) string {
	candidates := []string{
		stem + "-poster.jpg", stem + "-poster.png",
		"poster.jpg", "poster.png", "folder.jpg",
		stem + "-thumb.jpg", stem + "-fanart.jpg",
		"thumb.jpg", "fanart.jpg",
	}
	for _, name := range candidates {
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(entry.Name())) {
		case ".jpg", ".jpeg", ".png":
			return filepath.Join(dir, entry.Name())
		}
	}
	return ""
}

var (
	tokenSplitter   = regexp.MustCompile(`[-_@.\[\]() ]+`)
	crackedKeywords = []string{"restored", "破解", "uncensored", "无码"}
)

// looksCracked 只认文件名里明确的破解标记。-U/-UC 必须是独立的一段，
// 否则 -UHD、-ULTRA 这类会被误伤。
func looksCracked(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	for _, keyword := range crackedKeywords {
		if strings.Contains(name, keyword) {
			return true
		}
	}
	for _, token := range tokenSplitter.Split(name, -1) {
		if token == "u" || token == "uc" {
			return true
		}
	}
	return false
}
