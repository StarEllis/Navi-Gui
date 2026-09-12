package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"
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
	cfg                *config.Config
	logger             *zap.SugaredLogger
	artworkCache       *ArtworkCache
	captureFrameRunner func(context.Context, string, string, float64, int) error
	ffmpegGovernor     *processGovernor
}

func NewThumbnailService(cfg *config.Config, logger *zap.SugaredLogger) *ThumbnailService {
	limit := 1
	if cfg != nil && cfg.App.FFmpegConcurrency > 0 {
		limit = cfg.App.FFmpegConcurrency
	}
	governor := newProcessGovernor(context.Background(), limit)
	return &ThumbnailService{
		cfg: cfg, logger: logger, ffmpegGovernor: governor,
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
	return t.EnsurePrimaryArtworkContext(context.Background(), media, sidecars, settings)
}

func (t *ThumbnailService) EnsurePrimaryArtworkContext(ctx context.Context, media *model.Media, sidecars *directorySidecarFiles, settings ThumbnailSettings) (bool, error) {
	if media == nil {
		return false, nil
	}

	changed := t.syncPrimaryArtworkPaths(media, sidecars)
	if !t.isBaseEligible(media, sidecars, settings) || !t.needsPrimaryArtwork(media, sidecars) {
		return changed, nil
	}

	var warnings []string
	posterPath := t.generatedPosterPath(media)
	if err := t.captureFrameContext(ctx, media.FilePath, mediaDurationSeconds(media)*0.25, posterPath, thumbnailPrimaryHeight); err != nil {
		warnings = append(warnings, fmt.Sprintf("poster: %v", err))
	} else if fileExists(posterPath) {
		if !samePath(media.PosterPath, posterPath) {
			media.PosterPath = posterPath
			changed = true
		}
	}

	backdropPath := t.generatedBackdropPath(media)
	if err := t.captureFrameContext(ctx, media.FilePath, mediaDurationSeconds(media)*0.58, backdropPath, thumbnailPrimaryHeight); err != nil {
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
	return t.GeneratePreviewsContext(context.Background(), media, sidecars, settings)
}

func (t *ThumbnailService) GeneratePreviewsContext(ctx context.Context, media *model.Media, sidecars *directorySidecarFiles, settings ThumbnailSettings) (int, error) {
	if media == nil {
		return 0, nil
	}

	settings = normalizeThumbnailSettings(settings)
	if t.hasDedicatedPreviewImages(media.FilePath, sidecars) {
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
		if err := ctx.Err(); err != nil {
			return generated, err
		}
		outputPath := t.generatedPreviewPath(media, index+1)
		if fileExists(outputPath) {
			continue
		}
		if err := t.captureFrameContext(ctx, media.FilePath, seekSeconds, outputPath, thumbnailPreviewHeight); err != nil {
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
	if media == nil {
		return ""
	}
	return generatedPosterPath(media.FilePath)
}

func (t *ThumbnailService) generatedBackdropPath(media *model.Media) string {
	if media == nil {
		return ""
	}
	return generatedBackdropPath(media.FilePath)
}

func (t *ThumbnailService) generatedPreviewPath(media *model.Media, index int) string {
	if media == nil {
		return ""
	}
	return filepath.Join(previewDirectory(media.FilePath), generatedPreviewName(media.FilePath, index))
}

func (t *ThumbnailService) cachedPreviewCount(media *model.Media) int {
	if t == nil || t.artworkCache == nil || media == nil {
		return 0
	}
	return len(t.artworkCache.GeneratedMediaPreviews(media.ID))
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
	return t.captureFrameContext(context.Background(), mediaPath, seekSeconds, outputPath, outputHeight)
}

func (t *ThumbnailService) captureFrameContext(ctx context.Context, mediaPath string, seekSeconds float64, outputPath string, outputHeight int) error {
	if t == nil {
		return fmt.Errorf("thumbnail service is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if t.artworkCache != nil && t.artworkCache.IsCachedPath(outputPath) {
		producerCtx, done, err := t.artworkCache.beginProducer(ctx)
		if err != nil {
			return err
		}
		defer done()
		ctx = producerCtx
	}
	select {
	case <-t.ffmpegGovernor.ctx.Done():
		return ErrProcessGovernorStopped
	default:
	}
	key := filepath.Clean(outputPath)
	_, err := t.ffmpegGovernor.runKeyed(ctx, key, func(processCtx context.Context) ([]byte, error) {
		return nil, t.captureFrameUnshared(processCtx, mediaPath, seekSeconds, outputPath, outputHeight)
	})
	return err
}

func (t *ThumbnailService) captureFrameUnshared(ctx context.Context, mediaPath string, seekSeconds float64, outputPath string, outputHeight int) error {
	if strings.TrimSpace(mediaPath) == "" || strings.TrimSpace(outputPath) == "" {
		return fmt.Errorf("empty media or output path")
	}
	if fileExists(outputPath) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if t == nil {
		return fmt.Errorf("thumbnail service is unavailable")
	}
	if t.captureFrameRunner == nil && (t.cfg == nil || strings.TrimSpace(t.cfg.App.FFmpegPath) == "") {
		return fmt.Errorf("ffmpeg configuration is unavailable")
	}
	ext := filepath.Ext(outputPath)
	tempPath := strings.TrimSuffix(outputPath, ext) + ".part-" + uuid.NewString() + ext
	defer os.Remove(tempPath)

	if t.captureFrameRunner != nil {
		if err := t.captureFrameRunner(ctx, mediaPath, tempPath, seekSeconds, outputHeight); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return err
		}
	} else {
		output, err := runBackgroundCommand(ctx, mediaTranscodeTimeout, true, t.cfg.App.FFmpegPath,
			"-y",
			"-ss", strconv.FormatFloat(maxFloat(seekSeconds, 0), 'f', 3, 64),
			"-i", mediaPath,
			"-frames:v", "1",
			"-vf", fmt.Sprintf("scale=-2:%d:force_original_aspect_ratio=decrease", outputHeight),
			"-q:v", "2",
			tempPath,
		)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return fmt.Errorf("%w, output=%s", err, strings.TrimSpace(string(output)))
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if t.artworkCache != nil && t.artworkCache.IsCachedPath(outputPath) {
		if err := t.artworkCache.prepareFileCommit(ctx); err != nil {
			return err
		}
		defer t.artworkCache.completeFileCommit()
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if err := os.Rename(tempPath, outputPath); err != nil {
		return err
	}
	if t.artworkCache != nil && t.artworkCache.IsCachedPath(outputPath) {
		t.artworkCache.recordFile(outputPath)
	}
	return nil
}

func (t *ThumbnailService) Shutdown() {
	if t == nil {
		return
	}
	if t.ffmpegGovernor != nil {
		t.ffmpegGovernor.shutdown()
	}
}

func (t *ThumbnailService) FFmpegDiagnostics() ProcessDiagnostics {
	if t == nil {
		return ProcessDiagnostics{}
	}
	return t.ffmpegGovernor.diagnostics()
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

// RemoveLegacyGeneratedThumbnailFiles removes only the old on-disk thumbnail
// layout used before generated artwork moved into the managed cache.
func RemoveLegacyGeneratedThumbnailFiles(media *model.Media) (int, error) {
	if media == nil || strings.TrimSpace(media.FilePath) == "" || strings.TrimSpace(media.ThumbnailFingerprint) == "" {
		return 0, nil
	}
	switch normalizeThumbnailStatus(media.ThumbnailStatus) {
	case ThumbnailStatusProcessing, ThumbnailStatusGenerated, ThumbnailStatusPartial,
		ThumbnailStatusFailed, ThumbnailStatusCanceled, ThumbnailStatusStale:
	default:
		return 0, nil
	}

	sidecars := collectDirectorySidecarFiles(filepath.Dir(media.FilePath))
	if sidecars != nil && strings.TrimSpace(sidecars.nfoPathForMedia(media.FilePath)) != "" {
		return 0, nil
	}

	previewDir := previewDirectory(media.FilePath)
	entries, err := os.ReadDir(previewDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	stem := strings.TrimSuffix(filepath.Base(media.FilePath), filepath.Ext(media.FilePath))
	prefix := strings.ToLower(stem + "-preview-")
	var targets []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".jpg") {
			continue
		}
		nameStem := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		lowerNameStem := strings.ToLower(nameStem)
		if !strings.HasPrefix(lowerNameStem, prefix) {
			continue
		}
		index, parseErr := strconv.Atoi(strings.TrimPrefix(lowerNameStem, prefix))
		if parseErr != nil || index <= 0 {
			continue
		}
		targets = append(targets, filepath.Join(previewDir, entry.Name()))
	}
	// The exact preview naming is the strongest legacy ownership signal. Do not
	// delete poster/fanart sidecars when no generated preview can prove origin.
	if len(targets) == 0 {
		return 0, nil
	}
	targets = append(targets, generatedPosterPath(media.FilePath), generatedBackdropPath(media.FilePath))

	removed := 0
	var removeErrs []error
	for _, path := range targets {
		if err := os.Remove(path); err != nil {
			if !os.IsNotExist(err) {
				removeErrs = append(removeErrs, fmt.Errorf("remove %s: %w", path, err))
			}
			continue
		}
		removed++
	}
	_ = os.Remove(previewDir)
	return removed, errors.Join(removeErrs...)
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
