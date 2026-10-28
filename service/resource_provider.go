package service

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"github.com/PuerkitoBio/goquery"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ResourceQuery identifies a title without coupling providers to the database model.
type ResourceQuery struct {
	Code       string
	Title      string
	UseBuiltIn bool
}
type ResourceCandidate struct {
	Provider, ExternalID, Number, DetailURL string
	MatchScore                              float64
}
type ResolvedResources struct {
	Provider, Number string
	Trailer          *TrailerResource
	PreviewImages    []ImageResource
}
type TrailerResource struct{ URL, Format string }
type ImageResource struct {
	URL       string
	SortOrder int
}

type resourceProviderSpec struct {
	name, env, cookie, kind, defaultBase string
}

var resourceProviderSpecs = []resourceProviderSpec{
	{name: "thejavdb_api", env: "NAVI_THEJAVDB_API_URL", cookie: "NAVI_THEJAVDB_COOKIE", kind: "thejavdb_api", defaultBase: "https://api.thejavdb.net"},
	{name: "javdb_app", env: "NAVI_JAVDB_APP_URL", cookie: "NAVI_JAVDB_COOKIE", kind: "javdb_app", defaultBase: "https://apidd.czssdgz.com"},
	{name: "javdb_api", env: "NAVI_JAVDB_API_URL", cookie: "NAVI_JAVDB_COOKIE", kind: "javdb_api", defaultBase: "https://javdb573.com"},
	{name: "javdb", env: "NAVI_JAVDB_URL", cookie: "NAVI_JAVDB_COOKIE", kind: "javdb", defaultBase: "https://javdb573.com"},
	{name: "javbus", env: "NAVI_JAVBUS_URL", cookie: "NAVI_JAVBUS_COOKIE", kind: "javbus", defaultBase: "https://www.dmmsee.cyou"},
	{name: "avmoo", env: "NAVI_AVMOO_URL", cookie: "NAVI_AVMOO_COOKIE", kind: "avmoo", defaultBase: "https://avmoo.shop"},
	{name: "freejavbt", env: "NAVI_FREEJAVBT_URL", cookie: "NAVI_FREEJAVBT_COOKIE", kind: "freejavbt", defaultBase: "https://freejavbt22.cc"},
	{name: "iqqtv", env: "NAVI_IQQTV_URL", cookie: "NAVI_IQQTV_COOKIE", kind: "iqqtv", defaultBase: "https://iqq5.xyz"},
	{name: "libredmm", env: "NAVI_LIBREDMM_URL", cookie: "NAVI_LIBREDMM_COOKIE", kind: "libredmm", defaultBase: "https://www.libredmm.com"},
}

const providerDiscoveryTimeout = 10 * time.Second

// SearchConfiguredProviders queries the built-in provider routes plus any user
// overrides. A custom URL may still be a simple {code}/{title} template, which
// preserves the old integration and makes local test servers easy to use.
func SearchConfiguredProviders(ctx context.Context, q ResourceQuery) ([]ResourceCandidate, error) {
	return searchConfiguredProviders(ctx, q, false)
}

// SearchFirstConfiguredProvider returns as soon as any provider yields a strict
// match. Outstanding requests are canceled; the actual backfill performs a
// full ordered discovery so later providers remain available as fallbacks.
func SearchFirstConfiguredProvider(ctx context.Context, q ResourceQuery) ([]ResourceCandidate, error) {
	return searchConfiguredProviders(ctx, q, true)
}

