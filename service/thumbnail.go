package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go.uber.org/zap"
	"navi-desktop/config"
	"navi-desktop/model"
)

const (
	defaultThumbnailPreviewCount       = 6
	defaultThumbnailMinDurationSeconds = 1200
	thumbnailPrimaryHeight             = 1080
	thumbnailPreviewHeight             = 720
	thumbnailPreviewDirName            = "extrafanart"
)

type ThumbnailSettings struct {
	Enabled            bool
	PreviewCount       int
	MinDurationSeconds int
}

type ThumbnailSettingsProvider func() ThumbnailSettings

func DefaultThumbnailSettings() ThumbnailSettings {
	return ThumbnailSettings{
		Enabled:            true,
		PreviewCount:       defaultThumbnailPreviewCount,
		MinDurationSeconds: defaultThumbnailMinDurationSeconds,
	}
}

func normalizeThumbnailSettings(settings ThumbnailSettings) ThumbnailSettings {
	defaults := DefaultThumbnailSettings()
	if settings.PreviewCount <= 0 {
		settings.PreviewCount = defaults.PreviewCount
	}
	if settings.PreviewCount > 12 {
		settings.PreviewCount = 12
	}
	if settings.MinDurationSeconds <= 0 {
		settings.MinDurationSeconds = defaults.MinDurationSeconds
	}
	if !settings.Enabled && settings == (ThumbnailSettings{}) {
		settings.Enabled = defaults.Enabled
	}
	return settings
}

type ThumbnailService struct {
	cfg          *config.Config
	logger       *zap.SugaredLogger
	artworkCache *ArtworkCache
}

func NewThumbnailService(cfg *config.Config, logger *zap.SugaredLogger) *ThumbnailService {
	return &ThumbnailService{
		cfg:    cfg,
		logger: logger,
	}
}

func (t *ThumbnailService) SetArtworkCache(cache *ArtworkCache) {
	if t == nil {
		return
	}
	t.artworkCache = cache
}

func (s *ScannerService) SetThumbnailSettingsProvider(provider ThumbnailSettingsProvider) {
	s.thumbnailSettingsProvider = provider
}

func (s *ScannerService) FindLocalArtworkForMedia(mediaPath string) (string, string) {
	if s == nil || strings.TrimSpace(mediaPath) == "" {
		return "", ""
	}
	sidecars := s.buildDirectorySidecarFiles(filepath.Dir(mediaPath))
	if sidecars == nil {
		return "", ""
	}
	return sidecars.posterPathForMedia(mediaPath), sidecars.backdropPathForMedia(mediaPath)
}

func (s *ScannerService) thumbnailSettings() ThumbnailSettings {
	if s != nil && s.thumbnailSettingsProvider != nil {
		return normalizeThumbnailSettings(s.thumbnailSettingsProvider())
	}
	return DefaultThumbnailSettings()
}

func (t *ThumbnailService) ShouldGeneratePreviews(media *model.Media, sidecars *directorySidecarFiles, settings ThumbnailSettings) bool {
	if !t.isBaseEligible(media, sidecars, settings) {
		return false
	}
	if t.hasDedicatedPreviewImages(media.FilePath, sidecars) {
		return false
	}

	return t.needsPrimaryArtwork(media, sidecars) || t.hasGeneratedPrimaryArtwork(media, sidecars)
}

func (t *ThumbnailService) EnsurePrimaryArtwork(media *model.Media, sidecars *directorySidecarFiles, settings ThumbnailSettings) (bool, error) {
	if media == nil {
		return false, nil
	}

	changed := t.syncPrimaryArtworkPaths(media, sidecars)
	if !t.isBaseEligible(media, sidecars, settings) || !t.needsPrimaryArtwork(media, sidecars) {
		return changed, nil
	}

	var warnings []string
	posterPath := t.generatedPosterPath(media)
	if err := t.captureFrame(media.FilePath, mediaDurationSeconds(media)*0.25, posterPath, thumbnailPrimaryHeight); err != nil {
		warnings = append(warnings, fmt.Sprintf("poster: %v", err))
	} else if fileExists(posterPath) {
		if !samePath(media.PosterPath, posterPath) {
			media.PosterPath = posterPath
			changed = true
		}
	}

	backdropPath := t.generatedBackdropPath(media)
	if err := t.captureFrame(media.FilePath, mediaDurationSeconds(media)*0.58, backdropPath, thumbnailPrimaryHeight); err != nil {
		warnings = append(warnings, fmt.Sprintf("fanart: %v", err))
	} else if fileExists(backdropPath) {
		if !samePath(media.BackdropPath, backdropPath) {
			media.BackdropPath = backdropPath
			changed = true
		}
	}

	if len(warnings) > 0 {
		return changed, fmt.Errorf(strings.Join(warnings, "; "))
	}
	return changed, nil
}

