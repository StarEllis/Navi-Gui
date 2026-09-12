package main

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"navi-desktop/config"
	"navi-desktop/service"
)

// FFmpeg 缺失时应用照常能用，只是拿不到时长/分辨率/字幕轨，也生成不了缩略图。
// 这里做的事情：启动后在后台查一次，查不到就让界面提示，用户点一下把精简版
// （LGPL shared，只要解码和截图，用不上 x264/x265 编码器）下载到 data/ffmpeg。
const (
	ffmpegDownloadURL   = "https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-win64-lgpl-shared.zip"
	ffmpegChecksumURL   = "https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/checksums.sha256"
	ffmpegSetupEvent    = "ffmpeg:setup-progress"
	ffmpegDownloadLimit = 600 * time.Second
	ffmpegChecksumLimit = 45 * time.Second
)

// 下载源：GitHub 直连排第一，后面几个是常用的 GitHub 反向代理，
// 直连不通时依次顶上。
//
// 走代理意味着文件经过第三方的手，所以校验不是可选项：安装包必须对得上
// 官方 checksums.sha256 里的值才会落地，校验值本身也从这条链上取——各源
// 内容一致，任何一个能通就够了。
var ffmpegMirrors = []string{
	"",
	"https://ghfast.top/",
	"https://ghproxy.net/",
	"https://gh-proxy.com/",
}

type downloadSource struct {
	label string
	url   string
}

func ffmpegSources(targetURL string) []downloadSource {
	sources := make([]downloadSource, 0, len(ffmpegMirrors))
	for _, prefix := range ffmpegMirrors {
		if prefix == "" {
			sources = append(sources, downloadSource{label: "GitHub", url: targetURL})
			continue
		}
		label := strings.TrimSuffix(strings.TrimPrefix(prefix, "https://"), "/")
		sources = append(sources, downloadSource{label: label, url: prefix + targetURL})
	}
	return sources
}

// FFmpegStatus 是界面判断"要不要提示"的唯一依据。
type FFmpegStatus struct {
	Available   bool   `json:"available"`
	FFmpegPath  string `json:"ffmpeg_path"`
	FFprobePath string `json:"ffprobe_path"`
	Bundled     bool   `json:"bundled"`
	Downloading bool   `json:"downloading"`
	Supported   bool   `json:"supported"`
}

// FFmpegSetupProgress 通过 ffmpeg:setup-progress 事件推给界面。
type FFmpegSetupProgress struct {
	Phase    string `json:"phase"` // downloading / extracting / verifying / done / failed
	Received int64  `json:"received"`
	Total    int64  `json:"total"`
	Message  string `json:"message"`
}

var ffmpegDownloading atomic.Bool

// GetFFmpegStatus 检查当前配置指向的 ffmpeg/ffprobe 是不是真的能用。
func (a *App) GetFFmpegStatus() FFmpegStatus {
	cfg := a.currentConfig()
	ffmpegPath := resolveExecutable(cfg.App.FFmpegPath)
	ffprobePath := resolveExecutable(cfg.App.FFprobePath)

	status := FFmpegStatus{
		Available:   ffmpegPath != "" && ffprobePath != "",
		FFmpegPath:  ffmpegPath,
		FFprobePath: ffprobePath,
		Downloading: ffmpegDownloading.Load(),
		Supported:   runtime.GOOS == "windows" && runtime.GOARCH == "amd64",
	}
	if ffprobePath != "" {
		status.Bundled = strings.EqualFold(filepath.Dir(ffprobePath), config.FFmpegDir())
	}
	return status
}

// DownloadFFmpeg 下载并解压精简版 FFmpeg，进度走事件推送。
func (a *App) DownloadFFmpeg() error {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		return fmt.Errorf("自动下载只支持 64 位 Windows，请手动安装 FFmpeg")
	}
	if !ffmpegDownloading.CompareAndSwap(false, true) {
		return fmt.Errorf("已经在下载了")
	}

	go func() {
		defer ffmpegDownloading.Store(false)
		if err := a.installFFmpeg(); err != nil {
			if a.logger != nil {
				a.logger.Warnf("ffmpeg setup failed: %v", err)
			}
			a.emitFFmpegProgress(FFmpegSetupProgress{Phase: "failed", Message: err.Error()})
			return
		}
		a.emitFFmpegProgress(FFmpegSetupProgress{Phase: "done", Message: "FFmpeg 已就绪"})
	}()

	return nil
}