func searchConfiguredProviders(ctx context.Context, q ResourceQuery, firstOnly bool) ([]ResourceCandidate, error) {
	code := strings.TrimSpace(q.Code)
	if code == "" {
		return nil, fmt.Errorf("media code is empty")
	}
	queryCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type providerResult struct {
		index      int
		name       string
		candidates []ResourceCandidate
		err        error
	}
	results := make(chan providerResult, len(resourceProviderSpecs))
	pending := 0
	for index, p := range resourceProviderSpecs {
		template := strings.TrimSpace(os.Getenv(p.env))
		if template == "" && p.name == "thejavdb_api" {
			template = strings.TrimSpace(os.Getenv("NAVI_THEJAVDB_URL"))
		}
		if template == "" && p.name == "javdb_app" {
			template = strings.TrimSpace(os.Getenv("NAVI_JAVDB_APP_URL"))
		}
		if template == "" && !q.UseBuiltIn {
			continue
		}
		if template == "" {
			template = p.defaultBase
		}
		pending++
		go func(index int, p resourceProviderSpec, template string) {
			providerCtx, cancel := context.WithTimeout(queryCtx, providerDiscoveryTimeout)
			defer cancel()
			var candidates []ResourceCandidate
			var err error
			if strings.Contains(template, "{code}") || strings.Contains(template, "{title}") {
				candidates, err = searchResourceTemplate(providerCtx, p, template, q)
			} else {
				candidates, err = searchResourceProvider(providerCtx, p, template, q)
			}
			results <- providerResult{index: index, name: p.name, candidates: candidates, err: err}
		}(index, p, template)
	}
	if firstOnly {
		var errs []string
		for i := 0; i < pending; i++ {
			result := <-results
			if result.err == nil && len(result.candidates) > 0 {
				cancel()
				return result.candidates, nil
			}
			if result.err != nil {
				errs = append(errs, result.name+": "+result.err.Error())
			}
		}
		if len(errs) > 0 {
			return nil, fmt.Errorf("no resource provider succeeded: %s", strings.Join(errs, "; "))
		}
		return nil, nil
	}
	ordered := make([]providerResult, len(resourceProviderSpecs))
	for i := 0; i < pending; i++ {
		result := <-results
		ordered[result.index] = result
	}
	var out []ResourceCandidate
	var errs []string
	for _, result := range ordered {
		if result.name == "" {
			continue
		}
		if result.err != nil {
			errs = append(errs, result.name+": "+result.err.Error())
			continue
		}
		out = append(out, result.candidates...)
	}
	if len(out) == 0 && len(errs) > 0 {
		return nil, fmt.Errorf("no resource provider succeeded: %s", strings.Join(errs, "; "))
	}
	return out, nil
}

func searchResourceTemplate(ctx context.Context, p resourceProviderSpec, template string, q ResourceQuery) ([]ResourceCandidate, error) {
	code := strings.TrimSpace(q.Code)
	u := strings.ReplaceAll(template, "{code}", url.QueryEscape(code))
	u = strings.ReplaceAll(u, "{title}", url.QueryEscape(strings.TrimSpace(q.Title)))
	body, contentType, status, err := doProviderRequest(ctx, http.MethodGet, u, nil, providerRequestHeaders(p, u), nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, providerHTTPError(status, p.name)
	}
	return candidateFromResponse(p.name, u, body, contentType, code)
}

