package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"navi-desktop/model"
)

const (
	ThumbnailStatusNone       = "none"
	ThumbnailStatusPending    = "pending"
	ThumbnailStatusProcessing = "processing"
	ThumbnailStatusGenerated  = "generated"
	ThumbnailStatusPartial    = "partial"
	ThumbnailStatusFailed     = "failed"
	ThumbnailStatusCanceled   = "canceled"
	ThumbnailStatusStale      = "stale"
)

func normalizeThumbnailStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case ThumbnailStatusPending:
		return ThumbnailStatusPending
	case ThumbnailStatusProcessing:
		return ThumbnailStatusProcessing
	case ThumbnailStatusGenerated:
		return ThumbnailStatusGenerated
	case ThumbnailStatusPartial:
		return ThumbnailStatusPartial
	case ThumbnailStatusFailed:
		return ThumbnailStatusFailed
	case ThumbnailStatusCanceled:
		return ThumbnailStatusCanceled
	case ThumbnailStatusStale:
		return ThumbnailStatusStale
	default:
		return ThumbnailStatusNone
	}
}

func ComputeThumbnailFingerprint(fileSize int64, modUnix int64) string {
	if fileSize <= 0 || modUnix <= 0 {
		return ""
	}
	return fmt.Sprintf("%d|%d", fileSize, modUnix)
}

func CurrentThumbnailFingerprint(media *model.Media) string {
	if media == nil || media.FileModTime == nil || media.FileModTime.IsZero() {
		return ""
	}
	return ComputeThumbnailFingerprint(media.FileSize, media.FileModTime.Unix())
}

func IsThumbnailAutoEligible(media *model.Media, sidecars *directorySidecarFiles, settings ThumbnailSettings) bool {
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

func HasThumbnailArtwork(media *model.Media, sidecars *directorySidecarFiles) bool {
	return hasThumbnailPoster(media, sidecars) || hasThumbnailBackdrop(media, sidecars)
}

func HasAllThumbnailAssets(media *model.Media, sidecars *directorySidecarFiles, settings ThumbnailSettings) bool {
	settings = normalizeThumbnailSettings(settings)
	if media == nil {
		return false
	}
	return hasThumbnailPoster(media, sidecars) && hasThumbnailBackdrop(media, sidecars)
}

func CountThumbnailPreviewImages(mediaPath string, sidecars *directorySidecarFiles) int {
	if strings.TrimSpace(mediaPath) == "" {
		return 0
	}

	multipleVideos := sidecars != nil && sidecars.hasMultipleVideos()
	total := 0
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
				total++
			}
		}
	}
	return total
}

func ResolveThumbnailState(media *model.Media, sidecars *directorySidecarFiles, settings ThumbnailSettings) string {
	if media == nil {
		return ThumbnailStatusNone
	}
	return resolveThumbnailStateWithPreviewCount(media, sidecars, settings, CountThumbnailPreviewImages(media.FilePath, sidecars))
}

func resolveThumbnailStateWithPreviewCount(media *model.Media, sidecars *directorySidecarFiles, settings ThumbnailSettings, previewCount int) string {
	settings = normalizeThumbnailSettings(settings)
	if media == nil {
		return ThumbnailStatusNone
	}

	status := normalizeThumbnailStatus(media.ThumbnailStatus)
	currentFingerprint := CurrentThumbnailFingerprint(media)
	previousFingerprint := strings.TrimSpace(media.ThumbnailFingerprint)
	hasAssets := HasThumbnailArtwork(media, sidecars) || previewCount > 0
	hasAllAssets := HasAllThumbnailAssets(media, sidecars, settings)
	autoEligible := IsThumbnailAutoEligible(media, sidecars, settings)

	if !autoEligible {
		if hasAssets {
			return ThumbnailStatusGenerated
		}
		return ThumbnailStatusNone
	}
	if hasAllAssets {
		if previewCount == 0 && (status == ThumbnailStatusGenerated || status == ThumbnailStatusPartial) {
			return ThumbnailStatusStale
		}
		return ThumbnailStatusGenerated
	}
	if status == ThumbnailStatusFailed && currentFingerprint != "" && currentFingerprint == previousFingerprint {
		return ThumbnailStatusFailed
	}
	if status == ThumbnailStatusProcessing {
		return ThumbnailStatusProcessing
	}
	if status == ThumbnailStatusGenerated || status == ThumbnailStatusPartial {
		return ThumbnailStatusStale
	}
	if previousFingerprint != "" && currentFingerprint != "" && previousFingerprint != currentFingerprint {
		return ThumbnailStatusStale
	}
	if hasAssets {
		return ThumbnailStatusStale
	}
	return ThumbnailStatusPending
}

func ShouldWorkerProcess(media *model.Media) bool {
	if media == nil {
		return false
	}
	status := normalizeThumbnailStatus(media.ThumbnailStatus)
	return status == ThumbnailStatusPending || status == ThumbnailStatusStale
}

func ResolveThumbnailStateFromDisk(media *model.Media, thumbSvc *ThumbnailService, settings ThumbnailSettings) (string, string, error) {
	if media == nil {
		return ThumbnailStatusNone, "", nil
	}
	if strings.TrimSpace(media.FilePath) == "" {
		return ThumbnailStatusNone, "", nil
	}

	info, err := os.Stat(media.FilePath)
	if err != nil || info.IsDir() {
		return ThumbnailStatusNone, "", err
	}

	applyFileTimes(media, info)
	sidecars := collectDirectorySidecarFiles(filepath.Dir(media.FilePath))
	if thumbSvc != nil {
		thumbSvc.syncPrimaryArtworkPaths(media, sidecars)
		thumbSvc.syncGeneratedArtworkPaths(media)
	} else {
		syncGeneratedArtworkPaths(media)
	}

	status := ResolveThumbnailState(media, sidecars, settings)
	if thumbSvc != nil {
		status = thumbSvc.resolveThumbnailState(media, sidecars, settings)
	}
	return status, CurrentThumbnailFingerprint(media), nil
}
