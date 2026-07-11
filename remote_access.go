package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"navi-desktop/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	defaultRemoteBindHost     = "0.0.0.0"
	defaultJellyfinPort       = 18096
	defaultJellyfinServerName = "Navi Media Sidecar"
	jellyfinTicksPerSecond    = int64(10_000_000)
	jellyfinUserID            = desktopUserID
	remoteAccessLogPath       = "remote_access.log"
	jellyfinCompatVersion     = "10.10.7"
)

type remoteAccessState struct {
	mu               sync.Mutex
	jellyfinServer   *http.Server
	jellyfinServerID string

	logMu     sync.Mutex
	sessionMu sync.RWMutex
	sessions  map[string]jellyfinSession
}

type statusCapturingResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (w *statusCapturingResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *statusCapturingResponseWriter) Write(data []byte) (int, error) {
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	return w.ResponseWriter.Write(data)
}

type jellyfinSession struct {
	Token      string
	Client     string
	Device     string
	DeviceID   string
	Version    string
	UserID     string
	Username   string
	CreatedAt  time.Time
	LastSeenAt time.Time
}

func newRemoteAccessState() *remoteAccessState {
	return &remoteAccessState{
		jellyfinServerID: uuid.NewString(),
		sessions:         make(map[string]jellyfinSession),
	}
}

func (a *App) shutdown(_ context.Context) {
	if a == nil {
		return
	}
	a.shutdownOnce.Do(func() {
		a.scanMu.Lock()
		a.shuttingDown = true
		cancels := make([]context.CancelFunc, 0, len(a.activeScanIDs))
		for _, task := range a.activeScanIDs {
			if task != nil && task.cancel != nil {
				cancels = append(cancels, task.cancel)
			}
		}
		a.scanMu.Unlock()

		shutdownTimeout := a.shutdownTimeout
		if shutdownTimeout <= 0 {
			shutdownTimeout = 8 * time.Second
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if a.artworkCache != nil {
			a.artworkCache.BeginShutdown()
		}
		if a.appCancel != nil {
			a.appCancel()
		}
		for _, cancel := range cancels {
			cancel()
		}
		if a.thumbnailWorker != nil {
			a.thumbnailWorker.Stop()
		}
		waiters := 2
		results := make(chan error, 5)
		go func() {
			a.scanWG.Wait()
			results <- nil
		}()
		go func() {
			a.maintenanceWG.Wait()
			results <- nil
		}()
		if a.thumbnailWorker != nil {
			waiters++
			go func() { results <- a.thumbnailWorker.Shutdown(shutdownCtx) }()
		}
		if a.scanner != nil {
			waiters++
			go func() { results <- a.scanner.Shutdown(shutdownCtx) }()
		}
		if a.artworkCache != nil {
			waiters++
			go func() { results <- a.artworkCache.ShutdownContext(shutdownCtx) }()
		}

		for completed := 0; completed < waiters; completed++ {
			select {
			case err := <-results:
				if err != nil && a.logger != nil {
					a.logger.Warnf("background shutdown incomplete: %v", err)
				}
			case <-shutdownCtx.Done():
				if a.logger != nil {
					a.logger.Warnf("background shutdown timed out after %s; continuing application exit", shutdownTimeout)
				}
				completed = waiters
			}
		}

		if a.eventHub != nil {
			a.eventHub.Close()
		}
		a.shutdownDesktopIntegration()
		a.shutdownRemoteServices()
		if a.dbManager != nil {
			if err := a.dbManager.Close(); err != nil && a.logger != nil {
				a.logger.Warnf("database checkpoint or close failed: %v", err)
			}
		}
		if a.logger != nil {
			_ = a.logger.Sync()
		}
		if a.logFile != nil {
			_ = a.logFile.Close()
		}
	})
}

func (a *App) shutdownRemoteServices() {
	if a.remote == nil {
		return
	}

	a.remote.mu.Lock()
	defer a.remote.mu.Unlock()

	shutdownServer(a.remote.jellyfinServer)
	a.remote.jellyfinServer = nil
	a.resetJellyfinSessions()
}

func shutdownServer(server *http.Server) {
	if server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
}

func (a *App) validateRemoteSettings(settings *DesktopSettings) error {
	if settings == nil {
		return fmt.Errorf("settings is nil")
	}

	settings.RemoteBindHost = strings.TrimSpace(settings.RemoteBindHost)
	if settings.RemoteBindHost == "" {
		settings.RemoteBindHost = defaultRemoteBindHost
	}
	if strings.Contains(settings.RemoteBindHost, "://") {
		return fmt.Errorf("remote bind host must not include a scheme")
	}

	if settings.JellyfinPort <= 0 {
		settings.JellyfinPort = defaultJellyfinPort
	}
	if settings.JellyfinPort > 65535 {
		return fmt.Errorf("remote port must be between 1 and 65535")
	}
	if settings.JellyfinEnabled {
		if strings.TrimSpace(settings.RemoteUsername) == "" {
			return fmt.Errorf("remote username is required when remote access is enabled")
		}
		if strings.TrimSpace(settings.RemotePassword) == "" {
			return fmt.Errorf("remote password is required when remote access is enabled")
		}
	}
	if strings.TrimSpace(settings.JellyfinServerName) == "" {
		settings.JellyfinServerName = defaultJellyfinServerName
	}
	return nil
}

func (a *App) syncRemoteServices(settings *DesktopSettings) error {
	if settings == nil {
		return nil
	}
	if a.remote == nil {
		a.remote = newRemoteAccessState()
	}

	a.remote.mu.Lock()
	defer a.remote.mu.Unlock()

	shutdownServer(a.remote.jellyfinServer)
	a.remote.jellyfinServer = nil
	a.resetJellyfinSessions()

	if settings.JellyfinEnabled {
		server, err := a.buildJellyfinServer(settings)
		if err != nil {
			return err
		}
		a.remote.jellyfinServer = server
	}

	return nil
}

func (a *App) buildJellyfinServer(settings *DesktopSettings) (*http.Server, error) {
	addr := net.JoinHostPort(normalizeRemoteBindHost(settings.RemoteBindHost), strconv.Itoa(settings.JellyfinPort))
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen jellyfin on %s failed: %w", addr, err)
	}

	server := &http.Server{
		Addr:              addr,
		Handler:           a.newJellyfinMux(settings),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go a.serveRemoteServer("jellyfin", server, listener)
	return server, nil
}

func (a *App) serveRemoteServer(name string, server *http.Server, listener net.Listener) {
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		if a.logger != nil {
			a.logger.Warnf("%s server stopped unexpectedly: %v", name, err)
		}
	}
}

func normalizeRemoteBindHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return defaultRemoteBindHost
	}
	return host
}

