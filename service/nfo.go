package service

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
	"navi-desktop/model"
)

// NFOService NFO 本地元数据解析服务
// 支持 Kodi / Emby / Jellyfin 风格的 NFO XML 文件
// 增强：宽松兼容非标准字段、日期归一化、原始 XML 保留
type NFOService struct {
	logger        *zap.SugaredLogger
	readFile      func(string) ([]byte, error)
	statFile      func(string) (os.FileInfo, error)
	readDir       func(string) ([]os.DirEntry, error)
	preparedNFOs  map[string]*preparedNFOFile
	createNFOtemp func(string, string) (*os.File, error)
	writeNFOtemp  func(*os.File, []byte) (int, error)
	validateNFO   func([]byte) error
	replaceNFO    func(string, string) error
}

func NewNFOService(logger *zap.SugaredLogger) *NFOService {
	return &NFOService{logger: logger}
}

type preparedNFOFile struct {
	data          []byte
	info          os.FileInfo
	movie         *NFOMovie
	movieErr      error
	tvShow        *NFOTVShow
	tvShowErr     error
	fieldPresence map[string]bool
	fieldErr      error
	actorMetadata *NFOActorMetadata
	actorsErr     error
}

func nfoPathKey(path string) string {
	path = filepath.Clean(strings.TrimSpace(path))
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}

func (s *NFOService) read(path string) ([]byte, error) {
	if s != nil && s.readFile != nil {
		return s.readFile(path)
	}
	return os.ReadFile(path)
}

func (s *NFOService) stat(path string) (os.FileInfo, error) {
	if s != nil && s.statFile != nil {
		return s.statFile(path)
	}
	return os.Stat(path)
}

func (s *NFOService) listDir(path string) ([]os.DirEntry, error) {
	if s != nil && s.readDir != nil {
		return s.readDir(path)
	}
	return os.ReadDir(path)
}

func (s *NFOService) prepareNFO(path string, data []byte, info os.FileInfo) *preparedNFOFile {
	prepared := &preparedNFOFile{data: append([]byte(nil), data...), info: info}
	var movie NFOMovie
	prepared.movieErr = s.unmarshalNFOXML(data, &movie, path)
	if prepared.movieErr == nil {
		prepared.movie = &movie
	}
	var tvShow NFOTVShow
	prepared.tvShowErr = s.unmarshalNFOXML(data, &tvShow, path)
	if prepared.tvShowErr == nil {
		prepared.tvShow = &tvShow
	}
	prepared.fieldPresence, prepared.fieldErr = nfoXMLFieldPresence(data)
	prepared.actorMetadata, prepared.actorsErr = parseNFOActorMetadata(data)
	return prepared
}

func (s *NFOService) cloneWithPreparedIO(
	readFile func(string) ([]byte, error),
	statFile func(string) (os.FileInfo, error),
	readDir func(string) ([]os.DirEntry, error),
	prepared map[string]*preparedNFOFile,
) *NFOService {
	return &NFOService{
		logger:       s.logger,
		readFile:     readFile,
		statFile:     statFile,
		readDir:      readDir,
		preparedNFOs: prepared,
	}
}

type NFOSet struct {
	Name string `xml:"name"`
	Text string `xml:",chardata"`
}

func (s *NFOSet) Value() string {
	if s == nil {
		return ""
	}
	if value := strings.TrimSpace(s.Name); value != "" {
		return value
	}
	return strings.TrimSpace(s.Text)
}

// ==================== NFO XML 结构体（增强版） ====================

// NFOMovie 电影 NFO XML 根元素（宽松兼容）
type NFOMovie struct {
	XMLName xml.Name `xml:"movie"`
	// 标准字段
	Title     string     `xml:"title"`
	OrigTitle string     `xml:"originaltitle"`
	SortTitle string     `xml:"sorttitle"`
	Year      int        `xml:"year"`
	Plot      string     `xml:"plot"`
	Outline   string     `xml:"outline"`
	Tagline   string     `xml:"tagline"`
	Rating    float64    `xml:"rating"`
	Runtime   int        `xml:"runtime"`
	Studio    string     `xml:"studio"`
	Country   string     `xml:"country"`
	TMDbID    int        `xml:"tmdbid"`
	DoubanID  string     `xml:"doubanid"`
	Genres    []string   `xml:"genre"`
	Tags      []string   `xml:"tag"`
	Directors []string   `xml:"director"`
	Actors    []NFOActor `xml:"actor"`
	Set       *NFOSet    `xml:"set,omitempty"`
	Series    string     `xml:"series"`
	// 增强字段：日期（多种来源，后续归一化）
	Premiered   string `xml:"premiered"`
	ReleaseDate string `xml:"releasedate"`
	Release     string `xml:"release"`
	// 增强字段：评分/分级
	CriticRating float64 `xml:"criticrating"`
	MPAA         string  `xml:"mpaa"`
	CustomRating string  `xml:"customrating"`
	CountryCode  string  `xml:"countrycode"`
	// 增强字段：制作信息（非标准但常见于特定刮削器）
	OriginalPlot string `xml:"originalplot"`
	Maker        string `xml:"maker"`
	Publisher    string `xml:"publisher"`
	Label        string `xml:"label"`
	Num          string `xml:"num"`
	// 增强字段：远程图片路径
	Poster string `xml:"poster"`
	Cover  string `xml:"cover"`
	Fanart string `xml:"fanart"`
	Thumb  string `xml:"thumb"`
	// 增强字段：站点来源 Provider IDs
	JavbusID      string         `xml:"javbusid"`
	AiravCcid     string         `xml:"airav_ccid"`
	JavdbSearchID string         `xml:"javdbsearchid"`
	LockData      string         `xml:"lockdata"`
	DateAdded     string         `xml:"dateadded"`
	Trailer       string         `xml:"trailer"`
	Votes         string         `xml:"votes"`
	Website       string         `xml:"website"`
	FileInfo      *NFORawSection `xml:"fileinfo"`
}

