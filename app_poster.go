package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/disintegration/imaging"
	"github.com/gen2brain/jpegn"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
	_ "golang.org/x/image/webp"
	"navi-desktop/model"
)

const (
	posterSourceMaxBytes = 20 << 20
	posterJPEGQuality    = 95
	posterDefaultSize    = 5
)

//go:embed resources/Img/*.png
var posterWatermarkFiles embed.FS

var posterWatermarkAssetPaths = map[string]string{
	"sub":   "resources/Img/sub.png",
	"youma": "resources/Img/youma.png",
	"wuma":  "resources/Img/wuma.png",
	"umr":   "resources/Img/umr.png",
	"leak":  "resources/Img/leak.png",
	"4k":    "resources/Img/4k.png",
	"8k":    "resources/Img/8k.png",
}

type PosterWatermarkConfig struct {
	Subtitle       bool   `json:"subtitle"`
	TypeMark       string `json:"type_mark"`
	QualityMark    string `json:"quality_mark"`
	SubtitleCorner string `json:"subtitle_corner"`
	TypeCorner     string `json:"type_corner"`
	QualityCorner  string `json:"quality_corner"`
	Size           int    `json:"size"`
}

type PosterEditorState struct {
	SourcePath        string                `json:"source_path"`
	CurrentPosterPath string                `json:"current_poster_path"`
	CanRestore        bool                  `json:"can_restore"`
	Config            PosterWatermarkConfig `json:"config"`
	WatermarkAssets   map[string]string     `json:"watermark_assets"`
}

type SaveMediaPosterRequest struct {
	MediaID    string                `json:"media_id"`
	SourceKind string                `json:"source_kind"`
	Source     string                `json:"source"`
	CropMode   string                `json:"crop_mode"`
	CropX      int                   `json:"crop_x"`
	CropY      int                   `json:"crop_y"`
	CropWidth  int                   `json:"crop_width"`
	CropHeight int                   `json:"crop_height"`
	Watermarks PosterWatermarkConfig `json:"watermarks"`
}

// SelectPosterImageFile asks for an image without opening a second application window.
func (a *App) SelectPosterImageFile() (string, error) {
	return wailsRuntime.OpenFileDialog(a.ctx, wailsRuntime.OpenDialogOptions{
		Title: "选择封面图片",
		Filters: []wailsRuntime.FileFilter{
			{DisplayName: "封面图片 (*.jpg;*.jpeg;*.png;*.webp)", Pattern: "*.jpg;*.jpeg;*.png;*.webp"},
		},
	})
}

func (a *App) GetPosterEditorState(mediaID string) (*PosterEditorState, error) {
	media, err := a.repos.Media.FindByID(strings.TrimSpace(mediaID))
	if err != nil {
		return nil, err
	}
	config := inferPosterWatermarkConfig(media)
	if stored := strings.TrimSpace(media.PosterWatermarkConfig); stored != "" {
		if err := json.Unmarshal([]byte(stored), &config); err != nil && a.logger != nil {
			a.logger.Warnf("decode poster watermark config failed: media=%s err=%v", media.ID, err)
		}
	}
	config = normalizePosterWatermarkConfig(config)

	sourcePath := posterEditorSourcePath(media.FilePath)
	if !regularFileExists(sourcePath) {
		sourcePath = strings.TrimSpace(media.PosterPath)
	}
	assets := make(map[string]string, len(posterWatermarkAssetPaths))
	for name, path := range posterWatermarkAssetPaths {
		data, readErr := posterWatermarkFiles.ReadFile(path)
		if readErr != nil {
			return nil, fmt.Errorf("read watermark %s: %w", name, readErr)
		}
		assets[name] = "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
	}

	return &PosterEditorState{
		SourcePath:        sourcePath,
		CurrentPosterPath: strings.TrimSpace(media.PosterPath),
		CanRestore:        regularFileExists(media.PosterOriginalPath),
		Config:            config,
		WatermarkAssets:   assets,
	}, nil
}

