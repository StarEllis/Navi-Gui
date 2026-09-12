package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"navi-desktop/config"
	"navi-desktop/model"
	"navi-desktop/repository"
)

// 支持的视频文件扩展名
var supportedExts = map[string]bool{
	".mkv":  true,
	".mp4":  true,
	".avi":  true,
	".mov":  true,
	".wmv":  true,
	".flv":  true,
	".webm": true,
	".m4v":  true,
	".ts":   true,
	".strm": true, // STRM 远程流文件
}

// extrasExcludeDirs Emby/Kodi 标准的非正片内容目录名（小写）
var extrasExcludeDirs = map[string]bool{
	"extras":            true,
	"extra":             true,
	"featurettes":       true,
	"behind the scenes": true,
	"deleted scenes":    true,
	"interviews":        true,
	"trailers":          true,
	"trailer":           true,
	"samples":           true,
	"sample":            true,
	"shorts":            true,
	"scenes":            true,
	"bonus":             true,
	"bonus features":    true,
}

// extrasSuffixes Emby 标准的特典文件命名后缀（小写）
var extrasSuffixes = []string{
	"-behindthescenes", "-deleted", "-featurette",
	"-interview", "-scene", "-short", "-trailer", "-sample",
}

// isExtrasPath 判断文件路径是否在非正片目录下
func isExtrasPath(filePath string) bool {
	parts := strings.Split(filepath.ToSlash(filePath), "/")
	for _, part := range parts {
		if extrasExcludeDirs[strings.ToLower(part)] {
			return true
		}
	}
	return false
}

// isExtrasFile 判断文件名是否含有非正片后缀
func isExtrasFile(filename string) bool {
	lower := strings.ToLower(strings.TrimSuffix(filename, filepath.Ext(filename)))
	for _, suffix := range extrasSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

// idTagPatterns 从文件名/文件夹名中提取元数据 ID 的正则
var idTagPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)[\[\{](tmdbid|tmdb)[=\-](\d+)[\]\}]`),
	regexp.MustCompile(`(?i)[\[\{](imdbid|imdb)[=\-](tt\d+)[\]\}]`),
	regexp.MustCompile(`(?i)[\[\{](tvdbid|tvdb)[=\-](\d+)[\]\}]`),
}

// yearInNamePattern 从文件名/文件夹名中提取年份 (2009) 或 [2009]
var yearInNamePattern = regexp.MustCompile(`[\(\[]((?:19|20)\d{2})[\)\]]`)

// parseIDFromName 从文件名/文件夹名中提取元数据 ID
func parseIDFromName(name string) (idType string, idValue string) {
	for _, pattern := range idTagPatterns {
		if m := pattern.FindStringSubmatch(name); len(m) >= 3 {
			return strings.ToLower(m[1]), m[2]
		}
	}
	return "", ""
}

// stackingPatterns 多 CD/多版本堆叠检测正则（P2）
var stackingPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)[_\-\.\s](cd|disc|disk|part|pt|dvd)\s*(\d+)`),
	regexp.MustCompile(`(?i)[_\-\.\s](cd|disc|disk|part|pt|dvd)\s*([a-d])`),
}

// versionPatterns 多版本检测正则（P2: Director's Cut, Extended, Remastered 等）
var versionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(director'?s?\s*cut|extended|unrated|remastered|theatrical|imax|criterion|special\s*edition)`),
	regexp.MustCompile(`(?i)\b(remux|2160p|1080p|720p|4k|uhd|hdr|sdr|3d)\b`),
}

// extractYearFromName 从文件名/文件夹名中提取年份
func extractYearFromName(name string) int {
	if m := yearInNamePattern.FindStringSubmatch(name); len(m) >= 2 {
		year, _ := strconv.Atoi(m[1])
		if year >= 1900 && year <= 2099 {
			return year
		}
	}
	return 0
}

// FFprobeResult FFprobe输出结构
type FFprobeResult struct {
	Streams []FFprobeStream `json:"streams"`
	Format  FFprobeFormat   `json:"format"`
}

