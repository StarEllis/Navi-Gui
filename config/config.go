package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// AppConfig defines the application configuration.
type AppConfig struct {
	FFprobePath        string
	FFmpegPath         string
	FFprobeConcurrency int
	FFmpegConcurrency  int
}

// CacheConfig defines the cache directory.
type CacheConfig struct {
	CacheDir string
}

// Config is a minimal shim for scanner.go compilation.
type Config struct {
	App   AppConfig
	Cache CacheConfig
}

// NewConfig creates a minimal configuration structure.
func NewConfig() *Config {
	return &Config{
		App: AppConfig{
			FFprobePath:        resolveBinaryPath("NAVI_FFPROBE_PATH", "ffprobe_path", `C:\ffmpeg\bin\ffprobe.exe`, "ffprobe"),
			FFmpegPath:         resolveBinaryPath("NAVI_FFMPEG_PATH", "ffmpeg_path", `C:\ffmpeg\bin\ffmpeg.exe`, "ffmpeg"),
			FFprobeConcurrency: resolvePositiveInt("NAVI_FFPROBE_CONCURRENCY", "ffprobe_concurrency", 2),
			FFmpegConcurrency:  resolvePositiveInt("NAVI_FFMPEG_CONCURRENCY", "ffmpeg_concurrency", 1),
		},
		Cache: CacheConfig{
			CacheDir: DataPath("cache"),
		},
	}
}

func resolvePositiveInt(envKey, yamlKey string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(envKey))
	if value == "" {
		value = strings.TrimSpace(readSimpleYAMLValue(DataPath("app.yaml"), yamlKey))
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return fallback
	}
	return parsed
}

func resolveBinaryPath(envKey string, yamlKey string, fallbackPath string, fallbackName string) string {
	if value := strings.TrimSpace(os.Getenv(envKey)); value != "" {
		return value
	}

	if value := strings.TrimSpace(readSimpleYAMLValue(DataPath("app.yaml"), yamlKey)); value != "" {
		return value
	}

	// 应用自己下载的那份排在自动探测之前：用户点了"下载"就该用这份，
	// 而不是继续用 PATH 里那个可能坏掉的。
	if bundled := BundledBinaryPath(fallbackName); bundled != "" {
		return bundled
	}

	if _, err := os.Stat(fallbackPath); err == nil {
		return fallbackPath
	}

	return fallbackName
}

// FFmpegDir 是应用自己下载的 FFmpeg 的存放位置。
func FFmpegDir() string { return DataPath("ffmpeg") }

// BundledBinaryPath 返回自带的可执行文件路径，没下载过则返回空串。
func BundledBinaryPath(name string) string {
	candidate := filepath.Join(FFmpegDir(), name+".exe")
	if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
		return candidate
	}
	return ""
}

func readSimpleYAMLValue(path string, key string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	prefix := key + ":"
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || !strings.HasPrefix(line, prefix) {
			continue
		}

		value := strings.TrimSpace(strings.TrimPrefix(line, prefix))
		value = strings.Trim(value, `"'`)
		return value
	}

	return ""
}