func (a *App) newJellyfinMux(settings *DesktopSettings) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"Name":    settings.JellyfinServerName,
			"Message": "Navi Jellyfin compatible sidecar",
		})
	})
	mux.HandleFunc("GET /System/Info/Public", a.handleJellyfinPublicInfo(settings))
	mux.HandleFunc("GET /System/Info", a.handleJellyfinSystemInfo(settings))
	mux.HandleFunc("GET /System/Configuration", a.handleJellyfinSystemConfiguration(settings))
	mux.HandleFunc("GET /System/Ping", a.handleJellyfinPing())
	mux.HandleFunc("POST /System/Ping", a.handleJellyfinPing())
	mux.HandleFunc("GET /Branding/Configuration", a.handleJellyfinBrandingConfiguration())
	mux.HandleFunc("GET /Branding/Css", a.handleJellyfinBrandingCSS())
	mux.HandleFunc("GET /Branding/Css.css", a.handleJellyfinBrandingCSS())
	mux.HandleFunc("GET /Users/Public", a.handleJellyfinPublicUsers(settings))
	mux.HandleFunc("GET /Users", a.handleJellyfinUsers(settings))
	mux.HandleFunc("GET /Users/{userId}", a.handleJellyfinUser(settings))
	mux.HandleFunc("POST /Users/AuthenticateByName", a.handleJellyfinAuthenticate(settings))
	mux.HandleFunc("GET /Plugins", a.requireJellyfinAuth(a.handleJellyfinPlugins()))
	mux.HandleFunc("GET /Packages", a.requireJellyfinAuth(a.handleJellyfinPackages()))
	mux.HandleFunc("GET /Packages/{name}", a.requireJellyfinAuth(a.handleJellyfinPackageByName()))
	mux.HandleFunc("GET /Sessions", a.requireJellyfinAuth(a.handleJellyfinSessions()))
	mux.HandleFunc("GET /Library/VirtualFolders", a.requireJellyfinAuth(a.handleJellyfinVirtualFolders()))
	mux.HandleFunc("GET /UserViews", a.requireJellyfinAuth(a.handleJellyfinUserViews(settings)))
	mux.HandleFunc("GET /UserViews/GroupingOptions", a.requireJellyfinAuth(a.handleJellyfinUserViewGroupingOptions()))
	mux.HandleFunc("GET /Users/{userId}/Views", a.requireJellyfinAuth(a.handleJellyfinUserViews(settings)))
	mux.HandleFunc("GET /Users/{userId}/Items", a.requireJellyfinAuth(a.handleJellyfinItems(settings)))
	mux.HandleFunc("GET /Users/{userId}/Items/Resume", a.requireJellyfinAuth(a.handleJellyfinResumeItems(settings)))
	mux.HandleFunc("GET /Users/{userId}/Items/Latest", a.requireJellyfinAuth(a.handleJellyfinLatestItems(settings)))
	mux.HandleFunc("GET /Users/{userId}/Items/{itemId}", a.requireJellyfinAuth(a.handleJellyfinItem(settings)))
	mux.HandleFunc("GET /Items", a.requireJellyfinAuth(a.handleJellyfinItems(settings)))
	mux.HandleFunc("GET /Items/Latest", a.requireJellyfinAuth(a.handleJellyfinLatestItems(settings)))
	mux.HandleFunc("GET /Items/{itemId}", a.requireJellyfinAuth(a.handleJellyfinItem(settings)))
	mux.HandleFunc("GET /Items/{itemId}/Images/{imageType}", a.requireJellyfinAuth(a.handleJellyfinImage(settings)))
	mux.HandleFunc("HEAD /Items/{itemId}/Images/{imageType}", a.requireJellyfinAuth(a.handleJellyfinImage(settings)))
	mux.HandleFunc("GET /Items/{itemId}/PlaybackInfo", a.requireJellyfinAuth(a.handleJellyfinPlaybackInfo(settings)))
	mux.HandleFunc("POST /Items/{itemId}/PlaybackInfo", a.requireJellyfinAuth(a.handleJellyfinPlaybackInfo(settings)))
	mux.HandleFunc("GET /DisplayPreferences/{displayPreferencesId}", a.requireJellyfinAuth(a.handleJellyfinDisplayPreferences()))
	mux.HandleFunc("POST /DisplayPreferences/{displayPreferencesId}", a.requireJellyfinAuth(a.handleJellyfinUpdateDisplayPreferences()))
	mux.HandleFunc("GET /Videos/{itemId}/{streamName}", a.requireJellyfinAuth(a.handleJellyfinVideoStream(settings)))
	mux.HandleFunc("HEAD /Videos/{itemId}/{streamName}", a.requireJellyfinAuth(a.handleJellyfinVideoStream(settings)))
	mux.HandleFunc("POST /Sessions/Capabilities", a.requireJellyfinAuth(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("POST /Sessions/Capabilities/Full", a.requireJellyfinAuth(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("POST /Sessions/Playing", a.requireJellyfinAuth(a.handleJellyfinPlaying(settings)))
	mux.HandleFunc("POST /Sessions/Playing/Progress", a.requireJellyfinAuth(a.handleJellyfinPlayingProgress(settings)))
	mux.HandleFunc("POST /Sessions/Playing/Stopped", a.requireJellyfinAuth(a.handleJellyfinPlayingProgress(settings)))

	return a.logJellyfinRequests(a.wrapJellyfinCompatibility(mux, settings))
}

func (a *App) wrapJellyfinCompatibility(next http.Handler, settings *DesktopSettings) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		normalized := r.Clone(r.Context())
		normalized.URL = cloneURL(r.URL)
		normalized.URL.Path = normalizeJellyfinPath(normalized.URL.Path)
		normalized.RequestURI = normalized.URL.RequestURI()

		switch normalized.URL.Path {
		case "/":
			if normalized.Method == http.MethodPost || normalized.Method == http.MethodHead {
				writeJSON(w, http.StatusOK, map[string]any{
					"Name":    settings.JellyfinServerName,
					"Message": "Navi Jellyfin compatible sidecar",
				})
				return
			}
		case "/System/Info/Public":
			if normalized.Method == http.MethodPost || normalized.Method == http.MethodHead {
				a.handleJellyfinPublicInfo(settings)(w, normalized)
				return
			}
		case "/Users/Public":
			if normalized.Method == http.MethodPost || normalized.Method == http.MethodHead {
				a.handleJellyfinPublicUsers(settings)(w, normalized)
				return
			}
		case "/Branding/Configuration":
			if normalized.Method == http.MethodHead {
				a.handleJellyfinBrandingConfiguration()(w, normalized)
				return
			}
		}

		next.ServeHTTP(w, normalized)
	})
}

func (a *App) logJellyfinRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := &statusCapturingResponseWriter{ResponseWriter: w}
		startedAt := time.Now()
		next.ServeHTTP(recorder, r)
		statusCode := recorder.statusCode
		if statusCode == 0 {
			statusCode = http.StatusOK
		}
		a.appendRemoteAccessLog(fmt.Sprintf(
			"%s jellyfin %s %s status=%d ua=%q remote=%s duration_ms=%d",
			startedAt.UTC().Format(time.RFC3339),
			r.Method,
			r.URL.RequestURI(),
			statusCode,
			r.UserAgent(),
			r.RemoteAddr,
			time.Since(startedAt).Milliseconds(),
		))
	})
}