func searchResourceProvider(ctx context.Context, p resourceProviderSpec, base string, q ResourceQuery) ([]ResourceCandidate, error) {
	base = strings.TrimRight(base, "/")
	code := strings.TrimSpace(q.Code)
	switch p.kind {
	case "thejavdb_api":
		u := appendQuery(base+"/v1/movies", "q", code)
		body, contentType, status, err := doProviderRequest(ctx, http.MethodGet, u, nil, providerRequestHeaders(p, u), nil)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, providerHTTPError(status, p.name)
		}
		return candidateFromResponse(p.name, u, body, contentType, code)
	case "javdb_app":
		return searchJavDBApp(ctx, p, base, code)
	case "javdb_api", "javdb":
		var lastErr error
		for _, candidateBase := range providerBaseCandidates(p, base) {
			u := appendQuery(candidateBase+"/search", "f", "all")
			u = appendQuery(u, "q", code)
			u = appendQuery(u, "page", "1")
			body, contentType, status, err := doProviderRequest(ctx, http.MethodGet, u, nil, providerRequestHeaders(p, u), nil)
			if err != nil {
				lastErr = err
				continue
			}
			if status < 200 || status >= 300 {
				lastErr = providerHTTPError(status, p.name)
				continue
			}
			if !strings.Contains(strings.ToLower(contentType), "html") && !bytes.Contains(bytes.ToLower(body), []byte("<html")) {
				return candidateFromResponse(p.name, u, body, contentType, code)
			}
			detail, ok := findMatchingDetailURL(body, u, code)
			if ok {
				return []ResourceCandidate{{Provider: p.name, Number: code, DetailURL: detail, MatchScore: 1}}, nil
			}
			lastErr = fmt.Errorf("no exact match")
		}
		return nil, lastErr
	case "javbus":
		var lastErr error
		for _, candidateBase := range providerBaseCandidates(p, base) {
			searchURLs := []string{
				candidateBase + "/" + url.PathEscape(code),
				candidateBase + "/search/" + url.PathEscape(code) + "&type=&parent=ce",
				candidateBase + "/uncensored/search/" + url.PathEscape(code) + "&type=0&parent=uc",
			}
			for _, u := range searchURLs {
				body, _, status, err := doProviderRequest(ctx, http.MethodGet, u, nil, providerRequestHeaders(p, u), nil)
				if err != nil {
					lastErr = err
					continue
				}
				if status < 200 || status >= 300 {
					lastErr = providerHTTPError(status, p.name)
					continue
				}
				if bytes.Contains(bytes.ToLower(body), []byte("driver-verify")) || bytes.Contains(bytes.ToLower(body), []byte("age verification")) {
					lastErr = fmt.Errorf("login_required: javbus age verification or cookie required")
					continue
				}
				if matched, _ := responseMatchesCode(body, "text/html", code); matched && !strings.Contains(u, "/search/") && !strings.Contains(u, "/uncensored/") {
					return []ResourceCandidate{{Provider: p.name, Number: code, DetailURL: u, MatchScore: 1}}, nil
				}
				if detail, ok := findMatchingDetailURL(body, u, code); ok {
					return []ResourceCandidate{{Provider: p.name, Number: code, DetailURL: detail, MatchScore: 1}}, nil
				}
				lastErr = fmt.Errorf("no exact match")
			}
		}
		return nil, lastErr
	case "avmoo":
		return searchAioProvider(ctx, p, base, code)
	case "freejavbt":
		var lastErr error
		for _, candidateBase := range providerBaseCandidates(p, base) {
			u := candidateBase + "/" + url.PathEscape(code)
			body, contentType, status, err := doProviderRequest(ctx, http.MethodGet, u, nil, providerRequestHeaders(p, u), nil)
			if err != nil {
				lastErr = err
				continue
			}
			if status < 200 || status >= 300 {
				lastErr = providerHTTPError(status, p.name)
				continue
			}
			matched, _ := responseMatchesCode(body, contentType, code)
			if matched || bytes.Contains(bytes.ToUpper(body), []byte(strings.ToUpper(code))) {
				return []ResourceCandidate{{Provider: p.name, Number: code, DetailURL: u, MatchScore: 1}}, nil
			}
			lastErr = fmt.Errorf("no exact match")
		}
		return nil, lastErr
	case "iqqtv":
		var lastErr error
		for _, candidateBase := range providerBaseCandidates(p, base) {
			u := candidateBase + "/cn/search.php?kw=" + url.QueryEscape(code)
			body, _, status, err := doProviderRequest(ctx, http.MethodGet, u, nil, providerRequestHeaders(p, u), nil)
			if err != nil {
				lastErr = err
				continue
			}
			if status < 200 || status >= 300 {
				lastErr = providerHTTPError(status, p.name)
				continue
			}
			if detail, ok := findMatchingDetailURL(body, u, code); ok {
				return []ResourceCandidate{{Provider: p.name, Number: code, DetailURL: detail, MatchScore: 1}}, nil
			}
			// Some mirrors render the matching player directly on the search page.
			if bytes.Contains(bytes.ToUpper(body), []byte(strings.ToUpper(code))) {
				return []ResourceCandidate{{Provider: p.name, Number: code, DetailURL: u, MatchScore: 0.9}}, nil
			}
			lastErr = fmt.Errorf("no exact match")
		}
		return nil, lastErr
	case "libredmm":
		u := base + "/movies/" + url.PathEscape(code)
		body, _, status, err := doProviderRequest(ctx, http.MethodGet, u, nil, providerRequestHeaders(p, u), nil)
		if err != nil {
			return nil, err
		}
		if status == http.StatusNotFound {
			u = appendQuery(base+"/search", "q", code)
			body, _, status, err = doProviderRequest(ctx, http.MethodGet, u, nil, providerRequestHeaders(p, u), nil)
		}
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, providerHTTPError(status, p.name)
		}
		if detail, ok := findMatchingDetailURL(body, u, code); ok && detail != u {
			u = detail
		}
		return []ResourceCandidate{{Provider: p.name, Number: code, DetailURL: u, MatchScore: 1}}, nil
	default:
		return nil, fmt.Errorf("unsupported provider")
	}
}

