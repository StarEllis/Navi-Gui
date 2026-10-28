package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/PuerkitoBio/goquery"
	_ "golang.org/x/image/webp"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// ResolveTrailerURL normalizes provider responses. HLS manifests are returned
// unchanged so callers can hand them to ffmpeg; ordinary non-empty URLs are
// treated as downloadable media (the provider may omit MIME metadata).
func ResolveTrailerURL(raw string) string { return strings.TrimSpace(raw) }

// ParseJavDBAppHLS extracts the current manifest from common App API shapes.
func ParseJavDBAppHLS(b []byte) (string, error) {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return "", err
	}
	var walk func(any, int) string
	walk = func(x any, depth int) string {
		if depth > 8 {
			return ""
		}
		switch z := x.(type) {
		case map[string]any:
			// App responses have changed shape over time.  Only playback-specific
			// fields are accepted; a generic `url` is commonly an artwork URL.
			for _, k := range []string{"hls_url", "m3u8", "play_url", "playback_url", "preview_video_url"} {
				if s, ok := z[k].(string); ok && isHLSURL(s) {
					return strings.TrimSpace(s)
				}
			}
			// Walk known envelopes first, then any explicitly playback-named
			// field in stable order so map iteration cannot select a random URL.
			for _, k := range []string{"data", "result", "response", "video", "preview", "resource"} {
				if child, ok := z[k]; ok {
					if s := walk(child, depth+1); s != "" {
						return s
					}
				}
			}
			keys := make([]string, 0, len(z))
			for k := range z {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				v := z[k]
				lk := strings.ToLower(k)
				if strings.Contains(lk, "hls") || strings.Contains(lk, "m3u8") || strings.Contains(lk, "play") || strings.Contains(lk, "video") {
					if s, ok := v.(string); ok && isHLSURL(s) {
						return strings.TrimSpace(s)
					}
				}
				if s := walk(v, depth+1); s != "" {
					return s
				}
			}
		case []any:
			for _, v := range z {
				if s := walk(v, depth+1); s != "" {
					return s
				}
			}
		}
		return ""
	}
	if s := walk(v, 0); s != "" {
		return s, nil
	}
	return "", fmt.Errorf("hls url not found")
}

// ParseJavDBAppResource handles the mobile API detail envelope. JavDB App is a
// trailer-only source here: its image fields include encrypted covers and
// recommendations, not the preview gallery used by media backfill.
func ParseJavDBAppResource(b []byte) (MediaResource, error) {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return MediaResource{}, err
	}
	var trailers []string
	var walk func(any, string)
	walk = func(x any, key string) {
		switch z := x.(type) {
		case map[string]any:
			for k, value := range z {
				walk(value, strings.ToLower(k))
			}
		case []any:
			for _, value := range z {
				walk(value, key)
			}
		case string:
			u := strings.TrimSpace(z)
			if u == "" {
				return
			}
			if key == "preview_video_url" || key == "sample_movie_url" || key == "hls_url" || key == "m3u8" || key == "play_url" || key == "playback_url" || key == "video_url" {
				if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "//") {
					trailers = append(trailers, u)
				}
				return
			}
		}
	}
	walk(v, "")
	trailer := ""
	for _, candidate := range trailers {
		trailer = preferTrailerURL(trailer, candidate)
	}
	if trailer == "" {
		return MediaResource{}, fmt.Errorf("javdb app returned no downloadable trailer")
	}
	return MediaResource{TrailerURL: trailer}, nil
}

// ParseAioMovieJSON extracts the poster/sample image arrays returned by the
// avmoo-style POST API. Those sites do not expose a trailer URL.
func ParseAioMovieJSON(b []byte) (MediaResource, error) {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return MediaResource{}, err
	}
	var images []string
	var walk func(any, string)
	walk = func(x any, key string) {
		switch z := x.(type) {
		case map[string]any:
			for k, value := range z {
				walk(value, strings.ToLower(k))
			}
		case []any:
			for _, value := range z {
				walk(value, key)
			}
		case string:
			u := strings.TrimSpace(z)
			if (strings.Contains(key, "poster") || strings.Contains(key, "sample") || strings.Contains(key, "image")) && isImageResource(u) {
				images = appendUniqueResource(images, u)
			}
		}
	}
	walk(v, "")
	if len(images) == 0 {
		return MediaResource{}, fmt.Errorf("aio provider returned no preview images")
	}
	return MediaResource{PreviewURLs: images}, nil
}