// FFprobeStream 流信息
type FFprobeStream struct {
	Index         int    `json:"index"`
	CodecType     string `json:"codec_type"` // video, audio, subtitle
	CodecName     string `json:"codec_name"` // h264, hevc, aac, srt, ass
	CodecLongName string `json:"codec_long_name"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	Duration      string `json:"duration"`
	BitRate       string `json:"bit_rate"`
	// 字幕相关
	Tags        map[string]string  `json:"tags"`
	Disposition FFprobeDisposition `json:"disposition"`
}

// FFprobeDisposition 流标志
type FFprobeDisposition struct {
	Default int `json:"default"`
	Forced  int `json:"forced"`
}

// FFprobeFormat 格式信息
type FFprobeFormat struct {
	Filename       string `json:"filename"`
	Duration       string `json:"duration"`
	Size           string `json:"size"`
	BitRate        string `json:"bit_rate"`
	FormatName     string `json:"format_name"`
	FormatLongName string `json:"format_long_name"`
}

// SubtitleTrack 字幕轨道信息
type SubtitleTrack struct {
	Index    int    `json:"index"`
	Codec    string `json:"codec"`    // srt, ass, subrip, hdmv_pgs_subtitle
	Language string `json:"language"` // chi, eng, jpn等
	Title    string `json:"title"`    // 字幕标题
	Default  bool   `json:"default"`  // 是否默认
	Forced   bool   `json:"forced"`   // 是否强制
	Bitmap   bool   `json:"bitmap"`   // 是否为图形字幕（PGS/VobSub等，不可提取为文本）
}

// isBitmapSubtitle 判断字幕编解码器是否为图形字幕
func isBitmapSubtitle(codec string) bool {
	switch strings.ToLower(codec) {
	case "hdmv_pgs_subtitle", "pgssub", "dvd_subtitle", "dvdsub", "dvb_subtitle", "xsub":
		return true
	default:
		return false
	}
}

// ScannerService 媒体文件扫描服务
type eventBroadcaster interface {
	BroadcastEvent(string, interface{})
}

type ScannerService struct {
	mediaRepo                 *repository.MediaRepo
	seriesRepo                *repository.SeriesRepo
	personRepo                *repository.PersonRepo
	mediaPersonRepo           *repository.MediaPersonRepo
	matchRuleRepo             *repository.MatchRuleRepo // P2: 自定义匹配规则
	cfg                       *config.Config
	logger                    *zap.SugaredLogger
	wsHub                     eventBroadcaster // WebSocket事件广播
	nfoService                *NFOService      // NFO 本地元数据解析服务
	metadataHighPri           chan metadataCompletionTask
	metadataNormal            chan metadataCompletionTask
	metadataMu                sync.Mutex
	metadataState             map[string]metadataTaskPriority
	metadataAccepting         bool
	metadataOwner             *ScannerService
	metadataWorkerCtx         context.Context
	metadataWorkerCancel      context.CancelFunc
	metadataWorkerWG          sync.WaitGroup
	metadataStopOnce          sync.Once
	metadataStopDone          chan struct{}
	thumbnailService          *ThumbnailService
	thumbnailSettingsProvider ThumbnailSettingsProvider
	artworkCache              *ArtworkCache
	gfriendsAvatarService     *GfriendsAvatarService
	gfriendsAvatarsEnabled    func() bool
	sidecarCacheMu            sync.RWMutex
	sidecarCache              map[string]directorySidecarCacheEntry
	walkFileTree              func(string, filepath.WalkFunc) error
	statFile                  func(string) (os.FileInfo, error)
	readDir                   func(string) ([]os.DirEntry, error)
	readFile                  func(string) ([]byte, error)
	probeMediaFile            func(string) ([]byte, error)
	probeMediaFileContext     func(context.Context, string) ([]byte, error)
	probeGovernor             *processGovernor
	probeScope                *probeScope
	scanContext               context.Context
	scanTaskID                string
	strictScan                bool
	scanFailureMu             sync.Mutex
	scanFailureCount          int
	scanFirstFailure          error
	deferMetadata             bool
	deferredMetadataTasks     []metadataCompletionTask
	deferMediaEvents          bool
	deferredMediaEvents       []MediaMetadataEventData
	preparedPreviewCounts     map[string]int
	preparedProbes            map[string]preparedMediaProbe
	scanStartAlreadyBroadcast bool
}

type ProbeDiagnostics struct {
	ProcessDiagnostics
	Requests, Shared int64
}

func (s *ScannerService) ProbeDiagnostics() ProbeDiagnostics {
	if s == nil {
		return ProbeDiagnostics{}
	}
	d := ProbeDiagnostics{ProcessDiagnostics: s.probeGovernor.diagnostics()}
	if s.probeScope != nil {
		d.Requests = s.probeScope.requests.Load()
		d.Shared = s.probeScope.hits.Load()
	}
	return d
}

func NewScannerService(mediaRepo *repository.MediaRepo, seriesRepo *repository.SeriesRepo, personRepo *repository.PersonRepo, mediaPersonRepo *repository.MediaPersonRepo, cfg *config.Config, logger *zap.SugaredLogger) *ScannerService {
	workerCtx, workerCancel := context.WithCancel(context.Background())
	probeLimit := 2
	if cfg != nil && cfg.App.FFprobeConcurrency > 0 {
		probeLimit = cfg.App.FFprobeConcurrency
	}
	probeGovernor := newProcessGovernor(workerCtx, probeLimit)
	service := &ScannerService{
		mediaRepo:            mediaRepo,
		seriesRepo:           seriesRepo,
		personRepo:           personRepo,
		mediaPersonRepo:      mediaPersonRepo,
		cfg:                  cfg,
		logger:               logger,
		nfoService:           NewNFOService(logger),
		metadataHighPri:      make(chan metadataCompletionTask, metadataQueueBufferSize),
		metadataNormal:       make(chan metadataCompletionTask, metadataQueueBufferSize),
		metadataState:        make(map[string]metadataTaskPriority),
		metadataAccepting:    true,
		metadataWorkerCtx:    workerCtx,
		metadataWorkerCancel: workerCancel,
		metadataStopDone:     make(chan struct{}),
		thumbnailService:     NewThumbnailService(cfg, logger),
		sidecarCache:         make(map[string]directorySidecarCacheEntry),
		walkFileTree:         filepath.Walk,
		statFile:             os.Stat,
		readDir:              os.ReadDir,
		readFile:             os.ReadFile,
		probeGovernor:        probeGovernor,
	}
	service.startMetadataWorkers()
	return service
}

func (s *ScannerService) cloneForScanContext(ctx context.Context, taskID string) *ScannerService {
	if ctx == nil {
		ctx = context.Background()
	}
	return &ScannerService{
		mediaRepo:                 s.mediaRepo,
		seriesRepo:                s.seriesRepo,
		personRepo:                s.personRepo,
		mediaPersonRepo:           s.mediaPersonRepo,
		matchRuleRepo:             s.matchRuleRepo,
		cfg:                       s.cfg,
		logger:                    s.logger,
		wsHub:                     s.wsHub,
		nfoService:                s.nfoService,
		metadataHighPri:           s.metadataHighPri,
		metadataNormal:            s.metadataNormal,
		metadataOwner:             s,
		thumbnailService:          s.thumbnailService,
		thumbnailSettingsProvider: s.thumbnailSettingsProvider,
		artworkCache:              s.artworkCache,
		gfriendsAvatarService:     s.gfriendsAvatarService,
		gfriendsAvatarsEnabled:    s.gfriendsAvatarsEnabled,
		// 缓存【不能】跨扫描保留。它的失效判断靠目录的修改时间，而网络盘（实测
		// J:/Y:/Z:）在目录里新增文件后根本不更新这个时间——留着上次的缓存，新加的
		// nfo、字幕、海报就再也扫不出来了。每次扫描从空的开始，重读一遍换正确。
		sidecarCache:          make(map[string]directorySidecarCacheEntry),
		walkFileTree:          s.walkFileTree,
		statFile:              s.statFile,
		readDir:               s.readDir,
		readFile:              s.readFile,
		probeMediaFile:        s.probeMediaFile,
		probeMediaFileContext: s.probeMediaFileContext,
		probeGovernor:         s.probeGovernor,
		probeScope:            newProbeScope(ctx),
		scanContext:           ctx,
		scanTaskID:            taskID,
	}
}

type ScanOptions struct {
	TaskID           string
	Mode             string
	Incremental      bool
	CleanDeleted     bool
	UseEverything    bool
	EverythingAddr   string
	Context          context.Context
	SuppressTerminal bool
}

type OverwriteScanResult struct {
	Scanned              int
	TotalTargets         int
	Cleaned              int
	DeletedMediaIDs      []string
	CacheCleanupWarnings []error
	LastScan             time.Time
}

type scanRunResult struct {
	Count        int
	TotalTargets int
	Cleaned      int
}

type preparedScanPath struct {
	path string
	info os.FileInfo
}

type preparedOverwriteIO struct {
	paths         []preparedScanPath
	infos         map[string]os.FileInfo
	dirs          map[string][]os.DirEntry
	files         map[string][]byte
	probes        map[string]preparedMediaProbe
	preparedNFOs  map[string]*preparedNFOFile
	previewCounts map[string]int
}

type preparedMediaProbe struct {
	videoCodec string
	resolution string
	audioCodec string
	duration   float64
	streamURL  string
}

func newPreparedMediaProbe(media *model.Media) preparedMediaProbe {
	return preparedMediaProbe{
		videoCodec: media.VideoCodec,
		resolution: media.Resolution,
		audioCodec: media.AudioCodec,
		duration:   media.Duration,
		streamURL:  media.StreamURL,
	}
}

func (p preparedMediaProbe) apply(media *model.Media) {
	media.VideoCodec = p.videoCodec
	media.Resolution = p.resolution
	media.AudioCodec = p.audioCodec
	media.Duration = p.duration
	media.StreamURL = p.streamURL
}

func newPreparedOverwriteIO() *preparedOverwriteIO {
	return &preparedOverwriteIO{
		infos:         make(map[string]os.FileInfo),
		dirs:          make(map[string][]os.DirEntry),
		files:         make(map[string][]byte),
		probes:        make(map[string]preparedMediaProbe),
		preparedNFOs:  make(map[string]*preparedNFOFile),
		previewCounts: make(map[string]int),
	}
}

func (p *preparedOverwriteIO) stat(path string) (os.FileInfo, error) {
	if info, ok := p.infos[nfoPathKey(path)]; ok {
		return info, nil
	}
	return nil, &os.PathError{Op: "stat", Path: path, Err: fs.ErrNotExist}
}

func (p *preparedOverwriteIO) readDir(path string) ([]os.DirEntry, error) {
	entries, ok := p.dirs[nfoPathKey(path)]
	if !ok {
		return nil, &os.PathError{Op: "readdir", Path: path, Err: fs.ErrNotExist}
	}
	return append([]os.DirEntry(nil), entries...), nil
}

func (p *preparedOverwriteIO) readFile(path string) ([]byte, error) {
	data, ok := p.files[nfoPathKey(path)]
	if !ok {
		return nil, &os.PathError{Op: "read", Path: path, Err: fs.ErrNotExist}
	}
	return append([]byte(nil), data...), nil
}

func (p *preparedOverwriteIO) walk(root string, walkFn filepath.WalkFunc) error {
	root = normalizeMediaPath(root)
	var skippedDir string
	for _, entry := range p.paths {
		if entry.path != root && !isPathWithinRoot(entry.path, root) {
			continue
		}
		if skippedDir != "" && isPathWithinRoot(entry.path, skippedDir) {
			continue
		}
		skippedDir = ""
		err := walkFn(entry.path, entry.info, nil)
		if errors.Is(err, filepath.SkipDir) && entry.info.IsDir() {
			skippedDir = entry.path
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *ScannerService) prepareOverwriteIO(library *model.Library, options ScanOptions) (*preparedOverwriteIO, error) {
	prepared := newPreparedOverwriteIO()
	seen := make(map[string]bool)
	roots := library.RootPaths()
	if len(roots) == 0 {
		roots = []string{strings.TrimSpace(library.Path)}
	}

	for _, root := range roots {
		root = normalizeMediaPath(root)
		if root == "" || root == "." {
			return nil, &ScanIncompleteError{Err: fmt.Errorf("library has no scan roots")}
		}
		err := s.walk(root, func(path string, info os.FileInfo, walkErr error) error {
			if options.Context != nil {
				select {
				case <-options.Context.Done():
					return options.Context.Err()
				default:
				}
			}
			if walkErr != nil {
				return walkErr
			}
			if info == nil {
				return fmt.Errorf("filesystem returned no information for %s", path)
			}
			path = normalizeMediaPath(path)
			key := nfoPathKey(path)
			if seen[key] {
				return nil
			}
			seen[key] = true
			prepared.paths = append(prepared.paths, preparedScanPath{path: path, info: info})
			prepared.infos[key] = info
			if info.IsDir() {
				if _, ok := prepared.dirs[key]; !ok {
					prepared.dirs[key] = nil
				}
			}
			if path != root {
				parentKey := nfoPathKey(filepath.Dir(path))
				prepared.dirs[parentKey] = append(prepared.dirs[parentKey], fs.FileInfoToDirEntry(info))
			}
			return nil
		})
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil, err
			}
			return nil, &ScanIncompleteError{Root: root, Err: err}
		}
		if err := options.Context.Err(); err != nil {
			return nil, err
		}
		rootInfo, ok := prepared.infos[nfoPathKey(root)]
		if !ok || !rootInfo.IsDir() {
			return nil, &ScanIncompleteError{Root: root, Err: fmt.Errorf("scan root is not a directory")}
		}
	}

	for key := range prepared.dirs {
		sort.Slice(prepared.dirs[key], func(i, j int) bool {
			return prepared.dirs[key][i].Name() < prepared.dirs[key][j].Name()
		})
	}

	existingPaths := make(map[string]bool)
	if records, err := s.mediaRepo.ListIDAndPathByLibrary(library.ID); err == nil {
		for _, record := range records {
			existingPaths[nfoPathKey(record.FilePath)] = true
			if s.artworkCache != nil {
				prepared.previewCounts[record.ID] = len(s.artworkCache.GeneratedMediaPreviews(record.ID))
			}
		}
	} else {
		return nil, fmt.Errorf("load media before overwrite preparation: %w", err)
	}
	var matchRules []model.MatchRule
	if library.Type == "movie" && s.matchRuleRepo != nil {
		matchRules, _ = s.matchRuleRepo.ListEnabled(library.ID)
	}

	for _, entry := range prepared.paths {
		if err := options.Context.Err(); err != nil {
			return nil, err
		}
		if entry.info.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.path))
		if ext == ".nfo" || ext == ".strm" {
			data, err := s.read(entry.path)
			if err != nil {
				return nil, fmt.Errorf("prepare metadata file %s: %w", entry.path, err)
			}
			if err := options.Context.Err(); err != nil {
				return nil, err
			}
			prepared.files[nfoPathKey(entry.path)] = data
			if ext == ".nfo" && s.nfoService != nil {
				prepared.preparedNFOs[nfoPathKey(entry.path)] = s.nfoService.prepareNFO(entry.path, data, entry.info)
				if err := options.Context.Err(); err != nil {
					return nil, err
				}
			}
		}
		if !supportedExts[ext] {
			continue
		}
		if library.Type == "movie" {
			if isExtrasPath(entry.path) || isExtrasFile(filepath.Base(entry.path)) {
				continue
			}
			if !existingPaths[nfoPathKey(entry.path)] {
				if library.EnableFileFilter && library.MinFileSize > 0 && entry.info.Size() < int64(library.MinFileSize)*1024*1024 {
					continue
				}
				if s.applyMatchRulesSkip(entry.path, matchRules) {
					continue
				}
			}
		}
		if options.Incremental {
			continue
		}
		if ext == ".strm" {
			streamURL, err := parseSTRMData(prepared.files[nfoPathKey(entry.path)])
			if err != nil {
				return nil, fmt.Errorf("probe media %s at STRM parse stage: %w", entry.path, err)
			}
			media := &model.Media{FilePath: entry.path}
			s.probeSTRMMedia(media, streamURL)
			prepared.probes[nfoPathKey(entry.path)] = newPreparedMediaProbe(media)
			continue
		}
		output, err := s.runMediaProbe(entry.path)
		if err != nil {
			return nil, fmt.Errorf("probe media %s at ffprobe execution stage: %w", entry.path, err)
		}
		media := &model.Media{FilePath: entry.path}
		if err := s.applyFFprobeOutput(media, output); err != nil {
			return nil, fmt.Errorf("probe media %s at ffprobe decode stage: %w", entry.path, err)
		}
		prepared.probes[nfoPathKey(entry.path)] = newPreparedMediaProbe(media)
	}
	if err := options.Context.Err(); err != nil {
		return nil, err
	}
	return prepared, nil
}

func (s *ScannerService) ScanLibraryOverwrite(db *gorm.DB, library *model.Library, options ScanOptions) (OverwriteScanResult, error) {
	options.Mode = "overwrite"
	options.Incremental = false
	options.CleanDeleted = true
	return s.ScanLibraryAtomic(db, library, options)
}

// ScanLibraryAtomic performs all external I/O before applying the scan in one
// database transaction. A canceled or failed preparation cannot mutate media
// state, and a canceled transaction is rolled back by GORM.
func (s *ScannerService) ScanLibraryAtomic(db *gorm.DB, library *model.Library, options ScanOptions) (OverwriteScanResult, error) {
	var result OverwriteScanResult
	var runResult scanRunResult
	if db == nil {
		return result, fmt.Errorf("database is nil")
	}
	if library == nil {
		return result, fmt.Errorf("library is nil")
	}
	if strings.TrimSpace(options.Mode) == "" {
		options.Mode = "incremental"
	}
	if strings.TrimSpace(options.TaskID) == "" {
		options.TaskID = uuid.NewString()
	}
	if options.Context == nil {
		options.Context = context.Background()
	}
	runScanner := s.cloneForScanContext(options.Context, options.TaskID)
	defer runScanner.probeScope.close()
	runScanner.broadcastScanEvent(EventScanStarted, &ScanProgressData{
		TaskID:      options.TaskID,
		LibraryID:   library.ID,
		LibraryName: library.Name,
		Mode:        options.Mode,
		Status:      "running",
		Phase:       "preparing",
		Message:     fmt.Sprintf("preparing scan: %s", library.Name),
	})
	prepared, prepareErr := runScanner.prepareOverwriteIO(library, options)
	if prepareErr != nil {
		if !options.SuppressTerminal {
			runScanner.broadcastScanTerminal(library, options, runResult, prepareErr)
		}
		return OverwriteScanResult{}, prepareErr
	}
	// The prepared snapshot is authoritative for this run. Reaching out to
	// Everything again here would put network and filesystem I/O back in the tx.
	options.UseEverything = false
	var committedMediaEvents []MediaMetadataEventData
	var committedMetadataTasks []metadataCompletionTask

	err := db.WithContext(options.Context).Transaction(func(tx *gorm.DB) error {
		if err := options.Context.Err(); err != nil {
			return err
		}
		txRepos := repository.NewRepositories(tx)
		before, err := txRepos.Media.ListIDAndPathByLibrary(library.ID)
		if err != nil {
			return fmt.Errorf("load media before overwrite: %w", err)
		}

		txScanner := runScanner.cloneForAtomicScan(txRepos, options.Context, prepared)
		runResult, err = txScanner.scanLibraryWithOptionsCore(library, options)
		if err != nil {
			return err
		}
		result.Scanned = runResult.Count
		result.TotalTargets = runResult.TotalTargets
		result.Cleaned = runResult.Cleaned

		after, err := txRepos.Media.ListIDAndPathByLibrary(library.ID)
		if err != nil {
			return fmt.Errorf("load media after overwrite: %w", err)
		}
		remaining := make(map[string]bool, len(after))
		for _, record := range after {
			remaining[record.ID] = true
		}
		for _, record := range before {
			if !remaining[record.ID] {
				result.DeletedMediaIDs = append(result.DeletedMediaIDs, record.ID)
			}
		}
		result.LastScan = time.Now().UTC().Truncate(time.Second)
		if err := options.Context.Err(); err != nil {
			return err
		}
		updatedLibrary := *library
		updatedLibrary.LastScan = &result.LastScan
		if err := txRepos.Library.Update(&updatedLibrary); err != nil {
			return fmt.Errorf("update library last scan: %w", err)
		}
		committedMediaEvents = append(committedMediaEvents, txScanner.deferredMediaEvents...)
		committedMetadataTasks = append(committedMetadataTasks, txScanner.deferredMetadataTasks...)
		return options.Context.Err()
	})
	if err != nil {
		if !options.SuppressTerminal {
			runScanner.broadcastScanTerminal(library, options, runResult, err)
		}
		return OverwriteScanResult{}, err
	}
	for _, event := range committedMediaEvents {
		runScanner.broadcastMediaMetadataEvent(event.MediaID, event.LibraryID, event.MetadataPhase, event.Message)
	}
	for _, task := range committedMetadataTasks {
		s.enqueueMetadataCompletion(task.MediaID, task.LibraryID, task.Priority)
	}
	if !options.SuppressTerminal {
		runScanner.broadcastScanTerminal(library, options, runResult, nil)
	}

	if runScanner.artworkCache != nil {
		for _, mediaID := range result.DeletedMediaIDs {
			if cacheErr := runScanner.artworkCache.RemoveMedia(mediaID); cacheErr != nil {
				runScanner.logger.Warnf("scan committed but artwork cache cleanup failed: media=%s err=%v", mediaID, cacheErr)
				result.CacheCleanupWarnings = append(result.CacheCleanupWarnings, cacheErr)
			}
		}
	}
	return result, nil
}

func (s *ScannerService) cloneForAtomicScan(repos *repository.Repositories, ctx context.Context, prepared *preparedOverwriteIO) *ScannerService {
	metadataOwner := s
	if s.metadataOwner != nil {
		metadataOwner = s.metadataOwner
	}
	clone := &ScannerService{
		mediaRepo:                 repos.Media,
		seriesRepo:                repos.Series,
		personRepo:                repos.Person,
		mediaPersonRepo:           repos.MediaPerson,
		cfg:                       s.cfg,
		logger:                    s.logger,
		wsHub:                     s.wsHub,
		nfoService:                s.nfoService,
		metadataHighPri:           s.metadataHighPri,
		metadataNormal:            s.metadataNormal,
		metadataOwner:             metadataOwner,
		metadataState:             make(map[string]metadataTaskPriority),
		thumbnailService:          s.thumbnailService,
		thumbnailSettingsProvider: s.thumbnailSettingsProvider,
		sidecarCache:              make(map[string]directorySidecarCacheEntry),
		walkFileTree:              prepared.walk,
		statFile:                  prepared.stat,
		readDir:                   prepared.readDir,
		readFile:                  prepared.readFile,
		preparedProbes:            prepared.probes,
		probeGovernor:             s.probeGovernor,
		probeScope:                s.probeScope,
		scanContext:               ctx,
		scanTaskID:                s.scanTaskID,
		strictScan:                true,
		deferMetadata:             true,
		deferMediaEvents:          true,
		preparedPreviewCounts:     prepared.previewCounts,
		scanStartAlreadyBroadcast: true,
	}
	if s.nfoService != nil {
		clone.nfoService = s.nfoService.cloneWithPreparedIO(prepared.readFile, prepared.stat, prepared.readDir, prepared.preparedNFOs)
	}
	if s.matchRuleRepo != nil {
		clone.matchRuleRepo = repos.MatchRule
	}
	return clone
}

func (s *ScannerService) checkScanCanceled() error {
	if s == nil || s.scanContext == nil {
		return nil
	}
	select {
	case <-s.scanContext.Done():
		return s.scanContext.Err()
	default:
		return nil
	}
}

// ScanIncompleteError means the filesystem could not be enumerated reliably.
// Callers must not treat it as a successful scan or use it to delete records.
type ScanIncompleteError struct {
	Root string
	Err  error
}

func (e *ScanIncompleteError) Error() string {
	if e == nil {
		return "scan incomplete"
	}
	if strings.TrimSpace(e.Root) == "" {
		return fmt.Sprintf("scan incomplete: %v", e.Err)
	}
	return fmt.Sprintf("scan incomplete for root %s: %v", e.Root, e.Err)
}

func (e *ScanIncompleteError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func IsScanIncomplete(err error) bool {
	var incomplete *ScanIncompleteError
	return errors.As(err, &incomplete)
}

type ScanPartialError struct {
	Failed int
	Err    error
}

func (e *ScanPartialError) Error() string {
	if e == nil {
		return "scan partially failed"
	}
	return fmt.Sprintf("scan partially failed: %d item(s) still failed after 3 retries: %v", e.Failed, e.Err)
}

func (e *ScanPartialError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func IsScanPartial(err error) bool {
	var partial *ScanPartialError
	return errors.As(err, &partial)
}

type scanRootSnapshot struct {
	roots     []string
	filePaths map[string]bool
	complete  bool
	failure   error
}

type scanRootResult struct {
	root      string
	filePaths map[string]bool
	complete  bool
	failure   error
}

func newScanRootResult(root string) *scanRootResult {
	return &scanRootResult{
		root:      normalizeMediaPath(root),
		filePaths: make(map[string]bool),
	}
}

func (r *scanRootResult) addFile(path string) {
	if r == nil {
		return
	}
	path = normalizeMediaPath(path)
	if path != "" && path != "." {
		r.filePaths[path] = true
	}
}

func newScanRootSnapshot() *scanRootSnapshot {
	return &scanRootSnapshot{
		filePaths: make(map[string]bool),
		complete:  true,
	}
}

func (s *scanRootSnapshot) add(result *scanRootResult) {
	if s == nil || result == nil {
		return
	}
	s.roots = append(s.roots, result.root)
	for path := range result.filePaths {
		s.filePaths[path] = true
	}
	if !result.complete {
		s.complete = false
		if s.failure == nil {
			s.failure = result.failure
		}
	}
}

func (s *ScannerService) walk(root string, walkFn filepath.WalkFunc) error {
	if s.walkFileTree != nil {
		return s.walkFileTree(root, walkFn)
	}
	return filepath.Walk(root, walkFn)
}

func (s *ScannerService) stat(path string) (os.FileInfo, error) {
	if s.statFile != nil {
		return s.statFile(path)
	}
	return os.Stat(path)
}

func (s *ScannerService) listDir(path string) ([]os.DirEntry, error) {
	if s.readDir != nil {
		return s.readDir(path)
	}
	return os.ReadDir(path)
}

func (s *ScannerService) read(path string) ([]byte, error) {
	if s.readFile != nil {
		return s.readFile(path)
	}
	return os.ReadFile(path)
}

type scanProgressTracker struct {
	taskID  string
	mode    string
	total   int
	current int
}

type scanProgressThrottleState struct {
	lastSentAt time.Time
	lastMetric int
	lastPhase  string
}

var scanProgressStateStore = struct {
	sync.Mutex
	items map[string]*scanProgressTracker
}{
	items: make(map[string]*scanProgressTracker),
}

var scanProgressThrottleStore = struct {
	sync.Mutex
	items map[string]*scanProgressThrottleState
}{
	items: make(map[string]*scanProgressThrottleState),
}

const (
	metadataTaskPriorityNormal metadataTaskPriority = iota
	metadataTaskPriorityHigh
	metadataTaskPriorityRunning
	scanProgressBroadcastMinInterval = 200 * time.Millisecond
	scanProgressBroadcastMinStep     = 20
	scanProgressLogStep              = 250
	scanCreateBatchSize              = 100
	scanWriteRetryCount              = 3
	scanWriteRetryBaseDelay          = 25 * time.Millisecond
	metadataQueueBufferSize          = 16384
)

type metadataTaskPriority int

type metadataCompletionTask struct {
	MediaID   string
	Priority  metadataTaskPriority
	LibraryID string
}

type ActorRelationRepairStats struct {
	Candidates  int
	Synced      int
	Skipped     int
	Failed      int
	Unavailable int
}

type subtitleFileCandidate struct {
	stem string
	path string
}

type imageFileCandidate struct {
	name string
	stem string
	path string
}

type previewFileCandidate struct {
	name     string
	path     string
	priority int
	groupKey string
	order    int
}

type directorySidecarFiles struct {
	nfoByStem       map[string]string
	fallbackNFOPath string
	posterPath      string
	backdropPath    string
	subtitleFiles   []subtitleFileCandidate
	imageFiles      []imageFileCandidate
	videoFiles      []string
	previewFiles    []previewFileCandidate
	videoCount      int
	// artworkAliases maps a real video path to stems exposed by symlinks.
	artworkAliases map[string][]string
}

type directorySidecarSignature struct {
	dirModTime          time.Time
	extraFanartModTime  time.Time
	behindScenesModTime time.Time
}

type directorySidecarCacheEntry struct {
	signature directorySidecarSignature
	sidecars  *directorySidecarFiles
}

var sidecarImageNamesPoster = []string{
	"poster.jpg", "poster.png", "poster.webp",
	"cover.jpg", "cover.png", "cover.webp",
	"folder.jpg", "folder.png", "folder.webp",
	"thumb.jpg", "thumb.png", "thumb.webp",
	"movie.jpg", "movie.png",
	"show.jpg", "show.png",
}

var sidecarImageNamesBackdrop = []string{
	"fanart.jpg", "fanart.png", "fanart.webp",
	"backdrop.jpg", "backdrop.png", "backdrop.webp",
	"banner.jpg", "banner.png", "banner.webp",
	"background.jpg", "background.png", "background.webp",
	"clearart.jpg", "clearart.png",
	"landscape.jpg", "landscape.png",
}

var sidecarImageExts = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".webp": true,
}

var sidecarSubtitleExts = map[string]bool{
	".srt": true,
	".ass": true,
	".ssa": true,
	".vtt": true,
	".sub": true,
	".idx": true,
}

func normalizeFileModTime(ts time.Time) time.Time {
	return ts.UTC().Truncate(time.Second)
}

func hasValidFileModTime(ts *time.Time) bool {
	return ts != nil && !ts.IsZero()
}

func hasValidFileCreatedTime(ts *time.Time) bool {
	return ts != nil && !ts.IsZero()
}

func applyFileTimes(media *model.Media, info os.FileInfo) {
	if media == nil || info == nil {
		return
	}

	fileModTime := normalizeFileModTime(info.ModTime())
	media.FileSize = info.Size()
	media.FileModTime = &fileModTime

	if fileCreatedAt := ResolveFileCreatedTime(info); hasValidFileCreatedTime(fileCreatedAt) {
		media.FileCreatedAt = fileCreatedAt
	}
}

func normalizePreviewOwnershipStem(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer("_", "-", ".", "-", " ", "-").Replace(value)
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == '-'
	})
	return strings.Join(parts, "-")
}

func previewBelongsToMediaFile(name string, mediaFilePath string, requirePrefix bool) bool {
	if !requirePrefix {
		return true
	}
	mediaStem := normalizePreviewOwnershipStem(strings.TrimSuffix(filepath.Base(mediaFilePath), filepath.Ext(mediaFilePath)))
	imageStem := normalizePreviewOwnershipStem(strings.TrimSuffix(name, filepath.Ext(name)))
	if mediaStem == "" || imageStem == "" {
		return false
	}
	return strings.HasPrefix(imageStem, mediaStem+"-")
}

func isASCIIDigit(value byte) bool {
	return value >= '0' && value <= '9'
}

// NaturalLess 按自然序比较文件名。刮削器写出的剧照是 fanart1…fanart10，
// 纯字典序会把 fanart10 排到 fanart2 前面，展示顺序就乱了。
func NaturalLess(left, right string) bool {
	li, ri := 0, 0
	for li < len(left) && ri < len(right) {
		leftDigit := isASCIIDigit(left[li])
		rightDigit := isASCIIDigit(right[ri])
		if leftDigit != rightDigit {
			return left[li] < right[ri]
		}
		if !leftDigit {
			if left[li] != right[ri] {
				return left[li] < right[ri]
			}
			li++
			ri++
			continue
		}

		leftStart, rightStart := li, ri
		for li < len(left) && isASCIIDigit(left[li]) {
			li++
		}
		for ri < len(right) && isASCIIDigit(right[ri]) {
			ri++
		}
		// 去掉前导零后按位数比大小，避免长数字超出整型范围。
		leftNumber := strings.TrimLeft(left[leftStart:li], "0")
		rightNumber := strings.TrimLeft(right[rightStart:ri], "0")
		if len(leftNumber) != len(rightNumber) {
			return len(leftNumber) < len(rightNumber)
		}
		if leftNumber != rightNumber {
			return leftNumber < rightNumber
		}
	}
	return len(left)-li < len(right)-ri
}

func previewGroupKey(path, rootDir string) string {
	name := strings.ToLower(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	name = strings.NewReplacer("_", "-", ".", "-", " ", "-").Replace(name)
	tokens := strings.FieldsFunc(name, func(r rune) bool {
		return r == '-'
	})

	var kept []string
	removedCategory := false
	for _, token := range tokens {
		switch token {
		case "", "poster", "fanart", "thumb", "cover", "folder", "backdrop", "preview", "landscape", "image", "images":
			if token != "" {
				removedCategory = true
			}
			continue
		default:
			kept = append(kept, token)
		}
	}

	if len(kept) == 0 && removedCategory && filepath.Clean(filepath.Dir(path)) == filepath.Clean(rootDir) {
		return "primary"
	}
	if len(kept) == 0 {
		return name
	}
	return strings.Join(kept, "-")
}

func readDirectoryModTime(dir string) time.Time {
	return readDirectoryModTimeWithStat(dir, os.Stat)
}

func readDirectoryModTimeWithStat(dir string, stat func(string) (os.FileInfo, error)) time.Time {
	info, err := stat(dir)
	if err != nil || !info.IsDir() {
		return time.Time{}
	}
	return normalizeFileModTime(info.ModTime())
}

func readDirectorySidecarSignature(dir string) directorySidecarSignature {
	return readDirectorySidecarSignatureWithStat(dir, os.Stat)
}

func readDirectorySidecarSignatureWithStat(dir string, stat func(string) (os.FileInfo, error)) directorySidecarSignature {
	return directorySidecarSignature{
		dirModTime:          readDirectoryModTimeWithStat(dir, stat),
		extraFanartModTime:  readDirectoryModTimeWithStat(filepath.Join(dir, "extrafanart"), stat),
		behindScenesModTime: readDirectoryModTimeWithStat(filepath.Join(dir, "behind the scenes"), stat),
	}
}

func (s directorySidecarSignature) equals(other directorySidecarSignature) bool {
	return s.dirModTime.Equal(other.dirModTime) &&
		s.extraFanartModTime.Equal(other.extraFanartModTime) &&
		s.behindScenesModTime.Equal(other.behindScenesModTime)
}

func (s *ScannerService) buildDirectorySidecarFiles(dir string) *directorySidecarFiles {
	dir = filepath.Clean(strings.TrimSpace(dir))
	if s == nil || dir == "." || dir == "" {
		return collectDirectorySidecarFiles(dir)
	}

	signature := readDirectorySidecarSignatureWithStat(dir, s.stat)

	s.sidecarCacheMu.RLock()
	if entry, ok := s.sidecarCache[dir]; ok && entry.signature.equals(signature) && entry.sidecars != nil {
		s.sidecarCacheMu.RUnlock()
		return entry.sidecars
	}
	s.sidecarCacheMu.RUnlock()

	sidecars := s.collectDirectorySidecarFiles(dir)

	s.sidecarCacheMu.Lock()
	s.sidecarCache[dir] = directorySidecarCacheEntry{
		signature: signature,
		sidecars:  sidecars,
	}
	s.sidecarCacheMu.Unlock()

	return sidecars
}

func collectDirectorySidecarFiles(dir string) *directorySidecarFiles {
	return collectDirectorySidecarFilesWithReadDir(dir, os.ReadDir)
}

func (s *ScannerService) collectDirectorySidecarFiles(dir string) *directorySidecarFiles {
	return collectDirectorySidecarFilesWithReadDir(dir, s.listDir)
}

func collectDirectorySidecarFilesWithReadDir(dir string, readDir func(string) ([]os.DirEntry, error)) *directorySidecarFiles {
	result := &directorySidecarFiles{
		nfoByStem:      make(map[string]string),
		artworkAliases: make(map[string][]string),
	}

	entries, err := readDir(dir)
	if err != nil {
		return result
	}

	nameToPath := make(map[string]string, len(entries))
	imageNames := make([]string, 0, len(entries))
	firstImagePath := ""

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		lowerName := strings.ToLower(name)
		path := filepath.Join(dir, name)
		nameToPath[lowerName] = path

		ext := strings.ToLower(filepath.Ext(name))
		stem := strings.ToLower(strings.TrimSuffix(name, ext))

		if ext == ".nfo" {
			if _, exists := result.nfoByStem[stem]; !exists {
				result.nfoByStem[stem] = path
			}
			if result.fallbackNFOPath == "" {
				result.fallbackNFOPath = path
			}
		}

		if sidecarSubtitleExts[ext] {
			result.subtitleFiles = append(result.subtitleFiles, subtitleFileCandidate{
				stem: stem,
				path: path,
			})
		}

		if supportedExts[ext] {
			result.videoCount++
			result.videoFiles = append(result.videoFiles, path)
			if info, infoErr := os.Lstat(path); infoErr == nil && info.Mode()&os.ModeSymlink != 0 {
				if target, targetErr := filepath.EvalSymlinks(path); targetErr == nil {
					result.artworkAliases[target] = append(result.artworkAliases[target], stem)
				}
			}
		}

		if sidecarImageExts[ext] {
			imageNames = append(imageNames, name)
			result.imageFiles = append(result.imageFiles, imageFileCandidate{
				name: name,
				stem: stem,
				path: path,
			})
			if firstImagePath == "" {
				firstImagePath = path
			}
		}
	}

	sort.Strings(result.videoFiles)

	previewSubDirs := []struct {
		name     string
		priority int
	}{
		{name: "extrafanart", priority: 0},
		{name: "behind the scenes", priority: 1},
	}
	for _, sub := range previewSubDirs {
		subPath := filepath.Join(dir, sub.name)
		entries, err := readDir(subPath)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			path := filepath.Join(subPath, entry.Name())
			ext := strings.ToLower(filepath.Ext(entry.Name()))
			if !sidecarImageExts[ext] {
				continue
			}
			result.previewFiles = append(result.previewFiles, previewFileCandidate{
				name:     entry.Name(),
				path:     path,
				priority: sub.priority,
				groupKey: previewGroupKey(path, dir),
				order:    len(result.previewFiles),
			})
		}
	}

	for _, name := range sidecarImageNamesPoster {
		if path, ok := nameToPath[name]; ok {
			result.posterPath = path
			break
		}
	}

	for _, name := range sidecarImageNamesBackdrop {
		if path, ok := nameToPath[name]; ok {
			result.backdropPath = path
			break
		}
	}

	hasToken := func(name string, token string) bool {
		lower := strings.ToLower(name)
		ext := strings.ToLower(filepath.Ext(lower))
		stem := strings.TrimSuffix(lower, ext)
		normalized := "-" + strings.NewReplacer("_", "-", ".", "-", " ", "-").Replace(stem) + "-"
		return strings.Contains(normalized, "-"+token+"-")
	}

	findByTokens := func(tokens []string, excludeTokens []string) string {
		for _, token := range tokens {
			for _, imageName := range imageNames {
				if !hasToken(imageName, token) {
					continue
				}

				skip := false
				for _, excludeToken := range excludeTokens {
					if hasToken(imageName, excludeToken) {
						skip = true
						break
					}
				}
				if skip {
					continue
				}

				if path, ok := nameToPath[strings.ToLower(imageName)]; ok {
					return path
				}
			}
		}
		return ""
	}

	if result.posterPath == "" {
		result.posterPath = findByTokens([]string{"poster"}, nil)
	}
	if result.backdropPath == "" {
		result.backdropPath = findByTokens([]string{"fanart"}, nil)
	}
	if result.backdropPath == "" {
		result.backdropPath = findByTokens([]string{"backdrop"}, nil)
	}
	if result.backdropPath == "" {
		result.backdropPath = findByTokens([]string{"background", "banner", "clearart", "landscape"}, nil)
	}
	if result.posterPath == "" {
		result.posterPath = findByTokens(
			[]string{"cover", "folder", "thumb", "movie", "show"},
			[]string{"fanart", "backdrop", "background", "banner", "clearart", "landscape"},
		)
	}
	if result.posterPath == "" && firstImagePath != "" && firstImagePath != result.backdropPath {
		result.posterPath = firstImagePath
	}

	return result
}

func normalizeSidecarStem(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer("_", "-", ".", "-", " ", "-").Replace(value)
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == '-'
	})
	return strings.Join(parts, "-")
}

func mediaSpecificSidecarMatch(imageStem string, mediaFilePath string, tokens []string) bool {
	mediaStem := normalizeSidecarStem(strings.TrimSuffix(filepath.Base(mediaFilePath), filepath.Ext(mediaFilePath)))
	imageStem = normalizeSidecarStem(imageStem)
	if mediaStem == "" || imageStem == "" {
		return false
	}
	stems := []string{mediaStem}
	// Some libraries keep a site/source prefix on the real file while the
	// shortcut and its artwork use the shorter title (for example
	// 489155.com@JUR-754-C.mp4 and JUR-754-C-poster.jpg).
	if at := strings.LastIndex(mediaStem, "@"); at >= 0 && at+1 < len(mediaStem) {
		stems = append(stems, normalizeSidecarStem(mediaStem[at+1:]))
	}
	for _, stem := range stems {
		prefix := stem + "-"
		if !strings.HasPrefix(imageStem, prefix) {
			continue
		}
		suffix := strings.TrimPrefix(imageStem, prefix)
		parts := strings.Split(suffix, "-")
		if len(parts) == 0 {
			return false
		}
		for _, token := range tokens {
			if parts[0] == token {
				return true
			}
		}
	}
	return false
}

func (d *directorySidecarFiles) hasMultipleVideos() bool {
	return d != nil && d.videoCount > 1
}

func (d *directorySidecarFiles) findMediaSpecificImagePath(mediaFilePath string, tokens []string) string {
	if d == nil {
		return ""
	}
	for _, image := range d.imageFiles {
		if mediaSpecificSidecarMatch(image.stem, mediaFilePath, tokens) {
			return image.path
		}
	}
	return ""
}

func (d *directorySidecarFiles) posterPathForMedia(mediaFilePath string) string {
	if d == nil {
		return ""
	}
	if path := d.findMediaSpecificImagePath(mediaFilePath, []string{"poster", "cover", "folder", "thumb", "movie", "show"}); path != "" {
		return path
	}
	if target, err := filepath.EvalSymlinks(mediaFilePath); err == nil {
		for _, alias := range d.artworkAliases[target] {
			if path := d.findMediaSpecificImagePath(alias+".mp4", []string{"poster", "cover", "folder", "thumb", "movie", "show"}); path != "" {
				return path
			}
		}
	}
	if d.hasMultipleVideos() {
		return ""
	}
	return d.posterPath
}

func (d *directorySidecarFiles) backdropPathForMedia(mediaFilePath string) string {
	if d == nil {
		return ""
	}
	if path := d.findMediaSpecificImagePath(mediaFilePath, []string{"fanart", "backdrop", "background", "banner", "clearart", "landscape"}); path != "" {
		return path
	}
	if d.hasMultipleVideos() {
		return ""
	}
	return d.backdropPath
}

func (d *directorySidecarFiles) nfoPathForMedia(mediaFilePath string) string {
	if d == nil {
		return ""
	}

	base := strings.ToLower(strings.TrimSuffix(filepath.Base(mediaFilePath), filepath.Ext(mediaFilePath)))
	if path, ok := d.nfoByStem[base]; ok {
		return path
	}
	return d.fallbackNFOPath
}

func (d *directorySidecarFiles) subtitlesForMedia(mediaFilePath string) []string {
	if d == nil || len(d.subtitleFiles) == 0 {
		return nil
	}

	base := strings.ToLower(strings.TrimSuffix(filepath.Base(mediaFilePath), filepath.Ext(mediaFilePath)))
	found := make([]string, 0)
	for _, subtitle := range d.subtitleFiles {
		if strings.HasPrefix(subtitle.stem, base) {
			found = append(found, subtitle.path)
		}
	}
	sort.Strings(found)
	return found
}

func (s *ScannerService) FindNFOForMedia(mediaPath string) string {
	if strings.TrimSpace(mediaPath) == "" {
		return ""
	}
	sidecars := s.buildDirectorySidecarFiles(filepath.Dir(mediaPath))
	if sidecars == nil {
		return ""
	}
	return sidecars.nfoPathForMedia(mediaPath)
}

func (s *ScannerService) ListMediaFiles(mediaPath string) []string {
	if strings.TrimSpace(mediaPath) == "" {
		return nil
	}
	sidecars := s.buildDirectorySidecarFiles(filepath.Dir(mediaPath))
	if sidecars == nil || len(sidecars.videoFiles) == 0 {
		return nil
	}
	files := make([]string, len(sidecars.videoFiles))
	copy(files, sidecars.videoFiles)
	return files
}

func (s *ScannerService) CollectMediaPreviews(mediaPath string) []string {
	if strings.TrimSpace(mediaPath) == "" {
		return nil
	}

	sidecars := s.buildDirectorySidecarFiles(filepath.Dir(mediaPath))
	if sidecars == nil || len(sidecars.previewFiles) == 0 {
		return nil
	}

	requirePrefix := sidecars.hasMultipleVideos()
	rootDir := filepath.Dir(mediaPath)
	candidates := make([]previewFileCandidate, 0, len(sidecars.previewFiles))
	for _, candidate := range sidecars.previewFiles {
		if !previewBelongsToMediaFile(candidate.name, mediaPath, requirePrefix) {
			continue
		}
		candidates = append(candidates, candidate)
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].priority != candidates[j].priority {
			return candidates[i].priority < candidates[j].priority
		}
		leftName := strings.ToLower(filepath.Base(candidates[i].path))
		rightName := strings.ToLower(filepath.Base(candidates[j].path))
		if leftName != rightName {
			return NaturalLess(leftName, rightName)
		}
		return candidates[i].order < candidates[j].order
	})

	previews := make([]string, 0, len(candidates))
	seenPaths := make(map[string]bool)
	seenGroups := make(map[string]bool)
	for _, candidate := range candidates {
		if seenPaths[candidate.path] {
			continue
		}
		groupKey := candidate.groupKey
		if groupKey == "" {
			groupKey = previewGroupKey(candidate.path, rootDir)
		}
		if groupKey != "" && seenGroups[groupKey] {
			continue
		}
		seenPaths[candidate.path] = true
		if groupKey != "" {
			seenGroups[groupKey] = true
		}
		previews = append(previews, candidate.path)
	}

	return previews
}

func (s *ScannerService) scanExternalSubtitlesWithSidecars(media *model.Media, sidecars *directorySidecarFiles) {
	if sidecars == nil {
		s.scanExternalSubtitles(media)
		return
	}

	found := sidecars.subtitlesForMedia(media.FilePath)
	if len(found) > 0 {
		media.SubtitlePaths = strings.Join(found, "|")
	}
}

type scanMediaEntry struct {
	path string
	info os.FileInfo
}

func repairMisencodedUTF8Text(raw string) string {
	if raw == "" {
		return raw
	}

	buf := make([]byte, 0, len(raw))
	for _, r := range raw {
		if r > 0xFF {
			return raw
		}
		buf = append(buf, byte(r))
	}

	repaired := string(buf)
	if !utf8.ValidString(repaired) {
		return raw
	}
	return repaired
}

func (entry scanMediaEntry) resolvePathAndInfo(stat func(string) (os.FileInfo, error)) (string, os.FileInfo, error) {
	path := normalizeMediaPath(entry.path)
	if entry.info != nil {
		return path, entry.info, nil
	}

	info, err := stat(path)
	if err == nil {
		return path, info, nil
	}

	repairedPath := normalizeMediaPath(repairMisencodedUTF8Text(path))
	if repairedPath != "" && repairedPath != path {
		repairedInfo, repairedErr := stat(repairedPath)
		if repairedErr == nil {
			return repairedPath, repairedInfo, nil
		}
	}

	return path, nil, err
}

func normalizeMediaPath(path string) string {
	return filepath.Clean(strings.TrimSpace(path))
}

func buildVideoFingerprintFromInfo(info os.FileInfo) string {
	if info == nil {
		return ""
	}
	return fmt.Sprintf("%d|%d", info.Size(), normalizeFileModTime(info.ModTime()).UnixNano())
}

func buildVideoFingerprintFromStored(signature repository.MediaFileSignature) string {
	if strings.TrimSpace(signature.VideoFingerprint) != "" {
		return strings.TrimSpace(signature.VideoFingerprint)
	}
	if signature.FileModTime == nil {
		return ""
	}
	return fmt.Sprintf("%d|%d", signature.FileSize, normalizeFileModTime(*signature.FileModTime).UnixNano())
}

func buildSidecarFileStamp(path string) string {
	return buildSidecarFileStampWithStat(path, os.Stat)
}

func buildSidecarFileStampWithStat(path string, stat func(string) (os.FileInfo, error)) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	info, err := stat(path)
	if err != nil || info.IsDir() {
		return ""
	}
	return fmt.Sprintf("%s|%d|%d", strings.ToLower(filepath.Clean(path)), info.Size(), normalizeFileModTime(info.ModTime()).UnixNano())
}

func buildSidecarFingerprint(mediaPath string, sidecars *directorySidecarFiles) string {
	return buildSidecarFingerprintWithStat(mediaPath, sidecars, os.Stat)
}

func buildSidecarFingerprintWithStat(mediaPath string, sidecars *directorySidecarFiles, stat func(string) (os.FileInfo, error)) string {
	if sidecars == nil {
		sidecars = collectDirectorySidecarFiles(filepath.Dir(mediaPath))
	}
	if sidecars == nil {
		return ""
	}

	parts := []string{
		buildSidecarFileStampWithStat(sidecars.nfoPathForMedia(mediaPath), stat),
		buildSidecarFileStampWithStat(sidecars.posterPathForMedia(mediaPath), stat),
		buildSidecarFileStampWithStat(sidecars.backdropPathForMedia(mediaPath), stat),
	}
	for _, subtitlePath := range sidecars.subtitlesForMedia(mediaPath) {
		parts = append(parts, buildSidecarFileStampWithStat(subtitlePath, stat))
	}
	return strings.Join(parts, "||")
}

func updateMediaSyncFingerprints(media *model.Media, mediaPath string, info os.FileInfo, sidecars *directorySidecarFiles) {
	updateMediaSyncFingerprintsWithStat(media, mediaPath, info, sidecars, os.Stat)
}

func updateMediaSyncFingerprintsWithStat(media *model.Media, mediaPath string, info os.FileInfo, sidecars *directorySidecarFiles, stat func(string) (os.FileInfo, error)) {
	if media == nil {
		return
	}
	media.VideoFingerprint = buildVideoFingerprintFromInfo(info)
	media.SidecarFingerprint = buildSidecarFingerprintWithStat(mediaPath, sidecars, stat)
}

func shouldRefreshExistingMovieMedia(options ScanOptions, signature repository.MediaFileSignature, mediaPath string, info os.FileInfo, sidecars *directorySidecarFiles) (bool, bool) {
	return shouldRefreshExistingMovieMediaWithStat(options, signature, mediaPath, info, sidecars, os.Stat)
}

func shouldRefreshExistingMovieMediaWithStat(options ScanOptions, signature repository.MediaFileSignature, mediaPath string, info os.FileInfo, sidecars *directorySidecarFiles, stat func(string) (os.FileInfo, error)) (bool, bool) {
	currentVideoFingerprint := buildVideoFingerprintFromInfo(info)
	currentSidecarFingerprint := buildSidecarFingerprintWithStat(mediaPath, sidecars, stat)
	storedVideoFingerprint := buildVideoFingerprintFromStored(signature)
	storedSidecarFingerprint := strings.TrimSpace(signature.SidecarFingerprint)

	videoChanged := currentVideoFingerprint != storedVideoFingerprint
	sidecarChanged := currentSidecarFingerprint != storedSidecarFingerprint
	if !videoChanged && !sidecarChanged {
		return false, false
	}
	if options.Incremental && !sidecarChanged {
		return false, false
	}
	return true, videoChanged
}

func (s *ScannerService) ScanLibraryWithOptions(library *model.Library, options ScanOptions) (int, error) {
	result, err := s.ScanLibraryWithOptionsResult(library, options)
	return result.Scanned, err
}

func (s *ScannerService) ScanLibraryWithOptionsResult(library *model.Library, options ScanOptions) (OverwriteScanResult, error) {
	if strings.TrimSpace(options.TaskID) == "" {
		options.TaskID = uuid.NewString()
	}
	if options.Context == nil {
		options.Context = context.Background()
	}
	runScanner := s.cloneForScanContext(options.Context, options.TaskID)
	defer runScanner.probeScope.close()
	result, err := runScanner.scanLibraryWithOptionsCore(library, options)
	if library != nil && !options.SuppressTerminal {
		runScanner.broadcastScanTerminal(library, options, result, err)
	}
	return OverwriteScanResult{
		Scanned:      result.Count,
		TotalTargets: result.TotalTargets,
		Cleaned:      result.Cleaned,
	}, err
}

func (s *ScannerService) scanLibraryWithOptionsCore(library *model.Library, options ScanOptions) (scanRunResult, error) {
	if library == nil {
		return scanRunResult{}, fmt.Errorf("library is nil")
	}
	if options.Context != nil {
		select {
		case <-options.Context.Done():
			return scanRunResult{}, options.Context.Err()
		default:
		}
	}

	s.logger.Infof("start scanning library: %s (%s), mode=%s", library.Name, library.Path, options.Mode)
	totalTargets := 0
	s.beginScanProgress(library, options.Mode, totalTargets)
	defer s.endScanProgress(library.ID)

	if !s.scanStartAlreadyBroadcast {
		s.broadcastScanEvent(EventScanStarted, &ScanProgressData{
			TaskID:      options.TaskID,
			LibraryID:   library.ID,
			LibraryName: library.Name,
			Mode:        options.Mode,
			Status:      "running",
			Phase:       "scanning",
			Total:       totalTargets,
			Message:     fmt.Sprintf("start scanning: %s", library.Name),
		})
	}

	rootPaths := library.RootPaths()
	if len(rootPaths) == 0 {
		rootPaths = []string{strings.TrimSpace(library.Path)}
	}
	normalizedRoots := make([]string, 0, len(rootPaths))
	for _, rootPath := range rootPaths {
		rootPath = normalizeMediaPath(rootPath)
		if rootPath != "" && rootPath != "." {
			normalizedRoots = append(normalizedRoots, rootPath)
		}
	}

	var count int
	var scanErr error
	cleanDeleted := options.CleanDeleted || options.Mode == "delete_update"
	snapshot := newScanRootSnapshot()
	if len(normalizedRoots) == 0 {
		scanErr = &ScanIncompleteError{Err: fmt.Errorf("library has no scan roots")}
		snapshot.complete = false
		snapshot.failure = scanErr
	}

	for _, rootPath := range normalizedRoots {
		if scanErr != nil {
			break
		}
		if err := s.checkScanCanceled(); err != nil {
			scanErr = err
			break
		}

		rootLibrary := *library
		rootLibrary.Path = rootPath

		var scanned int
		var result *scanRootResult
		var err error
		switch library.Type {
		case "tvshow":
			scanned, result, err = s.scanTVShowLibrary(&rootLibrary)
		case "mixed":
			scanned, result, err = s.scanMixedLibrary(&rootLibrary)
		default:
			scanned, result, err = s.scanMovieLibraryWithOptions(&rootLibrary, options)
		}
		if result == nil {
			result = newScanRootResult(rootPath)
		}
		result.complete = err == nil
		result.failure = err
		snapshot.add(result)
		count += scanned
		if err != nil {
			scanErr = err
			break
		}
	}
	totalTargets = len(snapshot.filePaths)
	if scanErr == nil {
		scanErr = s.checkScanCanceled()
	}
	if scanErr == nil && options.Mode == "overwrite" && s.strictScan {
		scanErr = s.forceRefreshSnapshotMetadata(library, snapshot)
	}

	cleaned := 0
	if scanErr == nil && cleanDeleted {
		if !snapshot.complete || len(snapshot.roots) != len(normalizedRoots) {
			scanErr = &ScanIncompleteError{Err: snapshot.failure}
		} else {
			cleaned, scanErr = s.syncDeletedRecords(library, snapshot)
		}
	}
	if scanErr == nil {
		scanErr = s.partialScanError()
	}

	result := scanRunResult{
		Count:        count,
		TotalTargets: totalTargets,
		Cleaned:      cleaned,
	}

	s.logger.Infof("scan database phase finished: %s, new=%d, cleaned=%d", library.Name, count, cleaned)
	return result, scanErr
}

func (s *ScannerService) broadcastScanTerminal(library *model.Library, options ScanOptions, result scanRunResult, scanErr error) {
	if library == nil {
		return
	}
	eventType := scanTerminalEvent(scanErr)
	phase := "completed"
	message := fmt.Sprintf("scan completed: %s, new media: %d", library.Name, result.Count)
	if scanErr != nil {
		phase = "failed"
		message = scanErr.Error()
		if errors.Is(scanErr, context.Canceled) {
			phase = "canceled"
		} else if IsScanIncomplete(scanErr) || IsScanPartial(scanErr) {
			phase = "incomplete"
		}
	}

	s.broadcastScanEvent(eventType, &ScanProgressData{
		TaskID:       options.TaskID,
		LibraryID:    library.ID,
		LibraryName:  library.Name,
		Mode:         options.Mode,
		Status:       phase,
		Phase:        phase,
		FailureStage: scanFailureStage(scanErr),
		Retryable:    scanErr != nil && !errors.Is(scanErr, context.Canceled),
		NewFound:     result.Count,
		Current:      result.TotalTargets,
		Total:        result.TotalTargets,
		Cleaned:      result.Cleaned,
		Message:      message,
	})
	s.logger.Infof("scan finished: %s, new=%d, cleaned=%d, err=%v", library.Name, result.Count, result.Cleaned, scanErr)
}

func scanTerminalEvent(err error) string {
	if err == nil {
		return EventScanCompleted
	}
	if errors.Is(err, context.Canceled) {
		return EventScanCanceled
	}
	if IsScanIncomplete(err) || IsScanPartial(err) {
		return EventScanIncomplete
	}
	return EventScanFailed
}

func scanFailureStage(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if IsScanIncomplete(err) {
		return "enumeration"
	}
	if IsScanPartial(err) {
		return "database"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "ffprobe"), strings.Contains(message, "probe media"):
		return "ffprobe"
	case strings.Contains(message, "nfo"), strings.Contains(message, "metadata"):
		return "metadata"
	case strings.Contains(message, "strm"):
		return "strm"
	case strings.Contains(message, "transaction"), strings.Contains(message, "database"), strings.Contains(message, "last scan"):
		return "database"
	default:
		return "scan"
	}
}

func (s *ScannerService) BroadcastScanTerminal(library *model.Library, options ScanOptions, scanned int, total int, cleaned int, scanErr error) {
	s.broadcastScanTerminal(library, options, scanRunResult{
		Count:        scanned,
		TotalTargets: total,
		Cleaned:      cleaned,
	}, scanErr)
}

func (s *ScannerService) SetMatchRuleRepo(repo *repository.MatchRuleRepo) {
	s.matchRuleRepo = repo
}

// SetWSHub 设置WebSocket Hub（延迟注入，避免循环依赖）
func (s *ScannerService) SetWSHub(hub *WSHub) {
	s.wsHub = hub
}

func (s *ScannerService) SetArtworkCache(cache *ArtworkCache) {
	if s == nil {
		return
	}
	s.artworkCache = cache
	if s.thumbnailService != nil {
		s.thumbnailService.SetArtworkCache(cache)
	}
}

func (s *ScannerService) SetGfriendsAvatarService(service *GfriendsAvatarService, enabled func() bool) {
	if s == nil {
		return
	}
	s.gfriendsAvatarService = service
	s.gfriendsAvatarsEnabled = enabled
}

func (s *ScannerService) CachedMediaPreviewsForSources(mediaID string, sourcePaths []string) []string {
	if s == nil || s.artworkCache == nil {
		return nil
	}
	return s.artworkCache.CachedMediaPreviewsForSources(mediaID, sourcePaths)
}

func (s *ScannerService) GeneratedMediaPreviews(mediaID string) []string {
	if s == nil || s.artworkCache == nil {
		return nil
	}
	return s.artworkCache.GeneratedMediaPreviews(mediaID)
}

func (s *ScannerService) CacheMediaPreviews(media *model.Media, sourcePaths []string, limit int) []string {
	if s == nil || s.artworkCache == nil || media == nil || len(sourcePaths) == 0 {
		return nil
	}
	paths, err := s.artworkCache.CacheMediaPreviews(media, sourcePaths, limit)
	if err != nil {
		if s.logger != nil {
			s.logger.Debugf("cache media previews failed: media=%s err=%v", media.ID, err)
		}
		return nil
	}
	return paths
}

func (s *ScannerService) CacheMediaArtworkForMedia(media *model.Media) bool {
	if s == nil || media == nil || strings.TrimSpace(media.FilePath) == "" {
		return false
	}
	return s.cacheMediaArtwork(media, s.buildDirectorySidecarFiles(filepath.Dir(media.FilePath)))
}

func (s *ScannerService) cacheMediaArtwork(media *model.Media, sidecars *directorySidecarFiles) bool {
	if s == nil || s.artworkCache == nil || media == nil {
		return false
	}
	_, _, changed, err := s.artworkCache.CacheMediaArtwork(media, sidecars)
	if err != nil {
		if s.logger != nil {
			s.logger.Debugf("cache media artwork failed: media=%s err=%v", media.ID, err)
		}
		return false
	}
	return changed
}

func (s *ScannerService) shouldFetchGfriendsAvatars() bool {
	if s == nil || s.gfriendsAvatarService == nil {
		return false
	}
	if s.gfriendsAvatarsEnabled == nil {
		return true
	}
	return s.gfriendsAvatarsEnabled()
}

func (s *ScannerService) startMetadataWorkers() {
	workers := runtime.NumCPU() / 4
	if workers < 1 {
		workers = 1
	}
	if workers > 3 {
		workers = 3
	}

	for i := 0; i < workers; i++ {
		s.metadataWorkerWG.Add(1)
		go func() {
			defer s.metadataWorkerWG.Done()
			s.metadataWorkerLoop()
		}()
	}
	go func() {
		s.metadataWorkerWG.Wait()
		close(s.metadataStopDone)
	}()
}

func (s *ScannerService) metadataWorkerLoop() {
	for {
		var task metadataCompletionTask

		select {
		case <-s.metadataWorkerCtx.Done():
			return
		case task = <-s.metadataHighPri:
		default:
			select {
			case <-s.metadataWorkerCtx.Done():
				return
			case task = <-s.metadataHighPri:
			case task = <-s.metadataNormal:
			}
		}

		if s.metadataWorkerCtx.Err() != nil {
			return
		}
		s.runMetadataCompletionTask(task)
	}
}

func (s *ScannerService) EnqueueMetadataCompletion(mediaID string, highPriority bool) bool {
	priority := metadataTaskPriorityNormal
	if highPriority {
		priority = metadataTaskPriorityHigh
	}
	return s.enqueueMetadataCompletion(mediaID, "", priority)
}

func (s *ScannerService) ResumeInterruptedMetadataCompletion(ctx context.Context) (int, error) {
	if s == nil || s.mediaRepo == nil {
		return 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	mediaIDs, err := s.mediaRepo.ListQuickMetadataIDs()
	if err != nil {
		return 0, err
	}

	resumed := 0
	for _, mediaID := range mediaIDs {
		select {
		case <-ctx.Done():
			return resumed, ctx.Err()
		default:
		}
		if s.EnqueueMetadataCompletion(mediaID, false) {
			resumed++
		}
	}
	return resumed, nil
}

func (s *ScannerService) enqueueMetadataCompletion(mediaID string, libraryID string, priority metadataTaskPriority) bool {
	mediaID = strings.TrimSpace(mediaID)
	if mediaID == "" {
		return false
	}
	task := metadataCompletionTask{
		MediaID:   mediaID,
		LibraryID: libraryID,
		Priority:  priority,
	}
	if s.deferMetadata {
		s.deferredMetadataTasks = append(s.deferredMetadataTasks, task)
		return true
	}
	if s.metadataOwner != nil {
		return s.metadataOwner.enqueueMetadataCompletion(mediaID, libraryID, priority)
	}
	if s.metadataWorkerCtx != nil {
		select {
		case <-s.metadataWorkerCtx.Done():
			return false
		default:
		}
	}

	s.metadataMu.Lock()
	if !s.metadataAccepting {
		s.metadataMu.Unlock()
		return false
	}
	if s.metadataState == nil {
		s.metadataState = make(map[string]metadataTaskPriority)
	}
	current, exists := s.metadataState[mediaID]
	switch {
	case !exists:
		s.metadataState[mediaID] = priority
	case current == metadataTaskPriorityRunning:
		s.metadataMu.Unlock()
		return false
	case current == metadataTaskPriorityHigh || current == priority:
		s.metadataMu.Unlock()
		return false
	default:
		s.metadataState[mediaID] = priority
	}
	s.metadataMu.Unlock()

	queue := s.metadataNormal
	if priority == metadataTaskPriorityHigh {
		queue = s.metadataHighPri
	}
	if queue == nil {
		s.metadataMu.Lock()
		delete(s.metadataState, mediaID)
		s.metadataMu.Unlock()
		return false
	}
	select {
	case queue <- task:
		return true
	case <-contextDone(s.scanContext):
	case <-contextDone(s.metadataWorkerCtx):
	}
	s.metadataMu.Lock()
	delete(s.metadataState, mediaID)
	s.metadataMu.Unlock()
	return false
}

func contextDone(ctx context.Context) <-chan struct{} {
	if ctx == nil {
		return nil
	}
	return ctx.Done()
}

func (s *ScannerService) Shutdown(ctx context.Context) error {
	if s == nil || s.metadataWorkerCancel == nil {
		return nil
	}
	s.metadataStopOnce.Do(func() {
		s.metadataMu.Lock()
		s.metadataAccepting = false
		s.metadataMu.Unlock()
		s.metadataWorkerCancel()
		if s.thumbnailService != nil {
			s.thumbnailService.Shutdown()
		}
		if s.probeScope != nil {
			s.probeScope.close()
		}
		if s.probeGovernor != nil {
			s.probeGovernor.shutdown()
		}
	})
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-s.metadataStopDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *ScannerService) runMetadataCompletionTask(task metadataCompletionTask) {
	if strings.TrimSpace(task.MediaID) == "" {
		return
	}

	s.metadataMu.Lock()
	state, exists := s.metadataState[task.MediaID]
	switch {
	case !exists:
		s.metadataMu.Unlock()
		return
	case state == metadataTaskPriorityRunning:
		s.metadataMu.Unlock()
		return
	case task.Priority < state:
		s.metadataMu.Unlock()
		return
	default:
		s.metadataState[task.MediaID] = metadataTaskPriorityRunning
	}
	s.metadataMu.Unlock()

	if err := s.completeMediaMetadataByID(task.MediaID); err != nil {
		s.logger.Warnf("complete media metadata failed: media=%s err=%v", task.MediaID, err)
	}

	s.metadataMu.Lock()
	delete(s.metadataState, task.MediaID)
	s.metadataMu.Unlock()
}

func (s *ScannerService) completeMediaMetadataByID(mediaID string) error {
	return s.completeMediaMetadataByIDWithMode(mediaID, false)
}

func (s *ScannerService) completeMediaMetadataByIDWithMode(mediaID string, force bool) error {
	media, err := s.mediaRepo.FindByID(mediaID)
	if err != nil || media == nil {
		return err
	}
	if !force && !NeedsMetadataCompletion(media) {
		return nil
	}

	info, statErr := s.stat(media.FilePath)
	if statErr != nil || info.IsDir() {
		s.markMediaMetadataPhase(media.ID, media.LibraryID, MetadataPhaseFailed, "metadata completion failed")
		if statErr != nil {
			return statErr
		}
		return fmt.Errorf("media path is not a file: %s", media.FilePath)
	}

	applyFileTimes(media, info)
	var sidecars *directorySidecarFiles
	if media.MediaType == "movie" {
		sidecars = s.buildDirectorySidecarFiles(filepath.Dir(media.FilePath))
		s.scanExternalSubtitlesWithSidecars(media, sidecars)
		if err := s.applyLocalSidecarsWithMode(media, media.FilePath, sidecars, true); err != nil {
			if force {
				return fmt.Errorf("parse local metadata for %s: %w", media.FilePath, err)
			}
			s.logger.Warnf("local metadata parse failed during background completion: path=%s err=%v", media.FilePath, err)
		}
	} else {
		s.scanExternalSubtitles(media)
	}
	if probeErr := s.probeMediaInfo(media); probeErr != nil {
		s.logger.Warnf("media probe failed: path=%s stage=%v", media.FilePath, probeErr)
		if force {
			return fmt.Errorf("probe media %s: %w", media.FilePath, probeErr)
		}
	}
	if media.MediaType == "movie" {
		s.resolveThumbnailState(media, sidecars)
	}
	updateMediaSyncFingerprintsWithStat(media, media.FilePath, info, sidecars, s.stat)
	media.MetadataPhase = MetadataPhaseFull

	if err := s.mediaRepo.Update(media); err != nil {
		s.markMediaMetadataPhase(media.ID, media.LibraryID, MetadataPhaseFailed, "metadata completion failed")
		return err
	}

	if media.MediaType == "movie" {
		if err := s.syncActorsForMedia(media, force); err != nil {
			return err
		}
	}
	s.broadcastMediaMetadataEvent(media.ID, media.LibraryID, media.MetadataPhase, "metadata completed")
	return nil
}

func (s *ScannerService) markMediaMetadataPhase(mediaID string, libraryID string, phase string, message string) {
	if strings.TrimSpace(mediaID) == "" {
		return
	}
	if err := s.mediaRepo.UpdateFields(mediaID, map[string]interface{}{
		"metadata_phase": phase,
	}); err != nil {
		s.logger.Warnf("update media metadata phase failed: media=%s phase=%s err=%v", mediaID, phase, err)
	}
	s.broadcastMediaMetadataEvent(mediaID, libraryID, phase, message)
}

func (s *ScannerService) broadcastMediaMetadataEvent(mediaID string, libraryID string, phase string, message string) {
	if s.deferMediaEvents {
		s.deferredMediaEvents = append(s.deferredMediaEvents, MediaMetadataEventData{
			MediaID:       mediaID,
			LibraryID:     libraryID,
			MetadataPhase: NormalizeMetadataPhase(phase),
			Message:       message,
		})
		return
	}
	if s.wsHub == nil {
		return
	}

	s.wsHub.BroadcastEvent(EventMediaMetadataUpdated, &MediaMetadataEventData{
		MediaID:       mediaID,
		LibraryID:     libraryID,
		MetadataPhase: NormalizeMetadataPhase(phase),
		Message:       message,
	})
}

func (s *ScannerService) applyLocalSidecars(media *model.Media, mediaPath string, sidecars *directorySidecarFiles) {
	_ = s.applyLocalSidecarsWithMode(media, mediaPath, sidecars, false)
}

func (s *ScannerService) applyLocalSidecarsWithMode(media *model.Media, mediaPath string, sidecars *directorySidecarFiles, overwrite bool) error {
	if media == nil {
		return nil
	}
	if sidecars == nil {
		return nil
	}

	// NFO 解析失败不能连带丢掉海报和背景图：媒体库列表全靠 poster_path 出图，
	// 解析出错就直接返回的话，这些条目在列表里只剩一张空卡片。
	var nfoErr error
	if nfoPath := sidecars.nfoPathForMedia(mediaPath); nfoPath != "" {
		if parseErr := s.nfoService.ParseMovieNFO(nfoPath, media); parseErr != nil {
			s.logger.Debugf("parse NFO failed: %s, err=%v", nfoPath, parseErr)
			nfoErr = parseErr
		}
	}

	posterPath := sidecars.posterPathForMedia(mediaPath)
	backdropPath := sidecars.backdropPathForMedia(mediaPath)
	if s.artworkCache != nil {
		s.cacheMediaArtwork(media, sidecars)
		if overwrite {
			if posterPath == "" && !s.artworkCache.IsCachedPath(media.PosterPath) {
				media.PosterPath = ""
			}
			if backdropPath == "" && !s.artworkCache.IsCachedPath(media.BackdropPath) {
				media.BackdropPath = ""
			}
		}
		if posterPath != "" && media.PosterPath == "" {
			media.PosterPath = posterPath
		}
		if backdropPath != "" && media.BackdropPath == "" {
			media.BackdropPath = backdropPath
		}
		return nfoErr
	}
	if overwrite {
		media.PosterPath = posterPath
		media.BackdropPath = backdropPath
		return nfoErr
	}
	if posterPath != "" && media.PosterPath == "" {
		media.PosterPath = posterPath
	}
	if backdropPath != "" && media.BackdropPath == "" {
		media.BackdropPath = backdropPath
	}
	return nfoErr
}

func (s *ScannerService) resolveThumbnailState(media *model.Media, sidecars *directorySidecarFiles) {
	if media == nil {
		return
	}
	if sidecars == nil && strings.TrimSpace(media.FilePath) != "" {
		sidecars = s.buildDirectorySidecarFiles(filepath.Dir(media.FilePath))
	}

	if s.strictScan && s.preparedPreviewCounts != nil {
		previewCount := s.preparedPreviewCounts[media.ID] + countPreparedSidecarPreviews(media.FilePath, sidecars)
		media.ThumbnailStatus = resolveThumbnailStateWithPreviewCount(media, sidecars, s.thumbnailSettings(), previewCount)
	} else if s.thumbnailService != nil {
		media.ThumbnailStatus = s.thumbnailService.resolveThumbnailState(media, sidecars, s.thumbnailSettings())
	} else {
		media.ThumbnailStatus = ResolveThumbnailState(media, sidecars, s.thumbnailSettings())
	}
	media.ThumbnailFingerprint = CurrentThumbnailFingerprint(media)
}

func countPreparedSidecarPreviews(mediaPath string, sidecars *directorySidecarFiles) int {
	if sidecars == nil {
		return 0
	}
	requirePrefix := sidecars.hasMultipleVideos()
	seen := make(map[string]bool)
	for _, candidate := range sidecars.previewFiles {
		if !seen[candidate.path] && previewBelongsToMediaFile(candidate.name, mediaPath, requirePrefix) {
			seen[candidate.path] = true
		}
	}
	return len(seen)
}

func (s *ScannerService) applyLibraryMetadataMode(library *model.Library, media *model.Media) {
	if library == nil || media == nil {
		return
	}

	switch library.MetadataMode {
	case "local_only":
		media.ScrapeStatus = "manual"
	case "local_preferred":
		if media.NfoRawXml != "" {
			media.ScrapeStatus = "manual"
		}
	}
}

func (s *ScannerService) prepareQuickMovieMedia(library *model.Library, media *model.Media, sidecars *directorySidecarFiles) {
	if media == nil {
		return
	}
	if sidecars == nil {
		sidecars = s.buildDirectorySidecarFiles(filepath.Dir(media.FilePath))
	}
	s.scanExternalSubtitlesWithSidecars(media, sidecars)
	s.applyLocalSidecars(media, media.FilePath, sidecars)
	media.MetadataPhase = MetadataPhaseQuick
	s.resolveThumbnailState(media, sidecars)
	s.applyLibraryMetadataMode(library, media)
}

func (s *ScannerService) prepareQuickEpisodeMedia(library *model.Library, media *model.Media) {
	if media == nil {
		return
	}
	s.scanExternalSubtitles(media)
	media.MetadataPhase = MetadataPhaseQuick
	s.resolveThumbnailState(media, nil)
	s.applyLibraryMetadataMode(library, media)
}

// applyLibraryAddedAt 把「加入时间」填进 media 行。权威值在 media_added_times
// 表里，这里只是取一份副本，好让排序不用每次 JOIN。
func (s *ScannerService) applyLibraryAddedAt(media *model.Media) {
	if s == nil || s.mediaRepo == nil || media == nil {
		return
	}
	addedAt, err := s.mediaRepo.ResolveAddedAt(media, time.Now())
	if err != nil {
		if s.logger != nil {
			s.logger.Warnf("resolve library added at failed: path=%s err=%v", media.FilePath, err)
		}
		return
	}
	media.LibraryAddedAt = &addedAt
}

func (s *ScannerService) persistQuickMedia(media *model.Media) error {
	if media == nil {
		return fmt.Errorf("media is nil")
	}
	s.applyLibraryAddedAt(media)
	err := s.retryScanWrite(fmt.Sprintf("save media %s", media.FilePath), func() error {
		return s.mediaRepo.Create(media)
	})
	if err == nil {
		if actorErr := s.syncQuickMediaActors(media); actorErr != nil {
			return actorErr
		}
		s.enqueueMetadataCompletion(media.ID, media.LibraryID, metadataTaskPriorityNormal)
		return nil
	}
	return err
}

func (s *ScannerService) retryScanWrite(label string, operation func() error) error {
	err := s.retryScanOperation(label, operation)
	if err != nil && !s.strictScan && !errors.Is(err, context.Canceled) {
		s.recordScanFailure(err)
	}
	return err
}

func (s *ScannerService) retryScanOperation(label string, operation func() error) error {
	var err error
	for attempt := 0; attempt <= scanWriteRetryCount; attempt++ {
		err = operation()
		if err == nil {
			return nil
		}
		if attempt == scanWriteRetryCount {
			break
		}
		if s.logger != nil {
			s.logger.Warnf("scan operation failed, retrying: operation=%s attempt=%d/%d err=%v", label, attempt+1, scanWriteRetryCount, err)
		}
		if waitErr := s.waitForScanRetry(time.Duration(attempt+1) * scanWriteRetryBaseDelay); waitErr != nil {
			return waitErr
		}
	}
	return fmt.Errorf("%s failed after %d retries: %w", label, scanWriteRetryCount, err)
}

func (s *ScannerService) waitForScanRetry(delay time.Duration) error {
	ctx := s.scanContext
	if ctx == nil {
		ctx = context.Background()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *ScannerService) recordScanFailure(err error) {
	if s == nil || err == nil {
		return
	}
	s.scanFailureMu.Lock()
	defer s.scanFailureMu.Unlock()
	s.scanFailureCount++
	if s.scanFirstFailure == nil {
		s.scanFirstFailure = err
	}
}

func (s *ScannerService) partialScanError() error {
	if s == nil {
		return nil
	}
	s.scanFailureMu.Lock()
	defer s.scanFailureMu.Unlock()
	if s.scanFailureCount == 0 {
		return nil
	}
	return &ScanPartialError{Failed: s.scanFailureCount, Err: s.scanFirstFailure}
}

func (s *ScannerService) requestMetadataCompletionIfNeeded(media *model.Media) bool {
	if media == nil || !NeedsMetadataCompletion(media) {
		return false
	}
	return s.enqueueMetadataCompletion(media.ID, media.LibraryID, metadataTaskPriorityNormal)
}

func (s *ScannerService) updateExistingEpisodeRecord(existing *model.Media, seriesID string, title string, ep EpisodeInfo) (bool, error) {
	if existing == nil {
		return false, nil
	}

	needUpdate := false
	if strings.TrimSpace(seriesID) != "" && existing.SeriesID != seriesID {
		existing.SeriesID = seriesID
		needUpdate = true
	}
	if strings.TrimSpace(title) != "" && existing.Title != title {
		existing.Title = title
		needUpdate = true
	}
	if existing.EpisodeTitle != ep.EpisodeTitle {
		existing.EpisodeTitle = ep.EpisodeTitle
		needUpdate = true
	}
	if existing.SeasonNum != ep.SeasonNum {
		existing.SeasonNum = ep.SeasonNum
		needUpdate = true
	}
	if existing.EpisodeNum != ep.EpisodeNum {
		existing.EpisodeNum = ep.EpisodeNum
		needUpdate = true
	}
	if ep.FileInfo != nil {
		prevFileSize := existing.FileSize
		prevModTime := existing.FileModTime
		applyFileTimes(existing, ep.FileInfo)
		if existing.FileSize != prevFileSize {
			needUpdate = true
		} else if prevModTime == nil || existing.FileModTime == nil {
			if !(prevModTime == nil && existing.FileModTime == nil) {
				needUpdate = true
			}
		} else if !prevModTime.Equal(*existing.FileModTime) {
			needUpdate = true
		}
	}

	if needUpdate {
		if err := s.mediaRepo.Update(existing); err != nil {
			s.logger.Warnf("update existing episode failed: %s, err=%v", existing.FilePath, err)
			return false, err
		}
	}

	s.requestMetadataCompletionIfNeeded(existing)
	return needUpdate, nil
}

// SyncActorsForMedia replaces a movie's actor relations with the actors from its local NFO.
func (s *ScannerService) SyncActorsForMedia(media *model.Media) {
	if err := s.syncActorsForMedia(media, false); err != nil && s.logger != nil {
		s.logger.Warnf("sync media actors failed: media=%s err=%v", media.ID, err)
	}
}

// SyncActorsForMediaStrict is used after an explicit NFO edit. Unlike ordinary
// scanning, relation failures are returned so callers cannot report success.
func (s *ScannerService) SyncActorsForMediaStrict(media *model.Media) error {
	return s.syncActorsForMedia(media, true)
}

// SyncActorsAfterMediaPathRepair refreshes actor relations from the repaired
// media path atomically and without performing optional avatar network I/O.
func (s *ScannerService) SyncActorsAfterMediaPathRepair(media *model.Media, db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("database is nil")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		repos := repository.NewRepositories(tx)
		return s.syncActorsForMediaWithOptions(media, true, repos.Person, repos.MediaPerson, false)
	})
}

// SyncActorsForMediaStrictWithDB binds the relation work to the caller's
// transaction. NFO file replacement is completed before callers open that tx.
func (s *ScannerService) SyncActorsForMediaStrictWithDB(media *model.Media, db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("database is nil")
	}
	repos := repository.NewRepositories(db)
	return s.syncActorsForMediaWithRepos(media, true, repos.Person, repos.MediaPerson)
}

func (s *ScannerService) syncActorsForMedia(media *model.Media, strict bool) error {
	return s.syncActorsForMediaWithRepos(media, strict, s.personRepo, s.mediaPersonRepo)
}

func (s *ScannerService) syncActorsForMediaWithRepos(media *model.Media, strict bool, personRepo *repository.PersonRepo, mediaPersonRepo *repository.MediaPersonRepo) error {
	return s.syncActorsForMediaWithOptions(media, strict, personRepo, mediaPersonRepo, true)
}

func (s *ScannerService) syncQuickMediaActors(media *model.Media) error {
	return s.syncActorsForMediaWithOptions(media, s.strictScan, s.personRepo, s.mediaPersonRepo, false)
}

func (s *ScannerService) syncActorsForMediaWithOptions(
	media *model.Media,
	strict bool,
	personRepo *repository.PersonRepo,
	mediaPersonRepo *repository.MediaPersonRepo,
	fetchAvatars bool,
) error {
	if media == nil || media.ID == "" || media.FilePath == "" || personRepo == nil || mediaPersonRepo == nil {
		return nil
	}

	nfoPath := s.nfoService.FindNFOForMedia(media.FilePath)
	if nfoPath == "" {
		return nil
	}

	var actors []NFOActor
	if strict {
		actorMetadata, err := s.nfoService.GetActorMetadataFromNFO(nfoPath)
		if err != nil {
			return fmt.Errorf("parse actors from %s: %w", nfoPath, err)
		}
		if !actorMetadata.ActorsPresent {
			return nil
		}
		actors = actorMetadata.Actors
	} else {
		var err error
		actors, _, err = s.nfoService.GetActorsFromNFO(nfoPath)
		if err != nil || len(actors) == 0 {
			return nil
		}
	}

	_, err := s.replaceActorRelations(media, actors, strict, personRepo, mediaPersonRepo, fetchAvatars)
	return err
}

func (s *ScannerService) replaceActorRelations(
	media *model.Media,
	actors []NFOActor,
	strict bool,
	personRepo *repository.PersonRepo,
	mediaPersonRepo *repository.MediaPersonRepo,
	fetchAvatars bool,
) (int, error) {
	if err := mediaPersonRepo.DeleteByMediaIDAndRole(media.ID, "actor"); err != nil {
		if strict {
			return 0, fmt.Errorf("delete old actor relations for %s: %w", media.ID, err)
		}
		return 0, nil
	}
	seen := make(map[string]bool)
	persisted := 0
	for index, actor := range actors {
		name := strings.TrimSpace(actor.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true

		person, err := personRepo.FindOrCreate(name, 0)
		if err != nil || person == nil {
			if strict && err != nil {
				return persisted, fmt.Errorf("find or create actor %s: %w", name, err)
			}
			continue
		}
		if fetchAvatars && s.shouldFetchGfriendsAvatars() && (strings.TrimSpace(person.ProfileURL) == "" || !fileExists(person.ProfileURL)) {
			changed, avatarErr := s.gfriendsAvatarService.EnsureActorAvatar(person)
			if avatarErr != nil {
				s.logger.Debugf("ensure actor avatar failed: actor=%s err=%v", name, avatarErr)
			} else if changed {
				if updateErr := personRepo.Update(person); updateErr != nil {
					s.logger.Warnf("persist actor avatar failed: actor=%s err=%v", name, updateErr)
				}
			}
		}

		sortOrder := actor.SortOrder
		if sortOrder <= 0 {
			sortOrder = index
		}

		if err := mediaPersonRepo.Create(&model.MediaPerson{
			MediaID:   media.ID,
			PersonID:  person.ID,
			Role:      "actor",
			SortOrder: sortOrder,
		}); err != nil {
			if strict {
				return persisted, fmt.Errorf("persist actor relation for %s: %w", media.ID, err)
			}
			s.logger.Warnf("persist actor relation failed: media=%s actor=%s err=%v", media.ID, name, err)
			continue
		}
		persisted++
	}
	if err := mediaPersonRepo.RefreshMediaSearchIndex(media.ID); err != nil {
		if strict {
			return persisted, fmt.Errorf("refresh media search index for %s: %w", media.ID, err)
		}
		s.logger.Warnf("refresh media search index failed: media=%s err=%v", media.ID, err)
	}
	return persisted, nil
}

func (s *ScannerService) RepairMissingActorRelations(ctx context.Context) (ActorRelationRepairStats, error) {
	var stats ActorRelationRepairStats
	if s == nil || s.mediaRepo == nil || s.personRepo == nil || s.mediaPersonRepo == nil || s.nfoService == nil {
		return stats, fmt.Errorf("actor relation repair is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	mediaItems, err := s.mediaRepo.ListMoviesMissingActorRelations()
	if err != nil {
		return stats, fmt.Errorf("load media missing actor relations: %w", err)
	}
	stats.Candidates = len(mediaItems)
	for i := range mediaItems {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		media := &mediaItems[i]
		if _, err := s.stat(media.FilePath); err != nil {
			stats.Unavailable++
			continue
		}
		nfoPath := s.nfoService.FindNFOForMedia(media.FilePath)
		if nfoPath == "" {
			stats.Skipped++
			continue
		}
		actors, _, parseErr := s.nfoService.GetActorsFromNFO(nfoPath)
		if parseErr != nil {
			stats.Failed++
			if s.logger != nil {
				s.logger.Warnf("repair actor relations failed: media=%s path=%s err=parse actors from %s: %v", media.ID, media.FilePath, nfoPath, parseErr)
			}
			continue
		}
		if len(actors) == 0 {
			stats.Skipped++
			continue
		}
		persisted, persistErr := s.replaceActorRelations(media, actors, true, s.personRepo, s.mediaPersonRepo, false)
		if persistErr != nil {
			stats.Failed++
			if s.logger != nil {
				s.logger.Warnf("repair actor relations failed: media=%s path=%s err=%v", media.ID, media.FilePath, persistErr)
			}
			continue
		}
		if persisted == 0 {
			stats.Skipped++
			continue
		}
		stats.Synced++
	}
	return stats, nil
}

// ScanLibrary 扫描媒体库目录
func (s *ScannerService) ScanLibrary(library *model.Library) (int, error) {
	return s.ScanLibraryWithOptions(library, ScanOptions{
		Mode:         "incremental",
		Incremental:  true,
		CleanDeleted: true,
	})
}

func isPathWithinAnyRoot(path string, roots []string) bool {
	for _, root := range roots {
		if isPathWithinRoot(path, root) {
			return true
		}
	}
	return false
}

func (s *ScannerService) ensureSnapshotRootsAvailable(snapshot *scanRootSnapshot) error {
	if snapshot == nil || len(snapshot.roots) == 0 {
		return &ScanIncompleteError{Err: fmt.Errorf("scan root snapshot is empty")}
	}
	for _, root := range snapshot.roots {
		info, err := s.stat(root)
		if err != nil {
			return &ScanIncompleteError{Root: root, Err: err}
		}
		if !info.IsDir() {
			return &ScanIncompleteError{Root: root, Err: fmt.Errorf("scan root is not a directory")}
		}
	}
	return nil
}

// ==================== P2: 并行 FFprobe 探测 ====================

// pendingMedia 待处理的媒体文件信息（P2: 用于并行 FFprobe 和批量入库）
func (s *ScannerService) refreshExistingMovieMedia(library *model.Library, existing *model.Media, mediaPath string, info os.FileInfo, sidecars *directorySidecarFiles, refreshVideo bool) (bool, error) {
	if existing == nil || info == nil {
		return false, nil
	}

	applyFileTimes(existing, info)
	s.scanExternalSubtitlesWithSidecars(existing, sidecars)
	if err := s.applyLocalSidecarsWithMode(existing, mediaPath, sidecars, true); err != nil {
		return false, err
	}
	if refreshVideo {
		if probeErr := s.probeMediaInfo(existing); probeErr != nil {
			s.logger.Warnf("media probe failed: path=%s stage=%v", mediaPath, probeErr)
			if s.strictScan {
				return false, fmt.Errorf("probe media %s: %w", mediaPath, probeErr)
			}
		}
	}
	existing.MetadataPhase = MetadataPhaseFull
	s.resolveThumbnailState(existing, sidecars)
	s.applyLibraryMetadataMode(library, existing)
	s.applyLibraryAddedAt(existing)
	updateMediaSyncFingerprintsWithStat(existing, mediaPath, info, sidecars, s.stat)

	if err := s.mediaRepo.Update(existing); err != nil {
		s.logger.Warnf("update existing movie failed: %s, err=%v", mediaPath, err)
		return false, err
	}

	if err := s.syncActorsForMedia(existing, s.strictScan); err != nil {
		return false, err
	}
	s.broadcastMediaMetadataEvent(existing.ID, existing.LibraryID, existing.MetadataPhase, "metadata updated")
	return true, nil
}

func (s *ScannerService) forceRefreshSnapshotMetadata(library *model.Library, snapshot *scanRootSnapshot) error {
	if library == nil || snapshot == nil || !snapshot.complete {
		return &ScanIncompleteError{Err: fmt.Errorf("cannot refresh metadata from incomplete scan")}
	}
	mediaItems, err := s.mediaRepo.ListByLibraryID(library.ID)
	if err != nil {
		return fmt.Errorf("load media for overwrite metadata refresh: %w", err)
	}
	for i := range mediaItems {
		if err := s.checkScanCanceled(); err != nil {
			return err
		}
		mediaPath := normalizeMediaPath(mediaItems[i].FilePath)
		if !snapshot.filePaths[mediaPath] {
			continue
		}
		if err := s.completeMediaMetadataByIDWithMode(mediaItems[i].ID, true); err != nil {
			return fmt.Errorf("force refresh metadata %s: %w", mediaPath, err)
		}
	}
	return nil
}

func (s *ScannerService) syncDeletedRecords(library *model.Library, snapshot *scanRootSnapshot) (int, error) {
	if library == nil {
		return 0, fmt.Errorf("library is nil")
	}
	if snapshot == nil || len(snapshot.roots) == 0 || !snapshot.complete {
		return 0, &ScanIncompleteError{Err: fmt.Errorf("scan root snapshot is empty")}
	}

	records, err := s.mediaRepo.ListIDAndPathByLibrary(library.ID)
	if err != nil {
		return 0, fmt.Errorf("load media paths before cleanup: %w", err)
	}
	if len(records) == 0 {
		return 0, nil
	}

	var deleteIDs []string

	for _, record := range records {
		normalizedPath := normalizeMediaPath(record.FilePath)
		if normalizedPath == "" || !isPathWithinAnyRoot(normalizedPath, snapshot.roots) {
			continue
		}
		if snapshot.filePaths[normalizedPath] {
			continue
		}

		_, statErr := s.stat(normalizedPath)
		if statErr == nil {
			continue
		}
		if !os.IsNotExist(statErr) {
			return 0, &ScanIncompleteError{Root: normalizedPath, Err: statErr}
		}

		deleteIDs = append(deleteIDs, record.ID)
	}

	if len(deleteIDs) == 0 {
		return 0, nil
	}
	if err := s.ensureSnapshotRootsAvailable(snapshot); err != nil {
		return 0, err
	}

	deleted, err := s.mediaRepo.DeleteByIDsAndRepairSeries(deleteIDs)
	if err != nil {
		return 0, fmt.Errorf("delete stale media transaction: %w", err)
	}
	totalDeleted := int(deleted)

	if s.artworkCache != nil {
		for _, mediaID := range deleteIDs {
			if cacheErr := s.artworkCache.RemoveMedia(mediaID); cacheErr != nil {
				s.logger.Debugf("remove stale media artwork cache failed: media=%s err=%v", mediaID, cacheErr)
			}
		}
	}
	s.broadcastScanEvent(EventScanProgress, &ScanProgressData{
		LibraryID:   library.ID,
		LibraryName: library.Name,
		Phase:       "cleaning",
		Current:     totalDeleted,
		Total:       len(deleteIDs),
		Cleaned:     totalDeleted,
		Message:     fmt.Sprintf("已清理 %d/%d 个已删除文件", totalDeleted, len(deleteIDs)),
	})

	return totalDeleted, nil
}

func (s *ScannerService) scanMovieLibraryWithOptions(library *model.Library, options ScanOptions) (int, *scanRootResult, error) {
	var count int
	var skippedExist int
	var skippedUpdated int
	var skippedRule int
	result := newScanRootResult(library.Path)

	s.logger.Infof("movie scan start: %s, path=%s, mode=%s", library.Name, library.Path, options.Mode)

	existingSignatures, err := s.mediaRepo.GetAllFileSignaturesByLibrary(library.ID)
	if err != nil {
		if s.strictScan {
			return 0, result, fmt.Errorf("preload media signatures: %w", err)
		}
		s.logger.Warnf("preload media signatures failed, fallback to path records: %v", err)
		pathRecords, listErr := s.mediaRepo.ListIDAndPathByLibrary(library.ID)
		if listErr != nil {
			s.logger.Warnf("preload media path records failed, fallback to single query: %v", listErr)
			existingSignatures = nil
		} else {
			existingSignatures = make(map[string]repository.MediaFileSignature, len(pathRecords))
			for _, record := range pathRecords {
				normalizedPath := normalizeMediaPath(record.FilePath)
				existingSignatures[normalizedPath] = repository.MediaFileSignature{
					ID:       record.ID,
					FilePath: normalizedPath,
					SeriesID: record.SeriesID,
				}
			}
		}
	} else {
		normalizedSignatures := make(map[string]repository.MediaFileSignature, len(existingSignatures))
		for path, signature := range existingSignatures {
			normalizedPath := normalizeMediaPath(path)
			signature.FilePath = normalizedPath
			normalizedSignatures[normalizedPath] = signature
		}
		existingSignatures = normalizedSignatures
		s.logger.Infof("preloaded %d media signatures", len(existingSignatures))
	}

	var matchRules []model.MatchRule
	if s.matchRuleRepo != nil {
		matchRules, _ = s.matchRuleRepo.ListEnabled(library.ID)
		if len(matchRules) > 0 {
			s.logger.Infof("loaded %d match rules", len(matchRules))
		}
	}

	entries, err := s.listMovieEntries(library, options, result)
	if err != nil {
		return 0, result, err
	}
	s.setScanProgressTotal(library, len(entries))

	prepared := s.prepareScanEntries(entries, existingSignatures, options)

	var pendingList []pendingMedia
	sidecarCache := make(map[string]*directorySidecarFiles)

	getSidecars := func(mediaPath string) *directorySidecarFiles {
		dir := filepath.Dir(mediaPath)
		if sidecars, ok := sidecarCache[dir]; ok {
			return sidecars
		}
		sidecars := s.buildDirectorySidecarFiles(dir)
		sidecarCache[dir] = sidecars
		return sidecars
	}

	for index, entry := range entries {
		if err := s.checkScanCanceled(); err != nil {
			return count, result, err
		}
		item := prepared[index]
		// 预处理被取消打断时这一项是空的，退回原地自己算一次。
		if !item.resolved {
			item.path, item.info, item.err = entry.resolvePathAndInfo(s.stat)
		}
		mediaPath, info, infoErr := item.path, item.info, item.err
		if infoErr != nil {
			return count, result, &ScanIncompleteError{Root: entry.path, Err: infoErr}
		}
		if info == nil || info.IsDir() {
			continue
		}
		if library.EnableFileFilter && library.MinFileSize > 0 {
			minBytes := int64(library.MinFileSize) * 1024 * 1024
			if info.Size() < minBytes {
				s.advanceScanProgress(library, fmt.Sprintf("跳过过小文件: %s", filepath.Base(mediaPath)))
				continue
			}
		}
		if s.applyMatchRulesSkip(mediaPath, matchRules) {
			skippedRule++
			s.advanceScanProgress(library, fmt.Sprintf("跳过规则: %s", filepath.Base(mediaPath)))
			continue
		}

		progressMessage := fmt.Sprintf("正在扫描: %s", filepath.Base(mediaPath))

		if existingSignatures != nil {
			if signature, ok := existingSignatures[mediaPath]; ok {
				sidecars := item.sidecars
				shouldRefresh, refreshVideo := item.shouldRefresh, item.refreshVideo
				if !item.compared {
					sidecars = getSidecars(mediaPath)
					shouldRefresh, refreshVideo = shouldRefreshExistingMovieMediaWithStat(options, signature, mediaPath, info, sidecars, s.stat)
				}
				if !shouldRefresh {
					skippedExist++
					s.advanceScanProgress(library, progressMessage)
					continue
				}

				existing, findErr := s.mediaRepo.FindByFilePathInLibrary(library.ID, mediaPath)
				if findErr != nil || existing == nil {
					if s.strictScan && findErr != nil {
						return count, result, fmt.Errorf("load existing movie %s: %w", mediaPath, findErr)
					}
					s.logger.Warnf("load existing movie failed: %s, err=%v", mediaPath, findErr)
					s.advanceScanProgress(library, progressMessage)
					continue
				}

				var updated bool
				refreshErr := s.retryScanWrite(fmt.Sprintf("refresh existing movie %s", mediaPath), func() error {
					var err error
					updated, err = s.refreshExistingMovieMedia(library, existing, mediaPath, info, sidecars, refreshVideo)
					return err
				})
				if refreshErr != nil {
					if s.strictScan {
						return count, result, fmt.Errorf("refresh existing movie %s: %w", mediaPath, refreshErr)
					}
					s.logger.Warnf("refresh existing movie failed: %s, err=%v", mediaPath, refreshErr)
				}
				if updated {
					skippedUpdated++
				}
				s.advanceScanProgress(library, progressMessage)
				continue
			}
		}

		if options.Mode == "delete_update" && existingSignatures == nil {
			existing, findErr := s.mediaRepo.FindByFilePathInLibrary(library.ID, mediaPath)
			if findErr == nil && existing != nil {
				sidecars := getSidecars(mediaPath)
				var updated bool
				refreshErr := s.retryScanWrite(fmt.Sprintf("refresh existing movie %s", mediaPath), func() error {
					var err error
					updated, err = s.refreshExistingMovieMedia(library, existing, mediaPath, info, sidecars, true)
					return err
				})
				if refreshErr != nil {
					if s.strictScan {
						return count, result, fmt.Errorf("refresh existing movie %s: %w", mediaPath, refreshErr)
					}
					s.logger.Warnf("refresh existing movie failed: %s, err=%v", mediaPath, refreshErr)
				}
				if updated {
					skippedUpdated++
				}
				s.advanceScanProgress(library, progressMessage)
				continue
			}
		}

		filename := filepath.Base(mediaPath)
		title, year, tmdbID := s.extractTitleEnhanced(filename)
		fileModTime := normalizeFileModTime(info.ModTime())
		media := &model.Media{
			LibraryID:     library.ID,
			Title:         title,
			FilePath:      mediaPath,
			FileSize:      info.Size(),
			FileModTime:   &fileModTime,
			MediaType:     "movie",
			Year:          year,
			TMDbID:        tmdbID,
			MetadataPhase: MetadataPhaseQuick,
			ScrapeStatus:  "pending",
		}
		applyFileTimes(media, info)
		s.applyMatchRulesAction(media, mediaPath, matchRules)

		stackBase, stackOrder := detectStacking(filename)
		if stackOrder > 0 {
			media.StackGroup = stackBase
			media.StackOrder = stackOrder
		}
		if versionTag := detectVersionTag(filename); versionTag != "" {
			media.VersionTag = versionTag
		}

		pendingList = append(pendingList, pendingMedia{
			media:   media,
			path:    mediaPath,
			info:    info,
			message: progressMessage,
		})
	}

	if len(pendingList) > 0 {
		stackGroups := make(map[string][]*pendingMedia)
		for i := range pendingList {
			if pendingList[i].media.StackGroup != "" {
				stackGroups[pendingList[i].media.StackGroup] = append(stackGroups[pendingList[i].media.StackGroup], &pendingList[i])
			}
		}
		for _, group := range stackGroups {
			if len(group) <= 1 {
				continue
			}
			groupID := group[0].media.Title
			for _, item := range group {
				item.media.VersionGroup = groupID
			}
		}

		flushCreateBatch := func(batch []pendingMedia) error {
			if len(batch) == 0 {
				return nil
			}

			mediaBatch := make([]*model.Media, 0, len(batch))
			for _, item := range batch {
				mediaBatch = append(mediaBatch, item.media)
			}

			if err := s.mediaRepo.BatchCreate(mediaBatch); err != nil {
				if s.strictScan {
					return fmt.Errorf("batch save media: %w", err)
				}
				s.logger.Warnf("batch save media failed, fallback to single insert: batch=%d err=%v", len(batch), err)
				for _, item := range batch {
					if singleErr := s.persistQuickMedia(item.media); singleErr != nil {
						s.logger.Warnf("save media failed: %s, err=%v", item.path, singleErr)
						s.advanceScanProgress(library, item.message)
						continue
					}
					count++
					s.advanceScanProgress(library, item.message)
				}
				return nil
			}

			for _, item := range batch {
				if actorErr := s.syncQuickMediaActors(item.media); actorErr != nil {
					return fmt.Errorf("sync quick media actors %s: %w", item.path, actorErr)
				}
				s.enqueueMetadataCompletion(item.media.ID, item.media.LibraryID, metadataTaskPriorityNormal)
				count++
				s.advanceScanProgress(library, item.message)
			}
			return nil
		}

		createBatch := make([]pendingMedia, 0, scanCreateBatchSize)
		for _, item := range pendingList {
			sidecars := getSidecars(item.path)
			s.prepareQuickMovieMedia(library, item.media, sidecars)
			updateMediaSyncFingerprintsWithStat(item.media, item.path, item.info, sidecars, s.stat)

			createBatch = append(createBatch, item)
			if len(createBatch) >= scanCreateBatchSize {
				if err := flushCreateBatch(createBatch); err != nil {
					return count, result, err
				}
				createBatch = createBatch[:0]
			}
		}
		if err := flushCreateBatch(createBatch); err != nil {
			return count, result, err
		}
	}

	s.logger.Infof("movie scan stats: %s total=%d new=%d unchanged=%d updated=%d ruleSkipped=%d",
		library.Name, len(entries), count, skippedExist, skippedUpdated, skippedRule)

	return count, result, nil
}

// scanPrepareConcurrency 是判定阶段的并发度。这一段只读——stat 文件、读目录、
// 算指纹——在网络盘上时间几乎全花在等一次次往返上（实测本地盘 0.6ms/部，网络盘
// 20ms/部），并发把等待重叠起来，实测 1.6~2.2 倍；再往上加到 32、64 收益就平了。
// 写数据库的活儿不在这里，仍然按原顺序串行做。
const scanPrepareConcurrency = 16

// preparedScanEntry 是一部片在判定阶段的全部只读结果。
type preparedScanEntry struct {
	path     string
	info     os.FileInfo
	err      error
	resolved bool // path/info/err 已填好

	sidecars      *directorySidecarFiles
	compared      bool // 跟库里已有记录比过了，下面三个字段才有意义
	shouldRefresh bool
	refreshVideo  bool
}

// prepareScanEntries 并发把每部片"要不要刷新"先算出来。结果按下标对号入座，
// 所以后面的主循环拿到的顺序和 entries 完全一致。
func (s *ScannerService) prepareScanEntries(
	entries []scanMediaEntry,
	existingSignatures map[string]repository.MediaFileSignature,
	options ScanOptions,
) []preparedScanEntry {
	prepared := make([]preparedScanEntry, len(entries))
	if len(entries) == 0 {
		return prepared
	}

	workers := scanPrepareConcurrency
	if workers > len(entries) {
		workers = len(entries)
	}

	indexes := make(chan int)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range indexes {
				item := &prepared[index]
				item.path, item.info, item.err = entries[index].resolvePathAndInfo(s.stat)
				item.resolved = true
				if item.err != nil || item.info == nil || item.info.IsDir() {
					continue
				}
				signature, ok := existingSignatures[item.path]
				if !ok {
					// 新片：sidecar 留给主循环去读，那边本来就要用它建记录。
					continue
				}
				item.sidecars = s.buildDirectorySidecarFiles(filepath.Dir(item.path))
				item.shouldRefresh, item.refreshVideo = shouldRefreshExistingMovieMediaWithStat(
					options, signature, item.path, item.info, item.sidecars, s.stat)
				item.compared = true
			}
		}()
	}

	for index := range entries {
		if s.checkScanCanceled() != nil {
			break
		}
		indexes <- index
	}
	close(indexes)
	wg.Wait()

	return prepared
}

type pendingMedia struct {
	media   *model.Media
	path    string
	info    os.FileInfo
	message string
}

const mediaProbeTimeout = 30 * time.Second
const mediaTranscodeTimeout = 2 * time.Minute

func newBackgroundCommand(parent context.Context, timeout time.Duration, executable string, args ...string) (*exec.Cmd, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	ctx := parent
	cancel := func() {}
	if timeout <= 0 {
		ctx, cancel = context.WithCancel(parent)
	} else {
		ctx, cancel = context.WithTimeout(parent, timeout)
	}
	cmd := exec.CommandContext(ctx, executable, args...)
	configureBackgroundCommand(cmd)
	return cmd, cancel
}

func runBackgroundCommand(parent context.Context, timeout time.Duration, combined bool, executable string, args ...string) ([]byte, error) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	configureBackgroundCommand(cmd)
	var output []byte
	var err error
	if combined {
		output, err = cmd.CombinedOutput()
	} else {
		output, err = cmd.Output()
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return output, ctxErr
	}
	return output, err
}

func (s *ScannerService) commandContext() context.Context {
	if s != nil && s.scanContext != nil {
		return s.scanContext
	}
	if s != nil && s.metadataWorkerCtx != nil {
		return s.metadataWorkerCtx
	}
	return context.Background()
}

// parallelProbe 使用 Worker Pool 并行执行 FFprobe 探测
func (s *ScannerService) parallelProbe(items []pendingMedia) {
	// 并发数 = min(CPU核数, 4)，避免 FFprobe 进程过多导致系统负载过高
	workers := runtime.NumCPU()
	if workers > 4 {
		workers = 4
	}
	if workers < 1 {
		workers = 1
	}

	type probeJob struct {
		index int
	}

	ctx := s.commandContext()
	jobs := make(chan probeJob, workers)
	var wg sync.WaitGroup

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case job, ok := <-jobs:
					if !ok {
						return
					}
					media := items[job.index].media
					if err := s.probeMediaInfo(media); err != nil && !errors.Is(err, context.Canceled) {
						s.logger.Warnf("media probe failed: path=%s stage=%v", media.FilePath, err)
					}
				}
			}
		}()
	}

enqueue:
	for i := range items {
		select {
		case <-ctx.Done():
			break enqueue
		case jobs <- probeJob{index: i}:
		}
	}
	close(jobs)
	wg.Wait()
}

// ==================== P2: 多 CD 堆叠检测 ====================

// detectStacking 检测文件名中的多 CD/多分卷标识
// 返回: (去除堆叠后缀的基础名, 堆叠序号)，序号为 0 表示非堆叠文件
func detectStacking(filename string) (baseName string, order int) {
	nameWithoutExt := strings.TrimSuffix(filename, filepath.Ext(filename))
	for _, pattern := range stackingPatterns {
		if m := pattern.FindStringSubmatchIndex(nameWithoutExt); m != nil {
			// 提取序号
			orderStr := nameWithoutExt[m[4]:m[5]]
			// 字母序号转数字: a=1, b=2, c=3, d=4
			if len(orderStr) == 1 && orderStr[0] >= 'a' && orderStr[0] <= 'd' {
				order = int(orderStr[0]-'a') + 1
			} else {
				order, _ = strconv.Atoi(orderStr)
			}
			if order > 0 {
				// 基础名 = 去除堆叠标识的部分
				baseName = strings.TrimSpace(nameWithoutExt[:m[0]])
				return baseName, order
			}
		}
	}
	return "", 0
}

// detectVersionTag 检测文件名中的版本标识（Director's Cut, Extended 等）
func detectVersionTag(filename string) string {
	nameWithoutExt := strings.TrimSuffix(filename, filepath.Ext(filename))
	if m := versionPatterns[0].FindStringSubmatch(nameWithoutExt); len(m) >= 2 {
		return m[1]
	}
	return ""
}

// ==================== P2: 自定义匹配规则集成 ====================

// applyMatchRulesSkip 检查文件是否应被匹配规则跳过
func (s *ScannerService) applyMatchRulesSkip(filePath string, rules []model.MatchRule) bool {
	for _, rule := range rules {
		if rule.Action != "skip" {
			continue
		}
		if s.matchRule(filePath, &rule) {
			// 更新命中计数
			if s.matchRuleRepo != nil {
				s.matchRuleRepo.IncrementHitCount(rule.ID)
			}
			return true
		}
	}
	return false
}

// applyMatchRulesAction 对媒体应用匹配规则的非跳过动作
func (s *ScannerService) applyMatchRulesAction(media *model.Media, filePath string, rules []model.MatchRule) {
	for _, rule := range rules {
		if rule.Action == "skip" {
			continue
		}
		if s.matchRule(filePath, &rule) {
			switch rule.Action {
			case "set_type":
				media.MediaType = rule.ActionValue
			case "set_genre":
				if media.Genres == "" {
					media.Genres = rule.ActionValue
				} else {
					media.Genres += "," + rule.ActionValue
				}
			}
			// 更新命中计数
			if s.matchRuleRepo != nil {
				s.matchRuleRepo.IncrementHitCount(rule.ID)
			}
			s.logger.Debugf("匹配规则命中: %s -> %s=%s", filepath.Base(filePath), rule.Action, rule.ActionValue)
		}
	}
}

// matchRule 测试文件路径是否匹配指定规则
func (s *ScannerService) matchRule(filePath string, rule *model.MatchRule) bool {
	target := filePath
	switch rule.RuleType {
	case "filename":
		target = filepath.Base(filePath)
		return strings.Contains(strings.ToLower(target), strings.ToLower(rule.Pattern))
	case "path":
		return strings.Contains(strings.ToLower(target), strings.ToLower(rule.Pattern))
	case "regex":
		re, err := regexp.Compile(rule.Pattern)
		if err != nil {
			return false
		}
		return re.MatchString(target)
	case "keyword":
		lower := strings.ToLower(filepath.Base(filePath))
		keywords := strings.Split(rule.Pattern, ",")
		for _, kw := range keywords {
			if strings.Contains(lower, strings.ToLower(strings.TrimSpace(kw))) {
				return true
			}
		}
		return false
	}
	return false
}

// scanMixedLibrary 扫描混合媒体库（智能区分电影和电视剧）
// 策略：遍历根目录第一层，对每个子目录判断是电影还是电视剧文件夹
// - 如果子目录内包含多个视频文件，或文件名匹配剧集命名模式，则视为电视剧
// - 如果子目录内只有单个视频文件且不匹配剧集模式，则视为电影
// - 根目录下的散落视频文件按电影处理
func (s *ScannerService) scanMixedLibrary(library *model.Library) (int, *scanRootResult, error) {
	s.logger.Infof("混合媒体库扫描: %s (%s)", library.Name, library.Path)
	result := newScanRootResult(library.Path)

	entries, err := s.listDir(library.Path)
	if err != nil {
		return 0, result, &ScanIncompleteError{Root: library.Path, Err: err}
	}

	s.logger.Infof("混合库根目录包含 %d 个条目", len(entries))

	var totalCount int
	movieSidecarCache := make(map[string]*directorySidecarFiles)
	getMovieSidecars := func(mediaPath string) *directorySidecarFiles {
		dir := filepath.Dir(mediaPath)
		if sidecars, ok := movieSidecarCache[dir]; ok {
			return sidecars
		}
		sidecars := s.buildDirectorySidecarFiles(dir)
		movieSidecarCache[dir] = sidecars
		return sidecars
	}
	// === 阶段一：收集子目录，按标准化系列名分组（用于多季合并检测） ===
	seriesDirGroups := make(map[string][]seriesFolder) // 标准化系列名 -> 目录列表
	var movieDirs []os.DirEntry                        // 被判定为电影的目录
	var looseVideoFiles []os.DirEntry                  // 根目录散落的视频文件

	for _, entry := range entries {
		if err := s.checkScanCanceled(); err != nil {
			return totalCount, result, err
		}
		if !entry.IsDir() {
			// 根目录下的散落视频文件
			ext := strings.ToLower(filepath.Ext(entry.Name()))
			if supportedExts[ext] {
				looseVideoFiles = append(looseVideoFiles, entry)
			}
			continue
		}

		dirName := entry.Name()
		folderPath := filepath.Join(library.Path, dirName)

		// 智能判断：该目录是电视剧还是电影
		isTV, classifyErr := s.isTVShowFolder(folderPath)
		if classifyErr != nil {
			return totalCount, result, &ScanIncompleteError{Root: folderPath, Err: classifyErr}
		}
		if isTV {
			// 电视剧目录：按标准化系列名分组（支持多季合并）
			normalizedName := s.normalizeSeriesName(dirName)
			seasonNum := s.extractSeasonFromDirName(dirName)
			seriesDirGroups[normalizedName] = append(seriesDirGroups[normalizedName], seriesFolder{
				path:      folderPath,
				dirName:   dirName,
				seasonNum: seasonNum,
			})
		} else {
			// 电影目录
			movieDirs = append(movieDirs, entry)
		}
	}

	// === 阶段二：处理电视剧目录（复用 scanTVShowLibrary 的分组逻辑） ===
	for normalizedName, folders := range seriesDirGroups {
		if err := s.checkScanCanceled(); err != nil {
			return totalCount, result, err
		}
		if len(folders) == 1 && folders[0].seasonNum == 0 {
			// 单个目录且未识别到季号 → 独立处理
			f := folders[0]
			seriesTitle := s.extractSeriesTitle(f.dirName)
			newCount, err := s.scanSeriesFolder(library, f.path, seriesTitle, result)
			if err != nil {
				return totalCount, result, err
			}
			totalCount += newCount
		} else {
			// 多季合并
			newCount, err := s.scanMultiSeasonSeries(library, normalizedName, folders, result)
			if err != nil {
				return totalCount, result, err
			}
			totalCount += newCount
		}
	}

	// === 阶段三：处理电影目录（扫描目录内的视频文件作为电影） ===
	for _, entry := range movieDirs {
		if err := s.checkScanCanceled(); err != nil {
			return totalCount, result, err
		}
		folderPath := filepath.Join(library.Path, entry.Name())
		err := s.walk(folderPath, func(path string, info os.FileInfo, walkErr error) error {
			if err := s.checkScanCanceled(); err != nil {
				return err
			}
			if walkErr != nil {
				return walkErr
			}
			if info.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			if !supportedExts[ext] {
				return nil
			}
			result.addFile(path)
			if existing, err := s.mediaRepo.FindByFilePathInLibrary(library.ID, path); err == nil {
				s.requestMetadataCompletionIfNeeded(existing)
				return nil // 已存在
			} else if s.strictScan && !errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("find existing media %s: %w", path, err)
			}
			title := s.extractTitle(filepath.Base(path))
			media := &model.Media{
				LibraryID: library.ID,
				Title:     title,
				FilePath:  path,
				FileSize:  info.Size(),
				MediaType: "movie",
			}
			applyFileTimes(media, info)
			s.prepareQuickMovieMedia(library, media, getMovieSidecars(path))
			if err := s.persistQuickMedia(media); err != nil {
				s.logger.Warnf("保存媒体失败: %s, 错误: %v", path, err)
				if s.strictScan {
					return fmt.Errorf("save mixed movie %s: %w", path, err)
				}
				return nil
			}
			totalCount++
			s.logger.Debugf("发现电影(混合库): %s", title)
			s.broadcastScanEvent(EventScanProgress, &ScanProgressData{
				LibraryID:   library.ID,
				LibraryName: library.Name,
				Phase:       "scanning",
				NewFound:    totalCount,
				Message:     fmt.Sprintf("发现电影: %s", title),
			})
			return nil
		})
		if err != nil {
			return totalCount, result, &ScanIncompleteError{Root: folderPath, Err: err}
		}
	}

	// === 阶段四：处理根目录散落的视频文件（作为电影） ===
	for _, entry := range looseVideoFiles {
		if err := s.checkScanCanceled(); err != nil {
			return totalCount, result, err
		}
		filePath := filepath.Join(library.Path, entry.Name())
		result.addFile(filePath)
		if existing, err := s.mediaRepo.FindByFilePathInLibrary(library.ID, filePath); err == nil {
			s.requestMetadataCompletionIfNeeded(existing)
			continue // 已存在
		} else if s.strictScan && !errors.Is(err, gorm.ErrRecordNotFound) {
			return totalCount, result, fmt.Errorf("find existing media %s: %w", filePath, err)
		}
		info, err := entry.Info()
		if err != nil {
			return totalCount, result, &ScanIncompleteError{Root: filePath, Err: err}
		}
		title := s.extractTitle(entry.Name())
		media := &model.Media{
			LibraryID: library.ID,
			Title:     title,
			FilePath:  filePath,
			FileSize:  info.Size(),
			MediaType: "movie",
		}
		applyFileTimes(media, info)
		s.prepareQuickMovieMedia(library, media, getMovieSidecars(filePath))
		if err := s.persistQuickMedia(media); err != nil {
			s.logger.Warnf("保存媒体失败: %s, 错误: %v", filePath, err)
			if s.strictScan {
				return totalCount, result, fmt.Errorf("save loose mixed movie %s: %w", filePath, err)
			}
			continue
		}
		totalCount++
		s.logger.Debugf("发现电影(散落): %s", title)
		s.broadcastScanEvent(EventScanProgress, &ScanProgressData{
			LibraryID:   library.ID,
			LibraryName: library.Name,
			Phase:       "scanning",
			NewFound:    totalCount,
			Message:     fmt.Sprintf("发现电影: %s", title),
		})
	}

	s.logger.Infof("混合媒体库扫描完成: %s, 新增 %d 个媒体", library.Name, totalCount)
	return totalCount, result, nil
}

// isTVShowFolder 智能判断一个目录是否为电视剧文件夹
// 判断依据（满足任一即认定为电视剧）：
// 1. 目录名包含季号标识（如 S1、Season 1、第一季）
// 2. 目录内包含 Season 子目录
// 3. 目录内有多个视频文件且文件名匹配剧集命名模式（S01E01、EP01、第N集等）
// 4. 目录内有多个视频文件且文件名包含连续编号
func (s *ScannerService) isTVShowFolder(folderPath string) (bool, error) {
	dirName := filepath.Base(folderPath)

	// 规则1: 目录名包含季号标识
	if s.extractSeasonFromDirName(dirName) > 0 {
		return true, nil
	}

	// 读取目录内容
	entries, err := s.listDir(folderPath)
	if err != nil {
		return false, err
	}

	// 规则2: 包含 Season 子目录
	var videoFiles []string
	for _, entry := range entries {
		if entry.IsDir() {
			for _, pattern := range seasonDirPatterns {
				if pattern.MatchString(entry.Name()) {
					return true, nil
				}
			}
			// 递归检查子目录中的视频文件（只深入一层）
			subEntries, err := s.listDir(filepath.Join(folderPath, entry.Name()))
			if err != nil && s.strictScan {
				return false, err
			}
			if err == nil {
				for _, subEntry := range subEntries {
					if !subEntry.IsDir() {
						ext := strings.ToLower(filepath.Ext(subEntry.Name()))
						if supportedExts[ext] {
							videoFiles = append(videoFiles, subEntry.Name())
						}
					}
				}
			}
		} else {
			ext := strings.ToLower(filepath.Ext(entry.Name()))
			if supportedExts[ext] {
				videoFiles = append(videoFiles, entry.Name())
			}
		}
	}

	// 只有0或1个视频文件 → 大概率是电影
	if len(videoFiles) <= 1 {
		return false, nil
	}

	// 规则3: 多个视频文件中有匹配剧集命名模式的
	episodeMatchCount := 0
	for _, vf := range videoFiles {
		ep := s.parseEpisodeInfo(vf)
		if ep.EpisodeNum > 0 {
			episodeMatchCount++
		}
	}

	// 如果超过一半的视频文件匹配剧集模式，认定为电视剧
	if episodeMatchCount > 0 && episodeMatchCount >= len(videoFiles)/2 {
		return true, nil
	}

	// 规则4: 有3个及以上视频文件（即使无法解析集号，多文件目录更可能是剧集）
	if len(videoFiles) >= 3 {
		return true, nil
	}

	return false, nil
}

// ==================== 剧集扫描逻辑 ====================

// 常见分辨率数字，用于排除误匹配
var resolutionNums = map[int]bool{
	240: true, 360: true, 480: true, 540: true,
	720: true, 1080: true, 1440: true, 2160: true, 4320: true,
}

// isResolutionContext 检查匹配位置前后是否有分辨率标志（如 p, P, i, I）
func isResolutionContext(filename string, matchEnd int) bool {
	if matchEnd < len(filename) {
		nextChar := filename[matchEnd]
		if nextChar == 'p' || nextChar == 'P' || nextChar == 'i' || nextChar == 'I' {
			return true
		}
	}
	return false
}

// 剧集命名模式正则
var episodePatterns = []*regexp.Regexp{
	// 模式0: S01E01 / S1E1 / s01e01
	regexp.MustCompile(`(?i)S(\d{1,2})\s*E(\d{1,4})`),
	// 模式1: S01.E01
	regexp.MustCompile(`(?i)S(\d{1,2})\.E(\d{1,4})`),
	// 模式2: 1x01 / 01x01
	regexp.MustCompile(`(?i)(\d{1,2})x(\d{1,4})`),
	// 模式3: 第01集 / 第1集
	regexp.MustCompile(`第\s*(\d{1,4})\s*集`),
	// 模式4: EP01 / EP.01 / Episode 01
	regexp.MustCompile(`(?i)(?:EP|Episode)\s*\.?\s*(\d{1,4})`),
	// 模式5: OVA01 / OVA 01 / SP01 / SP 01（特殊剧集类型+数字）
	regexp.MustCompile(`(?i)(?:OVA|OAD|SP|SPECIAL|NCOP|NCED)\s*(\d{1,4})`),
	// 模式6: E01（单独的E+数字）
	regexp.MustCompile(`(?i)\bE(\d{1,4})\b`),
	// 模式7: [01] / [001] / [12END] / [24END] — 方括号内的数字（可能带END/FINAL/完等后缀）
	regexp.MustCompile(`(?i)\[(\d{2,4})(?:END|FINAL|完)?\]`),
	// 模式8: - 01 - / .01. / 空格01空格
	regexp.MustCompile(`[\-\.\s](\d{2,4})[\]\-\.\s]`),
}

// multiEpPatterns 多集连播文件正则（优先于单集模式匹配）
var multiEpPatterns = []*regexp.Regexp{
	// S01E02-E03 / S01E02-E05 / S01E02-e03
	regexp.MustCompile(`(?i)S(\d{1,2})E(\d{1,4})\s*[-–~]\s*E(\d{1,4})`),
	// S01E02-03 (无前缀 E 的范围)
	regexp.MustCompile(`(?i)S(\d{1,2})E(\d{1,4})\s*[-–~]\s*(\d{1,4})`),
}

// dateEpisodePattern 日期格式集号正则（用于脱口秀/日播剧等）
// 匹配: 2024.01.15 / 2024-01-15 / 2024_01_15
var dateEpisodePattern = regexp.MustCompile(`((?:19|20)\d{2})[\.\-_](\d{2})[\.\-_](\d{2})`)

// 独立季号正则：从文件名中提取 S2、Season 2 等季号（不依赖集号）
var seasonInFilenamePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bS(\d{1,2})\b`),
	regexp.MustCompile(`(?i)\bSeason\s*(\d{1,2})\b`),
	regexp.MustCompile(`第\s*(\d{1,2})\s*季`),
}