func searchJavDBApp(ctx context.Context, p resourceProviderSpec, base, code string) ([]ResourceCandidate, error) {
	params := javDBAppParams(map[string]string{"q": code, "page": "1", "type": "movie", "limit": "50"})
	searchURL := base + "/api/v2/search?" + params.Encode()
	body, contentType, status, err := doProviderRequest(ctx, http.MethodGet, searchURL, nil, providerRequestHeaders(p, searchURL), nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, providerHTTPError(status, p.name)
	}
	var envelope struct {
		Data struct {
			Movies []struct {
				ID     string `json:"id"`
				Number string `json:"number"`
			} `json:"movies"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("invalid javdb_app response: %w", err)
	}
	want := normalizeProviderCode(code)
	for _, movie := range envelope.Data.Movies {
		if normalizeProviderCode(movie.Number) != want || strings.TrimSpace(movie.ID) == "" {
			continue
		}
		detailParams := javDBAppParams(nil)
		detailURL := base + "/api/v4/movies/" + url.PathEscape(movie.ID) + "?" + detailParams.Encode()
		return []ResourceCandidate{{Provider: p.name, ExternalID: movie.ID, Number: movie.Number, DetailURL: detailURL, MatchScore: 1}}, nil
	}
	_ = contentType
	return nil, fmt.Errorf("no exact match")
}

func searchAioProvider(ctx context.Context, p resourceProviderSpec, base, code string) ([]ResourceCandidate, error) {
	searchURL := base + "/jav/data/api/search"
	payload, _ := json.Marshal([]any{map[string]string{"search": code, "lang": "cn"}, 60, 1})
	body, _, status, err := doProviderRequest(ctx, http.MethodPost, searchURL, payload, providerRequestHeaders(p, searchURL), map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, providerHTTPError(status, p.name)
	}
	var envelope struct {
		Data []struct {
			ID     any    `json:"movieId"`
			Number string `json:"movieFanHao"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("invalid avmoo response: %w", err)
	}
	for _, movie := range envelope.Data {
		if normalizeProviderCode(movie.Number) != normalizeProviderCode(code) {
			continue
		}
		id := fmt.Sprint(movie.ID)
		detailURL := base + "/jav/data/api/getMovie?movieId=" + url.QueryEscape(id)
		return []ResourceCandidate{{Provider: p.name, ExternalID: id, Number: movie.Number, DetailURL: detailURL, MatchScore: 1}}, nil
	}
	return nil, fmt.Errorf("no exact match")
}

func candidateFromResponse(provider, detailURL string, body []byte, contentType, code string) ([]ResourceCandidate, error) {
	cand := ResourceCandidate{Provider: provider, Number: code, DetailURL: detailURL, MatchScore: 0.8}
	matched, explicit := responseMatchesCode(body, contentType, code)
	if explicit && !matched {
		return nil, fmt.Errorf("no exact match")
	}
	if !strings.Contains(strings.ToLower(contentType), "json") && !bytes.HasPrefix(bytes.TrimSpace(body), []byte("{")) && !bytes.HasPrefix(bytes.TrimSpace(body), []byte("[")) {
		if detail, ok := findMatchingDetailURL(body, detailURL, code); ok {
			cand.DetailURL, matched = detail, true
		}
		if !matched {
			return nil, fmt.Errorf("no exact match")
		}
	}
	if matched {
		cand.MatchScore = 1
	}
	var obj any
	if json.Unmarshal(body, &obj) == nil {
		cand.ExternalID = firstIdentityValue(obj)
	}
	return []ResourceCandidate{cand}, nil
}

func doProviderRequest(ctx context.Context, method, rawURL string, body []byte, headers http.Header, extra map[string]string) ([]byte, string, int, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, "", 0, err
	}
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	for key, value := range extra {
		req.Header.Set(key, value)
	}
	resp, err := NewHTTPClient(30 * time.Second).Do(req)
	if err != nil {
		return nil, "", 0, err
	}
	b, readErr := readLimited(resp.Body, 16<<20)
	resp.Body.Close()
	return b, resp.Header.Get("content-type"), resp.StatusCode, readErr
}