func (a *App) SaveMediaPoster(request SaveMediaPosterRequest) (*model.Media, error) {
	request.MediaID = strings.TrimSpace(request.MediaID)
	request.SourceKind = strings.TrimSpace(strings.ToLower(request.SourceKind))
	request.Source = strings.TrimSpace(request.Source)
	if request.MediaID == "" || request.Source == "" {
		return nil, fmt.Errorf("媒体或图片来源为空")
	}
	media, err := a.repos.Media.FindByID(request.MediaID)
	if err != nil {
		return nil, err
	}
	targetPath, err := posterOutputPath(media.FilePath)
	if err != nil {
		return nil, err
	}

	data, err := readPosterSource(a.ctx, request.SourceKind, request.Source)
	if err != nil {
		return nil, err
	}
	sourceImage, err := imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true))
	if err != nil {
		return nil, fmt.Errorf("读取封面图片失败: %w", err)
	}
	config := normalizePosterWatermarkConfig(request.Watermarks)
	rendered, cropRect, err := renderPosterImage(sourceImage, request, config)
	if err != nil {
		return nil, err
	}

	originalPath := strings.TrimSpace(media.PosterOriginalPath)
	if originalPath == "" && regularFileExists(media.PosterPath) {
		originalPath = posterOriginalBackupPath(media.FilePath, media.PosterPath)
		if err := copyFileAtomically(media.PosterPath, originalPath); err != nil {
			return nil, fmt.Errorf("备份刮削原图失败: %w", err)
		}
	}

	targetSnapshot, targetExisted, err := snapshotFile(targetPath)
	if err != nil {
		return nil, err
	}
	defer removeSnapshot(targetSnapshot)
	sourcePath := posterEditorSourcePath(media.FilePath)
	sourceSnapshot, sourceExisted, err := snapshotFile(sourcePath)
	if err != nil {
		return nil, err
	}
	defer removeSnapshot(sourceSnapshot)

	if err := writePNGAtomically(sourcePath, sourceImage); err != nil {
		return nil, fmt.Errorf("保存封面编辑源图失败: %w", err)
	}
	if err := writeJPEG444Atomically(targetPath, rendered); err != nil {
		_ = restoreSnapshot(sourcePath, sourceSnapshot, sourceExisted)
		return nil, fmt.Errorf("保存封面失败: %w", err)
	}
	configJSON, err := json.Marshal(config)
	if err != nil {
		_ = restoreSnapshot(targetPath, targetSnapshot, targetExisted)
		_ = restoreSnapshot(sourcePath, sourceSnapshot, sourceExisted)
		return nil, err
	}
	if err := a.repos.Media.UpdateFields(media.ID, map[string]interface{}{
		"poster_path":             targetPath,
		"poster_original_path":    originalPath,
		"poster_watermark_config": string(configJSON),
	}); err != nil {
		_ = restoreSnapshot(targetPath, targetSnapshot, targetExisted)
		_ = restoreSnapshot(sourcePath, sourceSnapshot, sourceExisted)
		return nil, fmt.Errorf("更新封面记录失败: %w", err)
	}
	if a.logger != nil {
		a.logger.Infof("media poster saved: media=%s source=%s crop=%v target=%s", media.ID, request.SourceKind, cropRect, targetPath)
	}
	return a.GetMediaDetail(media.ID)
}

func (a *App) RestoreMediaPoster(mediaID string) (*model.Media, error) {
	media, err := a.repos.Media.FindByID(strings.TrimSpace(mediaID))
	if err != nil {
		return nil, err
	}
	originalPath := strings.TrimSpace(media.PosterOriginalPath)
	if !regularFileExists(originalPath) {
		return nil, fmt.Errorf("没有可恢复的刮削原图")
	}
	manualPosterPath, err := posterOutputPath(media.FilePath)
	if err != nil {
		return nil, err
	}
	targetPath := posterRestoreOutputPath(media.FilePath, originalPath)
	targetSnapshot, targetExisted, err := snapshotFile(targetPath)
	if err != nil {
		return nil, err
	}
	defer removeSnapshot(targetSnapshot)
	if err := copyFileAtomically(originalPath, targetPath); err != nil {
		return nil, fmt.Errorf("恢复刮削原图失败: %w", err)
	}
	if err := a.repos.Media.UpdateFields(media.ID, map[string]interface{}{
		"poster_path":          targetPath,
		"poster_original_path": "",
	}); err != nil {
		_ = restoreSnapshot(targetPath, targetSnapshot, targetExisted)
		return nil, fmt.Errorf("更新封面记录失败: %w", err)
	}
	if !strings.EqualFold(targetPath, manualPosterPath) {
		_ = os.Remove(manualPosterPath)
	}
	_ = os.Remove(posterEditorSourcePath(media.FilePath))
	return a.GetMediaDetail(media.ID)
}