func (a *App) installFFmpeg() error {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	targetDir := config.FFmpegDir()
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("创建 %s 失败：%w", targetDir, err)
	}

	archivePath := filepath.Join(targetDir, ".download.tmp")
	defer os.Remove(archivePath)

	// 校验值只有几 KB，先拿到再下 68MB：源全挂的话在这一步就能知道，
	// 不用等下载完才发现没法验。
	a.emitFFmpegProgress(FFmpegSetupProgress{Phase: "verifying", Message: "正在获取校验值"})
	expectedSum, err := fetchFFmpegChecksum(ctx)
	if err != nil {
		return err
	}

	a.emitFFmpegProgress(FFmpegSetupProgress{Phase: "downloading", Message: "正在下载 FFmpeg"})
	sum, err := a.downloadFFmpegArchive(ctx, archivePath)
	if err != nil {
		return err
	}

	a.emitFFmpegProgress(FFmpegSetupProgress{Phase: "verifying", Message: "正在校验安装包"})
	if expectedSum != "" && !strings.EqualFold(expectedSum, sum) {
		return fmt.Errorf("安装包校验不通过，可能下载中断或被篡改，请重试")
	}

	a.emitFFmpegProgress(FFmpegSetupProgress{Phase: "extracting", Message: "正在解压"})
	if err := extractFFmpegArchive(archivePath, targetDir); err != nil {
		return err
	}

	a.emitFFmpegProgress(FFmpegSetupProgress{Phase: "verifying", Message: "正在验证"})
	return a.adoptBundledFFmpeg(ctx)
}

func (a *App) downloadFFmpegArchive(ctx context.Context, archivePath string) (string, error) {
	var lastErr error
	for _, source := range ffmpegSources(ffmpegDownloadTarget()) {
		sum, err := a.downloadFrom(ctx, source, archivePath)
		if err == nil {
			return sum, nil
		}
		if ctx.Err() != nil {
			return "", err
		}
		if a.logger != nil {
			a.logger.Warnf("ffmpeg download from %s failed: %v", source.label, err)
		}
		lastErr = err
	}
	return "", fmt.Errorf("所有下载源都失败了（检查网络或代理）：%w", lastErr)
}

func (a *App) downloadFrom(ctx context.Context, source downloadSource, archivePath string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source.url, nil)
	if err != nil {
		return "", err
	}
	response, err := service.NewHTTPClient(ffmpegDownloadLimit).Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("服务器返回 %s", response.Status)
	}

	file, err := os.Create(archivePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	message := "正在下载 FFmpeg"
	if source.label != "GitHub" {
		message = "正在下载 FFmpeg（备用源 " + source.label + "）"
	}
	hasher := sha256.New()
	counter := &progressWriter{
		total: response.ContentLength,
		report: func(received, total int64) {
			a.emitFFmpegProgress(FFmpegSetupProgress{
				Phase:    "downloading",
				Received: received,
				Total:    total,
				Message:  message,
			})
		},
	}
	if _, err := io.Copy(io.MultiWriter(file, hasher, counter), response.Body); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func ffmpegDownloadTarget() string {
	if custom := strings.TrimSpace(os.Getenv("NAVI_FFMPEG_DOWNLOAD_URL")); custom != "" {
		return custom
	}
	return ffmpegDownloadURL
}

// fetchFFmpegChecksum 取官方发布的 SHA256。自定义下载源没有对应的校验值，
// 返回空串表示跳过比对——那是用户自己指定的地址，由用户自己负责。
func fetchFFmpegChecksum(ctx context.Context) (string, error) {
	if strings.TrimSpace(os.Getenv("NAVI_FFMPEG_DOWNLOAD_URL")) != "" {
		return "", nil
	}

	wantedFile := path.Base(ffmpegDownloadURL)
	var lastErr error
	for _, source := range ffmpegSources(ffmpegChecksumURL) {
		sum, err := readChecksumFrom(ctx, source.url, wantedFile)
		if err == nil {
			return sum, nil
		}
		if ctx.Err() != nil {
			return "", err
		}
		lastErr = err
	}
	return "", fmt.Errorf("取不到安装包校验值，已中止安装（检查网络或代理）：%w", lastErr)
}

func readChecksumFrom(ctx context.Context, url, wantedFile string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	response, err := service.NewHTTPClient(ffmpegChecksumLimit).Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("服务器返回 %s", response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", err
	}

	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if strings.TrimPrefix(fields[1], "*") == wantedFile {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("校验文件里没有 %s", wantedFile)
}

// extractFFmpegArchive 只取 bin/ 下的 exe 和 dll，并且丢掉 ffplay
// （播放器界面，这个应用用不上，能省十几 MB）。
func extractFFmpegArchive(archivePath, targetDir string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("安装包损坏：%w", err)
	}
	defer reader.Close()

	extracted := 0
	for _, entry := range reader.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		name := path.Base(entry.Name)
		if path.Base(path.Dir(entry.Name)) != "bin" {
			continue
		}
		lowered := strings.ToLower(name)
		if lowered == "ffplay.exe" {
			continue
		}
		if !strings.HasSuffix(lowered, ".exe") && !strings.HasSuffix(lowered, ".dll") {
			continue
		}
		if err := writeZipEntry(entry, filepath.Join(targetDir, name)); err != nil {
			return err
		}
		extracted++
	}
	if extracted == 0 {
		return fmt.Errorf("安装包里没有找到 FFmpeg 程序")
	}
	return nil
}