func (t *ThumbnailService) GeneratePreviews(media *model.Media, sidecars *directorySidecarFiles, settings ThumbnailSettings) (int, error) {
	if media == nil {
		return 0, nil
	}

	settings = normalizeThumbnailSettings(settings)
	if t.hasDedicatedPreviewImages(media.FilePath, sidecars) {
		return 0, nil
	}
	if t.cachedPreviewCount(media) > 0 {
		return 0, nil
	}

	duration := mediaDurationSeconds(media)
	if duration <= 0 {
		return 0, nil
	}

	targets := previewTargetTimes(duration, settings.PreviewCount)
	generated := 0
	var warnings []string
	for index, seekSeconds := range targets {
		outputPath := t.generatedPreviewPath(media, index+1)
		if fileExists(outputPath) {
			continue
		}
		if err := t.captureFrame(media.FilePath, seekSeconds, outputPath, thumbnailPreviewHeight); err != nil {
			warnings = append(warnings, fmt.Sprintf("preview-%02d: %v", index+1, err))
			continue
		}
		if fileExists(outputPath) {
			generated++
		}
	}

	if len(warnings) > 0 {
		return generated, fmt.Errorf(strings.Join(warnings, "; "))
	}
	return generated, nil
}

func (t *ThumbnailService) isBaseEligible(media *model.Media, sidecars *directorySidecarFiles, settings ThumbnailSettings) bool {
	settings = normalizeThumbnailSettings(settings)
	if !settings.Enabled || media == nil {
		return false
	}
	if strings.TrimSpace(media.FilePath) == "" || strings.TrimSpace(media.MediaType) != "movie" {
		return false
	}
	if isExtrasPath(media.FilePath) || isExtrasFile(filepath.Base(media.FilePath)) {
		return false
	}
	if mediaDurationSeconds(media) <= float64(settings.MinDurationSeconds) {
		return false
	}
	return sidecars == nil || sidecars.nfoPathForMedia(media.FilePath) == ""
}

func (t *ThumbnailService) needsPrimaryArtwork(media *model.Media, sidecars *directorySidecarFiles) bool {
	return !t.hasPrimaryArtwork(media, sidecars)
}

func (t *ThumbnailService) hasPrimaryArtwork(media *model.Media, sidecars *directorySidecarFiles) bool {
	if media == nil {
		return false
	}
	if hasThumbnailPoster(media, sidecars) || hasThumbnailBackdrop(media, sidecars) {
		return true
	}
	return false
}

func (t *ThumbnailService) hasGeneratedPrimaryArtwork(media *model.Media, sidecars *directorySidecarFiles) bool {
	if media == nil {
		return false
	}

	posterPath := t.generatedPosterPath(media)
	backdropPath := t.generatedBackdropPath(media)
	if fileExists(posterPath) || fileExists(backdropPath) {
		return true
	}
	if sidecars == nil {
		return false
	}
	return samePath(sidecars.posterPathForMedia(media.FilePath), posterPath) || samePath(sidecars.backdropPathForMedia(media.FilePath), backdropPath)
}

func (t *ThumbnailService) syncPrimaryArtworkPaths(media *model.Media, sidecars *directorySidecarFiles) bool {
	if media == nil || sidecars == nil {
		return false
	}

	changed := false
	if t != nil && t.artworkCache != nil {
		_, _, cacheChanged, err := t.artworkCache.CacheMediaArtwork(media, sidecars)
		if err != nil {
			if t.logger != nil {
				t.logger.Debugf("cache media artwork failed: media=%s err=%v", media.ID, err)
			}
		} else if cacheChanged {
			changed = true
		}
	}
	if !hasUsablePrimaryArtworkPath(media.PosterPath, media, sidecars, true) && strings.TrimSpace(media.PosterPath) != "" {
		media.PosterPath = ""
		changed = true
	}
	if !hasUsablePrimaryArtworkPath(media.BackdropPath, media, sidecars, false) && strings.TrimSpace(media.BackdropPath) != "" {
		media.BackdropPath = ""
		changed = true
	}
	if posterPath := sidecars.posterPathForMedia(media.FilePath); strings.TrimSpace(media.PosterPath) == "" && strings.TrimSpace(posterPath) != "" {
		media.PosterPath = posterPath
		changed = true
	}
	if backdropPath := sidecars.backdropPathForMedia(media.FilePath); strings.TrimSpace(media.BackdropPath) == "" && strings.TrimSpace(backdropPath) != "" {
		media.BackdropPath = backdropPath
		changed = true
	}
	return changed
}

