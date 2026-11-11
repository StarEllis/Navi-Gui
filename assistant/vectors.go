package assistant

import (
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"hash/fnv"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	embedBatchSize = 32
	// 搜索前顺手补上的新片上限。再多就先用已有的坐标，提示用户去设置里更新。
	maxQuickSync   = 64
	maxDocRunes    = 1200
	saveEveryBatch = 8
)

// vectorFile 是存在本机的坐标文件。坐标跟模型绑定，换了模型就要全部重建。
type vectorFile struct {
	Model     string
	Dims      int
	UpdatedAt int64
	Entries   map[string]vectorEntry // media.id → 坐标
}

type vectorEntry struct {
	Hash   uint64 // 建坐标时那段文字的指纹，文字变了就要重建这一部
	Vector []float32
}

// VectorIndex 管着坐标文件：建、补、搜。坐标单独存一个文件，不往影片表里塞大字段。
type VectorIndex struct {
	path string

	mu       sync.Mutex
	file     *vectorFile // 第一次用到时才读
	building bool
	cancel   context.CancelFunc
	done     int
	todo     int
	lastErr  string
}

func NewVectorIndex(path string) *VectorIndex {
	return &VectorIndex{path: path}
}

// IndexStatus 是设置里"按意思找片"那一栏显示的状态。
type IndexStatus struct {
	Model           string `json:"model"`            // 现有坐标是用哪个模型建的，空表示还没建过
	ConfiguredModel string `json:"configured_model"` // 设置里现在填的模型
	Dims            int    `json:"dims"`
	Total           int    `json:"total"`   // 库里的片
	Indexed         int    `json:"indexed"` // 坐标是最新的
	Pending         int    `json:"pending"` // 新加的或简介改过、还没建的
	ModelMismatch   bool   `json:"model_mismatch"`
	Building        bool   `json:"building"`
	Done            int    `json:"done"` // 这次建了多少
	Todo            int    `json:"todo"` // 这次一共要建多少
	Error           string `json:"error"`
	UpdatedAt       int64  `json:"updated_at"` // Unix 毫秒
}

var errIndexBusy = errors.New("坐标正在建，等这次建完再说")

func (x *VectorIndex) loadLocked() *vectorFile {
	if x.file != nil {
		return x.file
	}
	f := &vectorFile{Entries: map[string]vectorEntry{}}
	if handle, err := os.Open(x.path); err == nil {
		var loaded vectorFile
		if gob.NewDecoder(handle).Decode(&loaded) == nil && loaded.Entries != nil {
			f = &loaded
		}
		_ = handle.Close()
	}
	x.file = f
	return f
}

func (x *VectorIndex) saveLocked() error {
	f := x.loadLocked()
	f.UpdatedAt = time.Now().UnixMilli()
	if err := os.MkdirAll(filepath.Dir(x.path), 0o755); err != nil {
		return err
	}
	tmp := x.path + ".tmp"
	handle, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := gob.NewEncoder(handle).Encode(f); err != nil {
		_ = handle.Close()
		return err
	}
	if err := handle.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, x.path)
}

// Usable 表示有用这个模型建好的坐标，可以搜。
func (x *VectorIndex) Usable(model string) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	f := x.loadLocked()
	return f.Model == model && len(f.Entries) > 0
}

// mediaDoc 是一部片拿去换坐标的文字。
type mediaDoc struct {
	ID   string
	Text string
	Hash uint64
}

func (s *Sandbox) loadDocs(ctx context.Context) ([]mediaDoc, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, COALESCE(title, ''), COALESCE(genres, ''), COALESCE(overview, '')
FROM media WHERE deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var docs []mediaDoc
	for rows.Next() {
		var id, title, genres, overview string
		if err := rows.Scan(&id, &title, &genres, &overview); err != nil {
			return nil, err
		}
		text := documentText(title, genres, overview)
		hash := fnv.New64a()
		_, _ = hash.Write([]byte(text))
		docs = append(docs, mediaDoc{ID: id, Text: text, Hash: hash.Sum64()})
	}
	return docs, rows.Err()
}

var (
	htmlTagPattern = regexp.MustCompile(`(?i)<[^>]*>`)
	// 刮削来的简介里常见的机翻落款。
	translationNoise = regexp.MustCompile(`[（(]?\s*由\s*Google\s*提供翻译\s*[)）]?`)
	// 分类里混着的分辨率、编码，不是内容，喂进去只会干扰。
	techGenrePattern = regexp.MustCompile(`(?i)^(\d{3,4}p|\d+k|hevc|avc|av1|vp9|h\.?26[45]|x26[45]|hdr\d*|\d+fps)$`)
	spacePattern     = regexp.MustCompile(`\s+`)
)

