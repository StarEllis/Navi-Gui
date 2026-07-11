package player

import (
	"context"
	"errors"
	"time"
)

var (
	ErrUnsupported  = errors.New("PotPlayer progress sync is only supported on Windows")
	ErrWindowClosed = errors.New("PotPlayer window closed")
	ErrFileChanged  = errors.New("PotPlayer is displaying a different file")
	ErrAccessDenied = errors.New("PotPlayer IPC access denied; run Navi and PotPlayer at the same privilege level")
	ErrIPCTimeout   = errors.New("PotPlayer IPC timed out")
	ErrNotLoaded    = errors.New("PotPlayer has not loaded the requested file yet")
)

type StartedWithoutSyncError struct{ Cause error }

func (e *StartedWithoutSyncError) Error() string {
	return "player started without progress sync: " + e.Cause.Error()
}
func (e *StartedWithoutSyncError) Unwrap() error { return e.Cause }

type PlaybackState string

const (
	StateStarting PlaybackState = "starting"
	StatePlaying  PlaybackState = "playing"
	StatePaused   PlaybackState = "paused"
	StateStopped  PlaybackState = "stopped"
)

type Sample struct {
	Position time.Duration
	Duration time.Duration
	State    PlaybackState
}

// PlayerSession is permanently bound to the window selected by Start.
type PlayerSession interface {
	Sample(context.Context) (Sample, error)
	Seek(context.Context, time.Duration) error
	// Detach releases sync resources and must never terminate the player.
	Detach() error
}

type LaunchResult struct {
	Session PlayerSession
	Warning error
}

type PlayerAdapter interface {
	Start(context.Context, string, string) (LaunchResult, error)
}

type History struct {
	MediaID   string
	Position  time.Duration
	Duration  time.Duration
	Completed bool
	UpdatedAt time.Time
}

type HistoryStore interface {
	Load(context.Context, string) (History, error)
	Save(context.Context, *History) error
}

type StateEvent struct {
	MediaID         string        `json:"media_id"`
	Position        float64       `json:"position"`
	Duration        float64       `json:"duration"`
	ProgressPercent float64       `json:"progress_percent"`
	Completed       bool          `json:"completed"`
	IsWatched       bool          `json:"is_watched"`
	LastWatchedAt   time.Time     `json:"last_watched_at"`
	PlaybackState   PlaybackState `json:"playback_state"`
	Revision        uint64        `json:"revision"`
}

type EventSink interface {
	MediaStateUpdated(StateEvent)
}

type Logger interface {
	Errorf(string, ...interface{})
}