// Season目录模式
var seasonDirPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^Season\s*(\d{1,2})$`),
	regexp.MustCompile(`(?i)^S(\d{1,2})$`),
	regexp.MustCompile(`^第\s*(\d{1,2})\s*季$`),
	regexp.MustCompile(`(?i)^Specials?$`),   // 特别篇
	regexp.MustCompile(`(?i)^Season\s*0+$`), // Season 0 / Season 00（Emby 特别篇格式）
}

// seriesFolder 多季合并时使用的目录信息
type seriesFolder struct {
	path      string // 完整路径
	dirName   string // 原始目录名
	seasonNum int    // 从目录名提取的季号（0表示未识别到季号）
}

// EpisodeInfo 解析出的剧集信息
type EpisodeInfo struct {
	SeasonNum     int
	EpisodeNum    int
	EpisodeNumEnd int // 多集连播结束集号（0=单集），如 S01E02-E05 → Start=2, End=5
	EpisodeTitle  string
	AirDate       string // 日期格式集号：2024-01-15（脱口秀/日播剧）
	FilePath      string
	FileInfo      os.FileInfo
}

// scanTVShowLibrary 扫描剧集库（基于文件夹的合集识别 + 根目录散落文件智能归类）
func (s *ScannerService) scanTVShowLibrary(library *model.Library) (int, *scanRootResult, error) {
	var totalNewEpisodes int
	result := newScanRootResult(library.Path)

	s.logger.Infof("剧集库扫描开始: %s, 路径: %s", library.Name, library.Path)

	// 遍历媒体库根目录的第一层子目录，每个子目录视为一个剧集
	entries, err := s.listDir(library.Path)
	if err != nil {
		return 0, result, &ScanIncompleteError{Root: library.Path, Err: err}
	}

	s.logger.Infof("剧集库根目录包含 %d 个条目", len(entries))

	// 收集根目录下的散落视频文件，按系列名分组
	type looseFile struct {
		entry os.DirEntry
		info  os.FileInfo
	}
	seriesGroups := make(map[string][]looseFile) // 系列名 -> 文件列表

	// === 阶段一：收集所有子目录，按标准化系列名分组 ===
	// 标准化系列名 -> 目录列表
	seriesDirGroups := make(map[string][]seriesFolder)

	for _, entry := range entries {
		if err := s.checkScanCanceled(); err != nil {
			return totalNewEpisodes, result, err
		}
		if !entry.IsDir() {
			// 根目录下的视频文件
			ext := strings.ToLower(filepath.Ext(entry.Name()))
			if supportedExts[ext] {
				filePath := filepath.Join(library.Path, entry.Name())
				result.addFile(filePath)
				if existing, err := s.mediaRepo.FindByFilePathInLibrary(library.ID, filePath); err == nil {
					s.requestMetadataCompletionIfNeeded(existing)
					continue // 已存在
				} else if s.strictScan && !errors.Is(err, gorm.ErrRecordNotFound) {
					return totalNewEpisodes, result, fmt.Errorf("find existing episode %s: %w", filePath, err)
				}
				info, infoErr := entry.Info()
				if infoErr != nil {
					return totalNewEpisodes, result, &ScanIncompleteError{Root: filePath, Err: infoErr}
				}
				// 从文件名提取系列名称用于智能归类
				seriesName := s.extractSeriesNameFromFile(entry.Name())
				if seriesName == "" {
					seriesName = "__ungrouped__"
				}
				seriesGroups[seriesName] = append(seriesGroups[seriesName], looseFile{entry: entry, info: info})
			}
			continue
		}

		dirName := entry.Name()
		folderPath := filepath.Join(library.Path, dirName)

		// 从目录名提取标准化系列名（去掉季号标识）和季号
		normalizedName := s.normalizeSeriesName(dirName)
		seasonNum := s.extractSeasonFromDirName(dirName)

		seriesDirGroups[normalizedName] = append(seriesDirGroups[normalizedName], seriesFolder{
			path:      folderPath,
			dirName:   dirName,
			seasonNum: seasonNum,
		})
	}

	// === 阶段二：处理分组后的目录 ===
	for normalizedName, folders := range seriesDirGroups {
		if err := s.checkScanCanceled(); err != nil {
			return totalNewEpisodes, result, err
		}
		if len(folders) == 1 && folders[0].seasonNum == 0 {
			// 单个目录且未识别到季号 → 按原有逻辑独立处理
			f := folders[0]
			seriesTitle := s.extractSeriesTitle(f.dirName)
			newCount, err := s.scanSeriesFolder(library, f.path, seriesTitle, result)
			if err != nil {
				return totalNewEpisodes, result, err
			}
			totalNewEpisodes += newCount
		} else {
			// 多个目录属于同一系列（如"一拳超人 S1"和"一拳超人 S2"）
			// 或单个目录但明确包含季号标识 → 合并到同一个 Series
			newCount, err := s.scanMultiSeasonSeries(library, normalizedName, folders, result)
			if err != nil {
				return totalNewEpisodes, result, err
			}
			totalNewEpisodes += newCount
		}
	}

	// 处理根目录散落文件的智能归类
	for seriesName, files := range seriesGroups {
		if err := s.checkScanCanceled(); err != nil {
			return totalNewEpisodes, result, err
		}
		if len(files) <= 1 && seriesName == "__ungrouped__" {
			// 单个无法识别系列名的文件，作为独立媒体处理
			for _, f := range files {
				filePath := filepath.Join(library.Path, f.entry.Name())
				title := s.extractTitle(f.entry.Name())
				media := &model.Media{
					LibraryID: library.ID,
					Title:     title,
					FilePath:  filePath,
					FileSize:  f.info.Size(),
					MediaType: "episode",
				}
				applyFileTimes(media, f.info)
				s.prepareQuickEpisodeMedia(library, media)
				ep := s.parseEpisodeInfo(f.entry.Name())
				media.SeasonNum = ep.SeasonNum
				media.EpisodeNum = ep.EpisodeNum
				media.EpisodeTitle = ep.EpisodeTitle
				if err := s.persistQuickMedia(media); err != nil {
					s.logger.Warnf("保存媒体失败: %s, 错误: %v", filePath, err)
					if s.strictScan {
						return totalNewEpisodes, result, fmt.Errorf("save loose episode %s: %w", filePath, err)
					}
					continue
				}
				totalNewEpisodes++
			}
			continue
		}

		// 有多个同名系列的文件或者能识别系列名的文件，自动创建合集
		actualSeriesName := seriesName
		if seriesName == "__ungrouped__" {
			// 多个无法识别系列名的文件，使用文件名作为标题独立存储
			for _, f := range files {
				filePath := filepath.Join(library.Path, f.entry.Name())
				title := s.extractTitle(f.entry.Name())
				media := &model.Media{
					LibraryID: library.ID,
					Title:     title,
					FilePath:  filePath,
					FileSize:  f.info.Size(),
					MediaType: "episode",
				}
				applyFileTimes(media, f.info)
				s.prepareQuickEpisodeMedia(library, media)
				ep := s.parseEpisodeInfo(f.entry.Name())
				media.SeasonNum = ep.SeasonNum
				media.EpisodeNum = ep.EpisodeNum
				media.EpisodeTitle = ep.EpisodeTitle
				if err := s.persistQuickMedia(media); err != nil {
					s.logger.Warnf("保存媒体失败: %s, 错误: %v", filePath, err)
					if s.strictScan {
						return totalNewEpisodes, result, fmt.Errorf("save ungrouped episode %s: %w", filePath, err)
					}
					continue
				}
				totalNewEpisodes++
			}
			continue
		}

		// 为同系列的散落文件创建虚拟合集
		// 使用"__loose__:系列名"作为虚拟文件夹路径来区分
		virtualFolderPath := filepath.Join(library.Path, "__loose__:"+actualSeriesName)

		series, err := s.seriesRepo.FindByFolderPathInLibrary(library.ID, virtualFolderPath)
		if err != nil {
			series = &model.Series{
				LibraryID:  library.ID,
				Title:      actualSeriesName,
				FolderPath: virtualFolderPath,
			}
			if err := s.retryScanWrite(fmt.Sprintf("create loose series %s", actualSeriesName), func() error {
				return s.seriesRepo.Create(series)
			}); err != nil {
				s.logger.Warnf("创建散落剧集合集失败: %s, 错误: %v", actualSeriesName, err)
				if s.strictScan {
					return totalNewEpisodes, result, fmt.Errorf("create loose series %s: %w", actualSeriesName, err)
				}
				continue
			}
			s.logger.Infof("创建散落剧集合集: %s (ID=%s)", actualSeriesName, series.ID)
		}

		seasonSet := make(map[int]bool)
		var newCount int

		for _, f := range files {
			if err := s.checkScanCanceled(); err != nil {
				return totalNewEpisodes, result, err
			}
			filePath := filepath.Join(library.Path, f.entry.Name())
			ep := s.parseEpisodeInfo(f.entry.Name())
			if ep.SeasonNum == 0 {
				ep.SeasonNum = 1
			}

			media := &model.Media{
				LibraryID:    library.ID,
				SeriesID:     series.ID,
				Title:        actualSeriesName,
				FilePath:     filePath,
				FileSize:     f.info.Size(),
				MediaType:    "episode",
				SeasonNum:    ep.SeasonNum,
				EpisodeNum:   ep.EpisodeNum,
				EpisodeTitle: ep.EpisodeTitle,
			}
			applyFileTimes(media, f.info)
			s.prepareQuickEpisodeMedia(library, media)

			if err := s.persistQuickMedia(media); err != nil {
				s.logger.Warnf("保存剧集失败: %s, 错误: %v", filePath, err)
				if s.strictScan {
					return totalNewEpisodes, result, fmt.Errorf("save grouped episode %s: %w", filePath, err)
				}
				continue
			}

			seasonSet[ep.SeasonNum] = true
			newCount++

			s.logger.Debugf("发现散落剧集: %s S%02dE%02d", actualSeriesName, ep.SeasonNum, ep.EpisodeNum)
			s.broadcastScanEvent(EventScanProgress, &ScanProgressData{
				LibraryID:   library.ID,
				LibraryName: library.Name,
				Phase:       "scanning",
				NewFound:    newCount,
				Message:     fmt.Sprintf("发现: %s S%02dE%02d", actualSeriesName, ep.SeasonNum, ep.EpisodeNum),
			})
		}

		// 更新合集统计
		allEpisodes, listErr := s.mediaRepo.ListBySeriesID(series.ID)
		if listErr != nil && s.strictScan {
			return totalNewEpisodes, result, fmt.Errorf("list loose series episodes: %w", listErr)
		}
		series.EpisodeCount = len(allEpisodes)
		series.SeasonCount = len(seasonSet)
		if updateErr := s.retryScanWrite("update loose series", func() error {
			return s.seriesRepo.Update(series)
		}); updateErr != nil {
			if s.strictScan {
				return totalNewEpisodes, result, fmt.Errorf("update loose series: %w", updateErr)
			}
		}

		s.logger.Infof("散落剧集归类完成: %s, 新增 %d 集, 共 %d 季 %d 集",
			actualSeriesName, newCount, series.SeasonCount, series.EpisodeCount)

		totalNewEpisodes += newCount
	}

	return totalNewEpisodes, result, nil
}

// normalizeSeriesName 标准化系列名：从目录名中去掉季号标识，返回纯系列名
// 例如: "一拳超人 S1" → "一拳超人", "Breaking Bad Season 2" → "Breaking Bad", "一拳超人 第二季" → "一拳超人"
func (s *ScannerService) normalizeSeriesName(dirName string) string {
	title := s.extractSeriesTitle(dirName) // 先清理年份、编码等标记

	// 移除季号标识
	seasonPatterns := []string{
		`(?i)\s*S\d{1,2}\s*$`,            // 末尾 S1, S02
		`(?i)\s*Season\s*\d{1,2}\s*$`,    // 末尾 Season 1
		`\s*第\s*[一二三四五六七八九十\d]+\s*季\s*$`, // 末尾 第一季, 第2季
	}
	for _, p := range seasonPatterns {
		re := regexp.MustCompile(p)
		title = re.ReplaceAllString(title, "")
	}

	title = strings.TrimSpace(title)
	if title == "" {
		// 如果标准化后为空（极端情况），回退使用原始清理标题
		return s.extractSeriesTitle(dirName)
	}
	return title
}

// extractSeasonFromDirName 从目录名中提取季号
// 例如: "一拳超人 S2" → 2, "Breaking Bad Season 1" → 1, "一拳超人 第二季" → 2
func (s *ScannerService) extractSeasonFromDirName(dirName string) int {
	// 支持 S1, S02 格式
	if m := regexp.MustCompile(`(?i)\bS(\d{1,2})\b`).FindStringSubmatch(dirName); len(m) >= 2 {
		num, _ := strconv.Atoi(m[1])
		if num > 0 && num <= 30 {
			return num
		}
	}
	// 支持 Season 1, Season 02 格式
	if m := regexp.MustCompile(`(?i)\bSeason\s*(\d{1,2})\b`).FindStringSubmatch(dirName); len(m) >= 2 {
		num, _ := strconv.Atoi(m[1])
		if num > 0 && num <= 30 {
			return num
		}
	}
	// 支持中文 "第1季", "第二季"
	if m := regexp.MustCompile(`第\s*(\d{1,2})\s*季`).FindStringSubmatch(dirName); len(m) >= 2 {
		num, _ := strconv.Atoi(m[1])
		if num > 0 && num <= 30 {
			return num
		}
	}
	// 支持中文数字 "第一季" ~ "第十季"
	cnNumMap := map[string]int{
		"一": 1, "二": 2, "三": 3, "四": 4, "五": 5,
		"六": 6, "七": 7, "八": 8, "九": 9, "十": 10,
	}
	if m := regexp.MustCompile(`第\s*([一二三四五六七八九十]+)\s*季`).FindStringSubmatch(dirName); len(m) >= 2 {
		if num, ok := cnNumMap[m[1]]; ok {
			return num
		}
	}
	return 0
}

// scanMultiSeasonSeries 扫描属于同一系列的多季目录，将其合并到一个 Series 中
// folders 中的 seriesFolder 包含各个季目录的路径、目录名和从目录名提取的季号
func (s *ScannerService) scanMultiSeasonSeries(library *model.Library, seriesTitle string, folders []seriesFolder, result *scanRootResult) (int, error) {
	s.logger.Infof("扫描多季合集: %s (%d 个目录)", seriesTitle, len(folders))

	// 查找或创建统一的 Series 合集
	// 优先按第一个目录的 FolderPath 查找（兼容旧数据），
	// 然后按标题+媒体库查找，最后创建新的
	var series *model.Series

	// 1. 尝试按任意一个目录的 FolderPath 查找已有 Series
	for _, f := range folders {
		if err := s.checkScanCanceled(); err != nil {
			return 0, err
		}
		if existing, err := s.seriesRepo.FindByFolderPathInLibrary(library.ID, f.path); err == nil {
			series = existing
			break
		}
	}

	// 2. 按标题+媒体库查找（可能之前已经合并过）
	if series == nil {
		if existing, err := s.seriesRepo.FindByTitleAndLibrary(seriesTitle, library.ID); err == nil {
			series = existing
		}
	}

	// 3. 创建新合集，FolderPath 使用第一个目录（或虚拟路径）
	if series == nil {
		// 使用"__multi__:系列名"作为虚拟路径，标识这是一个多季合并的合集
		virtualPath := filepath.Join(library.Path, "__multi__:"+seriesTitle)
		series = &model.Series{
			LibraryID:  library.ID,
			Title:      seriesTitle,
			FolderPath: virtualPath,
		}
		if err := s.retryScanWrite(fmt.Sprintf("create multi-season series %s", seriesTitle), func() error {
			return s.seriesRepo.Create(series)
		}); err != nil {
			return 0, fmt.Errorf("创建多季合集失败: %w", err)
		}
		s.logger.Infof("创建多季合集: %s (ID=%s, %d 个季目录)", seriesTitle, series.ID, len(folders))
	}

	// 识别本地 NFO 信息文件（从各季目录中查找）
	for _, f := range folders {
		if nfoPath := s.nfoService.FindNFOFile(f.path); nfoPath != "" {
			if err := s.nfoService.ParseTVShowNFO(nfoPath, series); err != nil {
				s.logger.Debugf("解析多季合集NFO失败: %s, 错误: %v", nfoPath, err)
				if s.strictScan {
					return 0, fmt.Errorf("parse multi-season NFO %s: %w", nfoPath, err)
				}
			} else {
				s.logger.Debugf("从NFO读取多季合集元数据: %s -> %s", nfoPath, series.Title)
			}
			break // 只用第一个找到的NFO
		}
	}

	// 识别本地海报封面图片（从各季目录中查找）
	for _, f := range folders {
		if poster, backdrop := s.nfoService.FindLocalImages(f.path); poster != "" || backdrop != "" {
			if poster != "" && series.PosterPath == "" {
				series.PosterPath = poster
				s.logger.Debugf("发现多季合集本地海报: %s", poster)
			}
			if backdrop != "" && series.BackdropPath == "" {
				series.BackdropPath = backdrop
				s.logger.Debugf("发现多季合集本地背景图: %s", backdrop)
			}
			if series.PosterPath != "" && series.BackdropPath != "" {
				break
			}
		}
	}

	// 保存NFO和图片更新
	if err := s.retryScanWrite("update multi-season series metadata", func() error {
		return s.seriesRepo.Update(series)
	}); err != nil {
		if s.strictScan {
			return 0, fmt.Errorf("update multi-season series metadata: %w", err)
		}
	}

	var totalNewCount int
	seasonSet := make(map[int]bool)

	// 扫描每个季目录
	for _, f := range folders {
		if err := s.checkScanCanceled(); err != nil {
			return totalNewCount, err
		}
		episodes, collectErr := s.collectEpisodes(f.path, result)
		if collectErr != nil {
			return totalNewCount, collectErr
		}
		if len(episodes) == 0 {
			s.logger.Debugf("多季合集目录无视频文件: %s", f.path)
			continue
		}

		// 如果目录名带有明确的季号，且剧集文件未识别出季号，则使用目录季号
		dirSeasonNum := f.seasonNum
		if dirSeasonNum == 0 {
			// 尝试用 parseSeasonFromDir 再识别一次
			dirSeasonNum = s.parseSeasonFromDir(f.dirName)
		}

		// === 集号重编逻辑 ===
		// 当检测到同一季目录下的集号是全局连续编号（延续上一季），而非从1开始时，
		// 自动重新编为季内相对编号。
		// 例如：第二季目录下文件名编号 [13][14]...[24]，应重编为 1,2,...,12
		if dirSeasonNum > 1 && len(episodes) > 0 {
			// 收集本目录下属于相同季号的"普通"剧集（排除OVA/SP等特殊类型的集号）
			var normalEpNums []int
			for _, ep := range episodes {
				// 判断是否为特殊剧集类型（OVA/SP等），它们的集号不参与重编判断
				isSpecial := false
				if m := episodePatterns[5].FindStringSubmatch(filepath.Base(ep.FilePath)); len(m) >= 2 {
					isSpecial = true
				}
				if !isSpecial && ep.EpisodeNum > 0 {
					normalEpNums = append(normalEpNums, ep.EpisodeNum)
				}
			}

			// 如果普通集号的最小值大于1，且集号是连续的，说明是全局编号需要重编
			if len(normalEpNums) > 0 {
				sort.Ints(normalEpNums)
				minEp := normalEpNums[0]

				if minEp > 1 {
					// 检查集号是否大致连续（允许少量缺失）
					isSequential := true
					for i := 1; i < len(normalEpNums); i++ {
						gap := normalEpNums[i] - normalEpNums[i-1]
						if gap > 2 { // 允许最多跳1集
							isSequential = false
							break
						}
					}

					if isSequential {
						// 计算偏移量，将集号重编为从1开始
						offset := minEp - 1
						s.logger.Infof("多季合集集号重编: %s 第%d季, 集号偏移 -%d (原始范围: %d~%d → 重编为 1~%d)",
							seriesTitle, dirSeasonNum, offset, minEp, normalEpNums[len(normalEpNums)-1], len(normalEpNums))

						for i := range episodes {
							// 只重编普通剧集，不重编OVA/SP等
							isSpecial := false
							if m := episodePatterns[5].FindStringSubmatch(filepath.Base(episodes[i].FilePath)); len(m) >= 2 {
								isSpecial = true
							}
							if !isSpecial && episodes[i].EpisodeNum > offset {
								episodes[i].EpisodeNum -= offset
							}
						}
					}
				}
			}
		}

		for _, ep := range episodes {
			if err := s.checkScanCanceled(); err != nil {
				return totalNewCount, err
			}
			// 季号分配：
			// 当目录名有明确季号时，优先使用目录季号（除非文件名中有不同的、合理的季号如S2标识的OVA）
			seasonNum := ep.SeasonNum
			if dirSeasonNum > 0 {
				// 如果文件名中的季号与目录季号不同且>1，说明文件自带了明确季号（如OVA标S2），保留它
				// 否则一律使用目录季号
				if seasonNum <= 1 || seasonNum == dirSeasonNum {
					seasonNum = dirSeasonNum
				}
			}
			if seasonNum == 0 {
				seasonNum = 1
			}

			// 检查是否已存在，如果存在则修正可能的脏数据（如 episode_title、season_num、episode_num）
			epAdjusted := ep
			epAdjusted.SeasonNum = seasonNum
			if existing, err := s.mediaRepo.FindByFilePathInLibrary(library.ID, ep.FilePath); err == nil {
				seasonSet[seasonNum] = true
				if updateErr := s.retryScanWrite(fmt.Sprintf("update existing episode %s", ep.FilePath), func() error {
					_, err := s.updateExistingEpisodeRecord(existing, series.ID, seriesTitle, epAdjusted)
					return err
				}); updateErr != nil {
					if s.strictScan {
						return totalNewCount, fmt.Errorf("update existing episode %s: %w", ep.FilePath, updateErr)
					}
				}
				continue
			} else if s.strictScan && !errors.Is(err, gorm.ErrRecordNotFound) {
				return totalNewCount, fmt.Errorf("find existing episode %s: %w", ep.FilePath, err)
			}

			media := &model.Media{
				LibraryID:    library.ID,
				SeriesID:     series.ID,
				Title:        seriesTitle,
				FilePath:     ep.FilePath,
				FileSize:     ep.FileInfo.Size(),
				MediaType:    "episode",
				SeasonNum:    epAdjusted.SeasonNum,
				EpisodeNum:   epAdjusted.EpisodeNum,
				EpisodeTitle: epAdjusted.EpisodeTitle,
			}
			applyFileTimes(media, ep.FileInfo)

			s.prepareQuickEpisodeMedia(library, media)

			if err := s.persistQuickMedia(media); err != nil {
				s.logger.Warnf("保存剧集失败: %s, 错误: %v", ep.FilePath, err)
				if s.strictScan {
					return totalNewCount, fmt.Errorf("save multi-season episode %s: %w", ep.FilePath, err)
				}
				continue
			}

			seasonSet[epAdjusted.SeasonNum] = true
			totalNewCount++

			s.logger.Debugf("发现剧集(多季): %s S%02dE%02d",
				seriesTitle, seasonNum, ep.EpisodeNum)
			s.broadcastScanEvent(EventScanProgress, &ScanProgressData{
				LibraryID:   library.ID,
				LibraryName: library.Name,
				Phase:       "scanning",
				NewFound:    totalNewCount,
				Message:     fmt.Sprintf("发现: %s S%02dE%02d", seriesTitle, seasonNum, ep.EpisodeNum),
			})
		}
	}

	// 更新合集统计信息
	allEpisodes, listErr := s.mediaRepo.ListBySeriesID(series.ID)
	if listErr != nil && s.strictScan {
		return totalNewCount, fmt.Errorf("list multi-season episodes: %w", listErr)
	}
	series.EpisodeCount = len(allEpisodes)
	series.SeasonCount = len(seasonSet)
	if updateErr := s.retryScanWrite("update multi-season statistics", func() error {
		return s.seriesRepo.Update(series)
	}); updateErr != nil {
		if s.strictScan {
			return totalNewCount, fmt.Errorf("update multi-season statistics: %w", updateErr)
		}
	}

	if totalNewCount > 0 {
		s.logger.Infof("多季合集扫描完成: %s, 新增 %d 集, 共 %d 季 %d 集",
			seriesTitle, totalNewCount, series.SeasonCount, series.EpisodeCount)
	}

	return totalNewCount, nil
}

// scanSeriesFolder 扫描单个剧集文件夹
func (s *ScannerService) scanSeriesFolder(library *model.Library, folderPath, seriesTitle string, result *scanRootResult) (int, error) {
	s.logger.Infof("扫描剧集: %s (%s)", seriesTitle, folderPath)

	// 查找或创建剧集合集条目
	series, err := s.seriesRepo.FindByFolderPathInLibrary(library.ID, folderPath)
	if err != nil {
		// 新剧集，创建合集条目
		series = &model.Series{
			LibraryID:  library.ID,
			Title:      seriesTitle,
			FolderPath: folderPath,
		}
		if err := s.retryScanWrite(fmt.Sprintf("create series %s", seriesTitle), func() error {
			return s.seriesRepo.Create(series)
		}); err != nil {
			return 0, fmt.Errorf("创建剧集合集失败: %w", err)
		}
		s.logger.Infof("创建剧集合集: %s (ID=%s)", seriesTitle, series.ID)
	}

	// 识别本地 NFO 信息文件并解析剧集元数据
	if nfoPath := s.nfoService.FindNFOFile(folderPath); nfoPath != "" {
		if err := s.nfoService.ParseTVShowNFO(nfoPath, series); err != nil {
			s.logger.Debugf("解析剧集NFO失败: %s, 错误: %v", nfoPath, err)
			if s.strictScan {
				return 0, fmt.Errorf("parse series NFO %s: %w", nfoPath, err)
			}
		} else {
			s.logger.Debugf("从NFO读取剧集元数据: %s -> %s", nfoPath, series.Title)
			// 如果NFO中有标题，更新seriesTitle用于后续剧集
			if series.Title != "" {
				seriesTitle = series.Title
			}
		}
	}

	// 识别本地海报封面图片
	if poster, backdrop := s.nfoService.FindLocalImages(folderPath); poster != "" || backdrop != "" {
		if poster != "" && series.PosterPath == "" {
			series.PosterPath = poster
			s.logger.Debugf("发现剧集本地海报: %s", poster)
		}
		if backdrop != "" && series.BackdropPath == "" {
			series.BackdropPath = backdrop
			s.logger.Debugf("发现剧集本地背景图: %s", backdrop)
		}
	}

	// 保存NFO和图片更新
	if err := s.retryScanWrite("update series metadata", func() error {
		return s.seriesRepo.Update(series)
	}); err != nil {
		if s.strictScan {
			return 0, fmt.Errorf("update series metadata: %w", err)
		}
	}

	// 收集所有剧集文件
	episodes, collectErr := s.collectEpisodes(folderPath, result)
	if collectErr != nil {
		return 0, collectErr
	}

	if len(episodes) == 0 {
		s.logger.Debugf("剧集文件夹无视频文件: %s", folderPath)
		// 如果该合集下已经没有任何剧集，清理这个空合集
		existingEpisodes, listErr := s.mediaRepo.ListBySeriesID(series.ID)
		if listErr != nil && s.strictScan {
			return 0, fmt.Errorf("list empty series episodes: %w", listErr)
		}
		if len(existingEpisodes) == 0 {
			if deleteErr := s.seriesRepo.Delete(series.ID); deleteErr != nil && s.strictScan {
				return 0, fmt.Errorf("delete empty series: %w", deleteErr)
			}
			s.logger.Infof("清理空合集: %s (ID=%s)", seriesTitle, series.ID)
		}
		return 0, nil
	}

	// 导入剧集
	var newCount int
	seasonSet := make(map[int]bool)

	for _, ep := range episodes {
		if err := s.checkScanCanceled(); err != nil {
			return newCount, err
		}
		// 检查是否已存在，如果存在则修正可能的脏数据
		if existing, err := s.mediaRepo.FindByFilePathInLibrary(library.ID, ep.FilePath); err == nil {
			seasonSet[ep.SeasonNum] = true
			if updateErr := s.retryScanWrite(fmt.Sprintf("update existing episode %s", ep.FilePath), func() error {
				_, err := s.updateExistingEpisodeRecord(existing, series.ID, seriesTitle, ep)
				return err
			}); updateErr != nil {
				if s.strictScan {
					return newCount, fmt.Errorf("update existing episode %s: %w", ep.FilePath, updateErr)
				}
			}
			continue
		} else if s.strictScan && !errors.Is(err, gorm.ErrRecordNotFound) {
			return newCount, fmt.Errorf("find existing episode %s: %w", ep.FilePath, err)
		}

		media := &model.Media{
			LibraryID:    library.ID,
			SeriesID:     series.ID,
			Title:        seriesTitle,
			FilePath:     ep.FilePath,
			FileSize:     ep.FileInfo.Size(),
			MediaType:    "episode",
			SeasonNum:    ep.SeasonNum,
			EpisodeNum:   ep.EpisodeNum,
			EpisodeTitle: ep.EpisodeTitle,
		}
		applyFileTimes(media, ep.FileInfo)

		s.prepareQuickEpisodeMedia(library, media)

		if err := s.persistQuickMedia(media); err != nil {
			s.logger.Warnf("保存剧集失败: %s, 错误: %v", ep.FilePath, err)
			if s.strictScan {
				return newCount, fmt.Errorf("save series episode %s: %w", ep.FilePath, err)
			}
			continue
		}

		seasonSet[ep.SeasonNum] = true
		newCount++

		s.logger.Debugf("发现剧集: %s S%02dE%02d", seriesTitle, ep.SeasonNum, ep.EpisodeNum)
		s.broadcastScanEvent(EventScanProgress, &ScanProgressData{
			LibraryID:   library.ID,
			LibraryName: library.Name,
			Phase:       "scanning",
			NewFound:    newCount,
			Message:     fmt.Sprintf("发现: %s S%02dE%02d", seriesTitle, ep.SeasonNum, ep.EpisodeNum),
		})
	}

	// 更新合集统计信息
	allEpisodes, listErr := s.mediaRepo.ListBySeriesID(series.ID)
	if listErr != nil && s.strictScan {
		return newCount, fmt.Errorf("list series episodes: %w", listErr)
	}
	series.EpisodeCount = len(allEpisodes)
	series.SeasonCount = len(seasonSet)
	if updateErr := s.retryScanWrite("update series statistics", func() error {
		return s.seriesRepo.Update(series)
	}); updateErr != nil {
		if s.strictScan {
			return newCount, fmt.Errorf("update series statistics: %w", updateErr)
		}
	}

	s.logger.Infof("剧集扫描完成: %s, 新增 %d 集, 共 %d 季 %d 集",
		seriesTitle, newCount, series.SeasonCount, series.EpisodeCount)

	return newCount, nil
}

// collectEpisodes 递归收集剧集文件夹下的所有视频文件
func (s *ScannerService) collectEpisodes(folderPath string, result *scanRootResult) ([]EpisodeInfo, error) {
	var episodes []EpisodeInfo

	err := s.walk(folderPath, func(path string, info os.FileInfo, walkErr error) error {
		if err := s.checkScanCanceled(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if !supportedExts[ext] {
			return nil
		}
		result.addFile(path)

		fileName := filepath.Base(path)
		ep := s.parseEpisodeInfo(fileName)

		// 尝试从Season目录名获取季号（如果文件名中没有季号）
		if ep.SeasonNum == 0 {
			parentDir := filepath.Base(filepath.Dir(path))
			if seasonNum := s.parseSeasonFromDir(parentDir); seasonNum > 0 {
				ep.SeasonNum = seasonNum
			}
		}

		// 默认季号为1
		if ep.SeasonNum == 0 {
			ep.SeasonNum = 1
		}

		ep.FilePath = path
		ep.FileInfo = info

		episodes = append(episodes, ep)
		return nil
	})
	if err != nil {
		return nil, &ScanIncompleteError{Root: folderPath, Err: err}
	}

	// 按季号+集号排序
	sort.Slice(episodes, func(i, j int) bool {
		if episodes[i].SeasonNum != episodes[j].SeasonNum {
			return episodes[i].SeasonNum < episodes[j].SeasonNum
		}
		return episodes[i].EpisodeNum < episodes[j].EpisodeNum
	})

	// 如果所有集号都是0，按文件名排序后自动编号
	allZero := true
	for _, ep := range episodes {
		if ep.EpisodeNum > 0 {
			allZero = false
			break
		}
	}
	if allZero {
		sort.Slice(episodes, func(i, j int) bool {
			return episodes[i].FilePath < episodes[j].FilePath
		})
		for i := range episodes {
			episodes[i].EpisodeNum = i + 1
		}
	}

	return episodes, nil
}

// parseEpisodeInfo 从文件名解析剧集信息
// 支持的命名格式：
//
//	标准格式: [字幕组][剧名][One-Punch Man][01][1280x720][简体]
//	季集格式: [HYSUB][ONE PUNCH MAN S2][OVA01][GB_MP4][1280X720].mp4
//	通用格式: S01E01, 1x01, 第1集, EP01, OVA01 等
func (s *ScannerService) parseEpisodeInfo(filename string) EpisodeInfo {
	var ep EpisodeInfo

	// 预处理：移除文件扩展名，方便后续解析
	nameWithoutExt := strings.TrimSuffix(filename, filepath.Ext(filename))

	// === 阶段零：多集连播检测（优先于单集匹配） ===

	// 多集模式0: S01E02-E03 / S01E02-E05
	if m := multiEpPatterns[0].FindStringSubmatch(filename); len(m) >= 4 {
		sNum, _ := strconv.Atoi(m[1])
		eStart, _ := strconv.Atoi(m[2])
		eEnd, _ := strconv.Atoi(m[3])
		if eEnd > eStart && sNum <= 30 {
			ep.SeasonNum = sNum
			ep.EpisodeNum = eStart
			ep.EpisodeNumEnd = eEnd
			ep.EpisodeTitle = s.extractEpisodeTitle(nameWithoutExt, m[0])
			return ep
		}
	}

	// 多集模式1: S01E02-03 (无前缀E的范围)
	if m := multiEpPatterns[1].FindStringSubmatch(filename); len(m) >= 4 {
		sNum, _ := strconv.Atoi(m[1])
		eStart, _ := strconv.Atoi(m[2])
		eEnd, _ := strconv.Atoi(m[3])
		if eEnd > eStart && sNum <= 30 && !resolutionNums[eEnd] {
			ep.SeasonNum = sNum
			ep.EpisodeNum = eStart
			ep.EpisodeNumEnd = eEnd
			ep.EpisodeTitle = s.extractEpisodeTitle(nameWithoutExt, m[0])
			return ep
		}
	}

	// === 阶段零-B：日期格式集号检测（日播剧/脱口秀） ===
	if m := dateEpisodePattern.FindStringSubmatch(filename); len(m) >= 4 {
		year, _ := strconv.Atoi(m[1])
		month, _ := strconv.Atoi(m[2])
		day, _ := strconv.Atoi(m[3])
		// 验证日期合理性
		if year >= 1990 && year <= 2099 && month >= 1 && month <= 12 && day >= 1 && day <= 31 {
			// 不与 SxxExx 冲突：如果同时有 S01E01 格式，优先使用 SxxExx
			if !episodePatterns[0].MatchString(filename) && !episodePatterns[1].MatchString(filename) {
				ep.AirDate = fmt.Sprintf("%04d-%02d-%02d", year, month, day)
				// 将日期编码为集号: MMDD (方便排序)
				ep.EpisodeNum = month*100 + day
				ep.SeasonNum = year - 2000 // 年份作为季号标识（如 2024 → 24）
				ep.EpisodeTitle = s.extractEpisodeTitle(nameWithoutExt, m[0])
				return ep
			}
		}
	}

	// === 阶段一：提取集号（原有逻辑） ===

	// 模式 0: S01E01 — 最精确的格式，同时包含季号和集号
	if m := episodePatterns[0].FindStringSubmatch(filename); len(m) >= 3 {
		sNum, _ := strconv.Atoi(m[1])
		eNum, _ := strconv.Atoi(m[2])
		// 排除明显不合理的值：集号恰好是分辨率
		if !resolutionNums[eNum] || sNum <= 30 {
			ep.SeasonNum = sNum
			ep.EpisodeNum = eNum
			ep.EpisodeTitle = s.extractEpisodeTitle(nameWithoutExt, m[0])
			return ep
		}
	}

	// 模式 1: S01.E01
	if m := episodePatterns[1].FindStringSubmatch(filename); len(m) >= 3 {
		sNum, _ := strconv.Atoi(m[1])
		eNum, _ := strconv.Atoi(m[2])
		if !resolutionNums[eNum] || sNum <= 30 {
			ep.SeasonNum = sNum
			ep.EpisodeNum = eNum
			ep.EpisodeTitle = s.extractEpisodeTitle(nameWithoutExt, m[0])
			return ep
		}
	}

	// 模式 2: 1x01 — 排除分辨率如 "1920x1080" "1280x720"
	if m := episodePatterns[2].FindStringSubmatch(filename); len(m) >= 3 {
		sNum, _ := strconv.Atoi(m[1])
		eNum, _ := strconv.Atoi(m[2])
		if !resolutionNums[eNum] && !resolutionNums[sNum] && sNum < 100 {
			ep.SeasonNum = sNum
			ep.EpisodeNum = eNum
			ep.EpisodeTitle = s.extractEpisodeTitle(nameWithoutExt, m[0])
			return ep
		}
	}

	// 模式 3: 第01集
	if m := episodePatterns[3].FindStringSubmatch(filename); len(m) >= 2 {
		ep.EpisodeNum, _ = strconv.Atoi(m[1])
		ep.SeasonNum = s.extractSeasonFromFilename(filename)
		ep.EpisodeTitle = s.extractEpisodeTitle(nameWithoutExt, m[0])
		return ep
	}

	// 模式 4: EP01 / Episode 01
	if m := episodePatterns[4].FindStringSubmatch(filename); len(m) >= 2 {
		ep.EpisodeNum, _ = strconv.Atoi(m[1])
		ep.SeasonNum = s.extractSeasonFromFilename(filename)
		ep.EpisodeTitle = s.extractEpisodeTitle(nameWithoutExt, m[0])
		return ep
	}

	// 模式 5: OVA01 / SP01 / SPECIAL01 等特殊剧集类型
	if m := episodePatterns[5].FindStringSubmatch(filename); len(m) >= 2 {
		ep.EpisodeNum, _ = strconv.Atoi(m[1])
		ep.SeasonNum = s.extractSeasonFromFilename(filename)
		ep.EpisodeTitle = s.extractEpisodeTitle(nameWithoutExt, m[0])
		return ep
	}

	// 模式 6: E01（单独的E+数字）— 需排除分辨率上下文
	if m := episodePatterns[6].FindStringSubmatchIndex(filename); m != nil {
		full := filename[m[0]:m[1]]
		sub := filename[m[2]:m[3]]
		eNum, _ := strconv.Atoi(sub)
		if !resolutionNums[eNum] && !isResolutionContext(filename, m[1]) {
			ep.EpisodeNum = eNum
			ep.SeasonNum = s.extractSeasonFromFilename(filename)
			ep.EpisodeTitle = s.extractEpisodeTitle(nameWithoutExt, full)
			return ep
		}
	}

	// 模式 7: [01] / [001] — 方括号内的纯数字（字幕组常用格式）
	if m := episodePatterns[7].FindStringSubmatch(filename); len(m) >= 2 {
		num, _ := strconv.Atoi(m[1])
		// 排除年份和分辨率数字
		if num > 0 && num < 1900 && !resolutionNums[num] {
			ep.EpisodeNum = num
			ep.SeasonNum = s.extractSeasonFromFilename(filename)
			ep.EpisodeTitle = s.extractEpisodeTitle(nameWithoutExt, m[0])
			return ep
		}
	}

	// 模式 8: - 01 - / .01. — 最宽松的匹配，需要严格过滤
	if m := episodePatterns[8].FindStringSubmatchIndex(filename); m != nil {
		sub := filename[m[2]:m[3]]
		num, _ := strconv.Atoi(sub)
		if num > 0 && num < 1900 && !resolutionNums[num] && !isResolutionContext(filename, m[1]) {
			ep.EpisodeNum = num
			ep.SeasonNum = s.extractSeasonFromFilename(filename)
			ep.EpisodeTitle = s.extractEpisodeTitle(nameWithoutExt, filename[m[0]:m[1]])
			return ep
		}
	}

	return ep
}

// extractSeasonFromFilename 从文件名中独立提取季号
// 处理文件名中包含 S2、Season 2、第2季 等情况（不依赖集号格式）
func (s *ScannerService) extractSeasonFromFilename(filename string) int {
	for _, pattern := range seasonInFilenamePatterns {
		if m := pattern.FindStringSubmatch(filename); len(m) >= 2 {
			num, _ := strconv.Atoi(m[1])
			if num > 0 && num <= 30 {
				return num
			}
		}
	}
	return 0
}

// extractEpisodeTitle 从文件名中提取集标题（集号模式之后的部分）
func (s *ScannerService) extractEpisodeTitle(nameWithoutExt string, matchedPattern string) string {
	idx := strings.Index(nameWithoutExt, matchedPattern)
	if idx < 0 {
		return ""
	}
	after := nameWithoutExt[idx+len(matchedPattern):]
	// 清理开头的分隔符和空格
	after = strings.TrimLeft(after, " .-_")
	if after == "" {
		return ""
	}
	// 去除尾部常见的元信息标记（分辨率/编码/组名等括号内容）
	// 例如 "[1080p]" "(BDRip)" "[FLAC]" 等
	metaPattern := regexp.MustCompile(`[\[\(].*[\]\)]`)
	after = metaPattern.ReplaceAllString(after, "")
	after = strings.TrimRight(after, " .-_")
	// 如果剩余内容太短或全是数字，则不作为标题
	if len(after) <= 1 {
		return ""
	}
	// 排除纯数字（可能是分辨率等残留）
	if _, err := strconv.Atoi(after); err == nil {
		return ""
	}
	// 排除分辨率字符串（如 720p、1080p、4K 等）
	resPattern := regexp.MustCompile(`(?i)^\d{3,4}[pi]$|^[248]K$`)
	if resPattern.MatchString(after) {
		return ""
	}
	// 排除纯技术性标记（编码/混流/来源等），这些不是有意义的剧集标题
	// 例如：remux, remux nvl, x264, HEVC, BDRip, WEB-DL 等
	techPattern := regexp.MustCompile(`(?i)^[\s\-\.]*(?:remux|re-?mux|nvl|x26[45]|h\.?26[45]|hevc|avc|aac|flac|dts|bdri?p|dvdri?p|web-?dl|web-?rip|blu-?ray|hdr|10bit|ma[25]\.?[01]|truehd|atmos|opus)(?:[\s\-\.]+(?:remux|nvl|x26[45]|h\.?26[45]|hevc|avc|aac|flac|dts|bdri?p|dvdri?p|web-?dl|web-?rip|blu-?ray|hdr|10bit|ma[25]\.?[01]|truehd|atmos|opus))*[\s\-\.]*$`)
	if techPattern.MatchString(after) {
		return ""
	}
	return after
}

// parseSeasonFromDir 从Season目录名解析季号
func (s *ScannerService) parseSeasonFromDir(dirName string) int {
	for _, pattern := range seasonDirPatterns {
		if m := pattern.FindStringSubmatch(dirName); len(m) >= 2 {
			num, _ := strconv.Atoi(m[1])
			return num
		}
		// Specials特别篇 -> 季号 0
		if pattern.MatchString(dirName) && strings.Contains(strings.ToLower(dirName), "special") {
			return 0
		}
	}
	return 0
}

// extractSeriesNameFromFile 从视频文件名中提取系列名称
// 适用于根目录下散落的剧集文件，如 [HYSUB][ONE PUNCH MAN][01].mkv -> ONE PUNCH MAN
func (s *ScannerService) extractSeriesNameFromFile(filename string) string {
	// 去掉扩展名
	name := strings.TrimSuffix(filename, filepath.Ext(filename))

	// 模式1: [字幕组][系列名][集号] 格式
	// 匹配方括号中的内容，提取第二个方括号的内容作为系列名
	bracketPattern := regexp.MustCompile(`\[([^\[\]]+)\]`)
	matches := bracketPattern.FindAllStringSubmatch(name, -1)
	if len(matches) >= 2 {
		// 遍历方括号内容，找到最可能是系列名的部分
		// 跳过: 纯数字（集号）、分辨率（720P/1080P）、编码格式等
		skipPatterns := []*regexp.Regexp{
			regexp.MustCompile(`(?i)^\d+$`),                                                          // 纯数字
			regexp.MustCompile(`(?i)^\d{3,4}[PpKk]$`),                                                // 分辨率如720P
			regexp.MustCompile(`(?i)^\d+[Xx]\d+$`),                                                   // 分辨率如1280X720
			regexp.MustCompile(`(?i)^(BIG5|GB|UTF-?8|MP4|MKV|AVI|HEVC|H\.?26[45]|AAC|FLAC|x26[45])`), // 编码/格式
			regexp.MustCompile(`(?i)^(BIG5_MP4|GB_MP4|CHS|CHT|JPN|ENG)`),                             // 字幕/编码组合
			regexp.MustCompile(`(?i)^S\d+E\d+$`),                                                     // 剧集号 S01E01
			regexp.MustCompile(`(?i)^EP?\s*\d+$`),                                                    // EP01
			regexp.MustCompile(`(?i)^V\d+$`),                                                         // 版本号 V2
			regexp.MustCompile(`(?i)^(WebRip|BDRip|DVDRip|WEB-DL|BluRay|HDTV)$`),                     // 来源
		}

		// 通常第一个方括号是字幕组，第二个是系列名
		// 但也可能系列名在其他位置，需要智能判断
		candidates := []string{}
		for _, m := range matches {
			content := strings.TrimSpace(m[1])
			if content == "" {
				continue
			}
			skip := false
			for _, sp := range skipPatterns {
				if sp.MatchString(content) {
					skip = true
					break
				}
			}
			if !skip {
				candidates = append(candidates, content)
			}
		}

		// 如果有多个候选项，选择第二个（通常第一个是字幕组名）
		if len(candidates) >= 2 {
			return candidates[1]
		}
		if len(candidates) == 1 {
			return candidates[0]
		}
	}

	// 模式2: 尝试从文件名中移除集号信息后得到系列名
	// 先去掉所有方括号内容和常见标记
	cleanName := name
	cleanName = bracketPattern.ReplaceAllString(cleanName, " ")

	// 移除集号模式 S01E01, EP01, E01, 第N集
	epPatterns := []string{
		`(?i)S\d{1,2}\s*E\d{1,4}`,
		`(?i)S\d{1,2}\.\s*E\d{1,4}`,
		`(?i)\d{1,2}x\d{1,4}`,
		`第\s*\d{1,4}\s*集`,
		`(?i)(?:EP|Episode)\s*\.?\s*\d{1,4}`,
		`(?i)\bE\d{1,4}\b`,
	}
	for _, p := range epPatterns {
		re := regexp.MustCompile(p)
		cleanName = re.ReplaceAllString(cleanName, " ")
	}

	// 移除分辨率、编码等常见标记
	cleanPatterns := []string{
		`(?i)\b(BluRay|BDRip|HDRip|WEB-?DL|WEBRip|HDTV|COMPLETE)\b`,
		`(?i)\b(1080p|720p|2160p|4K)\b`,
		`(?i)\b(x264|x265|HEVC|AAC|FLAC)\b`,
	}
	for _, p := range cleanPatterns {
		re := regexp.MustCompile(p)
		cleanName = re.ReplaceAllString(cleanName, " ")
	}

	// 清理分隔符和多余空格
	cleanName = strings.ReplaceAll(cleanName, ".", " ")
	cleanName = strings.ReplaceAll(cleanName, "_", " ")
	cleanName = strings.ReplaceAll(cleanName, "-", " ")
	cleanName = regexp.MustCompile(`\s+`).ReplaceAllString(cleanName, " ")
	cleanName = strings.TrimSpace(cleanName)

	// 移除末尾的纯数字（可能是集号）
	cleanName = regexp.MustCompile(`\s+\d{1,4}\s*$`).ReplaceAllString(cleanName, "")
	cleanName = strings.TrimSpace(cleanName)

	if len(cleanName) > 0 {
		return cleanName
	}

	return ""
}

// extractSeriesTitle 从文件夹名提取剧集标题
func (s *ScannerService) extractSeriesTitle(folderName string) string {
	title := folderName

	// 移除年份信息，如 "Breaking Bad (2008)"
	yearRegex := regexp.MustCompile(`\s*[\(\[]\.?(\d{4})[\)\]]\.?\s*$`)
	title = yearRegex.ReplaceAllString(title, "")

	// 清理常见标记
	cleanPatterns := []string{
		`(?i)\b(BluRay|BDRip|HDRip|WEB-?DL|WEBRip|HDTV|COMPLETE)\b`,
		`(?i)\b(1080p|720p|2160p|4K)\b`,
		`(?i)\b(x264|x265|HEVC)\b`,
	}
	for _, p := range cleanPatterns {
		re := regexp.MustCompile(p)
		title = re.ReplaceAllString(title, "")
	}

	// 替换常见分隔符
	title = strings.ReplaceAll(title, ".", " ")
	title = strings.ReplaceAll(title, "_", " ")

	// 清理多余空格
	title = regexp.MustCompile(`\s+`).ReplaceAllString(title, " ")
	return strings.TrimSpace(title)
}

// broadcastScanEvent 广播扫描事件
func (s *ScannerService) broadcastScanEvent(eventType string, data *ScanProgressData) {
	if data != nil {
		if data.TaskID == "" {
			data.TaskID = s.scanTaskID
		}
		if data.Status == "" {
			switch eventType {
			case EventScanStarted, EventScanProgress:
				data.Status = "running"
			}
		}
	}
	if data != nil && data.LibraryID != "" {
		switch eventType {
		case EventScanStarted:
			resetScanProgressThrottle(data.LibraryID)
		case EventScanProgress:
			if !shouldBroadcastScanProgress(data) {
				return
			}
		case EventScanCompleted, EventScanIncomplete, EventScanFailed, EventScanCanceled:
			defer resetScanProgressThrottle(data.LibraryID)
		}
	}

	if s.wsHub != nil {
		s.wsHub.BroadcastEvent(eventType, data)
	}
}

func resetScanProgressThrottle(libraryID string) {
	if libraryID == "" {
		return
	}

	scanProgressThrottleStore.Lock()
	delete(scanProgressThrottleStore.items, libraryID)
	scanProgressThrottleStore.Unlock()
}

func shouldBroadcastScanProgress(data *ScanProgressData) bool {
	if data == nil || data.LibraryID == "" {
		return true
	}

	now := time.Now()
	metric := data.Current
	if data.NewFound > metric {
		metric = data.NewFound
	}
	if data.Cleaned > metric {
		metric = data.Cleaned
	}

	scanProgressThrottleStore.Lock()
	defer scanProgressThrottleStore.Unlock()

	state, ok := scanProgressThrottleStore.items[data.LibraryID]
	if !ok || state == nil || state.lastPhase != data.Phase {
		scanProgressThrottleStore.items[data.LibraryID] = &scanProgressThrottleState{
			lastSentAt: now,
			lastMetric: metric,
			lastPhase:  data.Phase,
		}
		return true
	}

	if metric >= state.lastMetric+scanProgressBroadcastMinStep || now.Sub(state.lastSentAt) >= scanProgressBroadcastMinInterval {
		state.lastSentAt = now
		state.lastMetric = metric
		state.lastPhase = data.Phase
		return true
	}

	return false
}

func (s *ScannerService) beginScanProgress(library *model.Library, mode string, total int) {
	if library == nil || library.ID == "" {
		return
	}
	if total < 0 {
		total = 0
	}

	scanProgressStateStore.Lock()
	scanProgressStateStore.items[library.ID] = &scanProgressTracker{
		taskID: s.scanTaskID,
		mode:   mode,
		total:  total,
	}
	scanProgressStateStore.Unlock()
}

func (s *ScannerService) setScanProgressTotal(library *model.Library, total int) {
	if library == nil || library.ID == "" {
		return
	}
	if total < 0 {
		total = 0
	}

	scanProgressStateStore.Lock()
	if tracker, ok := scanProgressStateStore.items[library.ID]; ok {
		tracker.total = tracker.current + total
		current := tracker.current
		cumulativeTotal := tracker.total
		taskID := tracker.taskID
		mode := tracker.mode
		scanProgressStateStore.Unlock()
		if s.logger != nil {
			s.logger.Infof("scan targets discovered: library=%s task=%s mode=%s current=%d root_targets=%d cumulative_total=%d", library.ID, taskID, mode, current, total, cumulativeTotal)
		}
		return
	}
	scanProgressStateStore.Unlock()
}

func (s *ScannerService) endScanProgress(libraryID string) {
	if libraryID == "" {
		return
	}

	scanProgressStateStore.Lock()
	delete(scanProgressStateStore.items, libraryID)
	scanProgressStateStore.Unlock()
}

func (s *ScannerService) advanceScanProgress(library *model.Library, message string) {
	if library == nil || library.ID == "" {
		return
	}

	scanProgressStateStore.Lock()
	tracker, ok := scanProgressStateStore.items[library.ID]
	if !ok {
		scanProgressStateStore.Unlock()
		return
	}
	tracker.current++
	current := tracker.current
	total := tracker.total
	mode := tracker.mode
	taskID := tracker.taskID
	scanProgressStateStore.Unlock()
	if s.logger != nil && (current == 1 || current%scanProgressLogStep == 0) {
		s.logger.Infof("scan progress: library=%s task=%s mode=%s current=%d total=%d message=%q", library.ID, taskID, mode, current, total, message)
	}

	s.broadcastScanEvent(EventScanProgress, &ScanProgressData{
		TaskID:      taskID,
		LibraryID:   library.ID,
		LibraryName: library.Name,
		Mode:        mode,
		Phase:       "scanning",
		Current:     current,
		Total:       total,
		Message:     message,
	})
}

type everythingHTTPResult struct {
	Type string `json:"type"`
	Name string `json:"name"`
	Path string `json:"path"`
	Size string `json:"size"`
}

type everythingHTTPResponse struct {
	TotalResults int                    `json:"totalResults"`
	Results      []everythingHTTPResult `json:"results"`
}

func normalizeEverythingAddr(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimRight(raw, "/")
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(raw), "http://") || strings.HasPrefix(strings.ToLower(raw), "https://") {
		return raw
	}
	return "http://" + raw
}

func everythingSearchForRoot(rootPath string) string {
	cleanRoot := filepath.Clean(strings.TrimSpace(rootPath))
	volume := strings.TrimSpace(filepath.VolumeName(cleanRoot))

	terms := []string{"file:"}
	if volume != "" {
		terms = append(terms, strings.ToLower(volume))
	}

	trimmed := strings.TrimPrefix(cleanRoot, volume)
	trimmed = strings.Trim(trimmed, `\/`)
	for _, segment := range strings.FieldsFunc(trimmed, func(r rune) bool {
		return r == '\\' || r == '/'
	}) {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		terms = append(terms, fmt.Sprintf("path:%q", segment))
	}

	extTerms := make([]string, 0, len(supportedExts))
	for ext := range supportedExts {
		extTerms = append(extTerms, "ext:"+strings.TrimPrefix(ext, "."))
	}
	sort.Strings(extTerms)
	terms = append(terms, strings.Join(extTerms, "|"))

	return strings.Join(terms, " ")
}

func isPathWithinRoot(path string, root string) bool {
	path = filepath.Clean(strings.TrimSpace(path))
	root = filepath.Clean(strings.TrimSpace(root))
	if path == "" || root == "" {
		return false
	}
	if strings.EqualFold(path, root) {
		return true
	}
	if len(path) <= len(root) || !strings.EqualFold(path[:len(root)], root) {
		return false
	}
	return os.IsPathSeparator(path[len(root)])
}

// everythingPageTimeout 是单页请求的上限。Everything 偶尔会把响应头回了、
// body 却再也不发（盘没建好索引时最常见），不设上限就只能死等系统的 TCP
// 超时——那是两分四十五秒，全耗在一个注定要回退走目录的请求上。
const everythingPageTimeout = 60 * time.Second

// fetchEverythingPage 取一页搜索结果。超时按页算，慢但能出数据的照常走完。
func fetchEverythingPage(ctx context.Context, client *http.Client, requestURL string) (*everythingHTTPResponse, error) {
	pageCtx, cancel := context.WithTimeout(ctx, everythingPageTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(pageCtx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("everything http status %d", resp.StatusCode)
	}

	var payload everythingHTTPResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

func (s *ScannerService) listMovieEntriesWithEverything(ctx context.Context, library *model.Library, addr string, result *scanRootResult) ([]scanMediaEntry, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	addr = normalizeEverythingAddr(addr)
	if library == nil || addr == "" {
		return nil, fmt.Errorf("everything address is empty")
	}

	rootPaths := library.RootPaths()
	if len(rootPaths) == 0 {
		rootPaths = []string{strings.TrimSpace(library.Path)}
	}

	// Everything 一页要回 2000 条结果，网络盘上光是读完 body 就可能超过十几秒。
	// 所以不给整个请求设死线（那会把"慢"误判成"坏"），只卡住"服务在不在"：
	// 连不上或迟迟不给响应头才算失败，读 body 的时长跟着扫描 ctx 走。
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 15 * time.Second
	client := &http.Client{Transport: transport}
	pageSize := 2000

	seen := make(map[string]bool)
	entries := make([]scanMediaEntry, 0)
	for _, rootPath := range rootPaths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rootPath = filepath.Clean(strings.TrimSpace(rootPath))
		if rootPath == "" {
			continue
		}
		rootInfo, statErr := s.stat(rootPath)
		if statErr != nil {
			return nil, &ScanIncompleteError{Root: rootPath, Err: statErr}
		}
		if !rootInfo.IsDir() {
			return nil, &ScanIncompleteError{Root: rootPath, Err: fmt.Errorf("scan root is not a directory")}
		}

		query := everythingSearchForRoot(rootPath)
		receivedResults := 0
		expectedResults := 0
		for offset := 0; ; offset += pageSize {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			params := url.Values{}
			params.Set("json", "1")
			params.Set("path_column", "1")
			params.Set("size_column", "1")
			params.Set("count", strconv.Itoa(pageSize))
			params.Set("offset", strconv.Itoa(offset))
			params.Set("search", query)

			payload, err := fetchEverythingPage(ctx, client, addr+"/?"+params.Encode())
			if err != nil {
				return nil, err
			}
			if payload.TotalResults > expectedResults {
				expectedResults = payload.TotalResults
			}
			if len(payload.Results) == 0 {
				if expectedResults > receivedResults {
					return nil, &ScanIncompleteError{Root: rootPath, Err: fmt.Errorf(
						"Everything enumeration returned %d of %d results", receivedResults, expectedResults)}
				}
				break
			}
			receivedResults += len(payload.Results)

			for _, item := range payload.Results {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if !strings.EqualFold(strings.TrimSpace(item.Type), "file") {
					continue
				}

				fullPath := normalizeMediaPath(filepath.Join(item.Path, item.Name))
				if !isPathWithinRoot(fullPath, rootPath) {
					continue
				}
				ext := strings.ToLower(filepath.Ext(item.Name))
				if !supportedExts[ext] {
					continue
				}
				if isExtrasPath(fullPath) || isExtrasFile(filepath.Base(fullPath)) {
					continue
				}
				if seen[fullPath] {
					continue
				}
				seen[fullPath] = true
				result.addFile(fullPath)
				entries = append(entries, scanMediaEntry{path: fullPath})
			}

			if expectedResults > 0 && receivedResults >= expectedResults {
				break
			}
			if len(payload.Results) < pageSize {
				if expectedResults > receivedResults {
					return nil, &ScanIncompleteError{Root: rootPath, Err: fmt.Errorf(
						"Everything enumeration returned %d of %d results", receivedResults, expectedResults)}
				}
				break
			}
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].path) < strings.ToLower(entries[j].path)
	})
	return entries, nil
}

func (s *ScannerService) listMovieEntriesWithWalk(library *model.Library, result *scanRootResult) ([]scanMediaEntry, error) {
	if library == nil {
		return nil, fmt.Errorf("library is nil")
	}

	rootPaths := library.RootPaths()
	if len(rootPaths) == 0 {
		rootPaths = []string{strings.TrimSpace(library.Path)}
	}

	entries := make([]scanMediaEntry, 0)
	seen := make(map[string]bool)
	for _, rootPath := range rootPaths {
		rootPath = normalizeMediaPath(rootPath)
		if rootPath == "" {
			continue
		}

		err := s.walk(rootPath, func(path string, info os.FileInfo, walkErr error) error {
			if err := s.checkScanCanceled(); err != nil {
				return err
			}
			if walkErr != nil {
				s.logger.Warnf("visit file failed: %s, err=%v", path, walkErr)
				return walkErr
			}
			if info.IsDir() {
				if extrasExcludeDirs[strings.ToLower(filepath.Base(path))] {
					return filepath.SkipDir
				}
				return nil
			}

			ext := strings.ToLower(filepath.Ext(path))
			if !supportedExts[ext] {
				return nil
			}
			if library.EnableFileFilter && library.MinFileSize > 0 {
				minBytes := int64(library.MinFileSize) * 1024 * 1024
				if info.Size() < minBytes {
					return nil
				}
			}
			if isExtrasPath(path) || isExtrasFile(filepath.Base(path)) {
				return nil
			}

			normalizedPath := normalizeMediaPath(path)
			if seen[normalizedPath] {
				return nil
			}
			seen[normalizedPath] = true
			result.addFile(normalizedPath)
			entries = append(entries, scanMediaEntry{
				path: normalizedPath,
				info: info,
			})
			return nil
		})
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			return nil, &ScanIncompleteError{Root: rootPath, Err: err}
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].path) < strings.ToLower(entries[j].path)
	})
	return entries, nil
}

func (s *ScannerService) listMovieEntries(library *model.Library, options ScanOptions, result *scanRootResult) ([]scanMediaEntry, error) {
	if options.UseEverything {
		entries, err := s.listMovieEntriesWithEverything(options.Context, library, options.EverythingAddr, result)
		if err == nil {
			return entries, nil
		}
		// 只有用户主动取消才中断。这里【不能】把 DeadlineExceeded 也算进来：
		// 扫描 ctx 是 WithCancel 建的，永远超不了时，能走到这儿的超时全是
		// Everything 请求自己慢——那正是该退回去走目录的情况，中断了就等于
		// 整个扫描白跑（曾经扫完 963 个文件后死在第三个根目录上）。
		if errors.Is(err, context.Canceled) {
			return nil, err
		}
		s.logger.Warnf("list movie entries via Everything HTTP failed, fallback to walk: library=%s err=%v", library.Name, err)
	}
	return s.listMovieEntriesWithWalk(library, result)
}

// ProbeMediaInfo 公开的 FFprobe 媒体信息探测方法（供外部服务调用）
func (s *ScannerService) ProbeMediaInfo(media *model.Media) {
	if media == nil {
		return
	}
	if err := s.probeMediaInfo(media); err != nil && s.logger != nil {
		s.logger.Warnf("media probe failed: path=%s stage=%v", media.FilePath, err)
	}
}

// parseSTRMFile 解析 .strm 文件，提取远程流 URL
// .strm 文件格式：纯文本文件，第一行为可播放的远程 URL
func (s *ScannerService) parseSTRMFile(filePath string) (string, error) {
	data, err := s.read(filePath)
	if err != nil {
		return "", fmt.Errorf("读取 .strm 文件失败: %w", err)
	}
	streamURL, err := parseSTRMData(data)
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, filePath)
	}
	return streamURL, nil
}

func parseSTRMData(data []byte) (string, error) {
	// 逐行读取，取第一个非空、非注释行作为 URL
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// 验证是否为有效的 URL
		if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
			parsed, parseErr := url.Parse(line)
			if parseErr != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
				return "", fmt.Errorf(".strm target URL is invalid")
			}
			return line, nil
		}
	}

	return "", fmt.Errorf(".strm file does not contain a valid URL")
}

// isSTRMFile 判断是否为 .strm 文件
func isSTRMFile(filePath string) bool {
	return strings.ToLower(filepath.Ext(filePath)) == ".strm"
}

// probeSTRMMedia 处理 .strm 文件的媒体信息
// 对于 .strm 文件，不使用 FFprobe 探测（远程 URL 可能很慢或不支持），
// 而是设置默认值，后续播放时由前端/后端动态处理
func (s *ScannerService) probeSTRMMedia(media *model.Media, streamURL string) {
	media.StreamURL = streamURL
	// 根据远程 URL 的扩展名推断基本信息
	urlLower := strings.ToLower(streamURL)
	if strings.Contains(urlLower, ".m3u8") {
		media.VideoCodec = "strm_hls"
	} else if strings.HasSuffix(urlLower, ".mp4") || strings.Contains(urlLower, ".mp4?") {
		media.VideoCodec = "strm_mp4"
	} else if strings.HasSuffix(urlLower, ".mkv") || strings.Contains(urlLower, ".mkv?") {
		media.VideoCodec = "strm_mkv"
	} else {
		media.VideoCodec = "strm_unknown"
	}
	s.logger.Debugf("STRM 文件: %s -> %s", media.FilePath, streamURL)
}

func (s *ScannerService) runMediaProbe(mediaPath string) ([]byte, error) {
	info, err := s.stat(mediaPath)
	if err != nil {
		return nil, err
	}
	absPath, err := filepath.Abs(mediaPath)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("%s:%d:%d", nfoPathKey(absPath), info.Size(), info.ModTime().UnixNano())
	waiter := s.commandContext()
	run := func(ctx context.Context) ([]byte, error) {
		return s.probeGovernor.runKeyed(ctx, key, func(processCtx context.Context) ([]byte, error) {
			if s.probeMediaFileContext != nil {
				return s.probeMediaFileContext(processCtx, mediaPath)
			}
			if s.probeMediaFile != nil {
				return s.probeMediaFile(mediaPath)
			}
			if s.cfg == nil || strings.TrimSpace(s.cfg.App.FFprobePath) == "" {
				return nil, fmt.Errorf("ffprobe configuration is unavailable")
			}
			return runBackgroundCommand(processCtx, mediaProbeTimeout, false, s.cfg.App.FFprobePath,
				"-v", "quiet", "-print_format", "json", "-show_format", "-show_streams", mediaPath)
		})
	}
	if s.probeScope != nil {
		return s.probeScope.do(waiter, key, run)
	}
	return run(waiter)
}

// probeMediaInfo 使用FFprobe提取视频元数据（.strm 文件走特殊逻辑）
func (s *ScannerService) probeMediaInfo(media *model.Media) error {
	if media == nil || strings.TrimSpace(media.FilePath) == "" {
		return fmt.Errorf("probe input is missing media path")
	}
	if s.preparedProbes != nil {
		probe, ok := s.preparedProbes[nfoPathKey(media.FilePath)]
		if !ok {
			return fmt.Errorf("prepared probe result is missing")
		}
		probe.apply(media)
		return nil
	}
	// .strm 文件：解析远程 URL，不使用 FFprobe
	if isSTRMFile(media.FilePath) {
		streamURL, err := s.parseSTRMFile(media.FilePath)
		if err != nil {
			return fmt.Errorf("strm parse: %w", err)
		}
		s.probeSTRMMedia(media, streamURL)
		return nil
	}
	output, err := s.runMediaProbe(media.FilePath)
	if err != nil {
		return fmt.Errorf("ffprobe execution: %w", err)
	}

	return s.applyFFprobeOutput(media, output)
}

func (s *ScannerService) applyFFprobeOutput(media *model.Media, output []byte) error {
	var result FFprobeResult
	if err := json.Unmarshal(output, &result); err != nil {
		return fmt.Errorf("ffprobe JSON decode: %w", err)
	}
	if len(result.Streams) == 0 {
		return fmt.Errorf("ffprobe output contains no media streams")
	}

	// 提取视频流信息
	for _, stream := range result.Streams {
		switch stream.CodecType {
		case "video":
			media.VideoCodec = stream.CodecName
			if stream.Width > 0 && stream.Height > 0 {
				media.Resolution = s.classifyResolution(stream.Width, stream.Height)
			}
		case "audio":
			if media.AudioCodec == "" {
				media.AudioCodec = stream.CodecName
			}
		}
	}

	// 提取时长
	if result.Format.Duration != "" {
		dur, err := strconv.ParseFloat(result.Format.Duration, 64)
		if err != nil {
			return fmt.Errorf("ffprobe duration parse: %w", err)
		}
		media.Duration = dur
	}
	return nil
}

// GetSubtitleTracks 获取媒体文件的内嵌字幕轨道列表
func (s *ScannerService) GetSubtitleTracks(filePath string) ([]SubtitleTrack, error) {
	output, err := s.probeGovernor.run(s.commandContext(), func(ctx context.Context) ([]byte, error) {
		return runBackgroundCommand(ctx, mediaProbeTimeout, false, s.cfg.App.FFprobePath,
			"-v", "quiet", "-print_format", "json", "-show_streams", "-select_streams", "s", filePath)
	})
	if err != nil {
		return nil, fmt.Errorf("FFprobe获取字幕失败: %w", err)
	}

	var result struct {
		Streams []FFprobeStream `json:"streams"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("解析字幕信息失败: %w", err)
	}

	var tracks []SubtitleTrack
	for _, stream := range result.Streams {
		track := SubtitleTrack{
			Index:   stream.Index,
			Codec:   stream.CodecName,
			Default: stream.Disposition.Default == 1,
			Forced:  stream.Disposition.Forced == 1,
			Bitmap:  isBitmapSubtitle(stream.CodecName),
		}
		if lang, ok := stream.Tags["language"]; ok {
			track.Language = lang
		}
		if title, ok := stream.Tags["title"]; ok {
			track.Title = title
		}
		tracks = append(tracks, track)
	}

	return tracks, nil
}

