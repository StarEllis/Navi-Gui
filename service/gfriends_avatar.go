package service

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"go.uber.org/zap"
	"navi-desktop/model"
)

const (
	gfriendsIndexTTL              = 7 * 24 * time.Hour
	gfriendsAvatarRetryDelay      = 6 * time.Hour
	gfriendsDownloadAttempts      = 2
	gfriendsDownloadBackoff       = 2 * time.Second
	maxGfriendsIndexDownloadBytes = 32 << 20
	maxGfriendsAvatarBytes        = 4 << 20
)

// gfriends uses Japanese stage names while local NFO files commonly contain
// Chinese translations, which OpenCC cannot bridge whenever the Japanese name
// is written in kana. mapping_actor.xml pairs every known spelling of an actor
// with that Japanese name, so the translation stays data instead of guesswork.
// Source: https://github.com/sqzw-x/mdcx/blob/master/resources/mapping_table/mapping_actor.xml
//
//go:embed data/mapping_actor.xml
var gfriendsActorMappingXML []byte

// gfriendsActorAliases maps a normalized spelling to the Japanese stage name.
var gfriendsActorAliases = sync.OnceValue(loadGfriendsActorAliases)

func loadGfriendsActorAliases() map[string]string {
	var table struct {
		Actors []struct {
			Simplified  string `xml:"zh_cn,attr"`
			Traditional string `xml:"zh_tw,attr"`
			Japanese    string `xml:"jp,attr"`
			Keyword     string `xml:"keyword,attr"`
		} `xml:"a"`
	}
	if err := xml.Unmarshal(gfriendsActorMappingXML, &table); err != nil {
		return nil
	}

	aliases := make(map[string]string, len(table.Actors)*3)
	for _, actor := range table.Actors {
		japanese := strings.TrimSpace(actor.Japanese)
		if japanese == "" {
			continue
		}
		names := append([]string{actor.Simplified, actor.Traditional}, strings.Split(actor.Keyword, ",")...)
		for _, name := range names {
			// 【同名異人】 and similar annotations mark names, they are not names.
			if strings.Contains(name, "【") {
				continue
			}
			key := normalizeGfriendsName(name)
			if key == "" {
				continue
			}
			// The first spelling wins so a later entry reusing a retired stage
			// name cannot steal it from the actor who is listed under it.
			if _, exists := aliases[key]; !exists {
				aliases[key] = japanese
			}
		}
	}
	return aliases
}

// Both entries serve the same upstream repository; the second one only reaches
// it through a different exit. GitHub throttles this content hard enough that a
// single path returns 429 for long stretches, and the two paths fail
// independently, so the fallback is what turns a run into eventual success.
var defaultGfriendsIndexURLs = []string{
	"https://raw.githubusercontent.com/gfriends/gfriends/master/Filetree.json",
	"https://ghfast.top/https://raw.githubusercontent.com/gfriends/gfriends/master/Filetree.json",
}

var defaultGfriendsContentBases = []string{
	"https://raw.githubusercontent.com/gfriends/gfriends/master/Content/",
	"https://ghfast.top/https://raw.githubusercontent.com/gfriends/gfriends/master/Content/",
}

// errGfriendsSourceUnavailable marks the source refusing or failing to answer —
// throttling, a gateway error, a timeout — rather than the file being missing.
// Those are worth retrying; a 404 is not.
var errGfriendsSourceUnavailable = errors.New("gfriends source unavailable")

type GfriendsAvatarOptions struct {
	CacheDir     string
	ArtworkCache *ArtworkCache
	Logger       *zap.SugaredLogger
	IndexURLs    []string
	ContentBases []string
	Client       *http.Client
}

type AvatarCandidate struct {
	ActorName string
	Studio    string
	FileName  string
	Query     string
	URL       string
}

type GfriendsAvatarService struct {
	cacheDir     string
	artworkCache *ArtworkCache
	logger       *zap.SugaredLogger
	indexURLs    []string
	contentBases []string
	client       *http.Client

	refreshMu   sync.Mutex
	mu          sync.RWMutex
	index       map[string]AvatarCandidate
	indexLoaded bool
	indexStamp  time.Time

	failureMu sync.Mutex
	failures  map[string]time.Time
}