func providerHTTPError(status int, provider string) error {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return fmt.Errorf("login_required: http %s", http.StatusText(status))
	}
	return fmt.Errorf("%s: http %s", provider, http.StatusText(status))
}

func providerBaseCandidates(p resourceProviderSpec, base string) []string {
	base = strings.TrimRight(base, "/")
	if base != strings.TrimRight(p.defaultBase, "/") {
		return []string{base}
	}
	switch p.kind {
	case "javdb_api", "javdb":
		return []string{base, "https://javdb574.com", "https://javdb575.com"}
	case "javbus":
		return []string{base, "https://www.javsee.cyou", "https://www.cdnbus.bond", "https://www.fanbus.bond", "https://www.dmmbus.bond", "https://www.javbus.com"}
	case "freejavbt":
		return []string{base, "https://freejavbt.com"}
	case "iqqtv":
		return []string{base, "https://iqqk4.quest"}
	default:
		return []string{base}
	}
}

func providerRequestHeaders(p resourceProviderSpec, referer string) http.Header {
	headers := make(http.Header)
	headers.Set("Accept", "application/json, text/html")
	headers.Set("Referer", referer)
	if cookie := strings.TrimSpace(os.Getenv(p.cookie)); cookie != "" {
		headers.Set("Cookie", cookie)
	}
	if p.kind == "javdb_app" {
		for key, values := range JavDBAppHeaders() {
			for _, value := range values {
				headers.Set(key, value)
			}
		}
	}
	return headers
}