// ExtractSubtitle 提取内嵌字幕到文件
func (s *ScannerService) ExtractSubtitle(filePath string, streamIndex int, outputFormat string) (string, error) {
	// 确定输出文件路径
	cacheDir := filepath.Join(s.cfg.Cache.CacheDir, "subtitles")
	os.MkdirAll(cacheDir, 0755)

	baseName := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	outputPath := filepath.Join(cacheDir, fmt.Sprintf("%s_%d.%s", baseName, streamIndex, outputFormat))

	// 检查缓存
	if _, err := os.Stat(outputPath); err == nil {
		return outputPath, nil
	}

	if _, err := s.runFFmpegAtomic(outputPath, []string{"-y", "-i", filePath, "-map", fmt.Sprintf("0:%d", streamIndex), "-c:s", s.getSubtitleCodec(outputFormat)}); err != nil {
		return "", fmt.Errorf("提取字幕失败: %w", err)
	}

	return outputPath, nil
}

// scanExternalSubtitles 扫描外挂字幕文件
func (s *ScannerService) scanExternalSubtitles(media *model.Media) {
	dir := filepath.Dir(media.FilePath)
	baseName := strings.TrimSuffix(filepath.Base(media.FilePath), filepath.Ext(media.FilePath))

	subtitleExts := []string{".srt", ".ass", ".ssa", ".vtt", ".sub", ".idx"}

	var found []string
	entries, err := s.listDir(dir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))

		// 检查是否为字幕文件且与视频同名前缀
		isSubtitle := false
		for _, subExt := range subtitleExts {
			if ext == subExt {
				isSubtitle = true
				break
			}
		}
		if !isSubtitle {
			continue
		}

		// 检查文件名前缀匹配
		nameWithoutExt := strings.TrimSuffix(name, ext)
		if strings.HasPrefix(strings.ToLower(nameWithoutExt), strings.ToLower(baseName)) {
			found = append(found, filepath.Join(dir, name))
		}
	}

	if len(found) > 0 {
		media.SubtitlePaths = strings.Join(found, "|")
		s.logger.Debugf("发现外挂字幕: %s -> %d 个", baseName, len(found))
	}
}