// NFOTVShow 剧集 NFO XML 根元素
type NFOTVShow struct {
	XMLName   xml.Name   `xml:"tvshow"`
	Title     string     `xml:"title"`
	OrigTitle string     `xml:"originaltitle"`
	Year      int        `xml:"year"`
	Plot      string     `xml:"plot"`
	Rating    float64    `xml:"rating"`
	Studio    string     `xml:"studio"`
	Country   string     `xml:"country"`
	TMDbID    int        `xml:"tmdbid"`
	DoubanID  string     `xml:"doubanid"`
	Genres    []string   `xml:"genre"`
	Tags      []string   `xml:"tag"`
	Directors []string   `xml:"director"`
	Actors    []NFOActor `xml:"actor"`
	// 增强日期字段
	Premiered   string `xml:"premiered"`
	ReleaseDate string `xml:"releasedate"`
}

// NFOActor NFO 演员信息（宽松兼容：name 可能为空）
type NFOActor struct {
	Name      string `xml:"name"`
	Role      string `xml:"role"`
	Thumb     string `xml:"thumb"`
	SortOrder int    `xml:"sortorder"`
}

type NFOActorMetadata struct {
	Actors        []NFOActor
	Directors     []string
	ActorsPresent bool
}

// NFOExtraFields 存储到 Media.NfoExtraFields 的 JSON 结构
// NOTE: 后续可根据需要扩展字段
type NFOExtraFields struct {
	SortTitle    string            `json:"sort_title,omitempty"`
	Outline      string            `json:"outline,omitempty"`
	OriginalPlot string            `json:"original_plot,omitempty"`
	MPAA         string            `json:"mpaa,omitempty"`
	CustomRating string            `json:"custom_rating,omitempty"`
	CriticRating float64           `json:"critic_rating,omitempty"`
	CountryCode  string            `json:"country_code,omitempty"`
	Maker        string            `json:"maker,omitempty"`
	Publisher    string            `json:"publisher,omitempty"`
	Label        string            `json:"label,omitempty"`
	Num          string            `json:"num,omitempty"`
	Poster       string            `json:"poster,omitempty"`
	Cover        string            `json:"cover,omitempty"`
	Fanart       string            `json:"fanart,omitempty"`
	Tags         []string          `json:"tags,omitempty"`
	ProviderIDs  map[string]string `json:"provider_ids,omitempty"`
}

type NFORawSection struct {
	InnerXML string `xml:",innerxml"`
}

type NFOEditorData struct {
	NFOPath           string   `json:"nfo_path"`
	Title             string   `json:"title"`
	Code              string   `json:"code"`
	ReleaseDate       string   `json:"release_date"`
	Director          string   `json:"director"`
	Series            string   `json:"series"`
	Publisher         string   `json:"publisher"`
	Maker             string   `json:"maker"`
	Genres            string   `json:"genres"`
	Actors            string   `json:"actors"`
	Plot              string   `json:"plot"`
	Runtime           string   `json:"runtime"`
	FileSize          string   `json:"file_size"`
	Resolution        string   `json:"resolution"`
	VideoCodec        string   `json:"video_codec"`
	Rating            string   `json:"rating"`
	SourceFingerprint string   `json:"source_fingerprint"`
	UpdatedFields     []string `json:"updated_fields"`
}

var nfoEditorWritableFields = []string{
	"title", "code", "release_date", "director", "series", "publisher",
	"maker", "genres", "actors", "plot", "runtime", "rating",
}

func joinEditorList(values []string) string {
	if len(values) == 0 {
		return ""
	}

	seen := make(map[string]bool)
	items := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		items = append(items, value)
	}
	return strings.Join(items, " / ")
}

func splitEditorList(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ',', '，', '/', '、', '\n', '\r', ';', '|':
			return true
		default:
			return false
		}
	})

	items := make([]string, 0, len(fields))
	seen := make(map[string]bool)
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" || seen[field] {
			continue
		}
		seen[field] = true
		items = append(items, field)
	}
	return items
}