func writeZipEntry(entry *zip.File, targetPath string) error {
	source, err := entry.Open()
	if err != nil {
		return err
	}
	defer source.Close()

	temporaryPath := targetPath + ".part"
	_ = os.Remove(temporaryPath)
	target, err := os.OpenFile(temporaryPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(target, source); err != nil {
		target.Close()
		os.Remove(temporaryPath)
		return err
	}
	if err := target.Close(); err != nil {
		os.Remove(temporaryPath)
		return err
	}
	// Windows 上覆盖正在运行的 exe 会失败，先挪开再换上。
	if _, err := os.Stat(targetPath); err == nil {
		_ = os.Remove(targetPath + ".old")
		_ = os.Rename(targetPath, targetPath+".old")
		defer os.Remove(targetPath + ".old")
	}
	return os.Rename(temporaryPath, targetPath)
}

// adoptBundledFFmpeg 真的跑一次 ffprobe -version，通过了才把路径切过去，
// 这样界面上的"已就绪"不是猜的。
func (a *App) adoptBundledFFmpeg(ctx context.Context) error {
	ffmpegPath := config.BundledBinaryPath("ffmpeg")
	ffprobePath := config.BundledBinaryPath("ffprobe")
	if ffmpegPath == "" || ffprobePath == "" {
		return fmt.Errorf("解压后没有找到 ffmpeg.exe / ffprobe.exe")
	}

	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(checkCtx, ffprobePath, "-version")
	configureDetachedCommand(command)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("下载的 FFmpeg 无法运行：%w（%s）", err, strings.TrimSpace(firstLine(string(output))))
	}

	cfg := a.currentConfig()
	cfg.App.FFmpegPath = ffmpegPath
	cfg.App.FFprobePath = ffprobePath
	if a.logger != nil {
		a.logger.Infof("ffmpeg ready: %s", ffprobePath)
	}
	return nil
}

func (a *App) emitFFmpegProgress(progress FFmpegSetupProgress) {
	if a == nil || a.ctx == nil {
		return
	}
	wailsRuntime.EventsEmit(a.ctx, ffmpegSetupEvent, progress)
}

func (a *App) currentConfig() *config.Config {
	if a != nil && a.cfg != nil {
		return a.cfg
	}
	return config.NewConfig()
}

// resolveExecutable 把配置里的值变成"确实存在的路径"，不存在就返回空串。
// 裸名字（比如 "ffprobe"）会去 PATH 里找。
func resolveExecutable(candidate string) string {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return ""
	}
	if strings.ContainsAny(candidate, `/\`) {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
		return ""
	}
	resolved, err := exec.LookPath(candidate)
	if err != nil {
		return ""
	}
	return resolved
}

func firstLine(text string) string {
	if index := strings.IndexAny(text, "\r\n"); index >= 0 {
		return text[:index]
	}
	return text
}

type progressWriter struct {
	received   int64
	total      int64
	lastReport time.Time
	report     func(received, total int64)
}

func (w *progressWriter) Write(data []byte) (int, error) {
	w.received += int64(len(data))
	// 68MB 的下载不需要每个 32KB 块都通知界面一次。
	if time.Since(w.lastReport) >= 200*time.Millisecond {
		w.lastReport = time.Now()
		if w.report != nil {
			w.report(w.received, w.total)
		}
	}
	return len(data), nil
}