func (a *App) appendRemoteAccessLog(line string) {
	if a.remote == nil {
		return
	}
	a.remote.logMu.Lock()
	defer a.remote.logMu.Unlock()

	file, err := os.OpenFile(remoteAccessLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = io.WriteString(file, line+"\n")
}

func normalizeJellyfinPath(rawPath string) string {
	if rawPath == "" {
		return "/"
	}
	pathValue := strings.TrimSpace(rawPath)
	if strings.HasPrefix(strings.ToLower(pathValue), "/emby") {
		pathValue = pathValue[5:]
		if pathValue == "" {
			pathValue = "/"
		}
	}
	if pathValue != "/" {
		pathValue = strings.TrimRight(pathValue, "/")
		if pathValue == "" {
			pathValue = "/"
		}
	}
	return pathValue
}

func cloneURL(source *url.URL) *url.URL {
	if source == nil {
		return &url.URL{}
	}
	clone := *source
	return &clone
}

func (a *App) handleJellyfinPublicInfo(settings *DesktopSettings) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"LocalAddress":           "http://" + r.Host,
			"ServerName":             settings.JellyfinServerName,
			"Version":                jellyfinCompatVersion,
			"ProductName":            "Jellyfin Server",
			"OperatingSystem":        runtime.GOOS,
			"Id":                     a.remoteServerID(),
			"StartupWizardCompleted": true,
		})
	}
}

func (a *App) handleJellyfinSystemInfo(settings *DesktopSettings) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"LocalAddress":               "http://" + r.Host,
			"ServerName":                 settings.JellyfinServerName,
			"Version":                    jellyfinCompatVersion,
			"ProductName":                "Jellyfin Server",
			"OperatingSystem":            runtime.GOOS,
			"Id":                         a.remoteServerID(),
			"StartupWizardCompleted":     true,
			"OperatingSystemDisplayName": runtime.GOOS,
			"PackageName":                "navi-sidecar",
			"HasPendingRestart":          false,
			"IsShuttingDown":             false,
			"SupportsLibraryMonitor":     false,
			"WebSocketPortNumber":        0,
			"CompletedInstallations":     []any{},
			"CanSelfRestart":             false,
			"CanLaunchWebBrowser":        false,
			"ProgramDataPath":            "",
			"WebPath":                    "",
			"ItemsByNamePath":            "",
			"CachePath":                  "",
			"LogPath":                    "",
			"InternalMetadataPath":       "",
			"TranscodingTempPath":        "",
			"CastReceiverApplications":   []any{},
			"HasUpdateAvailable":         false,
			"EncoderLocation":            "",
			"SystemArchitecture":         runtime.GOARCH,
		})
	}
}

func (a *App) handleJellyfinSystemConfiguration(settings *DesktopSettings) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"ServerName":   settings.JellyfinServerName,
			"UICulture":    "zh-CN",
			"MetadataPath": "",
		})
	}
}

func (a *App) handleJellyfinPing() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}
}

func (a *App) handleJellyfinBrandingConfiguration() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"LoginDisclaimer":     "",
			"CustomCss":           "",
			"SplashscreenEnabled": false,
		})
	}
}

func (a *App) handleJellyfinPlugins() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, []map[string]any{})
	}
}

func (a *App) handleJellyfinPackages() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, []map[string]any{})
	}
}

func (a *App) handleJellyfinPackageByName() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		packageName := strings.TrimSpace(r.PathValue("name"))
		if packageName == "" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"name":        packageName,
			"description": "",
			"overview":    "",
			"owner":       "",
			"category":    "",
			"guid":        "",
			"versions":    []any{},
			"imageUrl":    "",
		})
	}
}

func (a *App) handleJellyfinSessions() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		a.remote.sessionMu.RLock()
		defer a.remote.sessionMu.RUnlock()

		sessions := make([]map[string]any, 0, len(a.remote.sessions))
		for _, session := range a.remote.sessions {
			sessions = append(sessions, a.jellyfinSessionDTO(session))
		}
		writeJSON(w, http.StatusOK, sessions)
	}
}

func (a *App) handleJellyfinVirtualFolders() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		libraries, err := a.repos.Library.List()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		result := make([]map[string]any, 0, len(libraries))
		for _, library := range libraries {
			library.HydratePathConfig()
			result = append(result, map[string]any{
				"Name":               library.Name,
				"Locations":          library.RootPaths(),
				"CollectionType":     jellyfinCollectionType(library.Type),
				"ItemId":             "library:" + library.ID,
				"PrimaryImageItemId": "",
				"RefreshProgress":    0,
			})
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func (a *App) handleJellyfinBrandingCSS() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = io.WriteString(w, "")
	}
}

func (a *App) handleJellyfinPublicUsers(settings *DesktopSettings) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, []map[string]any{a.jellyfinUserDTO(settings, time.Time{}, time.Time{})})
	}
}

func (a *App) handleJellyfinUsers(settings *DesktopSettings) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, []map[string]any{a.jellyfinUserDTO(settings, time.Time{}, time.Time{})})
	}
}

func (a *App) handleJellyfinUser(settings *DesktopSettings) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := r.PathValue("userId")
		if !a.isAuthorizedJellyfinUser(userID) {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, a.jellyfinUserDTO(settings, time.Time{}, time.Time{}))
	}
}

func (a *App) handleJellyfinUserViews(settings *DesktopSettings) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		items, err := a.listJellyfinItems("", false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		responseItems := make([]map[string]any, 0, len(items))
		for _, item := range items {
			dto, err := a.jellyfinItemDTO(settings, item)
			if err != nil {
				continue
			}
			responseItems = append(responseItems, dto)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"Items":            responseItems,
			"TotalRecordCount": len(responseItems),
			"StartIndex":       0,
		})
	}
}

func (a *App) handleJellyfinUserViewGroupingOptions() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := strings.TrimSpace(r.URL.Query().Get("userId"))
		if userID != "" && !a.isAuthorizedJellyfinUser(userID) {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, []map[string]any{})
	}
}