func renderPosterImage(source image.Image, request SaveMediaPosterRequest, config PosterWatermarkConfig) (image.Image, image.Rectangle, error) {
	if source == nil {
		return nil, image.Rectangle{}, fmt.Errorf("封面图片为空")
	}
	bounds := source.Bounds()
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		return nil, image.Rectangle{}, fmt.Errorf("封面图片尺寸无效")
	}
	cropRect := bounds
	if strings.EqualFold(strings.TrimSpace(request.CropMode), "ratio") {
		cropRect = normalizePosterCropRect(bounds, image.Rect(
			request.CropX,
			request.CropY,
			request.CropX+request.CropWidth,
			request.CropY+request.CropHeight,
		))
	}
	canvas := image.Image(imaging.Crop(source, cropRect))
	withMarks, err := applyPosterWatermarks(canvas, config)
	if err != nil {
		return nil, image.Rectangle{}, err
	}
	return withMarks, cropRect, nil
}

func normalizePosterCropRect(bounds, requested image.Rectangle) image.Rectangle {
	width, height := bounds.Dx(), bounds.Dy()
	if requested.Dx() <= 0 || requested.Dy() <= 0 {
		cropWidth := int(float64(height) / 1.5)
		cropHeight := height
		if cropWidth > width {
			cropWidth = width
			cropHeight = int(float64(width) * 1.5)
		}
		left := bounds.Min.X + (width-cropWidth)/2
		top := bounds.Min.Y + (height-cropHeight)/2
		return image.Rect(left, top, left+cropWidth, top+cropHeight)
	}
	cropWidth := minPosterInt(requested.Dx(), width)
	cropHeight := minPosterInt(requested.Dy(), height)
	left := clampInt(requested.Min.X, bounds.Min.X, bounds.Max.X-cropWidth)
	top := clampInt(requested.Min.Y, bounds.Min.Y, bounds.Max.Y-cropHeight)
	return image.Rect(left, top, left+cropWidth, top+cropHeight)
}

func applyPosterWatermarks(source image.Image, config PosterWatermarkConfig) (image.Image, error) {
	config = normalizePosterWatermarkConfig(config)
	type placement struct {
		name   string
		corner string
		image  image.Image
		width  int
		height int
	}
	requested := []struct {
		name   string
		corner string
	}{
		{func() string {
			if config.Subtitle {
				return "sub"
			}
			return ""
		}(), config.SubtitleCorner},
		{config.TypeMark, config.TypeCorner},
		{config.QualityMark, config.QualityCorner},
	}
	placements := make([]placement, 0, 3)
	for _, item := range requested {
		path, ok := posterWatermarkAssetPaths[item.name]
		if !ok || item.name == "" {
			continue
		}
		data, err := posterWatermarkFiles.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("读取水印 %s 失败: %w", item.name, err)
		}
		mark, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("解析水印 %s 失败: %w", item.name, err)
		}
		markWidth, markHeight := posterWatermarkDrawSize(source.Bounds().Dy(), config.Size, mark.Bounds().Dx(), mark.Bounds().Dy())
		placements = append(placements, placement{
			name: item.name, corner: item.corner,
			image: imaging.Resize(mark, markWidth, markHeight, imaging.Lanczos),
			width: markWidth, height: markHeight,
		})
	}

	canvas := source
	for _, corner := range []string{"top_left", "top_right", "bottom_right", "bottom_left"} {
		row := make([]placement, 0, len(placements))
		totalWidth := 0
		for _, item := range placements {
			if item.corner == corner {
				row = append(row, item)
				totalWidth += item.width
			}
		}
		x := 0
		if strings.HasSuffix(corner, "right") {
			x = source.Bounds().Dx() - totalWidth
		}
		for _, item := range row {
			y := 0
			if strings.HasPrefix(corner, "bottom") {
				y = source.Bounds().Dy() - item.height
			}
			canvas = imaging.Overlay(canvas, item.image, image.Pt(x, y), 1)
			x += item.width
		}
	}
	return canvas, nil
}