func joinActorNames(actors []NFOActor) string {
	if len(actors) == 0 {
		return ""
	}
	names := make([]string, 0, len(actors))
	for _, actor := range actors {
		name := strings.TrimSpace(actor.Name)
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	return joinEditorList(names)
}

func splitEditorValues(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ',', '\uFF0C', '\u3001', '/', '\n', '\r', ';', '|':
			return true
		default:
			return false
		}
	})

	items := make([]string, 0, len(fields))
	seen := make(map[string]bool)
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" || seen[field] {
			continue
		}
		seen[field] = true
		items = append(items, field)
	}
	return items
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func formatEditorFloat(value float64) string {
	if value <= 0 {
		return ""
	}
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func formatEditorInt(value int) string {
	if value <= 0 {
		return ""
	}
	return strconv.Itoa(value)
}

func formatEditorFileSize(size int64) string {
	if size <= 0 {
		return ""
	}
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)

	switch {
	case size >= gb:
		return fmt.Sprintf("%.2f GB", float64(size)/float64(gb))
	case size >= mb:
		return fmt.Sprintf("%.1f MB", float64(size)/float64(mb))
	case size >= kb:
		return fmt.Sprintf("%.1f KB", float64(size)/float64(kb))
	default:
		return fmt.Sprintf("%d B", size)
	}
}

func deriveEditorCode(media *model.Media) string {
	if media == nil {
		return ""
	}

	if media.NfoExtraFields != "" {
		var extra NFOExtraFields
		if err := json.Unmarshal([]byte(media.NfoExtraFields), &extra); err == nil && strings.TrimSpace(extra.Num) != "" {
			return strings.TrimSpace(extra.Num)
		}
	}

	filename := filepath.Base(media.FilePath)
	stem := strings.TrimSuffix(filename, filepath.Ext(filename))
	return strings.TrimSpace(stem)
}

func sanitizeMalformedNFOXML(data []byte) ([]byte, bool) {
	if len(data) == 0 {
		return data, false
	}

	raw := string(data)
	var builder strings.Builder
	builder.Grow(len(raw))

	changed := false
	inCDATA := false

	for i := 0; i < len(raw); i++ {
		switch {
		case !inCDATA && strings.HasPrefix(raw[i:], "<![CDATA["):
			builder.WriteString("<![CDATA[")
			i += len("<![CDATA[") - 1
			inCDATA = true
			continue
		case inCDATA && strings.HasPrefix(raw[i:], "]]>"):
			builder.WriteString("]]>")
			i += len("]]>") - 1
			inCDATA = false
			continue
		}

		if !inCDATA && raw[i] == '&' && !looksLikeXMLEntity(raw, i) {
			builder.WriteString("&amp;")
			changed = true
			continue
		}

		builder.WriteByte(raw[i])
	}

	if !changed {
		return data, false
	}

	return []byte(builder.String()), true
}

func looksLikeXMLEntity(raw string, ampIndex int) bool {
	if ampIndex < 0 || ampIndex >= len(raw) || raw[ampIndex] != '&' || ampIndex+1 >= len(raw) {
		return false
	}

	i := ampIndex + 1
	if raw[i] == '#' {
		i++
		if i < len(raw) && (raw[i] == 'x' || raw[i] == 'X') {
			i++
			start := i
			for i < len(raw) && isHexDigit(raw[i]) {
				i++
			}
			return i > start && i < len(raw) && raw[i] == ';'
		}

		start := i
		for i < len(raw) && raw[i] >= '0' && raw[i] <= '9' {
			i++
		}
		return i > start && i < len(raw) && raw[i] == ';'
	}

	if !isXMLNameStart(raw[i]) {
		return false
	}

	i++
	for i < len(raw) && isXMLNameChar(raw[i]) {
		i++
	}

	return i < len(raw) && raw[i] == ';'
}

func isHexDigit(value byte) bool {
	return (value >= '0' && value <= '9') ||
		(value >= 'a' && value <= 'f') ||
		(value >= 'A' && value <= 'F')
}

func isXMLNameStart(value byte) bool {
	return (value >= 'a' && value <= 'z') ||
		(value >= 'A' && value <= 'Z') ||
		value == '_' || value == ':'
}

func isXMLNameChar(value byte) bool {
	return isXMLNameStart(value) ||
		(value >= '0' && value <= '9') ||
		value == '-' || value == '.'
}

func (s *NFOService) unmarshalNFOXML(data []byte, target interface{}, nfoPath string) error {
	decode := func(input []byte) error {
		filtered, err := filterNFOXMLNamespaces(input, false)
		if err != nil {
			return err
		}
		return xml.Unmarshal(filtered, target)
	}
	if err := decode(data); err == nil {
		return nil
	} else {
		sanitized, changed := sanitizeMalformedNFOXML(data)
		if !changed {
			return err
		}
		targetValue := reflect.ValueOf(target)
		if targetValue.Kind() == reflect.Ptr && !targetValue.IsNil() {
			targetValue.Elem().Set(reflect.Zero(targetValue.Elem().Type()))
		}
		if retryErr := decode(sanitized); retryErr == nil {
			if s.logger != nil {
				s.logger.Debugf("parsed malformed NFO after XML sanitization: %s", nfoPath)
			}
			return nil
		}
		return err
	}
}