func NewGfriendsAvatarService(options GfriendsAvatarOptions) *GfriendsAvatarService {
	cacheDir := strings.TrimSpace(options.CacheDir)
	if cacheDir == "" {
		cacheDir = "cache"
	}
	indexURLs := append([]string(nil), options.IndexURLs...)
	if len(indexURLs) == 0 {
		indexURLs = append([]string(nil), defaultGfriendsIndexURLs...)
	}
	contentBases := append([]string(nil), options.ContentBases...)
	if len(contentBases) == 0 {
		contentBases = append([]string(nil), defaultGfriendsContentBases...)
	}
	client := options.Client
	if client == nil {
		client = NewHTTPClient(20 * time.Second)
	}
	artworkCache := options.ArtworkCache
	if artworkCache == nil {
		artworkCache = NewArtworkCache(cacheDir, options.Logger)
	}
	return &GfriendsAvatarService{
		cacheDir:     filepath.Join(cacheDir, "gfriends"),
		artworkCache: artworkCache,
		logger:       options.Logger,
		indexURLs:    indexURLs,
		contentBases: contentBases,
		client:       client,
		index:        make(map[string]AvatarCandidate),
		failures:     make(map[string]time.Time),
	}
}

func (s *GfriendsAvatarService) RefreshIndexIfNeeded() error {
	if s == nil {
		return nil
	}

	if s.hasFreshIndex() {
		return nil
	}

	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	if s.hasFreshIndex() {
		return nil
	}

	indexPath := s.indexPath()
	if info, err := os.Stat(indexPath); err == nil && time.Since(info.ModTime()) < gfriendsIndexTTL {
		return s.loadIndexFromFile(indexPath, info.ModTime())
	}

	if data, err := s.downloadFirst(s.indexURLs); err == nil && len(data) > 0 {
		if writeErr := os.MkdirAll(filepath.Dir(indexPath), 0755); writeErr == nil {
			_ = os.WriteFile(indexPath, data, 0644)
		}
		return s.loadIndex(data, time.Now())
	} else if s.logger != nil {
		s.logger.Debugf("refresh gfriends index failed: %v", err)
	}

	if info, err := os.Stat(indexPath); err == nil {
		return s.loadIndexFromFile(indexPath, info.ModTime())
	}
	return nil
}

func (s *GfriendsAvatarService) hasFreshIndex() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.indexLoaded && time.Since(s.indexStamp) < gfriendsIndexTTL
}

func (s *GfriendsAvatarService) FindCandidate(actorName string) (*AvatarCandidate, bool) {
	if s == nil {
		return nil, false
	}
	key := normalizeGfriendsName(actorName)
	if key == "" {
		return nil, false
	}
	s.mu.RLock()
	candidate, ok := s.index[key]
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return &candidate, true
}

// gfriendsMaxCandidates 给候选数封顶。图源里个别演员在同一个片商目录下堆了近
// 千张（按作品拆的），一张张翻过去没有意义，取前若干张够用。
const gfriendsMaxCandidates = 20

// CandidateKey 是候选在图源里的身份：片商 + 文件名。存进 people.avatar_source，
// 下次「换一张」据此知道现在停在哪一张。
func (c AvatarCandidate) CandidateKey() string {
	return c.Studio + "/" + c.FileName
}

