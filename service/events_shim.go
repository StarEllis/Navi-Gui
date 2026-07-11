package service

import (
	"context"
	"sync"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// ScanEvent represents the type of scan progress event.
const (
	EventScanStarted          = "scan:start"
	EventScanProgress         = "scan:progress"
	EventScanCompleted        = "scan:completed"
	EventScanIncomplete       = "scan:incomplete"
	EventScanFailed           = "scan:failed"
	EventScanCanceled         = "scan:canceled"
	EventThumbnailPending     = "thumbnail:pending"
	EventThumbnailRunning     = "thumbnail:running"
	EventThumbnailCompleted   = "thumbnail:completed"
	EventThumbnailFailed      = "thumbnail:failed"
	EventThumbnailCanceled    = "thumbnail:canceled"
	EventMediaMetadataUpdated = "media:metadata-updated"
	EventMediaStateUpdated    = "media:state-updated"
)

// ScanProgressData holds the payload for a scan progress event.
type ScanProgressData struct {
	TaskID       string `json:"task_id"`
	LibraryID    string `json:"library_id"`
	LibraryName  string `json:"library_name"`
	Mode         string `json:"mode"`
	Status       string `json:"status"`
	Phase        string `json:"phase"`
	FailureStage string `json:"failure_stage,omitempty"`
	Retryable    bool   `json:"retryable"`
	Current      int    `json:"current"`
	Total        int    `json:"total"`
	NewFound     int    `json:"new_found"`
	Cleaned      int    `json:"cleaned"`
	Message      string `json:"message"`
}

type ThumbnailTaskEventData struct {
	TaskID    string `json:"task_id"`
	MediaID   string `json:"media_id"`
	LibraryID string `json:"library_id"`
	Path      string `json:"path"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	Phase     string `json:"phase"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type MediaMetadataEventData struct {
	MediaID       string `json:"media_id"`
	LibraryID     string `json:"library_id"`
	MetadataPhase string `json:"metadata_phase"`
	Message       string `json:"message"`
}

type MediaStateEventData struct {
	MediaID    string `json:"media_id"`
	IsWatched  *bool  `json:"is_watched,omitempty"`
	IsFavorite *bool  `json:"is_favorite,omitempty"`
}

// WSHub provides a shim for the original WebSocket hub.
// It proxies broadcast events to Wails runtime.EventsEmit.
type WSHub struct {
	ctx    context.Context
	mu     sync.RWMutex
	closed bool
}

// NewWSHub creates a new WSHub shim.
func NewWSHub(ctx context.Context) *WSHub {
	return &WSHub{
		ctx: ctx,
	}
}

// BroadcastEvent proxies to Wails runtime.EventsEmit
func (w *WSHub) BroadcastEvent(eventType string, data interface{}) {
	if w == nil {
		return
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	if !w.closed && w.ctx != nil {
		runtime.EventsEmit(w.ctx, eventType, data)
	}
}

func (w *WSHub) Close() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
}
