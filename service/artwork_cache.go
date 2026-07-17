package service

import (
	"bytes"
	"context"
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
	"sync"
	"sync/atomic"
	"time"

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
	root               string
	logger             *zap.SugaredLogger
	ctx                context.Context
	cancel             context.CancelFunc
	mu                 sync.Mutex
	closing            bool
	inflight           map[string]*artworkCall
	readDir            func(string) ([]os.DirEntry, error)
	rename             func(string, string) error
	remove             func(string) error
	active             map[string]int
	index              map[string]artworkIndexEntry
	indexUsable        bool
	reconcileNeeded    bool
	indexPath          string
	reconcilePath      string
	reconcileMarkerSet bool
	pendingCommits     int
	indexBytes         int64
	indexVersion       uint64
	persistedVersion   uint64
	dirty              bool
	pendingUpserts     map[string]artworkIndexEntry
	pendingDeletes     map[string]struct{}
	flushCh            chan struct{}
	cleanupCh          chan struct{}
	tempCleanupCh      chan tempCleanupRequest
	workerStop         chan struct{}
	flushDebounce      time.Duration
	indexWriter        func([]artworkIndexEntry) error
	highBytes          int64
	lowBytes           int64
	maxFiles           int
	cleanupBatch       int
	flushMu            sync.Mutex
	markerMu           sync.Mutex
	producerWG         sync.WaitGroup
	workerWG           sync.WaitGroup
	activeProducers    int
	activeWorkers      int
	producerDrained    chan struct{}
	workerDrained      chan struct{}
	shutdownBeginOnce  sync.Once
	producerDrainOnce  sync.Once
	workerDrainOnce    sync.Once
	shutdownFlushOnce  sync.Once
	shutdownFlushDone  chan struct{}
	shutdownFlushErr   error
	indexWrites        atomic.Int64
	hits               atomic.Int64
	misses             atomic.Int64
	temps              atomic.Int64
	walks              atomic.Int64
}

type artworkCall struct {
	done chan struct{}
	err  error
}

func (c *ArtworkCache) beginProducer(parent context.Context) (context.Context, func(), error) {
	if parent == nil {
		parent = context.Background()
	}
	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		return nil, func() {}, ErrProcessGovernorStopped
	}
	c.addProducerLocked()
	c.mu.Unlock()
	ctx, cancel := context.WithCancel(parent)
	stopCacheCancel := context.AfterFunc(c.ctx, cancel)
	var once sync.Once
	done := func() { once.Do(func() { stopCacheCancel(); cancel(); c.finishProducer() }) }
	return ctx, done, nil
}

func NewArtworkCache(cacheDir string, logger *zap.SugaredLogger) *ArtworkCache {
	cacheDir = strings.TrimSpace(cacheDir)
	if cacheDir == "" {
		cacheDir = "cache"
	}
	ctx, cancel := context.WithCancel(context.Background())
	root := filepath.Join(cacheDir, "artwork")
	cache := &ArtworkCache{
		root: root, logger: logger, ctx: ctx, cancel: cancel,
		inflight: make(map[string]*artworkCall), readDir: os.ReadDir, rename: os.Rename, remove: os.Remove,
		active: make(map[string]int), index: make(map[string]artworkIndexEntry),
		pendingUpserts: make(map[string]artworkIndexEntry), pendingDeletes: make(map[string]struct{}),
		indexPath: filepath.Join(root, ".index.json"), reconcilePath: filepath.Join(root, ".index-reconcile"),
		flushCh: make(chan struct{}, 1), cleanupCh: make(chan struct{}, 1), tempCleanupCh: make(chan tempCleanupRequest, 1), workerStop: make(chan struct{}),
		producerDrained: make(chan struct{}), workerDrained: make(chan struct{}), shutdownFlushDone: make(chan struct{}),
		flushDebounce: 100 * time.Millisecond,
		highBytes:     DefaultArtworkCacheHighBytes, lowBytes: DefaultArtworkCacheLowBytes,
		maxFiles: DefaultArtworkCacheMaxFiles, cleanupBatch: DefaultArtworkCleanupBatch,
	}
	cache.indexWriter = cache.writeIndexSnapshot
	cache.loadIndex()
	cache.startWorkers()
	return cache
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

	outputPath := filepath.Join(c.roleDir("actor"), keySource, fmt.Sprintf("%s.jpg", key))
	if fileExists(outputPath) {
		return outputPath, nil
	}
	if err := c.generateOnce(outputPath, func(ctx context.Context) error {
		return c.writeResizedJPEG(ctx, reader, outputPath, artworkActorMaxWidth, artworkActorMaxHeight)
	}); err != nil {
		return "", err
	}
	return outputPath, nil
}