func (a *App) handleJellyfinAuthenticate(settings *DesktopSettings) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Username string `json:"Username"`
			Pw       string `json:"Pw"`
			Password string `json:"Password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return
		}

		password := payload.Pw
		if password == "" {
			password = payload.Password
		}
		if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(payload.Username)), []byte(strings.TrimSpace(settings.RemoteUsername))) != 1 ||
			subtle.ConstantTimeCompare([]byte(password), []byte(settings.RemotePassword)) != 1 {
			http.Error(w, "invalid credentials", http.StatusUnauthorized)
			return
		}

		token := randomToken()
		sessionInfo := jellyfinSession{
			Token:      token,
			UserID:     jellyfinUserID,
			Username:   settings.RemoteUsername,
			CreatedAt:  time.Now().UTC(),
			LastSeenAt: time.Now().UTC(),
		}
		applyAuthorizationMetadata(&sessionInfo, r)
		a.storeJellyfinSession(sessionInfo)

		writeJSON(w, http.StatusOK, map[string]any{
			"User":        a.jellyfinUserDTO(settings, sessionInfo.CreatedAt, sessionInfo.LastSeenAt),
			"SessionInfo": a.jellyfinSessionDTO(sessionInfo),
			"AccessToken": token,
			"ServerId":    a.remoteServerID(),
		})
	}
}

func (a *App) handleJellyfinItems(settings *DesktopSettings) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items, total, startIndex, err := a.queryJellyfinItems(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		responseItems := make([]map[string]any, 0, len(items))
		for _, item := range items {
			dto, err := a.jellyfinItemDTO(settings, item)
			if err != nil {
				continue
			}
			responseItems = append(responseItems, dto)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"Items":            responseItems,
			"TotalRecordCount": total,
			"StartIndex":       startIndex,
		})
	}
}

func (a *App) handleJellyfinResumeItems(settings *DesktopSettings) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items, total, startIndex, err := a.queryJellyfinResumeItems(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		responseItems := make([]map[string]any, 0, len(items))
		for _, item := range items {
			dto, err := a.jellyfinItemDTO(settings, item)
			if err == nil {
				responseItems = append(responseItems, dto)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"Items": responseItems, "TotalRecordCount": total, "StartIndex": startIndex,
		})
	}
}

func (a *App) handleJellyfinLatestItems(settings *DesktopSettings) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items, err := a.queryJellyfinLatestItems(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		responseItems := make([]map[string]any, 0, len(items))
		for _, item := range items {
			dto, err := a.jellyfinItemDTO(settings, item)
			if err == nil {
				responseItems = append(responseItems, dto)
			}
		}
		writeJSON(w, http.StatusOK, responseItems)
	}
}

func (a *App) handleJellyfinItem(settings *DesktopSettings) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		item, err := a.resolveJellyfinItem(r.PathValue("itemId"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		dto, err := a.jellyfinItemDTO(settings, item)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, dto)
	}
}

func (a *App) handleJellyfinImage(_ *DesktopSettings) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		item, err := a.resolveJellyfinItem(r.PathValue("itemId"))
		if err != nil {
			http.NotFound(w, r)
			return
		}

		imageType := strings.ToLower(strings.TrimSpace(r.PathValue("imageType")))
		imagePath := a.jellyfinImagePathForType(item, imageType)
		if imagePath == "" {
			http.NotFound(w, r)
			return
		}
		release, ok := a.reserveArtworkPath(imagePath)
		if !ok {
			http.NotFound(w, r)
			return
		}
		defer release()
		http.ServeFile(w, r, imagePath)
	}
}

func (a *App) handleJellyfinPlaybackInfo(settings *DesktopSettings) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		item, err := a.resolveJellyfinItem(r.PathValue("itemId"))
		if err != nil || item.Media == nil {
			http.NotFound(w, r)
			return
		}
		session, _ := a.jellyfinSessionFromRequest(r)
		mediaSource := a.jellyfinMediaSource(settings, item, session)
		writeJSON(w, http.StatusOK, map[string]any{
			"MediaSources":  []map[string]any{mediaSource},
			"PlaySessionId": session.DeviceID + "-" + item.ID,
		})
	}
}

func (a *App) handleJellyfinDisplayPreferences() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		displayPreferencesID := strings.TrimSpace(r.PathValue("displayPreferencesId"))
		clientName := strings.TrimSpace(r.URL.Query().Get("client"))
		if clientName == "" {
			clientName = "Infuse"
		}
		writeJSON(w, http.StatusOK, newJellyfinDisplayPreferences(displayPreferencesID, clientName))
	}
}

type jellyfinDisplayPreferences struct {
	ID                 string         `json:"Id"`
	Client             string         `json:"Client"`
	ViewType           string         `json:"ViewType"`
	SortBy             string         `json:"SortBy"`
	IndexBy            string         `json:"IndexBy"`
	RememberIndexing   bool           `json:"RememberIndexing"`
	PrimaryImageHeight int            `json:"PrimaryImageHeight"`
	PrimaryImageWidth  int            `json:"PrimaryImageWidth"`
	CustomPrefs        map[string]any `json:"CustomPrefs"`
	ScrollDirection    string         `json:"ScrollDirection"`
	ShowBackdrop       bool           `json:"ShowBackdrop"`
	RememberSorting    bool           `json:"RememberSorting"`
	SortOrder          string         `json:"SortOrder"`
	ShowSidebar        bool           `json:"ShowSidebar"`
}

func newJellyfinDisplayPreferences(displayPreferencesID string, clientName string) jellyfinDisplayPreferences {
	return jellyfinDisplayPreferences{
		ID:                 displayPreferencesID,
		Client:             clientName,
		ViewType:           "Primary",
		SortBy:             "SortName",
		IndexBy:            "None",
		RememberIndexing:   false,
		PrimaryImageHeight: 360,
		PrimaryImageWidth:  240,
		CustomPrefs:        map[string]any{},
		ScrollDirection:    "Horizontal",
		ShowBackdrop:       true,
		RememberSorting:    false,
		SortOrder:          "Ascending",
		ShowSidebar:        true,
	}
}

func (a *App) handleJellyfinUpdateDisplayPreferences() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (a *App) handleJellyfinVideoStream(_ *DesktopSettings) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		streamName := strings.ToLower(strings.TrimSpace(r.PathValue("streamName")))
		if streamName != "stream" && !strings.HasPrefix(streamName, "stream.") {
			http.NotFound(w, r)
			return
		}

		item, err := a.resolveJellyfinItem(r.PathValue("itemId"))
		if err != nil || item.Media == nil {
			http.NotFound(w, r)
			return
		}

		if streamURL := strings.TrimSpace(item.Media.StreamURL); streamURL != "" {
			http.Redirect(w, r, streamURL, http.StatusTemporaryRedirect)
			return
		}

		file, err := os.Open(item.Media.FilePath)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer file.Close()

		info, err := file.Stat()
		if err != nil {
			http.NotFound(w, r)
			return
		}

		if contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(item.Media.FilePath))); contentType != "" {
			w.Header().Set("Content-Type", contentType)
		} else {
			w.Header().Set("Content-Type", "application/octet-stream")
		}
		http.ServeContent(w, r, filepath.Base(item.Media.FilePath), info.ModTime(), file)
	}
}

func (a *App) handleJellyfinPlaying(_ *DesktopSettings) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			ItemID        string `json:"ItemId"`
			PositionTicks int64  `json:"PositionTicks"`
		}
		if err := decodeOptionalJSONBody(r, &payload); err != nil {
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return
		}
		if payload.ItemID != "" {
			_ = a.updateJellyfinPlaybackState(payload.ItemID, payload.PositionTicks, false)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (a *App) handleJellyfinPlayingProgress(_ *DesktopSettings) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			ItemID        string `json:"ItemId"`
			PositionTicks int64  `json:"PositionTicks"`
		}
		if err := decodeOptionalJSONBody(r, &payload); err != nil {
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return
		}
		if payload.ItemID != "" {
			_ = a.updateJellyfinPlaybackState(payload.ItemID, payload.PositionTicks, strings.HasSuffix(r.URL.Path, "/Stopped"))
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (a *App) updateJellyfinPlaybackState(itemID string, positionTicks int64, stopped bool) error {
	item, err := a.resolveJellyfinItem(itemID)
	if err != nil || item.Media == nil {
		return err
	}

	durationTicks := mediaDurationTicks(item.Media)
	positionSeconds := ticksToSeconds(positionTicks)
	durationSeconds := ticksToSeconds(durationTicks)
	completed := stopped && durationTicks > 0 && positionTicks >= int64(float64(durationTicks)*0.9)

	return a.db.Transaction(func(tx *gorm.DB) error {
		var histories []model.WatchHistory
		if err := tx.Where("user_id = ? AND media_id = ?", desktopUserID, item.Media.ID).
			Order("updated_at DESC, created_at DESC").
			Find(&histories).Error; err != nil {
			return err
		}

		if len(histories) > 0 {
			history := histories[0]
			history.Position = positionSeconds
			history.Duration = durationSeconds
			if completed {
				history.Completed = true
			}
			if err := tx.Save(&history).Error; err != nil {
				return err
			}
			if len(histories) <= 1 {
				return nil
			}
			duplicateIDs := make([]string, 0, len(histories)-1)
			for _, duplicate := range histories[1:] {
				duplicateIDs = append(duplicateIDs, duplicate.ID)
			}
			return tx.Where("id IN ?", duplicateIDs).Delete(&model.WatchHistory{}).Error
		}

		history := model.WatchHistory{
			ID:        stableWatchHistoryID(desktopUserID, item.Media.ID),
			UserID:    desktopUserID,
			MediaID:   item.Media.ID,
			Position:  positionSeconds,
			Duration:  durationSeconds,
			Completed: completed,
		}
		return tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "id"}},
			DoUpdates: clause.Assignments(map[string]interface{}{
				"position":   positionSeconds,
				"duration":   durationSeconds,
				"completed":  gorm.Expr("completed OR ?", completed),
				"updated_at": time.Now(),
			}),
		}).Create(&history).Error
	})
}

func stableWatchHistoryID(userID string, mediaID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("watch-history:"+userID+":"+mediaID)).String()
}

type jellyfinResolvedItem struct {
	ID           string
	Kind         string
	Library      *model.Library
	Series       *model.Series
	Media        *model.Media
	UserData     map[string]any
	CountsLoaded bool
}

func (a *App) resolveJellyfinItem(id string) (*jellyfinResolvedItem, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("item id is empty")
	}

	switch {
	case strings.HasPrefix(id, "library:"):
		library, err := a.repos.Library.FindByID(strings.TrimPrefix(id, "library:"))
		if err != nil {
			return nil, err
		}
		return &jellyfinResolvedItem{ID: id, Kind: "library", Library: library}, nil
	case strings.HasPrefix(id, "series:"):
		series, err := a.repos.Series.FindByIDOnly(strings.TrimPrefix(id, "series:"))
		if err != nil {
			return nil, err
		}
		return &jellyfinResolvedItem{ID: id, Kind: "series", Series: series}, nil
	case strings.HasPrefix(id, "media:"):
		media, err := a.repos.Media.FindByID(strings.TrimPrefix(id, "media:"))
		if err != nil {
			return nil, err
		}
		return &jellyfinResolvedItem{ID: id, Kind: "media", Media: media}, nil
	}

	if library, err := a.repos.Library.FindByID(id); err == nil {
		return &jellyfinResolvedItem{ID: "library:" + library.ID, Kind: "library", Library: library}, nil
	}
	if series, err := a.repos.Series.FindByIDOnly(id); err == nil {
		return &jellyfinResolvedItem{ID: "series:" + series.ID, Kind: "series", Series: series}, nil
	}
	if media, err := a.repos.Media.FindByID(id); err == nil {
		return &jellyfinResolvedItem{ID: "media:" + media.ID, Kind: "media", Media: media}, nil
	}
	return nil, gorm.ErrRecordNotFound
}

func (a *App) queryJellyfinItems(r *http.Request) ([]*jellyfinResolvedItem, int, int, error) {
	return a.queryJellyfinItemsSQL(r)
}

func (a *App) listJellyfinItems(parentID string, recursive bool) ([]*jellyfinResolvedItem, error) {
	parentID = strings.TrimSpace(parentID)
	if parentID == "" {
		libraries, err := a.repos.Library.List()
		if err != nil {
			return nil, err
		}
		items := make([]*jellyfinResolvedItem, 0, len(libraries))
		for i := range libraries {
			library := libraries[i]
			library.HydratePathConfig()
			items = append(items, &jellyfinResolvedItem{
				ID:      "library:" + library.ID,
				Kind:    "library",
				Library: &library,
			})
		}
		if !recursive {
			return items, nil
		}
		var recursiveItems []*jellyfinResolvedItem
		for _, item := range items {
			children, err := a.listJellyfinItems(item.ID, true)
			if err != nil {
				return nil, err
			}
			recursiveItems = append(recursiveItems, children...)
		}
		return recursiveItems, nil
	}

	parent, err := a.resolveJellyfinItem(parentID)
	if err != nil {
		return nil, err
	}

	switch parent.Kind {
	case "library":
		return a.listJellyfinLibraryItems(parent.Library, recursive)
	case "series":
		episodes, err := a.repos.Media.ListBySeriesID(parent.Series.ID)
		if err != nil {
			return nil, err
		}
		items := make([]*jellyfinResolvedItem, 0, len(episodes))
		for i := range episodes {
			episode := episodes[i]
			items = append(items, &jellyfinResolvedItem{
				ID:    "media:" + episode.ID,
				Kind:  "media",
				Media: &episode,
			})
		}
		return items, nil
	default:
		return nil, nil
	}
}

func (a *App) listJellyfinLibraryItems(library *model.Library, recursive bool) ([]*jellyfinResolvedItem, error) {
	if library == nil {
		return nil, nil
	}

	seriesList, err := a.repos.Series.ListByLibraryID(library.ID)
	if err != nil {
		return nil, err
	}
	mediaList, err := a.repos.Media.ListByLibraryID(library.ID)
	if err != nil {
		return nil, err
	}

	isSeriesLibrary := libraryAllowsSeries(library.Type)
	isMovieLibrary := libraryAllowsMovies(library.Type)
	items := make([]*jellyfinResolvedItem, 0, len(seriesList)+len(mediaList))
	if isSeriesLibrary {
		for i := range seriesList {
			series := seriesList[i]
			items = append(items, &jellyfinResolvedItem{
				ID:     "series:" + series.ID,
				Kind:   "series",
				Series: &series,
			})
		}
	}
	if isMovieLibrary {
		for i := range mediaList {
			media := mediaList[i]
			if media.SeriesID != "" {
				continue
			}
			items = append(items, &jellyfinResolvedItem{
				ID:    "media:" + media.ID,
				Kind:  "media",
				Media: &media,
			})
		}
	}
	if !recursive {
		return items, nil
	}
	if !isSeriesLibrary {
		return items, nil
	}
	for i := range seriesList {
		episodes, err := a.repos.Media.ListBySeriesID(seriesList[i].ID)
		if err != nil {
			return nil, err
		}
		for j := range episodes {
			episode := episodes[j]
			items = append(items, &jellyfinResolvedItem{
				ID:    "media:" + episode.ID,
				Kind:  "media",
				Media: &episode,
			})
		}
	}
	return items, nil
}

func libraryAllowsSeries(libraryType string) bool {
	libraryType = strings.ToLower(strings.TrimSpace(libraryType))
	if libraryType == "" {
		return true
	}
	return strings.Contains(libraryType, "tv") || strings.Contains(libraryType, "series") || strings.Contains(libraryType, "mixed")
}

func libraryAllowsMovies(libraryType string) bool {
	libraryType = strings.ToLower(strings.TrimSpace(libraryType))
	if libraryType == "" {
		return true
	}
	return strings.Contains(libraryType, "movie") || strings.Contains(libraryType, "mixed") || strings.Contains(libraryType, "other")
}

func (a *App) jellyfinItemDTO(settings *DesktopSettings, item *jellyfinResolvedItem) (map[string]any, error) {
	if item == nil {
		return nil, fmt.Errorf("item is nil")
	}
	switch item.Kind {
	case "library":
		if item.CountsLoaded {
			return a.jellyfinLibraryDTOWithChildCount(item.Library, item.Library.MediaCount), nil
		}
		return a.jellyfinLibraryDTO(settings, item.Library), nil
	case "series":
		if !item.CountsLoaded {
			if err := a.hydrateJellyfinItems([]*jellyfinResolvedItem{item}); err != nil {
				return nil, err
			}
		}
		return a.jellyfinSeriesDTO(settings, item.Series)
	case "media":
		return a.jellyfinMediaDTOWithUserData(settings, item.Media, item.UserData)
	default:
		return nil, fmt.Errorf("unsupported item kind")
	}
}

func (a *App) jellyfinLibraryDTO(_ *DesktopSettings, library *model.Library) map[string]any {
	library.HydratePathConfig()
	childCount := 0
	if libraryAllowsSeries(library.Type) {
		if count, err := a.repos.Series.CountByLibrary(library.ID); err == nil {
			childCount += int(count)
		}
	}
	if libraryAllowsMovies(library.Type) {
		if media, _, err := a.repos.Media.ListNonEpisode(1, 1000000, library.ID); err == nil {
			childCount += len(media)
		}
	}
	return a.jellyfinLibraryDTOWithChildCount(library, childCount)
}

func (a *App) jellyfinLibraryDTOWithChildCount(library *model.Library, childCount int) map[string]any {
	return map[string]any{
		"Name":                      library.Name,
		"ServerId":                  a.remoteServerID(),
		"Id":                        "library:" + library.ID,
		"Type":                      "CollectionFolder",
		"CollectionType":            jellyfinCollectionType(library.Type),
		"IsFolder":                  true,
		"RecursiveItemCount":        childCount,
		"ChildCount":                childCount,
		"DateCreated":               library.CreatedAt.UTC().Format(time.RFC3339),
		"PreferredMetadataLanguage": "zh-CN",
	}
}

func (a *App) jellyfinSeriesDTO(_ *DesktopSettings, series *model.Series) (map[string]any, error) {
	if series == nil {
		return nil, fmt.Errorf("series is nil")
	}
	dto := map[string]any{
		"Name":               series.Title,
		"OriginalTitle":      series.OrigTitle,
		"ServerId":           a.remoteServerID(),
		"Id":                 "series:" + series.ID,
		"Type":               "Series",
		"IsFolder":           true,
		"Overview":           series.Overview,
		"ProductionYear":     series.Year,
		"DateCreated":        series.CreatedAt.UTC().Format(time.RFC3339),
		"ChildCount":         series.EpisodeCount,
		"RecursiveItemCount": series.EpisodeCount,
		"Genres":             splitCSV(series.Genres),
		"SortName":           series.Title,
	}
	if series.PosterPath != "" {
		dto["ImageTags"] = map[string]string{"Primary": imageTag(series.PosterPath, series.UpdatedAt)}
	}
	if series.BackdropPath != "" {
		dto["BackdropImageTags"] = []string{imageTag(series.BackdropPath, series.UpdatedAt)}
	}
	return dto, nil
}

func (a *App) jellyfinMediaDTO(settings *DesktopSettings, media *model.Media) (map[string]any, error) {
	return a.jellyfinMediaDTOWithUserData(settings, media, nil)
}

func (a *App) jellyfinMediaDTOWithUserData(settings *DesktopSettings, media *model.Media, prefetchedUserData map[string]any) (map[string]any, error) {
	if media == nil {
		return nil, fmt.Errorf("media is nil")
	}
	itemType := "Movie"
	if media.SeriesID != "" {
		itemType = "Episode"
	}

	dto := map[string]any{
		"Name":            mediaDisplayName(media),
		"OriginalTitle":   media.OrigTitle,
		"ServerId":        a.remoteServerID(),
		"Id":              "media:" + media.ID,
		"Type":            itemType,
		"IsFolder":        false,
		"CanDownload":     true,
		"Overview":        media.Overview,
		"ProductionYear":  media.Year,
		"DateCreated":     media.CreatedAt.UTC().Format(time.RFC3339),
		"Container":       mediaContainer(media),
		"MediaType":       "Video",
		"RunTimeTicks":    mediaDurationTicks(media),
		"HasSubtitles":    strings.TrimSpace(media.SubtitlePaths) != "",
		"VideoType":       "VideoFile",
		"LocationType":    jellyfinLocationType(media),
		"Genres":          splitCSV(media.Genres),
		"CommunityRating": media.Rating,
		"SortName":        mediaDisplayName(media),
		"MediaSources":    []map[string]any{a.jellyfinMediaSource(settings, &jellyfinResolvedItem{ID: "media:" + media.ID, Kind: "media", Media: media}, jellyfinSession{})},
	}
	if media.VideoCodec != "" {
		dto["VideoCodec"] = media.VideoCodec
	}
	if media.AudioCodec != "" {
		dto["AudioCodec"] = media.AudioCodec
	}
	if media.FilePath != "" {
		dto["Path"] = media.FilePath
	}
	if media.StreamURL != "" {
		dto["Path"] = media.StreamURL
	}
	if media.PosterPath != "" {
		dto["ImageTags"] = map[string]string{"Primary": imageTag(media.PosterPath, media.UpdatedAt)}
	}
	if media.BackdropPath != "" {
		dto["BackdropImageTags"] = []string{imageTag(media.BackdropPath, media.UpdatedAt)}
	}
	if media.SeriesID != "" {
		dto["SeriesId"] = "series:" + media.SeriesID
		dto["IndexNumber"] = media.EpisodeNum
		dto["ParentIndexNumber"] = media.SeasonNum
		if media.EpisodeTitle != "" {
			dto["EpisodeTitle"] = media.EpisodeTitle
		}
		series := media.Series
		if prefetchedUserData == nil && (series == nil || strings.TrimSpace(series.Title) == "") {
			series, _ = a.repos.Series.FindByIDOnly(media.SeriesID)
		}
		if series != nil && strings.TrimSpace(series.Title) != "" {
			dto["SeriesName"] = series.Title
			dto["SeasonName"] = fmt.Sprintf("Season %d", media.SeasonNum)
		}
	}
	if prefetchedUserData != nil {
		dto["UserData"] = prefetchedUserData
	} else if userData, err := a.jellyfinUserData(media.ID); err == nil {
		dto["UserData"] = userData
	}
	return dto, nil
}

func mediaDisplayName(media *model.Media) string {
	if media == nil {
		return ""
	}
	if strings.TrimSpace(media.EpisodeTitle) != "" {
		return media.EpisodeTitle
	}
	if strings.TrimSpace(media.Title) != "" {
		return media.Title
	}
	return filepath.Base(media.FilePath)
}

func mediaContainer(media *model.Media) string {
	if media == nil {
		return ""
	}
	if media.StreamURL != "" {
		if ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(media.StreamURL)), "."); ext != "" {
			return ext
		}
	}
	return strings.TrimPrefix(strings.ToLower(filepath.Ext(media.FilePath)), ".")
}

func jellyfinLocationType(media *model.Media) string {
	if media == nil {
		return "FileSystem"
	}
	if strings.TrimSpace(media.StreamURL) != "" {
		return "Remote"
	}
	return "FileSystem"
}

func mediaDurationTicks(media *model.Media) int64 {
	if media == nil {
		return 0
	}
	if media.Duration > 0 {
		return int64(media.Duration * float64(jellyfinTicksPerSecond))
	}
	if media.Runtime > 0 {
		return int64(media.Runtime) * 60 * jellyfinTicksPerSecond
	}
	return 0
}

func ticksToSeconds(ticks int64) float64 {
	if ticks <= 0 {
		return 0
	}
	return float64(ticks) / float64(jellyfinTicksPerSecond)
}

func jellyfinCollectionType(libraryType string) string {
	switch {
	case libraryAllowsSeries(libraryType) && !libraryAllowsMovies(libraryType):
		return "tvshows"
	case libraryAllowsMovies(libraryType) && !libraryAllowsSeries(libraryType):
		return "movies"
	default:
		return "mixed"
	}
}

func (a *App) jellyfinImagePathForType(item *jellyfinResolvedItem, imageType string) string {
	if item == nil {
		return ""
	}
	switch item.Kind {
	case "series":
		if imageType == "backdrop" {
			return item.Series.BackdropPath
		}
		return item.Series.PosterPath
	case "media":
		if imageType == "backdrop" {
			return item.Media.BackdropPath
		}
		return item.Media.PosterPath
	default:
		return ""
	}
}

func imageTag(filePath string, fallback time.Time) string {
	if info, err := os.Stat(filePath); err == nil {
		return strconv.FormatInt(info.ModTime().Unix(), 10)
	}
	if !fallback.IsZero() {
		return strconv.FormatInt(fallback.Unix(), 10)
	}
	return "1"
}

func (a *App) jellyfinMediaSource(settings *DesktopSettings, item *jellyfinResolvedItem, session jellyfinSession) map[string]any {
	media := item.Media
	source := map[string]any{
		"Protocol":             "File",
		"Id":                   media.ID,
		"Container":            mediaContainer(media),
		"Size":                 media.FileSize,
		"Name":                 mediaDisplayName(media),
		"IsRemote":             strings.TrimSpace(media.StreamURL) != "",
		"RunTimeTicks":         mediaDurationTicks(media),
		"SupportsTranscoding":  false,
		"SupportsDirectStream": true,
		"SupportsDirectPlay":   true,
		"VideoType":            "VideoFile",
		"MediaStreams":         []map[string]any{},
	}
	if session.Token != "" {
		source["RequiredHttpHeaders"] = map[string]string{
			"X-MediaBrowser-Token": session.Token,
		}
	}
	if media.FilePath != "" {
		source["Path"] = media.FilePath
	}
	if media.StreamURL != "" {
		source["Path"] = media.StreamURL
		source["Protocol"] = "Http"
	}
	if media.VideoCodec != "" || media.AudioCodec != "" || media.Resolution != "" {
		streams := make([]map[string]any, 0, 2)
		if media.VideoCodec != "" || media.Resolution != "" {
			videoStream := map[string]any{
				"Type":  "Video",
				"Codec": media.VideoCodec,
			}
			if width, height := parseResolution(media.Resolution); width > 0 && height > 0 {
				videoStream["Width"] = width
				videoStream["Height"] = height
			}
			streams = append(streams, videoStream)
		}
		if media.AudioCodec != "" {
			streams = append(streams, map[string]any{
				"Type":  "Audio",
				"Codec": media.AudioCodec,
			})
		}
		if strings.TrimSpace(media.SubtitlePaths) != "" {
			streams = append(streams, map[string]any{
				"Type":       "Subtitle",
				"IsExternal": true,
			})
		}
		source["MediaStreams"] = streams
	}
	return source
}

func parseResolution(value string) (int, int) {
	value = strings.TrimSpace(strings.ToLower(value))
	switch value {
	case "4k", "2160p":
		return 3840, 2160
	case "1440p":
		return 2560, 1440
	case "1080p":
		return 1920, 1080
	case "720p":
		return 1280, 720
	case "480p":
		return 854, 480
	default:
		return 0, 0
	}
}

func (a *App) jellyfinUserData(mediaID string) (map[string]any, error) {
	result := map[string]any{
		"ItemId":                "media:" + mediaID,
		"Key":                   mediaID,
		"IsFavorite":            false,
		"Played":                false,
		"PlaybackPositionTicks": int64(0),
		"PlayCount":             0,
	}

	var favorite model.Favorite
	if err := a.db.Where("user_id = ? AND media_id = ?", desktopUserID, mediaID).First(&favorite).Error; err == nil {
		result["IsFavorite"] = true
	}

	var history model.WatchHistory
	if err := a.db.Where("user_id = ? AND media_id = ?", desktopUserID, mediaID).First(&history).Error; err == nil {
		result["Played"] = history.Completed
		result["PlaybackPositionTicks"] = int64(history.Position * float64(jellyfinTicksPerSecond))
		if history.Completed {
			result["PlayCount"] = 1
		}
		if history.Duration > 0 && history.Position > 0 {
			result["PlayedPercentage"] = minFloat64(100, history.Position/history.Duration*100)
		}
		if !history.UpdatedAt.IsZero() {
			result["LastPlayedDate"] = history.UpdatedAt.UTC().Format(time.RFC3339)
		}
	}
	return result, nil
}

func minFloat64(left float64, right float64) float64 {
	if left < right {
		return left
	}
	return right
}

func sortJellyfinItems(items []*jellyfinResolvedItem, sortBy []string, sortOrder []string) {
	orderDesc := len(sortOrder) > 0 && strings.EqualFold(strings.TrimSpace(sortOrder[0]), "descending")
	field := ""
	if len(sortBy) > 0 {
		field = strings.ToLower(strings.TrimSpace(sortBy[0]))
	}
	sort.SliceStable(items, func(i int, j int) bool {
		left := items[i]
		right := items[j]
		compare := compareJellyfinItems(left, right, field)
		if orderDesc {
			return compare > 0
		}
		return compare < 0
	})
}

func compareJellyfinItems(left *jellyfinResolvedItem, right *jellyfinResolvedItem, field string) int {
	switch field {
	case "datecreated":
		return compareTime(jellyfinItemCreatedAt(left), jellyfinItemCreatedAt(right))
	case "productionyear":
		return compareInt(jellyfinItemYear(left), jellyfinItemYear(right))
	default:
		return strings.Compare(strings.ToLower(jellyfinItemName(left)), strings.ToLower(jellyfinItemName(right)))
	}
}

func compareTime(left time.Time, right time.Time) int {
	switch {
	case left.Before(right):
		return -1
	case left.After(right):
		return 1
	default:
		return 0
	}
}

func compareInt(left int, right int) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

func jellyfinItemCreatedAt(item *jellyfinResolvedItem) time.Time {
	switch item.Kind {
	case "library":
		return item.Library.CreatedAt
	case "series":
		return item.Series.CreatedAt
	case "media":
		return item.Media.CreatedAt
	default:
		return time.Time{}
	}
}

func jellyfinItemYear(item *jellyfinResolvedItem) int {
	switch item.Kind {
	case "library":
		return 0
	case "series":
		return item.Series.Year
	case "media":
		return item.Media.Year
	default:
		return 0
	}
}

func jellyfinItemTypeName(item *jellyfinResolvedItem) string {
	switch item.Kind {
	case "library":
		return "CollectionFolder"
	case "series":
		return "Series"
	case "media":
		if item.Media.SeriesID != "" {
			return "Episode"
		}
		return "Movie"
	default:
		return ""
	}
}

func jellyfinItemName(item *jellyfinResolvedItem) string {
	switch item.Kind {
	case "library":
		return item.Library.Name
	case "series":
		return item.Series.Title
	case "media":
		return mediaDisplayName(item.Media)
	default:
		return ""
	}
}

func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		result = append(result, part)
	}
	return result
}

func toSet(values []string) map[string]bool {
	if len(values) == 0 {
		return nil
	}
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[strings.ToLower(strings.TrimSpace(value))] = true
	}
	return set
}

func parseBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func parsePositiveInt(value string) int {
	parsed, _ := strconv.Atoi(strings.TrimSpace(value))
	if parsed < 0 {
		return 0
	}
	return parsed
}

func (a *App) requireJellyfinAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session, ok := a.jellyfinSessionFromRequest(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		a.touchJellyfinSession(session.Token)
		next(w, r)
	}
}

func (a *App) jellyfinSessionFromRequest(r *http.Request) (jellyfinSession, bool) {
	token := strings.TrimSpace(r.URL.Query().Get("api_key"))
	if token == "" {
		for _, headerName := range []string{
			"X-Emby-Token",
			"X-MediaBrowser-Token",
			"X-MediaBrowser-AccessToken",
		} {
			if candidate := strings.TrimSpace(r.Header.Get(headerName)); candidate != "" {
				token = candidate
				break
			}
		}
	}
	if token == "" {
		token = parseAuthorizationToken(r.Header.Get("X-Emby-Authorization"))
	}
	if token == "" {
		token = parseAuthorizationToken(r.Header.Get("Authorization"))
	}
	if token == "" {
		return jellyfinSession{}, false
	}

	a.remote.sessionMu.RLock()
	defer a.remote.sessionMu.RUnlock()
	session, ok := a.remote.sessions[token]
	return session, ok
}

func parseAuthorizationToken(headerValue string) string {
	headerValue = strings.TrimSpace(headerValue)
	if headerValue == "" {
		return ""
	}
	pairs := strings.Split(headerValue, ",")
	for _, pair := range pairs {
		part := strings.TrimSpace(pair)
		if strings.HasPrefix(strings.ToLower(part), "mediabrowser ") || strings.HasPrefix(strings.ToLower(part), "emby ") {
			part = strings.TrimSpace(part[strings.Index(part, " ")+1:])
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(kv[0]))
		value := strings.Trim(strings.TrimSpace(kv[1]), `"`)
		if key == "token" {
			return value
		}
	}
	return ""
}