// getSubtitleCodec 根据输出格式获取字幕编解码器
func (s *ScannerService) getSubtitleCodec(format string) string {
	switch format {
	case "srt":
		return "srt"
	case "ass", "ssa":
		return "ass"
	case "vtt", "webvtt":
		return "webvtt"
	default:
		return "srt"
	}
}

// classifyResolution 根据分辨率分类
func (s *ScannerService) classifyResolution(width, height int) string {
	// 以高度为主要判断标准
	maxDim := height
	if width > height {
		// 正常横向视频
		maxDim = height
	} else {
		// 竖向视频
		maxDim = width
	}

	switch {
	case maxDim >= 2160:
		return "4K"
	case maxDim >= 1440:
		return "2K"
	case maxDim >= 1080:
		return "1080p"
	case maxDim >= 720:
		return "720p"
	case maxDim >= 480:
		return "480p"
	default:
		return fmt.Sprintf("%dp", maxDim)
	}
}

// extractTitle 从文件名提取标题（保持向后兼容的简单版本）
func (s *ScannerService) extractTitle(filename string) string {
	title, _, _ := s.extractTitleEnhanced(filename)
	return title
}

// extractTitleEnhanced 从文件名增强提取标题、年份和 TMDb ID
// 支持 Emby 标准命名格式：Title (Year) [tmdbid=xxx]
func (s *ScannerService) extractTitleEnhanced(filename string) (title string, year int, tmdbID int) {
	// 去掉扩展名
	name := strings.TrimSuffix(filename, filepath.Ext(filename))

	// 步骤1：提取 ID 标签 [tmdbid=xxx]、{imdb-ttxxx} 等
	idType, idValue := parseIDFromName(name)
	if idType == "tmdbid" || idType == "tmdb" {
		tmdbID, _ = strconv.Atoi(idValue)
	}
	// 从名称中移除 ID 标签
	for _, pattern := range idTagPatterns {
		name = pattern.ReplaceAllString(name, "")
	}

	// 步骤2：提取年份 (2009) 或 [2009]
	year = extractYearFromName(name)
	// 移除年份标记
	name = yearInNamePattern.ReplaceAllString(name, "")

	// 步骤3：清理常见编码/来源/分辨率标记
	cleanPatterns := []string{
		`(?i)\b(BluRay|BDRip|HDRip|WEB-?DL|WEBRip|DVDRip|HDTV|HDCam|REMUX)\b`,
		`(?i)\b(x264|x265|h\.?264|h\.?265|HEVC|AVC|AAC|DTS|AC3|FLAC|OPUS)\b`,
		`(?i)\b(1080p|720p|480p|2160p|4K|UHD)\b`,
		`(?i)\b(PROPER|REPACK|EXTENDED|UNRATED|DIRECTORS\.?CUT|REMASTERED)\b`,
	}
	for _, p := range cleanPatterns {
		re := regexp.MustCompile(p)
		name = re.ReplaceAllString(name, " ")
	}

	// 步骤4：替换常见分隔符为空格
	replacer := strings.NewReplacer(
		".", " ",
		"_", " ",
	)
	name = replacer.Replace(name)

	// 步骤5：清理多余空格和首尾的分隔符
	name = regexp.MustCompile(`\s+`).ReplaceAllString(name, " ")
	name = strings.Trim(name, " -")

	title = strings.TrimSpace(name)
	return
}