// ListCandidates 返回这个演员在图源里的全部头像候选。
//
// 常驻内存的索引是「一个名字一张图」（同名后来的覆盖先来的），换一张需要看到
// 全部候选，所以这里按需重读磁盘上的 Filetree.json，不把几十万条候选常驻内存。
func (s *GfriendsAvatarService) ListCandidates(actorName string) ([]AvatarCandidate, error) {
	if s == nil || strings.TrimSpace(actorName) == "" {
		return nil, nil
	}
	if err := s.RefreshIndexIfNeeded(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.indexPath())
	if err != nil {
		return nil, err
	}

	var filetree struct {
		Content json.RawMessage `json:"Content"`
	}
	if err := json.Unmarshal(data, &filetree); err != nil {
		return nil, err
	}
	studios, err := decodeGfriendsStudios(filetree.Content)
	if err != nil {
		return nil, err
	}

	wanted := map[string]bool{normalizeGfriendsName(actorName): true}
	// 演员名可能是中文写法（樱空桃），图源按日文归档（桜空もも），走别名表补一个。
	if japanese, ok := gfriendsActorAliases()[normalizeGfriendsName(actorName)]; ok {
		if key := normalizeGfriendsName(japanese); key != "" {
			wanted[key] = true
		}
	}
	delete(wanted, "")
	if len(wanted) == 0 {
		return nil, nil
	}

	seen := make(map[string]bool)
	candidates := make([]AvatarCandidate, 0, 8)
	for _, entry := range studios {
		for aliasFileName, targetWithQuery := range entry.files {
			targetFileName, query := splitGfriendsTarget(targetWithQuery)
			if targetFileName == "" {
				targetFileName, query = splitGfriendsTarget(aliasFileName)
			}
			candidate := AvatarCandidate{
				ActorName: actorStem(targetFileName),
				Studio:    entry.name,
				FileName:  targetFileName,
				Query:     query,
			}
			matched := false
			for _, name := range []string{aliasFileName, targetFileName, candidate.ActorName} {
				if wanted[normalizeGfriendsName(actorStem(name))] ||
					wanted[normalizeGfriendsName(gfriendsVariantStem(name))] {
					matched = true
					break
				}
			}
			if !matched || seen[candidate.CandidateKey()] {
				continue
			}
			seen[candidate.CandidateKey()] = true
			candidate.URL = s.candidateURL(candidate, 0)
			candidates = append(candidates, candidate)
		}
	}

	// 片商目录名带排序前缀（0-Hand-Storage、8-GRAPHIS…），照它排序结果才稳定，
	// 不然每次读出来的顺序都跟着 map 遍历乱跳。
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Studio != candidates[j].Studio {
			return candidates[i].Studio < candidates[j].Studio
		}
		return NaturalLess(candidates[i].FileName, candidates[j].FileName)
	})
	if len(candidates) > gfriendsMaxCandidates {
		candidates = candidates[:gfriendsMaxCandidates]
	}
	return candidates, nil
}

// FetchCandidate 下载指定候选的图片数据。
func (s *GfriendsAvatarService) FetchCandidate(candidate AvatarCandidate) ([]byte, error) {
	if s == nil {
		return nil, fmt.Errorf("gfriends avatar service unavailable")
	}
	return s.downloadCandidate(candidate)
}

func (s *GfriendsAvatarService) EnsureActorAvatar(person *model.Person) (bool, error) {
	if s == nil || person == nil || strings.TrimSpace(person.Name) == "" {
		return false, nil
	}
	if fileExists(person.ProfileURL) {
		return false, nil
	}
	if s.downloadRecentlyFailed(person.Name) {
		return false, nil
	}
	if err := s.RefreshIndexIfNeeded(); err != nil {
		return false, err
	}
	candidate, ok := s.FindCandidate(person.Name)
	if !ok || candidate == nil {
		return false, nil
	}

	data, err := s.downloadCandidate(*candidate)
	if err != nil {
		// A source that refused says nothing about this actor, so it stays
		// eligible for the next run instead of sitting out the whole cooldown.
		if !errors.Is(err, errGfriendsSourceUnavailable) {
			s.recordDownloadFailure(person.Name)
		}
		if s.logger != nil {
			s.logger.Debugf("download gfriends avatar failed: actor=%s err=%v", person.Name, err)
		}
		return false, nil
	}
	cachedPath, err := s.artworkCache.CacheActorImage(person.ID, person.Name, data)
	if err != nil {
		return false, err
	}
	if cachedPath == "" || samePath(person.ProfileURL, cachedPath) {
		return false, nil
	}
	person.ProfileURL = cachedPath
	person.AvatarSource = candidate.CandidateKey()
	return true, nil
}

// downloadRecentlyFailed reports whether this actor's avatar was already tried
// and failed. Without it, an unreachable or rate-limited source is re-attempted
// for every actor every time the actor page is opened.
func (s *GfriendsAvatarService) downloadRecentlyFailed(actorName string) bool {
	key := normalizeGfriendsName(actorName)
	if key == "" {
		return false
	}
	s.failureMu.Lock()
	defer s.failureMu.Unlock()
	failedAt, ok := s.failures[key]
	if !ok {
		return false
	}
	if time.Since(failedAt) >= gfriendsAvatarRetryDelay {
		delete(s.failures, key)
		return false
	}
	return true
}