func (t *ThumbnailService) hasDedicatedPreviewImages(mediaPath string, sidecars *directorySidecarFiles) bool {
	multipleVideos := sidecars != nil && sidecars.hasMultipleVideos()
	for _, dir := range []string{
		previewDirectory(mediaPath),
		filepath.Join(filepath.Dir(mediaPath), "behind the scenes"),
	} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			if isThumbnailPreviewImage(entry.Name()) && previewImageBelongsToMedia(entry.Name(), mediaPath, multipleVideos) {
				return true
			}
		}
	}
	return false
}

func hasUsablePrimaryArtworkPath(artworkPath string, media *model.Media, sidecars *directorySidecarFiles, poster bool) bool {
	artworkPath = strings.TrimSpace(artworkPath)
	if media == nil || artworkPath == "" {
		return false
	}
	if sidecars == nil || !sidecars.hasMultipleVideos() {
		return true
	}

	mediaDir := strings.TrimSpace(filepath.Clean(filepath.Dir(media.FilePath)))
	artworkDir := strings.TrimSpace(filepath.Clean(filepath.Dir(artworkPath)))
	if mediaDir == "" || artworkDir == "" {
		return true
	}
	if !samePath(mediaDir, artworkDir) {
		return true
	}

	tokens := []string{"fanart", "backdrop", "background", "banner", "clearart", "landscape"}
	if poster {
		tokens = []string{"poster", "cover", "folder", "thumb", "movie", "show"}
	}
	stem := strings.TrimSuffix(filepath.Base(artworkPath), filepath.Ext(artworkPath))
	return mediaSpecificSidecarMatch(stem, media.FilePath, tokens)
}

func hasThumbnailPoster(media *model.Media, sidecars *directorySidecarFiles) bool {
	if media == nil {
		return false
	}
	if hasUsablePrimaryArtworkPath(media.PosterPath, media, sidecars, true) {
		return true
	}
	if sidecars != nil && strings.TrimSpace(sidecars.posterPathForMedia(media.FilePath)) != "" {
		return true
	}
	return fileExists(generatedPosterPath(media.FilePath))
}

func hasThumbnailBackdrop(media *model.Media, sidecars *directorySidecarFiles) bool {
	if media == nil {
		return false
	}
	if hasUsablePrimaryArtworkPath(media.BackdropPath, media, sidecars, false) {
		return true
	}
	if sidecars != nil && strings.TrimSpace(sidecars.backdropPathForMedia(media.FilePath)) != "" {
		return true
	}
	return fileExists(generatedBackdropPath(media.FilePath))
}

func syncGeneratedArtworkPaths(media *model.Media) bool {
	if media == nil {
		return false
	}

	changed := false
	if posterPath := generatedPosterPath(media.FilePath); fileExists(posterPath) && !samePath(media.PosterPath, posterPath) {
		media.PosterPath = posterPath
		changed = true
	}
	if backdropPath := generatedBackdropPath(media.FilePath); fileExists(backdropPath) && !samePath(media.BackdropPath, backdropPath) {
		media.BackdropPath = backdropPath
		changed = true
	}
	return changed
}

func (t *ThumbnailService) syncGeneratedArtworkPaths(media *model.Media) bool {
	if t == nil || t.artworkCache == nil || media == nil {
		return syncGeneratedArtworkPaths(media)
	}

	changed := false
	if posterPath := t.generatedPosterPath(media); fileExists(posterPath) && !samePath(media.PosterPath, posterPath) {
		media.PosterPath = posterPath
		changed = true
	}
	if backdropPath := t.generatedBackdropPath(media); fileExists(backdropPath) && !samePath(media.BackdropPath, backdropPath) {
		media.BackdropPath = backdropPath
		changed = true
	}
	return changed
}

func (t *ThumbnailService) generatedPosterPath(media *model.Media) string {
	if t != nil && t.artworkCache != nil {
		if path := t.artworkCache.GeneratedMediaArtworkPath(media, "poster"); path != "" {
			return path
		}
	}
	if media == nil {
		return ""
	}
	return generatedPosterPath(media.FilePath)
}

