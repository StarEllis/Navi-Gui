package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"navi-desktop/assistant"
)

const (
	maxAssistantDeleteMedia = 20
	// assistantTrashDir 建在影片所在盘的根目录下。库大多在 NAS 映射盘上，
	// Windows 回收站管不到网络盘，所以自己挪到同盘的文件夹里：同盘挪动不复制、瞬间完成，删错了还能挪回来。
	assistantTrashDir = "Navi回收站"
	// 整个文件夹一起挪之前要先数一遍里面的东西，太大的文件夹不整个挪，只挪这部片自己的文件。
	maxTrashFolderEntries = 500
)

// trashPlan 是一部片要挪进回收站的东西。
type trashPlan struct {
	media  changeMedia
	paths  []string // 要挪的文件，或者整个文件夹（folder 为 true 时只有一项）
	folder bool
	files  int
	size   int64
}

func (e assistantExecutor) libraryRoots() ([]string, error) {
	libs, err := e.app.repos.Library.List()
	if err != nil {
		return nil, err
	}
	var roots []string
	for i := range libs {
		libs[i].HydratePathConfig()
		roots = append(roots, libs[i].FolderPaths...)
	}
	return roots, nil
}

func (e assistantExecutor) planDeleteFiles(media []changeMedia) (assistant.ChangePreview, func() (string, error), error) {
	preview := assistant.ChangePreview{MediaIDs: mediaIDsOf(media), Danger: true, AlwaysConfirm: true}
	if len(media) > maxAssistantDeleteMedia {
		return preview, nil, fmt.Errorf("删文件一次最多 %d 部，这次给了 %d 部，分几次来", maxAssistantDeleteMedia, len(media))
	}
	roots, err := e.libraryRoots()
	if err != nil {
		return preview, nil, err
	}
	plans := make([]trashPlan, 0, len(media))
	var lines []string
	var total int64
	for _, m := range media {
		record, err := e.app.repos.Media.FindByID(m.ID)
		if err != nil {
			return preview, nil, fmt.Errorf("%s 找不到了：%w", m.label(), err)
		}
		plan, err := planTrash(record.FilePath, roots)
		if err != nil {
			return preview, nil, fmt.Errorf("%s：%w", m.label(), err)
		}
		plan.media = m
		plans = append(plans, plan)
		total += plan.size
		if plan.folder {
			lines = append(lines, fmt.Sprintf("%s：整个文件夹 %s（%d 个文件，%s）", m.label(), plan.paths[0], plan.files, formatBytes(plan.size)))
		} else {
			lines = append(lines, fmt.Sprintf("%s：%s 等 %d 个文件（%s）", m.label(), plan.paths[0], plan.files, formatBytes(plan.size)))
		}
	}
	preview.Title = fmt.Sprintf("删掉 %d 部的视频文件（共 %s）", len(plans), formatBytes(total))
	preview.Lines = append(limitLines(lines),
		"文件会挪进同一个盘根目录下的「"+assistantTrashDir+"」文件夹，保留原来的目录结构，删错了可以挪回来",
		"确认不要了，再自己去清空那个文件夹；这几部也会从库里移除")
	return preview, func() (string, error) {
		stamp := time.Now().Format("20060102-150405")
		for i, plan := range plans {
			if err := moveAllToTrash(plan.paths, stamp); err != nil {
				return "", fmt.Errorf("删好了 %d 部，%s 挪不动：%w", i, plan.media.label(), err)
			}
			if err := e.app.DeleteMedia(plan.media.ID); err != nil {
				return "", fmt.Errorf("%s 的文件已经挪走，但从库里移除时出错：%w", plan.media.label(), err)
			}
		}
		return fmt.Sprintf("已把 %d 部挪进「%s\\%s」", len(plans), assistantTrashDir, stamp), nil
	}, nil
}