// GetExternalSubtitles 获取媒体文件的外挂字幕列表
func (s *ScannerService) GetExternalSubtitles(filePath string) []ExternalSubtitle {
	dir := filepath.Dir(filePath)
	baseName := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))

	subtitleExts := []string{".srt", ".ass", ".ssa", ".vtt", ".sub"}

	var subs []ExternalSubtitle
	entries, err := os.ReadDir(dir)
	if err != nil {
		return subs
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))

		isSubtitle := false
		for _, subExt := range subtitleExts {
			if ext == subExt {
				isSubtitle = true
				break
			}
		}
		if !isSubtitle {
			continue
		}

		nameWithoutExt := strings.TrimSuffix(name, ext)
		if strings.HasPrefix(strings.ToLower(nameWithoutExt), strings.ToLower(baseName)) {
			// 尝试从文件名提取语言信息，如 movie.zh.srt, movie.eng.srt
			langs := strings.TrimPrefix(strings.ToLower(nameWithoutExt), strings.ToLower(baseName))
			langs = strings.Trim(langs, "._ ")
			lang := s.detectSubtitleLanguage(langs)

			subs = append(subs, ExternalSubtitle{
				Path:     filepath.Join(dir, name),
				Filename: name,
				Format:   strings.TrimPrefix(ext, "."),
				Language: lang,
			})
		}
	}

	return subs
}