type MediaResource struct {
	TrailerURL  string
	PreviewURLs []string
	Error       string
}

func ParseJavDBHTML(r io.Reader, base string) (MediaResource, error) {
	d, e := goquery.NewDocumentFromReader(r)
	if e != nil {
		return MediaResource{}, e
	}
	out := MediaResource{}
	d.Find("video source, video, a, [data-src], [data-video]").Each(func(_ int, s *goquery.Selection) {
		for _, attr := range []string{"src", "href", "data-src", "data-video"} {
			if h, ok := s.Attr(attr); ok {
				u := resolveResourceURL(base, h)
				if strings.Contains(strings.ToLower(u), ".m3u8") || strings.HasSuffix(strings.ToLower(strings.Split(u, "?")[0]), ".mp4") {
					out.TrailerURL = preferTrailerURL(out.TrailerURL, u)
				}
			}
		}
	})
	d.Find(".tile-images.preview-images a.tile-item").Each(func(_ int, s *goquery.Selection) {
		for _, attr := range []string{"href", "src", "data-src", "data-video"} {
			if h, ok := s.Attr(attr); ok {
				u := resolveResourceURL(base, h)
				if isImageResource(u) {
					out.PreviewURLs = appendUniqueResource(out.PreviewURLs, u)
				}
			}
		}
	})
	if out.TrailerURL == "" && len(out.PreviewURLs) == 0 {
		return MediaResource{}, fmt.Errorf("provider returned no downloadable resources")
	}
	return out, nil
}

// ParseProviderHTML uses each provider's sample-gallery container. Keeping the
// selectors separate prevents covers and recommendation cards elsewhere on a
// detail page from being mistaken for preview images.
func ParseProviderHTML(provider string, r io.Reader, base string) (MediaResource, error) {
	if provider == "javdb" || provider == "javdb_api" {
		return ParseJavDBHTML(r, base)
	}
	b, err := io.ReadAll(io.LimitReader(r, 16<<20))
	if err != nil {
		return MediaResource{}, err
	}
	d, err := goquery.NewDocumentFromReader(bytes.NewReader(b))
	if err != nil {
		return MediaResource{}, err
	}
	out := MediaResource{}
	d.Find("video source, video, [data-video]").Each(func(_ int, s *goquery.Selection) {
		for _, attr := range []string{"src", "href", "data-src", "data-video"} {
			if raw, ok := s.Attr(attr); ok {
				u := resolveResourceURL(base, raw)
				lower := strings.ToLower(strings.Split(u, "?")[0])
				if strings.HasSuffix(lower, ".mp4") || strings.HasSuffix(lower, ".m3u8") {
					out.TrailerURL = preferTrailerURL(out.TrailerURL, u)
				}
			}
		}
	})
	previewSelector := ".tile-images.preview-images a.tile-item"
	switch provider {
	case "javbus":
		previewSelector = "#sample-waterfall a[href]"
	case "freejavbt":
		previewSelector = "a.tile-item[href]"
	case "iqqtv":
		previewSelector = ".cover img[data-src]"
	case "libredmm":
		previewSelector = "#sample-images a[href]"
	case "avmoo":
		previewSelector = "[class*='sample'] a[href], [class*='preview'] a[href], [class*='sample'] [data-src], [class*='preview'] [data-src]"
	}
	d.Find(previewSelector).Each(func(_ int, s *goquery.Selection) {
		for _, attr := range []string{"href", "src", "data-src", "data-video"} {
			if raw, ok := s.Attr(attr); ok {
				u := resolveResourceURL(base, raw)
				if isImageResource(u) {
					out.PreviewURLs = appendUniqueResource(out.PreviewURLs, u)
				}
			}
		}
	})
	if out.TrailerURL == "" && len(out.PreviewURLs) == 0 {
		return MediaResource{}, fmt.Errorf("provider returned no downloadable resources")
	}
	return out, nil
}