func applyAuthorizationMetadata(session *jellyfinSession, r *http.Request) {
	if session == nil {
		return
	}
	headerValue := strings.TrimSpace(r.Header.Get("X-Emby-Authorization"))
	if headerValue == "" {
		headerValue = strings.TrimSpace(r.Header.Get("Authorization"))
	}
	if headerValue == "" {
		return
	}
	for _, pair := range strings.Split(headerValue, ",") {
		part := strings.TrimSpace(pair)
		if strings.HasPrefix(strings.ToLower(part), "mediabrowser ") || strings.HasPrefix(strings.ToLower(part), "emby ") {
			part = strings.TrimSpace(part[strings.Index(part, " ")+1:])
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(kv[0]))
		value := strings.Trim(strings.TrimSpace(kv[1]), `"`)
		switch key {
		case "client":
			session.Client = value
		case "device":
			session.Device = value
		case "deviceid":
			session.DeviceID = value
		case "version":
			session.Version = value
		}
	}
}

func (a *App) storeJellyfinSession(session jellyfinSession) {
	a.remote.sessionMu.Lock()
	defer a.remote.sessionMu.Unlock()
	a.remote.sessions[session.Token] = session
}

func (a *App) touchJellyfinSession(token string) {
	a.remote.sessionMu.Lock()
	defer a.remote.sessionMu.Unlock()
	session, ok := a.remote.sessions[token]
	if !ok {
		return
	}
	session.LastSeenAt = time.Now().UTC()
	a.remote.sessions[token] = session
}