// ExternalSubtitle 外挂字幕信息
type ExternalSubtitle struct {
	Path     string `json:"path"`
	Filename string `json:"filename"`
	Format   string `json:"format"`   // srt, ass, vtt等
	Language string `json:"language"` // 语言编码
}

// detectSubtitleLanguage 从文件名中检测字幕语言
func (s *ScannerService) detectSubtitleLanguage(namePart string) string {
	// 按优先级排序的语言映射（长匹配优先，避免短码误匹配）
	type langEntry struct {
		code string
		lang string
	}
	langEntries := []langEntry{
		// 长匹配优先
		{"chinese", "中文"},
		{"english", "English"},
		{"japanese", "日本語"},
		{"korean", "한국어"},
		{"简体", "简体中文"},
		{"繁体", "繁体中文"},
		{"简中", "简体中文"},
		{"繁中", "繁体中文"},
		// 三字母ISO 639-2
		{"chi", "中文"},
		{"chs", "简体中文"},
		{"cht", "繁体中文"},
		{"eng", "English"},
		{"jpn", "日本語"},
		{"kor", "한국어"},
		// 两字母ISO 639-1（使用分隔符精确匹配）
		{"zh", "中文"},
		{"en", "English"},
		{"ja", "日本語"},
		{"jp", "日本語"},
		{"ko", "한국어"},
		{"sc", "简体中文"},
		{"tc", "繁体中文"},
	}

	namePart = strings.ToLower(namePart)
	// 将分隔符统一为点号，方便精确匹配
	normalized := strings.NewReplacer("_", ".", "-", ".", " ", ".").Replace(namePart)
	parts := strings.Split(normalized, ".")

	// 先尝试精确匹配各段
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		for _, entry := range langEntries {
			if part == entry.code {
				return entry.lang
			}
		}
	}

	// 再尝试包含匹配（仅对长码，避免短码误匹配）
	for _, entry := range langEntries {
		if len(entry.code) >= 3 && strings.Contains(namePart, entry.code) {
			return entry.lang
		}
	}

	if namePart != "" {
		return namePart
	}
	return "未知"
}