// ResetDownloadFailures drops the cooldown so an explicit retry starts clean.
func (s *GfriendsAvatarService) ResetDownloadFailures() {
	if s == nil {
		return
	}
	s.failureMu.Lock()
	defer s.failureMu.Unlock()
	s.failures = make(map[string]time.Time)
}

func (s *GfriendsAvatarService) recordDownloadFailure(actorName string) {
	key := normalizeGfriendsName(actorName)
	if key == "" {
		return
	}
	s.failureMu.Lock()
	defer s.failureMu.Unlock()
	if s.failures == nil {
		s.failures = make(map[string]time.Time)
	}
	s.failures[key] = time.Now()
}

func (s *GfriendsAvatarService) loadIndexFromFile(indexPath string, stamp time.Time) error {
	data, err := os.ReadFile(indexPath)
	if err != nil {
		return err
	}
	return s.loadIndex(data, stamp)
}

func (s *GfriendsAvatarService) loadIndex(data []byte, stamp time.Time) error {
	var filetree struct {
		Content json.RawMessage `json:"Content"`
	}
	if err := json.Unmarshal(data, &filetree); err != nil {
		return err
	}
	studios, err := decodeGfriendsStudios(filetree.Content)
	if err != nil {
		return err
	}

	index := make(map[string]AvatarCandidate)
	for _, entry := range studios {
		studio, files := entry.name, entry.files
		for aliasFileName, targetWithQuery := range files {
			targetFileName, query := splitGfriendsTarget(targetWithQuery)
			if targetFileName == "" {
				targetFileName, query = splitGfriendsTarget(aliasFileName)
			}
			candidate := AvatarCandidate{
				ActorName: actorStem(targetFileName),
				Studio:    studio,
				FileName:  targetFileName,
				Query:     query,
			}
			candidate.URL = s.candidateURL(candidate, 0)
			for _, name := range []string{aliasFileName, targetFileName, candidate.ActorName} {
				if key := normalizeGfriendsName(actorStem(name)); key != "" {
					index[key] = candidate
				}
			}
		}
	}
	for alias, japanese := range gfriendsActorAliases() {
		// A spelling gfriends already files an avatar under always wins: the
		// alias table is only allowed to fill gaps, never to redirect a name.
		if _, exists := index[alias]; exists {
			continue
		}
		if candidate, ok := index[normalizeGfriendsName(japanese)]; ok {
			index[alias] = candidate
		}
	}

	s.mu.Lock()
	s.index = index
	s.indexLoaded = true
	s.indexStamp = stamp
	s.mu.Unlock()
	return nil
}

type gfriendsStudioFiles struct {
	name  string
	files map[string]string
}

func decodeGfriendsStudios(content json.RawMessage) ([]gfriendsStudioFiles, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, fmt.Errorf("invalid gfriends Content object")
	}

	var studios []gfriendsStudioFiles
	for decoder.More() {
		nameToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := nameToken.(string)
		if !ok {
			return nil, fmt.Errorf("invalid gfriends studio name")
		}
		var files map[string]string
		if err := decoder.Decode(&files); err != nil {
			return nil, err
		}
		studios = append(studios, gfriendsStudioFiles{name: name, files: files})
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	return studios, nil
}

func (s *GfriendsAvatarService) downloadCandidate(candidate AvatarCandidate) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < gfriendsDownloadAttempts; attempt++ {
		if attempt > 0 {
			// While throttled the sources answer probabilistically, so spacing
			// the retry is what turns a refusal into an eventual avatar.
			time.Sleep(time.Duration(attempt) * gfriendsDownloadBackoff)
		}
		data, err := s.downloadFromContentBases(candidate)
		if err == nil {
			return data, nil
		}
		lastErr = err
		if !errors.Is(err, errGfriendsSourceUnavailable) {
			break
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no gfriends content base configured")
	}
	return nil, lastErr
}

