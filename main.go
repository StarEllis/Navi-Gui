package main

import (
	"embed"
	"net/http"
	"net/url"
	"strings"

	"navi-desktop/config"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var assets embed.FS

type LocalFileHandler struct {
	reserve func(string) (func(), bool)
}

func (h *LocalFileHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/local/") {
		filePath := strings.TrimPrefix(r.URL.Path, "/local/")
		if unescaped, err := url.PathUnescape(filePath); err == nil {
			filePath = unescaped
		}
		if h.reserve != nil {
			release, ok := h.reserve(filePath)
			if !ok {
				http.NotFound(w, r)
				return
			}
			defer release()
		}
		http.ServeFile(w, r, filePath)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

func main() {
	app := NewApp()

	err := wails.Run(&options.App{
		Title:     "Navi",
		Width:     1260,
		Height:    860,
		MinWidth:  1090,
		MinHeight: 770,
		Frameless: true,
		AssetServer: &assetserver.Options{
			Assets:  assets,
			Handler: &LocalFileHandler{reserve: app.reserveArtworkPath},
		},
		BackgroundColour: &options.RGBA{R: 10, G: 14, B: 23, A: 1},
		OnStartup:        app.startup,
		OnBeforeClose:    app.beforeClose,
		OnShutdown:       app.shutdown,
		Windows: &windows.Options{
			Theme:                             windows.Dark,
			DisableFramelessWindowDecorations: false,
			// 便携版：WebView2 的 localStorage / 缓存跟数据一起放 exe 同目录，
			// 不再落在 %APPDATA%\Navi.exe（那份按 exe 文件名建，dev 和打包版会串）。
			WebviewUserDataPath: config.DataPath("webview2"),
		},
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