// planTrash 决定一部片挪什么：文件夹里只有这一部片时整个文件夹挪走（连带剧照、字幕），
// 否则只挪视频本身和同名的封面、NFO、字幕、预告片。
func planTrash(videoPath string, roots []string) (trashPlan, error) {
	info, err := os.Stat(videoPath)
	if err != nil || info.IsDir() {
		return trashPlan{}, errors.New("视频文件找不到了")
	}
	dir := filepath.Dir(videoPath)
	if inside, _ := insideAnyRoot(trashRootFor(videoPath), roots); inside {
		return trashPlan{}, fmt.Errorf("「%s」会落在媒体库目录里，扫描时又会被扫回来，这个盘没法用", trashRootFor(videoPath))
	}

	if inside, root := insideAnyRoot(dir, roots); inside && !samePath(dir, root) {
		if files, size, ok := folderOnlyHolds(dir, videoPath); ok {
			return trashPlan{paths: []string{dir}, folder: true, files: files, size: size}, nil
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return trashPlan{}, err
	}
	stem := strings.ToLower(strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath)))
	plan := trashPlan{paths: []string{videoPath}, files: 1, size: info.Size()}
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(dir, name)
		if entry.IsDir() || samePath(path, videoPath) || !belongsToStem(name, stem) {
			continue
		}
		// 同名开头的其他视频可能是另一部片（比如 -C 版），只带走预告片。
		if previewVideoExts[strings.ToLower(filepath.Ext(name))] && !isAdjacentTrailerFile(name) {
			continue
		}
		if fi, err := entry.Info(); err == nil {
			plan.size += fi.Size()
		}
		plan.paths = append(plan.paths, path)
		plan.files++
	}
	return plan, nil
}

// belongsToStem 判断文件名是不是这部片的附属文件：xxx.nfo、xxx-poster.jpg、xxx.zh.srt 这种。
func belongsToStem(name, stem string) bool {
	lower := strings.ToLower(name)
	if !strings.HasPrefix(lower, stem) || len(lower) == len(stem) {
		return false
	}
	return strings.ContainsRune(".-_ ", rune(lower[len(stem)]))
}

// folderOnlyHolds 检查文件夹里除了这部片（和它的预告片）之外没有别的视频，顺带数文件、算大小。
func folderOnlyHolds(dir, videoPath string) (int, int64, bool) {
	files, entries := 0, 0
	var size int64
	ok := true
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			ok = false
			return filepath.SkipAll
		}
		entries++
		if entries > maxTrashFolderEntries {
			ok = false
			return filepath.SkipAll
		}
		if d.IsDir() {
			return nil
		}
		if previewVideoExts[strings.ToLower(filepath.Ext(path))] && !samePath(path, videoPath) && !isAdjacentTrailerFile(d.Name()) {
			ok = false
			return filepath.SkipAll
		}
		files++
		if info, err := d.Info(); err == nil {
			size += info.Size()
		}
		return nil
	})
	return files, size, ok
}

// trashRootFor 是变量，测试里换成临时目录，免得往 C 盘根目录写东西。
var trashRootFor = func(path string) string {
	return filepath.Join(filepath.VolumeName(path)+`\`, assistantTrashDir)
}

// moveAllToTrash 挪走一部片的全部文件。中途有一个挪不动，就把已经挪走的挪回原处，
// 免得这部片还在库里、文件却缺了几个。
func moveAllToTrash(paths []string, stamp string) error {
	moved := make([]string, 0, len(paths))
	for _, path := range paths {
		if err := moveToTrash(path, stamp); err != nil {
			for i := len(moved) - 1; i >= 0; i-- {
				if undoErr := os.Rename(trashTarget(moved[i], stamp), moved[i]); undoErr != nil {
					return fmt.Errorf("%w；已挪走的 %s 也没能挪回去：%v", err, moved[i], undoErr)
				}
			}
			return fmt.Errorf("%w（这部片已经挪走的文件都挪回原处了）", err)
		}
		moved = append(moved, path)
	}
	return nil
}

// moveToTrash 把文件或文件夹挪进同盘的回收站，保留它在盘上的相对位置。不跨盘复制：挪不动就报错。
func moveToTrash(path, stamp string) error {
	target := trashTarget(path, stamp)
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("回收站里已经有 %s", target)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.Rename(path, target)
}

func trashTarget(path, stamp string) string {
	return filepath.Join(trashRootFor(path), stamp, strings.TrimPrefix(path, filepath.VolumeName(path)))
}

// insideAnyRoot 返回 path 是否在某个库目录里面（含库目录本身），以及是哪个。
func insideAnyRoot(path string, roots []string) (bool, string) {
	for _, root := range roots {
		rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
		if err != nil || rel == ".." || strings.HasPrefix(rel, `..\`) || strings.HasPrefix(rel, "../") {
			continue
		}
		return true, root
	}
	return false, ""
}

func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

func formatBytes(size int64) string {
	const gb = 1 << 30
	if size >= gb {
		return fmt.Sprintf("%.1f GB", float64(size)/gb)
	}
	return fmt.Sprintf("%d MB", size>>20)
}