func (s *NFOService) LoadEditorData(nfoPath string, media *model.Media) (*NFOEditorData, error) {
	ApplyDerivedMediaFields(media)

	data := &NFOEditorData{
		NFOPath:           strings.TrimSpace(nfoPath),
		Title:             strings.TrimSpace(media.Title),
		Code:              deriveEditorCode(media),
		ReleaseDate:       strings.TrimSpace(media.ReleaseDateNormalized),
		Publisher:         firstNonEmptyTrimmed(media.Label, media.Studio),
		Maker:             firstNonEmptyTrimmed(media.Maker, media.Studio),
		Genres:            joinEditorList(strings.Split(strings.TrimSpace(media.Genres), ",")),
		Actors:            joinEditorList(strings.Split(strings.TrimSpace(media.Actor), ",")),
		Plot:              strings.TrimSpace(media.Overview),
		Runtime:           formatEditorInt(media.Runtime),
		FileSize:          formatEditorFileSize(media.FileSize),
		Resolution:        strings.TrimSpace(media.Resolution),
		VideoCodec:        strings.TrimSpace(media.VideoCodec),
		Rating:            formatEditorFloat(media.Rating),
		SourceFingerprint: missingNFOFingerprint,
	}
	if media.Series != nil {
		data.Series = strings.TrimSpace(media.Series.Title)
	}

	if strings.TrimSpace(nfoPath) == "" {
		return data, nil
	}

	content, _, exists, err := readNFOState(nfoPath)
	if err != nil {
		return nil, fmt.Errorf("读取 NFO 文件失败: %w", err)
	}
	if !exists {
		return data, nil
	}
	data.SourceFingerprint = nfoContentFingerprint(content, true)

	var movie NFOMovie
	if err := s.unmarshalNFOXML(content, &movie, nfoPath); err != nil {
		return nil, fmt.Errorf("解析 NFO 文件失败: %w", err)
	}
	presence, err := nfoXMLFieldPresence(content)
	if err != nil {
		return nil, fmt.Errorf("解析 NFO 字段失败: %w", err)
	}

	if presence["title"] {
		data.Title = strings.TrimSpace(movie.Title)
	}
	if presence["num"] {
		data.Code = strings.TrimSpace(movie.Num)
	}
	switch {
	case presence["releasedate"]:
		data.ReleaseDate = strings.TrimSpace(movie.ReleaseDate)
	case presence["premiered"]:
		data.ReleaseDate = strings.TrimSpace(movie.Premiered)
	case presence["release"]:
		data.ReleaseDate = strings.TrimSpace(movie.Release)
	}
	if presence["director"] {
		data.Director = joinEditorList(movie.Directors)
	}
	if presence["set"] || presence["series"] {
		data.Series = firstNonEmpty(movie.Set.Value(), movie.Series)
	}
	if presence["publisher"] {
		data.Publisher = strings.TrimSpace(movie.Publisher)
	} else if presence["label"] {
		data.Publisher = strings.TrimSpace(movie.Label)
	}
	if presence["maker"] {
		data.Maker = strings.TrimSpace(movie.Maker)
	} else if presence["studio"] {
		data.Maker = strings.TrimSpace(movie.Studio)
	}
	if presence["genre"] || presence["tag"] {
		data.Genres = joinEditorList(append(append([]string(nil), movie.Genres...), movie.Tags...))
	}
	if actorMetadata, actorErr := parseNFOActorMetadata(content); actorErr == nil {
		if actorMetadata.ActorsPresent {
			data.Actors = joinActorNames(actorMetadata.Actors)
		}
	} else {
		return nil, fmt.Errorf("解析 NFO 演员失败: %w", actorErr)
	}
	if presence["plot"] {
		data.Plot = strings.TrimSpace(movie.Plot)
	} else if presence["outline"] {
		data.Plot = strings.TrimSpace(movie.Outline)
	}
	if presence["runtime"] {
		data.Runtime = formatEditorInt(movie.Runtime)
	}
	if presence["rating"] {
		data.Rating = formatEditorFloat(movie.Rating)
	}

	return data, nil
}

