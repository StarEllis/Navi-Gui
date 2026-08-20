package service

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
)

const maxImportedAvatarBytes = 16 << 20

// ActorAvatarStore keeps the avatars a user picked by hand. They deliberately
// live outside the artwork cache: that cache evicts by capacity, and while a
// fetched avatar can always be downloaded again, an imported one cannot — the
// file it came from may be long deleted.
//
// Entries are keyed by actor name rather than person id. An overwrite rescan
// wipes the media rows and with them the people, so the rebuilt library hands
// the same actor a brand new id — a hand-picked avatar must outlive that.
type ActorAvatarStore struct {
	dir    string
	client *http.Client
	logger *zap.SugaredLogger
}

func NewActorAvatarStore(cacheDir string, logger *zap.SugaredLogger) *ActorAvatarStore {
	cacheDir = strings.TrimSpace(cacheDir)
	if cacheDir == "" {
		cacheDir = "cache"
	}
	return &ActorAvatarStore{
		dir:    filepath.Join(cacheDir, "actors"),
		client: NewHTTPClient(20 * time.Second),
		logger: logger,
	}
}

// ImportFile copies a local image into the store. The stored copy is what the
// library uses from then on, so the source file can be deleted afterwards.
func (s *ActorAvatarStore) ImportFile(actorName, sourcePath string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("演员头像存储不可用")
	}
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return "", fmt.Errorf("没有选择图片")
	}
	file, err := os.Open(sourcePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxImportedAvatarBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxImportedAvatarBytes {
		return "", fmt.Errorf("图片过大，请控制在 %d MB 以内", maxImportedAvatarBytes>>20)
	}
	return s.store(actorName, data)
}

// ImportURL fetches an image address the user pasted — the practical answer for
// actors the avatar index does not cover.
func (s *ActorAvatarStore) ImportURL(actorName, imageURL string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("演员头像存储不可用")
	}
	imageURL = strings.TrimSpace(imageURL)
	if !strings.HasPrefix(imageURL, "http://") && !strings.HasPrefix(imageURL, "https://") {
		return "", fmt.Errorf("请输入 http 或 https 开头的图片链接")
	}

	req, err := http.NewRequest(http.MethodGet, imageURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "navi-desktop-actor-avatar/1.0")
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("下载失败：HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImportedAvatarBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxImportedAvatarBytes {
		return "", fmt.Errorf("图片过大，请控制在 %d MB 以内", maxImportedAvatarBytes>>20)
	}
	if err := validateGfriendsImageContent(resp.Header.Get("Content-Type"), data); err != nil {
		return "", fmt.Errorf("链接指向的不是图片：%w", err)
	}
	return s.store(actorName, data)
}

// Remove drops the stored avatar for a person, if this store owns one.
func (s *ActorAvatarStore) Remove(actorName string) error {
	if s == nil {
		return nil
	}
	dir := s.actorDir(actorName)
	if dir == "" {
		return nil
	}
	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Lookup returns the stored avatar for an actor, if one was ever imported. This
// is what puts hand-picked avatars back after a rescan rebuilt the people rows.
func (s *ActorAvatarStore) Lookup(actorName string) string {
	if s == nil {
		return ""
	}
	dir := s.actorDir(actorName)
	if dir == "" {
		return ""
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".jpg") {
			return filepath.Join(dir, entry.Name())
		}
	}
	return ""
}

// Adopt moves an entry filed under an old key to this actor's name, so avatars
// imported before names became the key are not stranded.
func (s *ActorAvatarStore) Adopt(oldKey, actorName string) (string, error) {
	if s == nil {
		return "", nil
	}
	oldDir := filepath.Join(s.dir, safeArtworkName(strings.TrimSpace(oldKey)))
	newDir := s.actorDir(actorName)
	if newDir == "" || oldDir == newDir || !dirExists(oldDir) {
		return "", nil
	}
	if dirExists(newDir) {
		// The actor already has a current avatar; the stale entry just goes.
		return "", os.RemoveAll(oldDir)
	}
	if err := os.MkdirAll(filepath.Dir(newDir), 0755); err != nil {
		return "", err
	}
	if err := os.Rename(oldDir, newDir); err != nil {
		return "", err
	}
	return s.Lookup(actorName), nil
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// Owns reports whether a profile URL points into this store.
func (s *ActorAvatarStore) Owns(path string) bool {
	if s == nil || strings.TrimSpace(path) == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(s.dir), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel != "." && rel != "" && !strings.HasPrefix(rel, "..")
}

func (s *ActorAvatarStore) store(actorName string, data []byte) (string, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("无法识别的图片格式：%w", err)
	}
	dir := s.actorDir(actorName)
	if dir == "" {
		return "", fmt.Errorf("演员名为空")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}

	// The file name carries the content hash so replacing an avatar produces a
	// new path — the UI would otherwise keep showing the previous image.
	outputPath := filepath.Join(dir, fmt.Sprintf("%s.jpg", shortSHA1(data)))
	temp, err := os.CreateTemp(dir, ".navi-avatar-*.part")
	if err != nil {
		return "", err
	}
	tempPath := temp.Name()
	committed := false
	defer func() {
		_ = temp.Close()
		if !committed {
			_ = os.Remove(tempPath)
		}
	}()

	if err := jpeg.Encode(temp, resizeImageToFit(img, artworkActorMaxWidth, artworkActorMaxHeight), &jpeg.Options{Quality: artworkJPEGQuality}); err != nil {
		return "", err
	}
	if err := temp.Sync(); err != nil {
		return "", err
	}
	if err := temp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tempPath, outputPath); err != nil {
		return "", err
	}
	committed = true

	s.removeOtherAvatars(dir, filepath.Base(outputPath))
	return outputPath, nil
}

func (s *ActorAvatarStore) removeOtherAvatars(dir, keep string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == keep {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && s.logger != nil {
			s.logger.Debugf("remove replaced actor avatar failed: path=%s err=%v", filepath.Join(dir, entry.Name()), err)
		}
	}
}

// actorDir keys by the same normalization the avatar index uses, so 繁体/日文
// spellings of one actor share a single entry. The normalized name keeps only
// letters and digits, which is already a safe directory name — running it
// through safeArtworkName instead would blank out every CJK name.
func (s *ActorAvatarStore) actorDir(actorName string) string {
	key := normalizeGfriendsName(actorName)
	if key == "" {
		return ""
	}
	if len(key) > 80 {
		key = shortSHA1([]byte(key))
	}
	return filepath.Join(s.dir, key)
}