// ConvertSubtitleToVTT 将外挂字幕文件转换为WebVTT格式（浏览器原生支持）
func (s *ScannerService) ConvertSubtitleToVTT(subtitlePath string) (string, error) {
	// 确定输出文件路径
	cacheDir := filepath.Join(s.cfg.Cache.CacheDir, "subtitles")
	os.MkdirAll(cacheDir, 0755)

	// 使用原始文件名+哈希避免冲突
	baseName := strings.TrimSuffix(filepath.Base(subtitlePath), filepath.Ext(subtitlePath))
	outputPath := filepath.Join(cacheDir, fmt.Sprintf("%s_ext.vtt", baseName))

	// 检查缓存：如果转换后的文件已存在且比源文件新，直接返回
	if outInfo, err := os.Stat(outputPath); err == nil {
		if srcInfo, err := os.Stat(subtitlePath); err == nil {
			if outInfo.ModTime().After(srcInfo.ModTime()) {
				return outputPath, nil
			}
		}
	}

	// 使用FFmpeg将字幕转换为WebVTT
	if output, err := s.runFFmpegAtomic(outputPath, []string{"-y", "-i", subtitlePath, "-c:s", "webvtt"}); err != nil {
		return "", fmt.Errorf("FFmpeg字幕转换失败: %w, 输出: %s", err, string(output))
	}

	s.logger.Debugf("字幕转换完成: %s -> %s", subtitlePath, outputPath)
	return outputPath, nil
}

func (s *ScannerService) runFFmpegAtomic(outputPath string, args []string) ([]byte, error) {
	if fileExists(outputPath) {
		return nil, nil
	}
	if s.thumbnailService == nil || s.thumbnailService.ffmpegGovernor == nil {
		return nil, fmt.Errorf("ffmpeg governor is unavailable")
	}
	ctx := s.commandContext()
	return s.thumbnailService.ffmpegGovernor.runKeyed(ctx, "ffmpeg:"+filepath.Clean(outputPath), func(runCtx context.Context) ([]byte, error) {
		if fileExists(outputPath) {
			return nil, nil
		}
		if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
			return nil, err
		}
		ext := filepath.Ext(outputPath)
		tempPath := strings.TrimSuffix(outputPath, ext) + ".navi-ffmpeg-" + uuid.NewString() + ".part" + ext
		defer os.Remove(tempPath)
		commandArgs := append(append([]string(nil), args...), tempPath)
		output, err := runBackgroundCommand(runCtx, mediaTranscodeTimeout, true, s.cfg.App.FFmpegPath, commandArgs...)
		if err != nil {
			return output, err
		}
		if err := os.Rename(tempPath, outputPath); err != nil {
			return output, err
		}
		return output, nil
	})
}

func (s *ScannerService) ThumbnailService() *ThumbnailService {
	if s == nil {
		return nil
	}
	return s.thumbnailService
}

// GetFileExt 获取文件扩展名（小写）
func GetFileExt(path string) string {
	return strings.ToLower(filepath.Ext(path))
}