func (s *GfriendsAvatarService) downloadFromContentBases(candidate AvatarCandidate) ([]byte, error) {
	var lastErr error
	for index := range s.contentBases {
		candidateURL := s.candidateURL(candidate, index)
		if strings.TrimSpace(candidateURL) == "" {
			continue
		}
		data, err := s.downloadURL(candidateURL)
		if err == nil {
			return data, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func (s *GfriendsAvatarService) downloadFirst(urls []string) ([]byte, error) {
	var lastErr error
	for _, rawURL := range urls {
		data, err := s.downloadIndexURL(rawURL)
		if err == nil {
			return data, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no URL configured")
	}
	return nil, lastErr
}

func (s *GfriendsAvatarService) downloadIndexURL(rawURL string) ([]byte, error) {
	return s.downloadURLLimited(rawURL, maxGfriendsIndexDownloadBytes, false)
}

func (s *GfriendsAvatarService) downloadURL(rawURL string) ([]byte, error) {
	return s.downloadURLLimited(rawURL, maxGfriendsAvatarBytes, true)
}

func (s *GfriendsAvatarService) downloadURLLimited(rawURL string, maxBytes int64, requireImage bool) ([]byte, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, fmt.Errorf("empty URL")
	}
	if maxBytes <= 0 {
		return nil, fmt.Errorf("invalid download limit")
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "alex-desktop-gfriends-cache/1.0")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errGfriendsSourceUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return nil, fmt.Errorf("%w: HTTP %d", errGfriendsSourceUnavailable, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxBytes {
		return nil, fmt.Errorf("download too large: %d bytes", resp.ContentLength)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("download too large: over %d bytes", maxBytes)
	}
	if requireImage {
		if err := validateGfriendsImageContent(resp.Header.Get("Content-Type"), data); err != nil {
			return nil, err
		}
	}
	return data, nil
}

func validateGfriendsImageContent(contentType string, data []byte) error {
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	if strings.HasPrefix(mediaType, "image/") {
		return nil
	}
	if mediaType == "" || mediaType == "application/octet-stream" {
		detected := strings.ToLower(http.DetectContentType(data))
		if strings.HasPrefix(detected, "image/") {
			return nil
		}
		return fmt.Errorf("unexpected content type: %s", detected)
	}
	return fmt.Errorf("unexpected content type: %s", contentType)
}

func (s *GfriendsAvatarService) candidateURL(candidate AvatarCandidate, baseIndex int) string {
	if baseIndex < 0 || baseIndex >= len(s.contentBases) {
		return ""
	}
	base := strings.TrimRight(strings.TrimSpace(s.contentBases[baseIndex]), "/")
	if base == "" || strings.TrimSpace(candidate.Studio) == "" || strings.TrimSpace(candidate.FileName) == "" {
		return ""
	}
	return base + "/" + escapeGfriendsPath(candidate.Studio) + "/" + escapeGfriendsPath(candidate.FileName) + candidate.Query
}

func (s *GfriendsAvatarService) indexPath() string {
	return filepath.Join(s.cacheDir, "Filetree.json")
}

func splitGfriendsTarget(value string) (string, string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ""
	}
	if parsed, err := url.Parse(value); err == nil && parsed.Path != "" {
		fileName := path.Base(parsed.Path)
		query := ""
		if parsed.RawQuery != "" {
			query = "?" + parsed.RawQuery
		}
		return fileName, query
	}
	if index := strings.Index(value, "?"); index >= 0 {
		return path.Base(value[:index]), value[index:]
	}
	return path.Base(value), ""
}

// gfriendsVariantStem 去掉 gfriends 给同一演员多张图编的序号（桜空もも-2.jpg）。
// 只用于列举候选：常驻索引那边的匹配规则不动，免得把已有头像认到别人身上。
func gfriendsVariantStem(fileName string) string {
	stem := actorStem(fileName)
	index := strings.LastIndex(stem, "-")
	if index <= 0 || index == len(stem)-1 {
		return stem
	}
	for _, r := range stem[index+1:] {
		if r < '0' || r > '9' {
			return stem
		}
	}
	return stem[:index]
}

func actorStem(fileName string) string {
	fileName, _ = splitGfriendsTarget(fileName)
	fileName = path.Base(filepath.ToSlash(fileName))
	return strings.TrimSuffix(fileName, path.Ext(fileName))
}

func normalizeGfriendsName(value string) string {
	value = model.NormalizeChineseVariants(actorStem(value))
	var builder strings.Builder
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			builder.WriteRune(unicode.ToLower(r))
		}
	}
	return builder.String()
}

func escapeGfriendsPath(value string) string {
	return url.PathEscape(strings.TrimSpace(value))
}