func (s *NFOService) SaveEditorData(nfoPath string, data *NFOEditorData) error {
	nfoPath = strings.TrimSpace(nfoPath)
	if nfoPath == "" {
		return fmt.Errorf("empty nfo path")
	}
	if data == nil {
		return fmt.Errorf("empty nfo editor data")
	}

	original, info, exists, err := readNFOState(nfoPath)
	if err != nil {
		return fmt.Errorf("读取 NFO 文件失败: %w", err)
	}
	currentFingerprint := nfoContentFingerprint(original, exists)
	if data.SourceFingerprint != "" && data.SourceFingerprint != currentFingerprint {
		return fmt.Errorf("NFO save conflict: source file was modified externally")
	}
	saveData := data
	if !exists {
		copy := *data
		copy.UpdatedFields = append([]string(nil), nfoEditorWritableFields...)
		saveData = &copy
	}
	if len(saveData.UpdatedFields) == 0 {
		return nil
	}
	mode := os.FileMode(0644)
	if exists {
		mode = info.Mode()
		if mode.Perm()&0222 == 0 {
			return fmt.Errorf("NFO file is read-only: %s", nfoPath)
		}
	} else {
		original = []byte(xml.Header + "<movie></movie>\n")
	}
	output, err := buildEditedNFO(original, saveData)
	if err != nil {
		return fmt.Errorf("更新 NFO XML 失败: %w", err)
	}
	if bytes.Equal(output, original) && exists {
		return nil
	}
	if err := s.writeNFOAtomically(nfoPath, output, mode, currentFingerprint); err != nil {
		return err
	}
	return nil
}

// ==================== 解析方法 ====================

// ParseMovieNFO 解析电影 NFO 文件并将数据应用到 Media 对象
func (s *NFOService) ParseMovieNFO(nfoPath string, media *model.Media) error {
	if prepared := s.preparedNFOs[nfoPathKey(nfoPath)]; prepared != nil {
		if prepared.movieErr == nil && prepared.movie != nil {
			if prepared.fieldErr != nil {
				return fmt.Errorf("parse NFO XML fields failed: %w", prepared.fieldErr)
			}
			applyPreparedNFOFileState(media, prepared)
			s.applyMovieNFOToMedia(media, prepared.movie, prepared.fieldPresence)
			return nil
		}
		if prepared.tvShowErr == nil && prepared.tvShow != nil {
			if prepared.fieldErr != nil {
				return fmt.Errorf("parse NFO XML fields failed: %w", prepared.fieldErr)
			}
			applyPreparedNFOFileState(media, prepared)
			s.applyTVShowNFOToMedia(media, prepared.tvShow, prepared.fieldPresence)
			return nil
		}
		return fmt.Errorf("parse NFO XML failed: %w", prepared.movieErr)
	}

	data, err := s.read(nfoPath)
	if err != nil {
		return fmt.Errorf("读取NFO文件失败: %w", err)
	}

	var nfoModTime *time.Time
	if info, statErr := s.stat(nfoPath); statErr == nil && info != nil && !info.IsDir() {
		value := info.ModTime().UTC().Truncate(time.Second)
		nfoModTime = &value
	}

	var nfo NFOMovie
	if err := s.unmarshalNFOXML(data, &nfo, nfoPath); err != nil {
		// 尝试作为 tvshow 解析
		var tvNFO NFOTVShow
		if err2 := s.unmarshalNFOXML(data, &tvNFO, nfoPath); err2 != nil {
			return fmt.Errorf("解析NFO XML失败: %w", err)
		}
		// 如果是 tvshow 格式，转换后应用
		presence, presenceErr := nfoXMLFieldPresence(data)
		if presenceErr != nil {
			return fmt.Errorf("解析NFO字段失败: %w", presenceErr)
		}
		media.NfoRawXml = string(data)
		media.NfoModTime = nfoModTime
		s.applyTVShowNFOToMedia(media, &tvNFO, presence)
		return nil
	}

	presence, err := nfoXMLFieldPresence(data)
	if err != nil {
		return fmt.Errorf("解析NFO字段失败: %w", err)
	}
	media.NfoRawXml = string(data)
	media.NfoModTime = nfoModTime
	s.applyMovieNFOToMedia(media, &nfo, presence)
	return nil
}

func applyPreparedNFOFileState(media *model.Media, prepared *preparedNFOFile) {
	media.NfoRawXml = string(prepared.data)
	if prepared.info != nil && !prepared.info.IsDir() {
		nfoModTime := prepared.info.ModTime().UTC().Truncate(time.Second)
		media.NfoModTime = &nfoModTime
	}
}

// ParseTVShowNFO 解析剧集 NFO 文件并将数据应用到 Series 对象
func (s *NFOService) ParseTVShowNFO(nfoPath string, series *model.Series) error {
	if prepared := s.preparedNFOs[nfoPathKey(nfoPath)]; prepared != nil {
		if prepared.tvShowErr != nil || prepared.tvShow == nil {
			return fmt.Errorf("parse NFO XML failed: %w", prepared.tvShowErr)
		}
		if prepared.fieldErr != nil {
			return fmt.Errorf("parse NFO XML fields failed: %w", prepared.fieldErr)
		}
		s.applyTVShowNFOToSeries(series, prepared.tvShow, prepared.fieldPresence)
		return nil
	}

	data, err := s.read(nfoPath)
	if err != nil {
		return fmt.Errorf("读取NFO文件失败: %w", err)
	}

	var nfo NFOTVShow
	if err := s.unmarshalNFOXML(data, &nfo, nfoPath); err != nil {
		return fmt.Errorf("解析NFO XML失败: %w", err)
	}

	presence, err := nfoXMLFieldPresence(data)
	if err != nil {
		return fmt.Errorf("解析NFO字段失败: %w", err)
	}
	s.applyTVShowNFOToSeries(series, &nfo, presence)
	return nil
}