func (a *App) resetJellyfinSessions() {
	if a.remote == nil {
		return
	}
	a.remote.sessionMu.Lock()
	defer a.remote.sessionMu.Unlock()
	a.remote.sessions = make(map[string]jellyfinSession)
}

func (a *App) remoteServerID() string {
	if a.remote == nil || a.remote.jellyfinServerID == "" {
		if a.remote == nil {
			a.remote = newRemoteAccessState()
		}
		a.remote.jellyfinServerID = uuid.NewString()
	}
	return a.remote.jellyfinServerID
}

func (a *App) jellyfinUserDTO(settings *DesktopSettings, lastLogin time.Time, lastActivity time.Time) map[string]any {
	dto := map[string]any{
		"Name":                      settings.RemoteUsername,
		"ServerId":                  a.remoteServerID(),
		"ServerName":                settings.JellyfinServerName,
		"Id":                        jellyfinUserID,
		"HasPassword":               true,
		"HasConfiguredPassword":     true,
		"HasConfiguredEasyPassword": false,
		"EnableAutoLogin":           false,
		"Configuration":             map[string]any{},
		"Policy": map[string]any{
			"IsAdministrator":            true,
			"EnableMediaPlayback":        true,
			"EnableContentDeletion":      false,
			"EnableRemoteAccess":         true,
			"EnableCollectionManagement": false,
		},
	}
	if !lastLogin.IsZero() {
		dto["LastLoginDate"] = lastLogin.UTC().Format(time.RFC3339)
	}
	if !lastActivity.IsZero() {
		dto["LastActivityDate"] = lastActivity.UTC().Format(time.RFC3339)
	}
	return dto
}