func posterWatermarkDrawSize(posterHeight, markSize, assetWidth, assetHeight int) (int, int) {
	height := maxPosterInt(1, int(float64(posterHeight)*float64(clampInt(markSize, 1, 12))/40))
	width := maxPosterInt(1, int(float64(height)*float64(assetWidth)/float64(assetHeight)))
	return width, height
}

func normalizePosterWatermarkConfig(config PosterWatermarkConfig) PosterWatermarkConfig {
	validType := map[string]bool{"": true, "youma": true, "wuma": true, "umr": true, "leak": true}
	validQuality := map[string]bool{"": true, "4k": true, "8k": true}
	validCorner := map[string]bool{"top_left": true, "top_right": true, "bottom_right": true, "bottom_left": true}
	config.TypeMark = strings.ToLower(strings.TrimSpace(config.TypeMark))
	config.QualityMark = strings.ToLower(strings.TrimSpace(config.QualityMark))
	if !validType[config.TypeMark] {
		config.TypeMark = ""
	}
	if !validQuality[config.QualityMark] {
		config.QualityMark = ""
	}
	if !validCorner[config.SubtitleCorner] {
		config.SubtitleCorner = "top_left"
	}
	if !validCorner[config.TypeCorner] {
		config.TypeCorner = "top_right"
	}
	if !validCorner[config.QualityCorner] {
		config.QualityCorner = "bottom_right"
	}
	if config.Size == 0 {
		config.Size = posterDefaultSize
	} else {
		config.Size = clampInt(config.Size, 1, 12)
	}
	return config
}

func inferPosterWatermarkConfig(media *model.Media) PosterWatermarkConfig {
	config := PosterWatermarkConfig{
		SubtitleCorner: "top_left",
		TypeCorner:     "top_right",
		QualityCorner:  "bottom_right",
		Size:           posterDefaultSize,
	}
	if media == nil {
		return config
	}
	config.Subtitle = strings.TrimSpace(media.SubtitlePaths) != ""
	identity := strings.ToLower(strings.Join([]string{media.Title, media.FilePath, media.Genres}, " "))
	switch {
	case strings.Contains(identity, "破解") || strings.Contains(identity, "-umr"):
		config.TypeMark = "umr"
	case strings.Contains(identity, "流出") || strings.Contains(identity, "leak"):
		config.TypeMark = "leak"
	case strings.Contains(identity, "无码") || strings.Contains(identity, "uncensored"):
		config.TypeMark = "wuma"
	case strings.Contains(identity, "有码") || strings.Contains(identity, "censored"):
		config.TypeMark = "youma"
	}
	resolution := strings.ToLower(media.Resolution)
	if strings.Contains(resolution, "8k") || strings.Contains(resolution, "4320") {
		config.QualityMark = "8k"
	} else if strings.Contains(resolution, "4k") || strings.Contains(resolution, "2160") || strings.Contains(resolution, "uhd") {
		config.QualityMark = "4k"
	}
	return config
}

func readPosterSource(ctx context.Context, kind, value string) ([]byte, error) {
	switch kind {
	case "path":
		info, err := os.Stat(value)
		if err != nil {
			return nil, fmt.Errorf("读取本地图片失败: %w", err)
		}
		if info.Size() > posterSourceMaxBytes {
			return nil, fmt.Errorf("图片超过 20MB")
		}
		return os.ReadFile(value)
	case "url":
		parsed, err := url.Parse(value)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return nil, fmt.Errorf("图片链接格式无效")
		}
		if ctx == nil {
			ctx = context.Background()
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, value, nil)
		if err != nil {
			return nil, err
		}
		client := &http.Client{Timeout: 25 * time.Second}
		response, err := client.Do(request)
		if err != nil {
			return nil, fmt.Errorf("获取图片失败: %w", err)
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, fmt.Errorf("获取图片失败: HTTP %d", response.StatusCode)
		}
		return readLimitedPosterBytes(response.Body)
	case "data":
		comma := strings.IndexByte(value, ',')
		if comma <= 0 || !strings.Contains(strings.ToLower(value[:comma]), ";base64") {
			return nil, fmt.Errorf("剪贴板图片格式无效")
		}
		data, err := base64.StdEncoding.DecodeString(value[comma+1:])
		if err != nil {
			return nil, fmt.Errorf("读取剪贴板图片失败: %w", err)
		}
		if len(data) > posterSourceMaxBytes {
			return nil, fmt.Errorf("图片超过 20MB")
		}
		return data, nil
	default:
		return nil, fmt.Errorf("不支持的图片来源")
	}
}

