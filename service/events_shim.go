package service

import (
	"context"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// ScanEvent represents the type of scan progress event.
const (
	EventScanStarted          = "scan:started"
	EventScanProgress         = "scan:progress"
	EventScanCompleted        = "scan:completed"
	EventScanIncomplete       = "scan:incomplete"
	EventScanFailed           = "scan:failed"
	EventMediaMetadataUpdated = "media:metadata-updated"
)

// ScanProgressData holds the payload for a scan progress event.
type ScanProgressData struct {
	LibraryID   string `json:"library_id"`
	LibraryName string `json:"library_name"`
	Mode        string `json:"mode"`
	Phase       string `json:"phase"`
	Current     int    `json:"current"`
	Total       int    `json:"total"`
	NewFound    int    `json:"new_found"`
	Cleaned     int    `json:"cleaned"`
	Message     string `json:"message"`
}

type MediaMetadataEventData struct {
	MediaID       string `json:"media_id"`
	LibraryID     string `json:"library_id"`
	MetadataPhase string `json:"metadata_phase"`
	Message       string `json:"message"`
}

// WSHub provides a shim for the original WebSocket hub.
// It proxies broadcast events to Wails runtime.EventsEmit.
type WSHub struct {
	ctx context.Context
}

// NewWSHub creates a new WSHub shim.
func NewWSHub(ctx context.Context) *WSHub {
	return &WSHub{
		ctx: ctx,
	}
}

// BroadcastEvent proxies to Wails runtime.EventsEmit
func (w *WSHub) BroadcastEvent(eventType string, data interface{}) {
	if w.ctx != nil {
		runtime.EventsEmit(w.ctx, eventType, data)
	}
}