func (c *ArtworkCache) CachedMediaPreviews(mediaID string) []string {
	if c == nil || strings.TrimSpace(mediaID) == "" {
		return nil
	}
	dir := c.mediaRoleDir("preview", mediaID)
	entries, err := c.readDir(dir)
	if err != nil {
		return nil
	}
	var paths []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.EqualFold(filepath.Ext(entry.Name()), ".jpg") {
			paths = append(paths, filepath.Join(dir, entry.Name()))
		}
	}
	sort.Strings(paths)
	c.touchMany(paths)
	return paths
}

func (c *ArtworkCache) RemoveMedia(mediaID string) error {
	if c == nil || strings.TrimSpace(mediaID) == "" {
		return nil
	}
	key := c.mediaKey(&model.Media{ID: mediaID})
	seen := make(map[string]struct{})
	var targets []string
	roles := []string{"poster", "fanart", "preview"}
	for _, role := range roles {
		dir := c.mediaRoleDir(role, mediaID)
		entries, err := c.readDir(dir)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				path := filepath.Join(dir, entry.Name())
				seen[path] = struct{}{}
				targets = append(targets, path)
			}
		}
	}

	c.mu.Lock()
	recoveryScan := c.reconcileNeeded || !c.indexUsable
	for path := range c.inflight {
		for _, role := range roles {
			if samePath(filepath.Dir(path), c.mediaRoleDir(role, mediaID)) {
				if _, exists := seen[path]; !exists {
					seen[path] = struct{}{}
					targets = append(targets, path)
				}
			}
		}
	}
	for path := range c.index {
		for _, role := range roles {
			roleDir := c.roleDir(role)
			if samePath(filepath.Dir(path), roleDir) && strings.HasPrefix(filepath.Base(path), key+"-") {
				if _, exists := seen[path]; !exists {
					seen[path] = struct{}{}
					targets = append(targets, path)
				}
			}
		}
	}
	c.mu.Unlock()

	// Only recovery mode enumerates legacy role directories. Once reconciliation
	// has built the migration index, media deletion remains O(files for media).
	if recoveryScan {
		for _, role := range roles {
			roleDir := c.roleDir(role)
			entries, err := c.readDir(roleDir)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return err
			}
			for _, entry := range entries {
				if entry.IsDir() || !strings.HasPrefix(entry.Name(), key+"-") {
					continue
				}
				path := filepath.Join(roleDir, entry.Name())
				if _, exists := seen[path]; !exists {
					seen[path] = struct{}{}
					targets = append(targets, path)
				}
			}
		}
	}
	for _, path := range targets {
		if _, err := c.removeMediaPath(path); err != nil {
			return err
		}
	}
	for _, role := range roles {
		_ = os.Remove(c.mediaRoleDir(role, mediaID))
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
	width, height := artworkWideMaxWidth, artworkWideMaxHeight
	if role == "poster" {
		width, height = artworkPosterMaxWidth, artworkPosterMaxHeight
	}
	return filepath.Join(c.mediaRoleDir(role, media.ID), fmt.Sprintf("generated-%s.jpg", c.mediaSourceKey(media, role, width, height)))
}

func (c *ArtworkCache) GeneratedMediaPreviewPath(media *model.Media, index int) string {
	if c == nil || media == nil || index <= 0 {
		return ""
	}
	return filepath.Join(c.mediaRoleDir("preview", media.ID), fmt.Sprintf("generated-%s-%02d.jpg", c.mediaSourceKey(media, "preview", artworkWideMaxWidth, artworkWideMaxHeight), index))
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
	key = fmt.Sprintf("%s-%dx%d", key, maxWidth, maxHeight)
	outputPath := filepath.Join(c.mediaRoleDir(role, id), fmt.Sprintf("%s.jpg", safeArtworkName(key)))
	if fileExists(outputPath) {
		c.hits.Add(1)
		c.touch(outputPath)
		return outputPath, nil
	}
	c.misses.Add(1)
	if err := c.generateOnce(outputPath, func(ctx context.Context) error { return c.writeResizedJPEG(ctx, file, outputPath, maxWidth, maxHeight) }); err != nil {
		return "", err
	}
	return outputPath, nil
}