func (a *App) jellyfinSessionDTO(session jellyfinSession) map[string]any {
	dto := map[string]any{
		"Id":                 session.Token,
		"UserId":             session.UserID,
		"UserName":           session.Username,
		"Client":             session.Client,
		"DeviceName":         session.Device,
		"DeviceId":           session.DeviceID,
		"ApplicationVersion": session.Version,
		"LastActivityDate":   session.LastSeenAt.UTC().Format(time.RFC3339),
	}
	if dto["DeviceName"] == "" {
		dto["DeviceName"] = "Infuse"
	}
	if dto["DeviceId"] == "" {
		dto["DeviceId"] = session.Token
	}
	if dto["ApplicationVersion"] == "" {
		dto["ApplicationVersion"] = "1.0"
	}
	return dto
}

func (a *App) isAuthorizedJellyfinUser(userID string) bool {
	userID = strings.TrimSpace(userID)
	return userID == "" || userID == jellyfinUserID
}

func randomToken() string {
	buffer := make([]byte, 24)
	if _, err := rand.Read(buffer); err == nil {
		return hex.EncodeToString(buffer)
	}
	return uuid.NewString()
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func decodeOptionalJSONBody(r *http.Request, target interface{}) error {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil
	}
	return json.Unmarshal(body, target)
}