// GetActorMetadataFromNFO extracts actors and whether the NFO explicitly
// supplied actor metadata. An empty <actors/> or <actor/> is a valid empty list;
// no actor element means the NFO did not authoritatively provide this field.
func (s *NFOService) GetActorMetadataFromNFO(nfoPath string) (*NFOActorMetadata, error) {
	if prepared := s.preparedNFOs[nfoPathKey(nfoPath)]; prepared != nil {
		if prepared.actorsErr != nil {
			return nil, prepared.actorsErr
		}
		if prepared.actorMetadata != nil {
			copy := *prepared.actorMetadata
			copy.Actors = append([]NFOActor(nil), prepared.actorMetadata.Actors...)
			copy.Directors = append([]string(nil), prepared.actorMetadata.Directors...)
			return &copy, nil
		}
		return nil, fmt.Errorf("unable to parse NFO file")
	}

	data, err := s.read(nfoPath)
	if err != nil {
		return nil, err
	}
	metadata, err := parseNFOActorMetadata(data)
	if err != nil {
		return nil, fmt.Errorf("无法解析NFO文件: %w", err)
	}
	return metadata, nil
}

// GetActorsFromNFO 从 NFO 文件中提取演员列表
func (s *NFOService) GetActorsFromNFO(nfoPath string) ([]NFOActor, []string, error) {
	metadata, err := s.GetActorMetadataFromNFO(nfoPath)
	if err != nil {
		return nil, nil, err
	}
	return metadata.Actors, metadata.Directors, nil
}

// ==================== 本地图片扫描 ====================

// FindLocalImages 在指定目录下查找本地图片（poster/fanart/banner 等）
// 支持 jpg、png、webp 等常见图片格式
func (s *NFOService) FindLocalImages(dir string) (poster, backdrop string) {
	// 常见本地海报文件名（按优先级排序）
	posterNames := []string{
		"poster.jpg", "poster.png", "poster.webp",
		"cover.jpg", "cover.png", "cover.webp",
		"folder.jpg", "folder.png", "folder.webp",
		"thumb.jpg", "thumb.png", "thumb.webp",
		"movie.jpg", "movie.png",
		"show.jpg", "show.png",
	}
	// 常见本地背景图文件名
	backdropNames := []string{
		"fanart.jpg", "fanart.png", "fanart.webp",
		"backdrop.jpg", "backdrop.png", "backdrop.webp",
		"banner.jpg", "banner.png", "banner.webp",
		"background.jpg", "background.png", "background.webp",
		"clearart.jpg", "clearart.png",
		"landscape.jpg", "landscape.png",
	}

	for _, name := range posterNames {
		path := filepath.Join(dir, name)
		if _, err := s.stat(path); err == nil {
			poster = path
			break
		}
	}

	for _, name := range backdropNames {
		path := filepath.Join(dir, name)
		if _, err := s.stat(path); err == nil {
			backdrop = path
			break
		}
	}

	imageExts := map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true}
	entries, err := s.listDir(dir)
	if err == nil {
		hasToken := func(name, token string) bool {
			lower := strings.ToLower(name)
			ext := strings.ToLower(filepath.Ext(lower))
			stem := strings.TrimSuffix(lower, ext)
			normalized := "-" + strings.NewReplacer("_", "-", ".", "-", " ", "-").Replace(stem) + "-"
			return strings.Contains(normalized, "-"+token+"-")
		}
		findByTokens := func(tokens []string, excludeTokens []string) string {
			for _, token := range tokens {
				for _, entry := range entries {
					if entry.IsDir() {
						continue
					}
					name := entry.Name()
					ext := strings.ToLower(filepath.Ext(name))
					if !imageExts[ext] || !hasToken(name, token) {
						continue
					}
					skip := false
					for _, excludeToken := range excludeTokens {
						if hasToken(name, excludeToken) {
							skip = true
							break
						}
					}
					if skip {
						continue
					}
					return filepath.Join(dir, name)
				}
			}
			return ""
		}

		if poster == "" {
			poster = findByTokens([]string{"poster"}, nil)
		}
		if backdrop == "" {
			backdrop = findByTokens([]string{"fanart"}, nil)
		}
		if backdrop == "" {
			backdrop = findByTokens([]string{"backdrop"}, nil)
		}
		if backdrop == "" {
			backdrop = findByTokens([]string{"background", "banner", "clearart", "landscape"}, nil)
		}
		if poster == "" {
			poster = findByTokens([]string{"cover", "folder", "thumb", "movie", "show"}, []string{"fanart", "backdrop", "background", "banner", "clearart", "landscape"})
		}
	}

	// 如果没有找到标准命名的海报，尝试查找目录中的第一张图片作为海报
	if poster == "" {
		entries, err := s.listDir(dir)
		if err == nil {
			imageExts := map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true}
			for _, entry := range entries {
				if !entry.IsDir() {
					ext := strings.ToLower(filepath.Ext(entry.Name()))
					if imageExts[ext] {
						// 排除已识别为backdrop的文件
						candidate := filepath.Join(dir, entry.Name())
						if candidate != backdrop {
							poster = candidate
							break
						}
					}
				}
			}
		}
	}

	return poster, backdrop
}