func (t *ThumbnailService) generatedBackdropPath(media *model.Media) string {
	if t != nil && t.artworkCache != nil {
		if path := t.artworkCache.GeneratedMediaArtworkPath(media, "fanart"); path != "" {
			return path
		}
	}
	if media == nil {
		return ""
	}
	return generatedBackdropPath(media.FilePath)
}

func (t *ThumbnailService) generatedPreviewPath(media *model.Media, index int) string {
	if t != nil && t.artworkCache != nil {
		if path := t.artworkCache.GeneratedMediaPreviewPath(media, index); path != "" {
			return path
		}
	}
	if media == nil {
		return ""
	}
	return filepath.Join(previewDirectory(media.FilePath), generatedPreviewName(media.FilePath, index))
}

func (t *ThumbnailService) cachedPreviewCount(media *model.Media) int {
	if t == nil || t.artworkCache == nil || media == nil {
		return 0
	}
	return len(t.artworkCache.CachedMediaPreviews(media.ID))
}

func (t *ThumbnailService) countThumbnailPreviewImages(media *model.Media, sidecars *directorySidecarFiles) int {
	if media == nil {
		return 0
	}
	count := CountThumbnailPreviewImages(media.FilePath, sidecars)
	count += t.cachedPreviewCount(media)
	return count
}

func (t *ThumbnailService) resolveThumbnailState(media *model.Media, sidecars *directorySidecarFiles, settings ThumbnailSettings) string {
	if media == nil {
		return ThumbnailStatusNone
	}
	return resolveThumbnailStateWithPreviewCount(media, sidecars, settings, t.countThumbnailPreviewImages(media, sidecars))
}

func (t *ThumbnailService) captureFrame(mediaPath string, seekSeconds float64, outputPath string, outputHeight int) error {
	if strings.TrimSpace(mediaPath) == "" || strings.TrimSpace(outputPath) == "" {
		return fmt.Errorf("empty media or output path")
	}
	if fileExists(outputPath) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return err
	}

	cmd, cancel := newBackgroundCommand(mediaTranscodeTimeout, t.cfg.App.FFmpegPath,
		"-y",
		"-ss", strconv.FormatFloat(maxFloat(seekSeconds, 0), 'f', 3, 64),
		"-i", mediaPath,
		"-frames:v", "1",
		"-vf", fmt.Sprintf("scale=-2:%d:force_original_aspect_ratio=decrease", outputHeight),
		"-q:v", "2",
		outputPath,
	)
	defer cancel()

	if output, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(outputPath)
		return fmt.Errorf("%w, output=%s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func mediaDurationSeconds(media *model.Media) float64 {
	if media == nil {
		return 0
	}
	if media.Duration > 0 {
		return media.Duration
	}
	if media.Runtime > 0 {
		return float64(media.Runtime * 60)
	}
	return 0
}

func previewTargetTimes(duration float64, count int) []float64 {
	if duration <= 0 || count <= 0 {
		return nil
	}

	if count == 1 {
		return []float64{duration * 0.33}
	}

	startFraction := 0.15
	endFraction := 0.85
	step := (endFraction - startFraction) / float64(count+1)
	result := make([]float64, 0, count)
	for index := 0; index < count; index++ {
		fraction := startFraction + step*float64(index+1)
		result = append(result, duration*fraction)
	}
	return result
}

func generatedPosterPath(mediaPath string) string {
	return strings.TrimSuffix(mediaPath, filepath.Ext(mediaPath)) + "-poster.jpg"
}

func generatedBackdropPath(mediaPath string) string {
	return strings.TrimSuffix(mediaPath, filepath.Ext(mediaPath)) + "-fanart.jpg"
}

func previewDirectory(mediaPath string) string {
	return filepath.Join(filepath.Dir(mediaPath), thumbnailPreviewDirName)
}

func generatedPreviewName(mediaPath string, index int) string {
	return fmt.Sprintf("%s-preview-%02d.jpg", strings.TrimSuffix(filepath.Base(mediaPath), filepath.Ext(mediaPath)), index)
}

func fileExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func samePath(left string, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" || right == "" {
		return false
	}
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}

func isThumbnailPreviewImage(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".webp":
		return true
	default:
		return false
	}
}

func previewImageBelongsToMedia(name string, mediaPath string, requirePrefix bool) bool {
	if !requirePrefix {
		return true
	}
	return mediaSpecificSidecarMatch(strings.TrimSuffix(name, filepath.Ext(name)), mediaPath, []string{"preview"})
}

func maxFloat(value float64, minimum float64) float64 {
	if value < minimum {
		return minimum
	}
	return value
}
