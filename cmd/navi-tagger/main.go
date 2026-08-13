// navi-tagger 是一个独立的小工具：把媒体库的封面铺成墙，人工挑出刮削错的
// 有码/无码破解/无码流出 标签，勾选后批量写回 NFO。
//
// 双击 exe 即可运行，它会在本机起一个临时端口并自动打开浏览器。
package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

//go:embed ui.html
var uiFS embed.FS

type server struct {
	mu    sync.RWMutex
	roots []string
	items []*Item
	byID  map[string]*Item
}

func main() {
	log.SetFlags(0)

	srv := &server{byID: map[string]*Item{}}
	srv.roots = loadConfig().Roots

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatal("无法监听本地端口: %v", err)
	}
	addr := fmt.Sprintf("http://127.0.0.1:%d", listener.Addr().(*net.TCPAddr).Port)

	mux := http.NewServeMux()
	mux.HandleFunc("/", srv.handleIndex)
	mux.HandleFunc("/api/state", srv.handleState)
	mux.HandleFunc("/api/scan", srv.handleScan)
	mux.HandleFunc("/api/pick", srv.handlePick)
	mux.HandleFunc("/api/apply", srv.handleApply)
	mux.HandleFunc("/thumb", srv.handleThumb)

	fmt.Println("Navi 标签整理工具")
	fmt.Println("已在浏览器中打开:", addr)
	fmt.Println("用完直接关掉这个黑窗口即可。")
	openBrowser(addr)

	if err := http.Serve(listener, mux); err != nil {
		fatal("服务异常退出: %v", err)
	}
}

func fatal(format string, args ...interface{}) {
	fmt.Printf(format+"\n", args...)
	fmt.Println("\n按回车键退出...")
	fmt.Scanln()
	os.Exit(1)
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	page, err := uiFS.ReadFile("ui.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(page)
}

type stateResponse struct {
	Roots []string `json:"roots"`
	Items []*Item  `json:"items"`
}

func (s *server) handleState(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	writeJSON(w, stateResponse{Roots: s.roots, Items: s.items})
}

func (s *server) handleScan(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Roots []string `json:"roots"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var roots []string
	for _, root := range body.Roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		if _, err := os.Stat(root); err != nil {
			writeJSON(w, map[string]interface{}{"error": fmt.Sprintf("目录打不开: %s", root)})
			return
		}
		roots = append(roots, filepath.Clean(root))
	}
	if len(roots) == 0 {
		writeJSON(w, map[string]interface{}{"error": "请先选一个目录"})
		return
	}

	items := scanRoots(roots)
	sort.Slice(items, func(i, j int) bool {
		if items[i].Code != items[j].Code {
			return items[i].Code < items[j].Code
		}
		return items[i].NFOPath < items[j].NFOPath
	})

	byID := make(map[string]*Item, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}

	s.mu.Lock()
	s.roots, s.items, s.byID = roots, items, byID
	s.mu.Unlock()

	saveConfig(config{Roots: roots})
	go warmThumbnails(items)

	writeJSON(w, stateResponse{Roots: roots, Items: items})
}

// handlePick 借 Windows 自带的目录选择对话框，省得手敲路径。
func (s *server) handlePick(w http.ResponseWriter, r *http.Request) {
	if runtime.GOOS != "windows" {
		writeJSON(w, map[string]string{"path": ""})
		return
	}
	const script = `$f=(New-Object -ComObject Shell.Application).BrowseForFolder(0,'选择媒体库根目录',0); if($f){$f.Self.Path}`
	out, err := exec.Command("powershell", "-NoProfile", "-STA", "-Command", script).Output()
	if err != nil {
		writeJSON(w, map[string]string{"path": ""})
		return
	}
	writeJSON(w, map[string]string{"path": strings.TrimSpace(string(out))})
}

type applyRequest struct {
	IDs    []string `json:"ids"`
	Status string   `json:"status"`
}

type applyResult struct {
	ID    string `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Tags  []string `json:"tags,omitempty"`
}

func (s *server) handleApply(w http.ResponseWriter, r *http.Request) {
	var body applyRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !isKnownStatus(body.Status) {
		http.Error(w, "未知的标签: "+body.Status, http.StatusBadRequest)
		return
	}

	results := make([]applyResult, 0, len(body.IDs))
	for _, id := range body.IDs {
		s.mu.RLock()
		item := s.byID[id]
		s.mu.RUnlock()
		if item == nil {
			results = append(results, applyResult{ID: id, Error: "条目已失效，请重新扫描"})
			continue
		}

		tags, err := writeStatusToNFO(item.NFOPath, body.Status)
		if err != nil {
			results = append(results, applyResult{ID: id, Error: err.Error()})
			continue
		}

		s.mu.Lock()
		item.Tags = tags
		item.Status = body.Status
		item.Guess = ""
		s.mu.Unlock()

		results = append(results, applyResult{ID: id, OK: true, Tags: tags})
	}
	writeJSON(w, results)
}

func (s *server) handleThumb(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	item := s.byID[r.URL.Query().Get("id")]
	s.mu.RUnlock()
	if item == nil || item.Poster == "" {
		http.NotFound(w, r)
		return
	}
	data, err := thumbnail(item)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "max-age=86400")
	w.Write(data)
}

func writeJSON(w http.ResponseWriter, value interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(value)
}

func openBrowser(url string) {
	switch runtime.GOOS {
	case "windows":
		exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		exec.Command("open", url).Start()
	default:
		exec.Command("xdg-open", url).Start()
	}
}

type config struct {
	Roots []string `json:"roots"`
}

func configPath() string {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "NaviTagger", "config.json")
}

func loadConfig() config {
	var cfg config
	data, err := os.ReadFile(configPath())
	if err == nil {
		json.Unmarshal(data, &cfg)
	}
	return cfg
}

func saveConfig(cfg config) {
	path := configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	if data, err := json.Marshal(cfg); err == nil {
		os.WriteFile(path, data, 0o644)
	}
}
