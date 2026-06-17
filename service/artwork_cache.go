package service

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.uber.org/zap"
	"navi-desktop/model"
)

const (
	artworkJPEGQuality = 82

	artworkPosterMaxWidth  = 400
	artworkPosterMaxHeight = 540
	artworkWideMaxWidth    = 1280
	artworkWideMaxHeight   = 720
	artworkActorMaxWidth   = 240
	artworkActorMaxHeight  = 240
)

type ArtworkCache struct {
	root   string
	logger *zap.SugaredLogger
}

func NewArtworkCache(cacheDir string, logger *zap.SugaredLogger) *ArtworkCache {
	cacheDir = strings.TrimSpace(cacheDir)
	if cacheDir == "" {
		cacheDir = "cache"
	}
	return &ArtworkCache{
		root:   filepath.Join(cacheDir, "artwork"),
		logger: logger,
	}
}

func (c *ArtworkCache) CacheMediaArtwork(media *model.Media, sidecars *directorySidecarFiles) (posterPath, fanartPath string, changed bool, err error) {
	if c == nil || media == nil {
		return "", "", false, nil
	}

	posterPath = strings.TrimSpace(media.PosterPath)
	fanartPath = strings.TrimSpace(media.BackdropPath)
	if sidecars == nil && strings.TrimSpace(media.FilePath) != "" {
		sidecars = collectDirectorySidecarFiles(filepath.Dir(media.FilePath))
	}
	if sidecars == nil {
		return posterPath, fanartPath, false, nil
	}

	if source := sidecars.posterPathForMedia(media.FilePath); strings.TrimSpace(source) != "" {
		cached, cacheErr := c.cacheImageFile("poster", media.ID, source, artworkPosterMaxWidth, artworkPosterMaxHeight)
		if cacheErr != nil {
			return posterPath, fanartPath, changed, cacheErr
		}
		if cached != "" && !samePath(media.PosterPath, cached) {
			media.PosterPath = cached
			posterPath = cached
			changed = true
		}
	}
	if source := sidecars.backdropPathForMedia(media.FilePath); strings.TrimSpace(source) != "" {
		cached, cacheErr := c.cacheImageFile("fanart", media.ID, source, artworkWideMaxWidth, artworkWideMaxHeight)
		if cacheErr != nil {
			return posterPath, fanartPath, changed, cacheErr
		}
		if cached != "" && !samePath(media.BackdropPath, cached) {
			media.BackdropPath = cached
			fanartPath = cached
			changed = true
		}
	}

	return posterPath, fanartPath, changed, nil
}

func (c *ArtworkCache) CacheMediaPreviews(media *model.Media, sourcePaths []string, limit int) ([]string, error) {
	if c == nil || media == nil || len(sourcePaths) == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > len(sourcePaths) {
		limit = len(sourcePaths)
	}

	paths := make([]string, 0, limit)
	for index, source := range sourcePaths {
		if len(paths) >= limit {
			break
		}
		source = strings.TrimSpace(source)
		if source == "" {
			continue
		}
		key := fmt.Sprintf("%03d-%s", index+1, c.sourceKey(source))
		cached, err := c.cacheImageFileWithKey("preview", media.ID, key, source, artworkWideMaxWidth, artworkWideMaxHeight)
		if err != nil {
			return paths, err
		}
		if cached != "" {
			paths = append(paths, cached)
		}
	}
	return paths, nil
}

func (c *ArtworkCache) CacheActorImage(personID, actorName string, sourcePathOrBytes interface{}) (string, error) {
	if c == nil {
		return "", nil
	}
	keySource := strings.TrimSpace(personID)
	if keySource == "" {
		keySource = actorName
	}
	keySource = safeArtworkName(keySource)
	if keySource == "" {
		keySource = "actor"
	}

	var (
		reader io.Reader
		key    string
	)
	switch source := sourcePathOrBytes.(type) {
	case string:
		source = strings.TrimSpace(source)
		if source == "" {
			return "", nil
		}
		file, err := os.Open(source)
		if err != nil {
			return "", err
		}
		defer file.Close()
		reader = file
		key = c.sourceKey(source)
	case []byte:
		if len(source) == 0 {
			return "", nil
		}
		reader = bytes.NewReader(source)
		key = shortSHA1(source)
	case io.Reader:
		data, err := io.ReadAll(source)
		if err != nil {
			return "", err
		}
		if len(data) == 0 {
			return "", nil
		}
		reader = bytes.NewReader(data)
		key = shortSHA1(data)
	default:
		return "", fmt.Errorf("unsupported actor image source %T", sourcePathOrBytes)
	}

	outputPath := filepath.Join(c.roleDir("actor"), fmt.Sprintf("%s-%s.jpg", keySource, key))
	if fileExists(outputPath) {
		return outputPath, nil
	}
	if err := c.writeResizedJPEG(reader, outputPath, artworkActorMaxWidth, artworkActorMaxHeight); err != nil {
		return "", err
	}
	return outputPath, nil
}

func (c *ArtworkCache) CachedMediaPreviews(mediaID string) []string {
	if c == nil || strings.TrimSpace(mediaID) == "" {
		return nil
	}
	prefix := c.mediaKey(&model.Media{ID: mediaID}) + "-"
	entries, err := os.ReadDir(c.roleDir("preview"))
	if err != nil {
		return nil
	}
	var paths []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasPrefix(entry.Name(), prefix) && strings.EqualFold(filepath.Ext(entry.Name()), ".jpg") {
			paths = append(paths, filepath.Join(c.roleDir("preview"), entry.Name()))
		}
	}
	sort.Strings(paths)
	return paths
}