// FindNFOFile 在指定目录下查找 NFO 文件
func (s *NFOService) FindNFOFile(dir string) string {
	entries, err := s.listDir(dir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(strings.ToLower(entry.Name()), ".nfo") {
			return filepath.Join(dir, entry.Name())
		}
	}
	return ""
}

// FindNFOForMedia 根据媒体文件路径查找关联的 NFO 文件
func (s *NFOService) FindNFOForMedia(mediaFilePath string) string {
	// 策略1: 同名 .nfo 文件
	ext := filepath.Ext(mediaFilePath)
	nfoPath := strings.TrimSuffix(mediaFilePath, ext) + ".nfo"
	if _, err := s.stat(nfoPath); err == nil {
		return nfoPath
	}

	// 策略2: 目录下任意 .nfo 文件
	dir := filepath.Dir(mediaFilePath)
	return s.FindNFOFile(dir)
}

// ==================== 日期归一化 ====================

// normalizeReleaseDate 从多个日期字段中选择优先级最高的并格式化
// 优先级: releasedate > premiered > release
func normalizeReleaseDate(releasedate, premiered, release string) string {
	candidates := []string{
		strings.TrimSpace(releasedate),
		strings.TrimSpace(premiered),
		strings.TrimSpace(release),
	}
	for _, d := range candidates {
		if d != "" && len(d) >= 4 {
			// 尝试识别常见日期格式，归一化为 YYYY-MM-DD
			// 已经是合法格式的直接返回
			return d
		}
	}
	return ""
}

// ==================== 应用 NFO 数据（增强版） ====================

func (s *NFOService) applyMovieNFOToMedia(media *model.Media, nfo *NFOMovie, fields map[string]bool) {
	if fields["title"] {
		media.Title = nfo.Title
	}
	if fields["originaltitle"] {
		media.OrigTitle = nfo.OrigTitle
	}
	if fields["year"] {
		media.Year = nfo.Year
	}
	if fields["plot"] {
		media.Overview = nfo.Plot
	} else if fields["outline"] {
		media.Overview = nfo.Outline
	}
	if fields["rating"] {
		media.Rating = nfo.Rating
	}
	if fields["runtime"] {
		media.Runtime = nfo.Runtime
	}
	// genre 和 tag 合并去重展示
	if fields["genre"] || fields["tag"] {
		allGenres := append(append([]string(nil), nfo.Genres...), nfo.Tags...)
		seen := make(map[string]bool)
		var deduped []string
		for _, g := range allGenres {
			g = strings.TrimSpace(g)
			if g != "" && !seen[g] {
				seen[g] = true
				deduped = append(deduped, g)
			}
		}
		media.Genres = strings.Join(deduped, ",")
	}
	if fields["tagline"] {
		media.Tagline = nfo.Tagline
	}
	if fields["studio"] {
		media.Studio = nfo.Studio
	}
	if fields["country"] {
		media.Country = nfo.Country
	}
	if fields["tmdbid"] {
		media.TMDbID = nfo.TMDbID
	}
	if fields["doubanid"] {
		media.DoubanID = nfo.DoubanID
	}

	// 日期归一化
	switch {
	case fields["releasedate"]:
		media.ReleaseDateNormalized = normalizeReleaseDate(nfo.ReleaseDate, "", "")
	case fields["premiered"]:
		media.ReleaseDateNormalized = normalizeReleaseDate(nfo.Premiered, "", "")
	case fields["release"]:
		media.ReleaseDateNormalized = normalizeReleaseDate(nfo.Release, "", "")
	}

	// Merge only fields actually present in the source NFO. Missing fields keep
	// their previous database values; explicit empty elements clear them.
	extra := parseNFOExtraFields(media.NfoExtraFields)
	touchedExtra := false
	stringExtraFields := []struct {
		name  string
		value string
		dest  *string
	}{
		{"sorttitle", nfo.SortTitle, &extra.SortTitle},
		{"outline", nfo.Outline, &extra.Outline},
		{"originalplot", nfo.OriginalPlot, &extra.OriginalPlot},
		{"mpaa", nfo.MPAA, &extra.MPAA},
		{"customrating", nfo.CustomRating, &extra.CustomRating},
		{"countrycode", nfo.CountryCode, &extra.CountryCode},
		{"maker", nfo.Maker, &extra.Maker},
		{"publisher", nfo.Publisher, &extra.Publisher},
		{"label", nfo.Label, &extra.Label},
		{"num", nfo.Num, &extra.Num},
		{"poster", nfo.Poster, &extra.Poster},
		{"cover", nfo.Cover, &extra.Cover},
		{"fanart", nfo.Fanart, &extra.Fanart},
	}
	for _, field := range stringExtraFields {
		if fields[field.name] {
			*field.dest = field.value
			touchedExtra = true
		}
	}
	if fields["criticrating"] {
		extra.CriticRating = nfo.CriticRating
		touchedExtra = true
	}
	if fields["tag"] {
		extra.Tags = append([]string(nil), nfo.Tags...)
		touchedExtra = true
	}

	providerFields := []struct {
		name  string
		value string
	}{
		{"javbusid", nfo.JavbusID},
		{"airav_ccid", nfo.AiravCcid},
		{"javdbsearchid", nfo.JavdbSearchID},
	}
	for _, field := range providerFields {
		if !fields[field.name] {
			continue
		}
		if extra.ProviderIDs == nil {
			extra.ProviderIDs = make(map[string]string)
		}
		if field.value == "" {
			delete(extra.ProviderIDs, field.name)
		} else {
			extra.ProviderIDs[field.name] = field.value
		}
		touchedExtra = true
	}

	if touchedExtra {
		if s.hasExtraContent(&extra) {
			if data, err := json.Marshal(extra); err == nil {
				media.NfoExtraFields = string(data)
			}
		} else {
			media.NfoExtraFields = ""
		}
	}

	ApplyDerivedMediaFields(media)
	if fields["maker"] {
		media.Maker = nfo.Maker
	} else if fields["studio"] {
		media.Maker = nfo.Studio
	}
	if fields["publisher"] {
		media.Label = nfo.Publisher
	} else if fields["label"] {
		media.Label = nfo.Label
	}
	if fields["num"] {
		media.Code = normalizeMediaCode(nfo.Num)
		media.CodePrefix = ParseCodePrefix(media.Code)
	}
	media.MetadataScore = ComputeMetadataScore(media)
}