// documentText 拼出一部片拿去换坐标的文字：标题 + 内容标签 + 简介。
// 分类里的片商、发行、系列（带冒号的）和分辨率、编码都去掉，简介里的 HTML 和机翻落款清掉。
func documentText(title, genres, overview string) string {
	var tags []string
	for _, tag := range strings.FieldsFunc(genres, func(r rune) bool { return r == ',' || r == '，' }) {
		tag = strings.TrimSpace(tag)
		if tag == "" || strings.ContainsAny(tag, ":：") || techGenrePattern.MatchString(tag) {
			continue
		}
		tags = append(tags, tag)
	}
	overview = html.UnescapeString(htmlTagPattern.ReplaceAllString(overview, " "))
	overview = translationNoise.ReplaceAllString(overview, " ")

	parts := []string{strings.TrimSpace(title)}
	if len(tags) > 0 {
		parts = append(parts, "标签："+strings.Join(tags, "、"))
	}
	if overview = strings.TrimSpace(overview); overview != "" {
		parts = append(parts, overview)
	}
	text := spacePattern.ReplaceAllString(strings.Join(parts, "\n"), " ")
	if utf8.RuneCountInString(text) > maxDocRunes {
		text = string([]rune(text)[:maxDocRunes])
	}
	return text
}

// planSync 对照库里的片，算出哪些要建坐标；换了模型就清空重来，删掉的片顺带清掉。
func (x *VectorIndex) planSync(docs []mediaDoc, model string) []mediaDoc {
	x.mu.Lock()
	defer x.mu.Unlock()
	f := x.loadLocked()
	if f.Model != model {
		f = &vectorFile{Model: model, Entries: map[string]vectorEntry{}}
		x.file = f
	}
	alive := make(map[string]bool, len(docs))
	var todo []mediaDoc
	for _, doc := range docs {
		alive[doc.ID] = true
		if entry, ok := f.Entries[doc.ID]; !ok || entry.Hash != doc.Hash {
			todo = append(todo, doc)
		}
	}
	for id := range f.Entries {
		if !alive[id] {
			delete(f.Entries, id)
		}
	}
	return todo
}