func ParseTheJavDBJSON(b []byte) (MediaResource, error) {
	var v struct {
		SampleMovieURL  string   `json:"sample_movie_url"`
		PreviewVideoURL string   `json:"preview_video_url"`
		HLSURL          string   `json:"hls_url"`
		SampleImages    []string `json:"sample_images"`
		Samples         []string `json:"samples"`
	}
	if e := json.Unmarshal(b, &v); e != nil {
		return MediaResource{}, e
	}
	trailer := v.SampleMovieURL
	if trailer == "" {
		trailer = v.PreviewVideoURL
	}
	if trailer == "" {
		trailer = v.HLSURL
	}
	o := MediaResource{TrailerURL: strings.TrimSpace(trailer)}
	for _, u := range v.SampleImages {
		if isImageResource(u) {
			o.PreviewURLs = appendUniqueResource(o.PreviewURLs, u)
		}
	}
	for _, u := range v.Samples {
		if isImageResource(u) {
			o.PreviewURLs = appendUniqueResource(o.PreviewURLs, u)
		}
	}
	if o.TrailerURL == "" && len(o.PreviewURLs) == 0 {
		return MediaResource{}, fmt.Errorf("provider returned no downloadable resources")
	}
	return o, nil
}

func preferTrailerURL(current, candidate string) string {
	if strings.TrimSpace(current) == "" {
		return strings.TrimSpace(candidate)
	}
	if isHLSURL(current) && !isHLSURL(candidate) {
		return strings.TrimSpace(candidate)
	}
	return current
}

func DownloadResource(ctx context.Context, c *http.Client, raw, dest string, validate func([]byte) error) error {
	return DownloadResourceWithHeaders(ctx, c, raw, dest, validate, nil)
}

func DownloadResourceWithHeaders(ctx context.Context, c *http.Client, raw, dest string, validate func([]byte) error, headers http.Header) error {
	return downloadResourceWithHeaders(ctx, c, raw, dest, validate, headers, false)
}

// DownloadResourceWithHeadersReplacing has the same validation and atomic
// temporary-file behavior as DownloadResourceWithHeaders, but replaces an
// existing destination when the caller explicitly requested overwrite mode.
func DownloadResourceWithHeadersReplacing(ctx context.Context, c *http.Client, raw, dest string, validate func([]byte) error, headers http.Header) error {
	return downloadResourceWithHeaders(ctx, c, raw, dest, validate, headers, true)
}