// hasExtraContent 检查扩展字段是否有实际内容（避免写入空 JSON）
func (s *NFOService) hasExtraContent(extra *NFOExtraFields) bool {
	return extra.SortTitle != "" || extra.Outline != "" || extra.OriginalPlot != "" ||
		extra.MPAA != "" || extra.CustomRating != "" || extra.CriticRating > 0 ||
		extra.CountryCode != "" || extra.Maker != "" || extra.Publisher != "" ||
		extra.Label != "" || extra.Num != "" || extra.Poster != "" ||
		extra.Cover != "" || extra.Fanart != "" || len(extra.Tags) > 0 ||
		len(extra.ProviderIDs) > 0
}

func (s *NFOService) applyTVShowNFOToMedia(media *model.Media, nfo *NFOTVShow, fields map[string]bool) {
	if fields["title"] {
		media.Title = nfo.Title
	}
	if fields["originaltitle"] {
		media.OrigTitle = nfo.OrigTitle
	}
	if fields["year"] {
		media.Year = nfo.Year
	}
	if fields["plot"] {
		media.Overview = nfo.Plot
	}
	if fields["rating"] {
		media.Rating = nfo.Rating
	}
	if fields["genre"] || fields["tag"] {
		allGenres := append(append([]string(nil), nfo.Genres...), nfo.Tags...)
		seen := make(map[string]bool)
		var deduped []string
		for _, g := range allGenres {
			g = strings.TrimSpace(g)
			if g != "" && !seen[g] {
				seen[g] = true
				deduped = append(deduped, g)
			}
		}
		media.Genres = strings.Join(deduped, ",")
	}
	if fields["studio"] {
		media.Studio = nfo.Studio
	}
	if fields["country"] {
		media.Country = nfo.Country
	}
	// 日期归一化
	if fields["releasedate"] || fields["premiered"] {
		media.ReleaseDateNormalized = normalizeReleaseDate(nfo.ReleaseDate, nfo.Premiered, "")
	}

	ApplyDerivedMediaFields(media)
}

func (s *NFOService) applyTVShowNFOToSeries(series *model.Series, nfo *NFOTVShow, fields map[string]bool) {
	if fields["title"] {
		series.Title = nfo.Title
	}
	if fields["originaltitle"] {
		series.OrigTitle = nfo.OrigTitle
	}
	if fields["year"] {
		series.Year = nfo.Year
	}
	if fields["plot"] {
		series.Overview = nfo.Plot
	}
	if fields["rating"] {
		series.Rating = nfo.Rating
	}
	if fields["genre"] || fields["tag"] {
		allGenres := append(append([]string(nil), nfo.Genres...), nfo.Tags...)
		seen := make(map[string]bool)
		var deduped []string
		for _, g := range allGenres {
			g = strings.TrimSpace(g)
			if g != "" && !seen[g] {
				seen[g] = true
				deduped = append(deduped, g)
			}
		}
		series.Genres = strings.Join(deduped, ",")
	}
	if fields["studio"] {
		series.Studio = nfo.Studio
	}
	if fields["country"] {
		series.Country = nfo.Country
	}
	if fields["tmdbid"] {
		series.TMDbID = nfo.TMDbID
	}
	if fields["doubanid"] {
		series.DoubanID = nfo.DoubanID
	}
}