// embedDocs 分批换坐标，每批写进内存，隔几批存一次盘；出错时已经建好的也存下来，下次接着建。
func (x *VectorIndex) embedDocs(ctx context.Context, cfg EmbeddingConfig, todo []mediaDoc, progress func(done int)) error {
	var err error
	for start := 0; start < len(todo) && err == nil; start += embedBatchSize {
		batch := todo[start:min(start+embedBatchSize, len(todo))]
		texts := make([]string, len(batch))
		for i, doc := range batch {
			texts[i] = doc.Text
		}
		var vectors [][]float32
		if vectors, err = embedTexts(ctx, cfg, texts); err != nil {
			break
		}
		x.mu.Lock()
		f := x.loadLocked()
		for i, doc := range batch {
			f.Entries[doc.ID] = vectorEntry{Hash: doc.Hash, Vector: vectors[i]}
		}
		f.Dims = len(vectors[0])
		if (start/embedBatchSize+1)%saveEveryBatch == 0 {
			err = x.saveLocked()
		}
		x.mu.Unlock()
		if progress != nil {
			progress(start + len(batch))
		}
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if saveErr := x.saveLocked(); err == nil {
		err = saveErr
	}
	return err
}

// IndexStatus 对照库里现在的片，算出坐标建到哪一步了。
func (s *Session) IndexStatus(ctx context.Context, cfg EmbeddingConfig) (IndexStatus, error) {
	x := s.Index
	if x == nil {
		return IndexStatus{}, errors.New("这个版本不支持按意思找片")
	}
	cfg = cfg.withDefaults()
	docs, err := s.sandbox.loadDocs(ctx)
	if err != nil {
		return IndexStatus{}, err
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	f := x.loadLocked()
	status := IndexStatus{
		Model: f.Model, ConfiguredModel: cfg.Model, Dims: f.Dims, Total: len(docs),
		ModelMismatch: f.Model != "" && f.Model != cfg.Model,
		Building:      x.building, Done: x.done, Todo: x.todo, Error: x.lastErr, UpdatedAt: f.UpdatedAt,
	}
	for _, doc := range docs {
		if entry, ok := f.Entries[doc.ID]; ok && entry.Hash == doc.Hash && !status.ModelMismatch {
			status.Indexed++
		} else {
			status.Pending++
		}
	}
	return status, nil
}

// BuildIndex 把还没建、或者简介改过的片都换成坐标，已经建好的不重复花钱。
// 同一时间只建一份；onProgress 每批之后调用一次。
func (s *Session) BuildIndex(ctx context.Context, cfg EmbeddingConfig, onProgress func(done, todo int)) error {
	x := s.Index
	if x == nil {
		return errors.New("这个版本不支持按意思找片")
	}
	if !cfg.Enabled() {
		return errors.New("先在设置里填向量/嵌入模型的 API Key")
	}
	cfg = cfg.withDefaults()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	x.mu.Lock()
	if x.building {
		x.mu.Unlock()
		return errIndexBusy
	}
	x.building, x.cancel, x.done, x.todo, x.lastErr = true, cancel, 0, 0, ""
	x.mu.Unlock()

	err := func() error {
		docs, err := s.sandbox.loadDocs(ctx)
		if err != nil {
			return err
		}
		todo := x.planSync(docs, cfg.Model)
		x.mu.Lock()
		x.todo = len(todo)
		x.mu.Unlock()
		return x.embedDocs(ctx, cfg, todo, func(done int) {
			x.mu.Lock()
			x.done = done
			x.mu.Unlock()
			if onProgress != nil {
				onProgress(done, len(todo))
			}
		})
	}()

	x.mu.Lock()
	defer x.mu.Unlock()
	x.building, x.cancel = false, nil
	switch {
	case errors.Is(err, context.Canceled):
		x.lastErr = "已停止，建好的部分已经存下，下次接着建"
	case err != nil:
		x.lastErr = err.Error()
	}
	return err
}

// StopIndex 停掉正在建的坐标。
func (s *Session) StopIndex() {
	if s.Index == nil {
		return
	}
	s.Index.mu.Lock()
	defer s.Index.mu.Unlock()
	if s.Index.cancel != nil {
		s.Index.cancel()
	}
}

// useQueryInstruction 是变量，方便对比带不带任务说明的效果。
var useQueryInstruction = true

// queryText 是搜索时拿去换坐标的那句话。Qwen3 的向量模型建议在搜索这一侧带上任务说明，片子那一侧不带。
func queryText(model, query string) string {
	if useQueryInstruction && strings.Contains(strings.ToLower(model), "qwen3-embedding") {
		return "Instruct: Given a Chinese description of a film's plot or setting, retrieve films whose title, tags and synopsis match it\nQuery: " + query
	}
	return query
}

type semanticHit struct {
	ID    string
	Score float32
}

// search 在坐标里挑出跟 query 意思最近的 limit 部。candidates 不为 nil 时只在这些片里挑。
// 新加的片不多时先顺手补上坐标；多的话先用已有的，在 note 里说明。
func (s *Session) search(ctx context.Context, cfg EmbeddingConfig, query string, candidates map[string]bool, limit int) ([]semanticHit, string, error) {
	x := s.Index
	cfg = cfg.withDefaults()
	var note string
	x.mu.Lock()
	building, model := x.building, x.loadLocked().Model
	x.mu.Unlock()
	// 先挡住换了模型的情况，不然下面补新片时会把旧坐标整个清掉。
	if model != cfg.Model {
		return nil, "", fmt.Errorf("坐标是用 %s 建的，设置里现在是 %s，要到助手设置里重建", model, cfg.Model)
	}
	if !building {
		docs, err := s.sandbox.loadDocs(ctx)
		if err != nil {
			return nil, "", err
		}
		if todo := x.planSync(docs, cfg.Model); len(todo) > maxQuickSync {
			note = fmt.Sprintf("有 %d 部是新加的或简介改过，还没建坐标，这次搜不到它们，可以提醒用户到助手设置里更新一下。", len(todo))
		} else if len(todo) > 0 {
			if err := x.embedDocs(ctx, cfg, todo, nil); err != nil {
				note = "有几部新片没来得及补上坐标：" + err.Error()
			}
		}
	}

	vectors, err := embedTexts(ctx, cfg, []string{queryText(cfg.Model, query)})
	if err != nil {
		return nil, "", err
	}
	q := vectors[0]

	x.mu.Lock()
	f := x.loadLocked()
	if f.Model != cfg.Model {
		x.mu.Unlock()
		return nil, "", fmt.Errorf("坐标是用 %s 建的，设置里现在是 %s，要到助手设置里重建", f.Model, cfg.Model)
	}
	hits := make([]semanticHit, 0, len(f.Entries))
	for id, entry := range f.Entries {
		if (candidates != nil && !candidates[id]) || len(entry.Vector) != len(q) {
			continue
		}
		hits = append(hits, semanticHit{ID: id, Score: dot(q, entry.Vector)})
	}
	x.mu.Unlock()

	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, note, nil
}