func (c *ArtworkCache) writeResizedJPEG(ctx context.Context, reader io.Reader, outputPath string, maxWidth, maxHeight int) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	img, _, err := image.Decode(reader)
	if err != nil {
		return err
	}
	img = resizeImageToFit(img, maxWidth, maxHeight)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(outputPath), ".navi-artwork-*.part")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	c.temps.Add(1)
	committed := false
	defer func() {
		c.temps.Add(-1)
		_ = temp.Close()
		if !committed {
			_ = os.Remove(tempPath)
		}
	}()
	if err := jpeg.Encode(temp, img, &jpeg.Options{Quality: artworkJPEGQuality}); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if fileExists(outputPath) {
		return nil
	}
	if err := c.prepareFileCommit(ctx); err != nil {
		return err
	}
	defer c.completeFileCommit()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.rename(tempPath, outputPath); err != nil {
		return err
	}
	committed = true
	c.recordFile(outputPath)
	return nil
}

type ArtworkDiagnostics struct {
	Hits, Misses, TemporaryFiles, Walks int64
	Stats                               ArtworkCacheStats
}

func (c *ArtworkCache) Diagnostics() ArtworkDiagnostics {
	if c == nil {
		return ArtworkDiagnostics{}
	}
	return ArtworkDiagnostics{Hits: c.hits.Load(), Misses: c.misses.Load(), TemporaryFiles: c.temps.Load(), Walks: c.walks.Load(), Stats: c.Stats()}
}

func (c *ArtworkCache) roleDir(role string) string {
	return filepath.Join(c.root, role)
}

func (c *ArtworkCache) mediaRoleDir(role, mediaID string) string {
	key := c.mediaKey(&model.Media{ID: mediaID})
	return filepath.Join(c.roleDir(role), key)
}

func (c *ArtworkCache) mediaSourceKey(media *model.Media, role string, width, height int) string {
	if media == nil {
		return shortSHA1([]byte(role))
	}
	info, err := os.Stat(media.FilePath)
	identity := filepath.Clean(media.FilePath)
	if err == nil {
		identity = fmt.Sprintf("%s:%d:%d", identity, info.Size(), info.ModTime().UnixNano())
	}
	return shortSHA1([]byte(fmt.Sprintf("%s:%s:%s:%dx%d", c.mediaKey(media), role, identity, width, height)))
}

func (c *ArtworkCache) generateOnce(path string, fn func(context.Context) error) error {
	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		return ErrProcessGovernorStopped
	}
	if call, ok := c.inflight[path]; ok {
		c.mu.Unlock()
		select {
		case <-c.ctx.Done():
			return ErrProcessGovernorStopped
		case <-call.done:
			return call.err
		}
	}
	call := &artworkCall{done: make(chan struct{})}
	c.inflight[path] = call
	c.active[path]++
	c.addProducerLocked()
	c.mu.Unlock()
	call.err = fn(c.ctx)
	if call.err == nil && c.ctx.Err() != nil {
		call.err = c.ctx.Err()
	}
	c.mu.Lock()
	delete(c.inflight, path)
	close(call.done)
	c.mu.Unlock()
	c.releaseActivePath(path)
	c.finishProducer()
	return call.err
}

func (c *ArtworkCache) releaseActivePath(path string) {
	path = filepath.Clean(path)
	c.mu.Lock()
	if c.active[path] <= 1 {
		delete(c.active, path)
	} else {
		c.active[path]--
	}
	entry, indexed := c.index[path]
	finishEviction := indexed && entry.Evicting && c.active[path] == 0
	overLimit := c.indexBytes > c.highBytes || len(c.index) > c.maxFiles
	c.mu.Unlock()

	if finishEviction {
		if _, err := c.finishEvictingPath(path); err != nil && c.logger != nil {
			c.logger.Warnf("remove released artwork cache path failed: path=%s err=%v", path, err)
		}
		return
	}
	if overLimit {
		c.requestCleanup()
	}
}

// Reserve prevents capacity cleanup from removing a file while a caller reads it.
func (c *ArtworkCache) Reserve(path string) (func(), bool) {
	if c == nil || !c.IsCachedPath(path) {
		return func() {}, false
	}
	path = filepath.Clean(path)
	c.mu.Lock()
	entry, indexed := c.index[path]
	if c.closing || (indexed && entry.Evicting) {
		c.mu.Unlock()
		return func() {}, false
	}
	c.active[path]++
	if indexed && time.Since(entry.LastAccess) >= time.Hour {
		entry.LastAccess = time.Now()
		c.index[path] = entry
		c.markDirtyLocked(path, &entry)
	}
	c.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			c.releaseActivePath(path)
		})
	}, true
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