func downloadResourceWithHeaders(ctx context.Context, c *http.Client, raw, dest string, validate func([]byte) error, headers http.Header, replaceExisting bool) error {
	if c == nil {
		c = http.DefaultClient
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if e != nil {
		return e
	}
	for k, values := range headers {
		for _, value := range values {
			req.Header.Add(k, value)
		}
	}
	resp, e := c.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("resource http %s", resp.Status)
	}
	const maxResourceBytes = 512 << 20
	b, e := io.ReadAll(io.LimitReader(resp.Body, maxResourceBytes+1))
	if e != nil {
		return e
	}
	if len(b) > maxResourceBytes {
		return fmt.Errorf("resource exceeds %d bytes", maxResourceBytes)
	}
	if validate != nil {
		if e = validate(b); e != nil {
			return e
		}
	}
	if e = os.MkdirAll(filepath.Dir(dest), 0755); e != nil {
		return e
	}
	tmpFile, e := os.CreateTemp(filepath.Dir(dest), filepath.Base(dest)+"-*.part")
	if e != nil {
		return e
	}
	tmp := tmpFile.Name()
	defer func() { _ = os.Remove(tmp) }()
	if _, e = tmpFile.Write(b); e != nil {
		_ = tmpFile.Close()
		return e
	}
	if e = tmpFile.Close(); e != nil {
		return e
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if e = os.Rename(tmp, dest); e != nil && replaceExisting {
		if removeErr := os.Remove(dest); removeErr != nil {
			return e
		}
		e = os.Rename(tmp, dest)
	}
	if e != nil {
		return e
	}
	if err := ctx.Err(); err != nil {
		_ = os.Remove(dest)
		return err
	}
	return nil
}

// DownloadTrailer is the shared atomic downloader for MP4 and provider media.
// Existing files are preserved; callers may pass an HLS URL to an ffmpeg-backed
// adapter instead of this byte downloader.
func DownloadTrailer(ctx context.Context, c *http.Client, raw, dest string) error {
	return DownloadTrailerWithOptions(ctx, c, raw, dest, nil, "", "", true)
}

// DownloadTrailerWithOptions carries provider authentication through MP4 and
// HLS requests and lets the app use its configured ffmpeg binaries.
func DownloadTrailerWithOptions(ctx context.Context, c *http.Client, raw, dest string, headers http.Header, ffmpegPath, ffprobePath string, replaceInvalid bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	raw = ResolveTrailerURL(raw)
	if raw == "" {
		return fmt.Errorf("empty trailer url")
	}
	if _, err := os.Stat(dest); err == nil {
		b, e := os.ReadFile(dest)
		if e == nil && validateTrailerBytes(b) == nil {
			return nil
		}
		if !replaceInvalid {
			return fmt.Errorf("existing trailer is invalid")
		}
		if e := os.Remove(dest); e != nil {
			return fmt.Errorf("remove invalid trailer: %w", e)
		}
	}
	if isHLSURL(raw) {
		return DownloadHLSTrailerWithOptions(ctx, c, raw, dest, headers, ffmpegPath, ffprobePath)
	}
	return DownloadResourceWithHeaders(ctx, c, raw, dest, validateTrailerBytes, headers)
}

// IsHLSURL reports whether a provider URL is an HLS manifest. Callers use it
// to defer HLS until direct MP4 candidates have been tried.
func IsHLSURL(raw string) bool { return isHLSURL(raw) }

const minTrailerBytes = 32 * 1024

func validateTrailerBytes(b []byte) error {
	if len(b) < minTrailerBytes {
		return fmt.Errorf("trailer too short")
	}
	trimmed := strings.TrimSpace(strings.ToLower(string(b[:minInt(len(b), 256)])))
	if strings.HasPrefix(trimmed, "<html") || strings.HasPrefix(trimmed, "<!doctype") || strings.Contains(trimmed, "<body") {
		return fmt.Errorf("trailer response is html, not mp4")
	}
	if !bytes.Equal(b[4:8], []byte("ftyp")) {
		return fmt.Errorf("not an mp4")
	}
	for _, x := range b[8:12] {
		if (x < '0' || x > '9') && (x < 'A' || x > 'Z') && (x < 'a' || x > 'z') && x != ' ' {
			return fmt.Errorf("invalid mp4 brand")
		}
	}
	return nil
}

// DownloadHLSTrailer uses ffmpeg and atomically publishes the resulting MP4.
func DownloadHLSTrailer(ctx context.Context, manifest, dest string) error {
	return DownloadHLSTrailerWithOptions(ctx, http.DefaultClient, manifest, dest, nil, "", "")
}

func DownloadHLSTrailerWithOptions(ctx context.Context, c *http.Client, manifest, dest string, headers http.Header, ffmpegPath, ffprobePath string) error {
	if manifest == "" {
		return fmt.Errorf("empty hls manifest")
	}
	if _, err := os.Stat(dest); err == nil {
		b, readErr := os.ReadFile(dest)
		if readErr == nil && validateTrailerBytes(b) == nil {
			return nil
		}
		_ = os.Remove(dest)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	if c == nil {
		c = http.DefaultClient
	}
	if ffmpegPath == "" {
		ffmpegPath, _ = exec.LookPath("ffmpeg")
	}
	if ffmpegPath == "" {
		return fmt.Errorf("ffmpeg not found")
	}
	if ffprobePath == "" {
		ffprobePath, _ = exec.LookPath("ffprobe")
	}
	if ffprobePath == "" {
		return fmt.Errorf("ffprobe not found; refusing to publish unverified HLS output")
	}
	tmpFile, err := os.CreateTemp(filepath.Dir(dest), filepath.Base(dest)+"-*.part")
	if err != nil {
		return err
	}
	tmp := tmpFile.Name()
	_ = tmpFile.Close()
	defer func() { _ = os.Remove(tmp) }()
	localManifest := manifest
	cleanupDir := ""
	if isHTTPURL(manifest) {
		localManifest, cleanupDir, err = stageHLSManifest(ctx, c, manifest, headers)
		if err != nil {
			return err
		}
		defer os.RemoveAll(cleanupDir)
	}
	cmd := exec.CommandContext(ctx, ffmpegPath, "-y", "-protocol_whitelist", "file,crypto,data", "-i", localManifest, "-c", "copy", "-movflags", "+faststart", "-f", "mp4", tmp)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(string(out)))
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if err := os.Rename(tmp, dest); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		_ = os.Remove(dest)
		return err
	}
	probe := exec.CommandContext(ctx, ffprobePath, "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=codec_type", "-of", "csv=p=0", dest)
	if out, err := probe.Output(); err != nil || !strings.Contains(string(out), "video") {
		_ = os.Remove(dest)
		return fmt.Errorf("ffprobe: generated trailer has no video stream")
	}
	if info, err := os.Stat(dest); err != nil || info.Size() < minTrailerBytes {
		_ = os.Remove(dest)
		return fmt.Errorf("generated trailer too short")
	}
	return nil
}

func isHTTPURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && (strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https"))
}

// stageHLSManifest downloads the manifest and its media segments with the
// configured HTTP client.  ffmpeg then consumes local files, avoiding a second
// unauthenticated/proxy-unaware HTTP stack on Windows.
func stageHLSManifest(ctx context.Context, c *http.Client, raw string, headers http.Header) (string, string, error) {
	dir, err := os.MkdirTemp("", "navi-hls-")
	if err != nil {
		return "", "", err
	}
	cleanup := func(e error) (string, string, error) {
		_ = os.RemoveAll(dir)
		return "", "", e
	}
	b, err := readHTTPResource(ctx, c, raw, headers, 16<<20)
	if err != nil {
		return cleanup(err)
	}
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	base, err := url.Parse(raw)
	if err != nil {
		return cleanup(err)
	}
	segmentIndex := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		u, err := url.Parse(trimmed)
		if err != nil {
			return cleanup(fmt.Errorf("invalid HLS segment URL: %w", err))
		}
		segmentURL := base.ResolveReference(u).String()
		segment, err := readHTTPResource(ctx, c, segmentURL, headers, 512<<20)
		if err != nil {
			return cleanup(fmt.Errorf("download HLS segment %q: %w", segmentURL, err))
		}
		name := fmt.Sprintf("segment-%06d.ts", segmentIndex)
		segmentIndex++
		if err := os.WriteFile(filepath.Join(dir, name), segment, 0600); err != nil {
			return cleanup(err)
		}
		lines[i] = name
	}
	if segmentIndex == 0 {
		return cleanup(fmt.Errorf("HLS manifest contains no media segments"))
	}
	manifestPath := filepath.Join(dir, "index.m3u8")
	if err := os.WriteFile(manifestPath, []byte(strings.Join(lines, "\n")), 0600); err != nil {
		return cleanup(err)
	}
	return manifestPath, dir, nil
}

func readHTTPResource(ctx context.Context, c *http.Client, raw string, headers http.Header, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	for k, values := range headers {
		for _, value := range values {
			req.Header.Add(k, value)
		}
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("resource http %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("resource exceeds %d bytes", limit)
	}
	return b, nil
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func resolveResourceURL(base, raw string) string {
	u, e := url.Parse(raw)
	if e != nil {
		return raw
	}
	if u.IsAbs() {
		return u.String()
	}
	b, _ := url.Parse(base)
	return b.ResolveReference(u).String()
}
func isImageResource(u string) bool {
	l := strings.ToLower(strings.Split(u, "?")[0])
	return strings.HasSuffix(l, ".jpg") || strings.HasSuffix(l, ".jpeg") || strings.HasSuffix(l, ".png") || strings.HasSuffix(l, ".webp")
}
func isHLSURL(u string) bool {
	l := strings.ToLower(strings.Split(u, "?")[0])
	return strings.HasPrefix(strings.TrimSpace(u), "http") && strings.HasSuffix(l, ".m3u8")
}

// ValidateImageBytes confirms that a downloaded preview is an actual image.
func ValidateImageBytes(b []byte) error {
	if len(b) == 0 {
		return fmt.Errorf("empty image")
	}
	_, _, err := image.Decode(bytes.NewReader(b))
	return err
}

// DetectImageExtension returns the extension implied by the decoded image
// format, rather than trusting a provider URL's suffix.
func DetectImageExtension(b []byte) (string, error) {
	_, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	switch strings.ToLower(format) {
	case "jpeg":
		return ".jpg", nil
	case "png":
		return ".png", nil
	case "webp":
		return ".webp", nil
	case "gif":
		return ".gif", nil
	default:
		return "", fmt.Errorf("unsupported image format %q", format)
	}
}
func appendUniqueResource(xs []string, v string) []string {
	for _, x := range xs {
		if x == v {
			return xs
		}
	}
	return append(xs, v)
}
