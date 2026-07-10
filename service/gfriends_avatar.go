package service

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"go.uber.org/zap"
	"navi-desktop/model"
)

const (
	gfriendsIndexTTL              = 7 * 24 * time.Hour
	maxGfriendsIndexDownloadBytes = 32 << 20
	maxGfriendsAvatarBytes        = 2 << 20
)

var defaultGfriendsIndexURLs = []string{
	"https://raw.githubusercontent.com/gfriends/gfriends/master/Filetree.json",
	"https://cdn.jsdelivr.net/gh/xinxin8816/gfriends@master/Filetree.json",
}

var defaultGfriendsContentBases = []string{
	"https://raw.githubusercontent.com/gfriends/gfriends/master/Content/",
	"https://cdn.jsdelivr.net/gh/xinxin8816/gfriends@master/Content/",
}

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
		client = &http.Client{Timeout: 20 * time.Second}
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

func (s *GfriendsAvatarService) EnsureActorAvatar(person *model.Person) (bool, error) {
	if s == nil || person == nil || strings.TrimSpace(person.Name) == "" {
		return false, nil
	}
	if fileExists(person.ProfileURL) {
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
	return true, nil
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
		Content map[string]map[string]string `json:"Content"`
	}
	if err := json.Unmarshal(data, &filetree); err != nil {
		return err
	}

	index := make(map[string]AvatarCandidate)
	for studio, files := range filetree.Content {
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

	s.mu.Lock()
	s.index = index
	s.indexLoaded = true
	s.indexStamp = stamp
	s.mu.Unlock()
	return nil
}

func (s *GfriendsAvatarService) downloadCandidate(candidate AvatarCandidate) ([]byte, error) {
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
	if lastErr == nil {
		lastErr = fmt.Errorf("no gfriends content base configured")
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
		return nil, err
	}
	defer resp.Body.Close()
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

func actorStem(fileName string) string {
	fileName, _ = splitGfriendsTarget(fileName)
	fileName = path.Base(filepath.ToSlash(fileName))
	return strings.TrimSuffix(fileName, path.Ext(fileName))
}

func normalizeGfriendsName(value string) string {
	value = actorStem(value)
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