func readLimitedPosterBytes(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, posterSourceMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > posterSourceMaxBytes {
		return nil, fmt.Errorf("图片超过 20MB")
	}
	return data, nil
}

func posterOutputPath(mediaPath string) (string, error) {
	mediaPath = strings.TrimSpace(mediaPath)
	if mediaPath == "" {
		return "", fmt.Errorf("媒体文件路径为空")
	}
	ext := filepath.Ext(mediaPath)
	stem := strings.TrimSuffix(filepath.Base(mediaPath), ext)
	if stem == "" {
		return "", fmt.Errorf("媒体文件名无效")
	}
	return filepath.Join(filepath.Dir(mediaPath), stem+"-poster.jpg"), nil
}

func posterEditorSourcePath(mediaPath string) string {
	target, err := posterOutputPath(mediaPath)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(target, filepath.Ext(target)) + ".source.png"
}

func posterOriginalBackupPath(mediaPath, posterPath string) string {
	target, _ := posterOutputPath(mediaPath)
	ext := strings.ToLower(filepath.Ext(posterPath))
	if ext == "" {
		ext = ".jpg"
	}
	return strings.TrimSuffix(target, filepath.Ext(target)) + ".original" + ext
}

func posterRestoreOutputPath(mediaPath, originalPath string) string {
	target, err := posterOutputPath(mediaPath)
	if err != nil {
		return ""
	}
	ext := strings.ToLower(filepath.Ext(originalPath))
	if ext == "" {
		ext = filepath.Ext(target)
	}
	return strings.TrimSuffix(target, filepath.Ext(target)) + ext
}

func writePNGAtomically(path string, img image.Image) error {
	return writeFileAtomically(path, func(writer io.Writer) error { return png.Encode(writer, img) })
}

func writeJPEG444Atomically(path string, img image.Image) error {
	return writeFileAtomically(path, func(writer io.Writer) error {
		return jpegn.Encode(writer, img, &jpegn.EncodeOptions{
			Quality:     posterJPEGQuality,
			Subsampling: jpegn.Subsample444,
		})
	})
}

func writeFileAtomically(path string, write func(io.Writer) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".navi-poster-*.part")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	committed := false
	defer func() {
		_ = temp.Close()
		if !committed {
			_ = os.Remove(tempPath)
		}
	}()
	if err := write(temp); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	committed = true
	return nil
}

func copyFileAtomically(source, target string) error {
	file, err := os.Open(source)
	if err != nil {
		return err
	}
	defer file.Close()
	return writeFileAtomically(target, func(writer io.Writer) error {
		_, err := io.Copy(writer, file)
		return err
	})
}

func snapshotFile(path string) (string, bool, error) {
	if !regularFileExists(path) {
		return "", false, nil
	}
	temp, err := os.CreateTemp("", "navi-poster-rollback-*")
	if err != nil {
		return "", false, err
	}
	name := temp.Name()
	_ = temp.Close()
	if err := copyFileAtomically(path, name); err != nil {
		_ = os.Remove(name)
		return "", false, err
	}
	return name, true, nil
}

func restoreSnapshot(path, snapshot string, existed bool) error {
	if !existed {
		return os.Remove(path)
	}
	return copyFileAtomically(snapshot, path)
}

func removeSnapshot(path string) {
	if path != "" {
		_ = os.Remove(path)
	}
}

func regularFileExists(path string) bool {
	info, err := os.Stat(strings.TrimSpace(path))
	return err == nil && info.Mode().IsRegular()
}

func clampInt(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func minPosterInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func maxPosterInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