// JavDBAppHeaders creates the short-lived header used by the mobile API. The
// constants intentionally mirror the reference project's documented APK flow;
// environment overrides let a future app-version rotation be fixed without a
// binary rebuild.
func JavDBAppHeaders() http.Header {
	prefix := strings.TrimSpace(os.Getenv("NAVI_JAVDB_APP_SIG_PREFIX"))
	if prefix == "" {
		prefix = "71cf27bb3c0bcdf207b64abecddc970098c7421ee7203b9cdae54478478a199e7d5a6e1a57691123c1a931c057842fb73ba3b3c83bcd69c17ccf174081e3d8aa"
	}
	suffix := strings.TrimSpace(os.Getenv("NAVI_JAVDB_APP_SIG_SUFFIX"))
	if suffix == "" {
		suffix = "lpw6vgqzsp"
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	digest := md5.Sum([]byte(ts + prefix))
	signature := fmt.Sprintf("%s.%s.%x", ts, suffix, digest)
	h := make(http.Header)
	h.Set("jdsignature", signature)
	h.Set("Accept-Language", "zh")
	h.Set("User-Agent", "Dart/3.5 (dart:io)")
	return h
}

func javDBAppParams(extra map[string]string) url.Values {
	v := url.Values{}
	v.Set("platform", "android")
	v.Set("app_channel", "official")
	v.Set("app_version", "official")
	version := strings.TrimSpace(os.Getenv("NAVI_JAVDB_APP_VERSION_NUMBER"))
	if version == "" {
		version = "1.9.35"
	}
	v.Set("app_version_number", version)
	v.Set("system_version", "13")
	v.Set("device_model", "Pixel 6")
	v.Set("device_name", "Pixel")
	v.Set("device_uuid", "6e2c33d0-6ed4-56e0-99de-4da1acb56e72")
	for key, value := range extra {
		v.Set(key, value)
	}
	return v
}

func appendQuery(rawURL, key, value string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	q.Set(key, value)
	u.RawQuery = q.Encode()
	return u.String()
}

func normalizeProviderCode(value string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(value)) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func firstIdentityValue(v any) string {
	var walk func(any) string
	walk = func(x any) string {
		switch z := x.(type) {
		case map[string]any:
			keys := make([]string, 0, len(z))
			for k := range z {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				lk := strings.ToLower(k)
				if strings.Contains(lk, "code") || strings.Contains(lk, "number") || strings.Contains(lk, "universal_id") || strings.Contains(lk, "external_id") || strings.Contains(lk, "movie_id") || lk == "public_id" {
					if s, ok := z[k].(string); ok && strings.TrimSpace(s) != "" {
						return strings.TrimSpace(s)
					}
				}
			}
			if s, ok := z["id"].(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
			for _, k := range keys {
				if s := walk(z[k]); s != "" {
					return s
				}
			}
		case []any:
			for _, child := range z {
				if s := walk(child); s != "" {
					return s
				}
			}
		}
		return ""
	}
	return walk(v)
}

func responseMatchesCode(body []byte, contentType, code string) (matched, explicit bool) {
	want := normalizeProviderCode(code)
	if strings.Contains(strings.ToLower(contentType), "json") {
		var v any
		if json.Unmarshal(body, &v) == nil {
			var values []string
			var walk func(any)
			walk = func(x any) {
				switch z := x.(type) {
				case map[string]any:
					for k, value := range z {
						lk := strings.ToLower(k)
						if strings.Contains(lk, "code") || strings.Contains(lk, "number") || strings.Contains(lk, "universal_id") || strings.Contains(lk, "external_id") || strings.Contains(lk, "movie_id") {
							if s, ok := value.(string); ok {
								explicit = true
								values = append(values, s)
							}
						}
						if lk == "id" {
							if s, ok := value.(string); ok && looksLikeMediaCode(s) {
								explicit = true
								values = append(values, s)
							}
						}
						walk(value)
					}
				case []any:
					for _, value := range z {
						walk(value)
					}
				}
			}
			walk(v)
			for _, value := range values {
				if normalizeProviderCode(value) == want {
					matched = true
					break
				}
			}
			return matched, explicit
		}
	}
	text := strings.ToUpper(string(body))
	pattern := `(?i)(^|[^A-Z0-9])` + regexp.QuoteMeta(strings.TrimSpace(code)) + `([^A-Z0-9]|$)`
	matched, _ = regexp.MatchString(pattern, text)
	return matched, matched
}

func looksLikeMediaCode(value string) bool {
	value = strings.TrimSpace(value)
	if !strings.Contains(value, "-") {
		return false
	}
	hasLetter, hasDigit := false, false
	for _, r := range value {
		hasLetter = hasLetter || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
		hasDigit = hasDigit || (r >= '0' && r <= '9')
	}
	return hasLetter && hasDigit
}

func findMatchingDetailURL(body []byte, base, code string) (string, bool) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if err != nil {
		return "", false
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return "", false
	}
	var found string
	doc.Find("a[href], [data-href], [data-url]").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		href := s.AttrOr("href", s.AttrOr("data-href", s.AttrOr("data-url", "")))
		text := strings.TrimSpace(s.Text() + " " + href)
		pattern := `(?i)(^|[^A-Z0-9])` + regexp.QuoteMeta(strings.TrimSpace(code)) + `([^A-Z0-9]|$)`
		ok, _ := regexp.MatchString(pattern, text)
		if !ok {
			return true
		}
		u, err := url.Parse(href)
		if err != nil {
			return true
		}
		resolved := baseURL.ResolveReference(u)
		if isResourceSearchURL(resolved) {
			return true
		}
		found = resolved.String()
		return false
	})
	return found, found != ""
}

func isResourceSearchURL(u *url.URL) bool {
	if u == nil {
		return true
	}
	path := strings.ToLower(u.EscapedPath())
	return strings.Contains(path, "/search")
}

func readLimited(r io.Reader, limit int) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return b, nil
}