func (c *ArtworkCache) RemoveMedia(mediaID string) error {
	if c == nil || strings.TrimSpace(mediaID) == "" {
		return nil
	}
	prefix := c.mediaKey(&model.Media{ID: mediaID}) + "-"
	for _, role := range []string{"poster", "fanart", "preview"} {
		dir := c.roleDir(role)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
				continue
			}
			if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

func (c *ArtworkCache) IsCachedPath(path string) bool {
	if c == nil || strings.TrimSpace(path) == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(c.root), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel != "." && rel != "" && !strings.HasPrefix(rel, "..")
}

func (c *ArtworkCache) GeneratedMediaArtworkPath(media *model.Media, role string) string {
	if c == nil || media == nil {
		return ""
	}
	role = strings.TrimSpace(role)
	switch role {
	case "poster", "fanart":
	default:
		return ""
	}
	return filepath.Join(c.roleDir(role), fmt.Sprintf("%s-generated.jpg", c.mediaKey(media)))
}

func (c *ArtworkCache) GeneratedMediaPreviewPath(media *model.Media, index int) string {
	if c == nil || media == nil || index <= 0 {
		return ""
	}
	return filepath.Join(c.roleDir("preview"), fmt.Sprintf("%s-generated-preview-%02d.jpg", c.mediaKey(media), index))
}

func (c *ArtworkCache) cacheImageFile(role, mediaID, sourcePath string, maxWidth, maxHeight int) (string, error) {
	return c.cacheImageFileWithKey(role, mediaID, c.sourceKey(sourcePath), sourcePath, maxWidth, maxHeight)
}

func (c *ArtworkCache) cacheImageFileWithKey(role, mediaID, key, sourcePath string, maxWidth, maxHeight int) (string, error) {
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return "", nil
	}
	file, err := os.Open(sourcePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	id := safeArtworkName(mediaID)
	if id == "" {
		id = safeArtworkName(strings.TrimSuffix(filepath.Base(sourcePath), filepath.Ext(sourcePath)))
	}
	if id == "" {
		id = "media"
	}
	outputPath := filepath.Join(c.roleDir(role), fmt.Sprintf("%s-%s.jpg", id, safeArtworkName(key)))
	if fileExists(outputPath) {
		return outputPath, nil
	}
	if err := c.writeResizedJPEG(file, outputPath, maxWidth, maxHeight); err != nil {
		return "", err
	}
	return outputPath, nil
}

func (c *ArtworkCache) writeResizedJPEG(reader io.Reader, outputPath string, maxWidth, maxHeight int) error {
	img, _, err := image.Decode(reader)
	if err != nil {
		return err
	}
	img = resizeImageToFit(img, maxWidth, maxHeight)
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return err
	}
	file, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer file.Close()
	return jpeg.Encode(file, img, &jpeg.Options{Quality: artworkJPEGQuality})
}

func (c *ArtworkCache) roleDir(role string) string {
	return filepath.Join(c.root, role)
}

func (c *ArtworkCache) mediaKey(media *model.Media) string {
	if media == nil {
		return "media"
	}
	id := safeArtworkName(media.ID)
	if id != "" {
		return id
	}
	if pathKey := safeArtworkName(shortSHA1([]byte(filepath.Clean(media.FilePath)))); pathKey != "" {
		return pathKey
	}
	return "media"
}

func (c *ArtworkCache) sourceKey(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return shortSHA1([]byte(timeIndependentCacheKey(path)))
	}
	info, err := os.Stat(path)
	if err != nil {
		return shortSHA1([]byte(timeIndependentCacheKey(path)))
	}
	key := fmt.Sprintf("%s:%d:%d", filepath.Clean(path), info.Size(), info.ModTime().UnixNano())
	return shortSHA1([]byte(key))
}

func resizeImageToFit(img image.Image, maxWidth, maxHeight int) image.Image {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	if width <= 0 || height <= 0 || maxWidth <= 0 || maxHeight <= 0 {
		return img
	}
	scale := minFloat(float64(maxWidth)/float64(width), float64(maxHeight)/float64(height))
	if scale >= 1 {
		return img
	}
	newWidth := maxInt(1, int(float64(width)*scale))
	newHeight := maxInt(1, int(float64(height)*scale))
	dst := image.NewRGBA(image.Rect(0, 0, newWidth, newHeight))
	for y := 0; y < newHeight; y++ {
		sourceY := bounds.Min.Y + y*height/newHeight
		for x := 0; x < newWidth; x++ {
			sourceX := bounds.Min.X + x*width/newWidth
			dst.Set(x, y, img.At(sourceX, sourceY))
		}
	}
	return dst
}

var unsafeArtworkNamePattern = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func safeArtworkName(value string) string {
	value = strings.TrimSpace(value)
	value = unsafeArtworkNamePattern.ReplaceAllString(value, "_")
	value = strings.Trim(value, "._-")
	if len(value) > 80 {
		value = value[:80]
	}
	return value
}

func shortSHA1(data []byte) string {
	sum := sha1.Sum(data)
	return hex.EncodeToString(sum[:])[:12]
}

func timeIndependentCacheKey(value string) string {
	return filepath.Clean(strings.TrimSpace(value))
}

func minFloat(left, right float64) float64 {
	if left < right {
		return left
	}
	return right
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
