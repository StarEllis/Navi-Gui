package player

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

type ManagerOptions struct {
	PollInterval   time.Duration
	WriteInterval  time.Duration
	RetryInterval  time.Duration
	MaxFailures    int
	MaxSaveRetries int
	LargeJump      time.Duration
}

func DefaultManagerOptions() ManagerOptions {
	return ManagerOptions{
		PollInterval: 3 * time.Second, WriteInterval: 15 * time.Second,
		RetryInterval: 500 * time.Millisecond, MaxFailures: 3, MaxSaveRetries: 3,
		LargeJump: 30 * time.Second,
	}
}

type PlaybackSessionManager struct {
	adapter  PlayerAdapter
	store    HistoryStore
	events   EventSink
	logger   Logger
	opts     ManagerOptions
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	playMu   sync.Mutex
	sessions map[uint64]*managedSession
	byMedia  map[string]uint64
	nextID   atomic.Uint64
	revision atomic.Uint64
	wg       sync.WaitGroup
	closed   bool
}

type managedSession struct {
	id          uint64
	mediaID     string
	player      PlayerSession
	cancel      context.CancelFunc
	history     History
	state       PlaybackState
	lastSaved   time.Time
	seekPending bool
	dirty       bool
	observed    bool
	done        chan struct{}
}

func NewPlaybackSessionManager(adapter PlayerAdapter, store HistoryStore, events EventSink, logger Logger, opts ManagerOptions) *PlaybackSessionManager {
	defaults := DefaultManagerOptions()
	if opts.PollInterval <= 0 {
		opts.PollInterval = defaults.PollInterval
	}
	if opts.WriteInterval <= 0 {
		opts.WriteInterval = defaults.WriteInterval
	}
	if opts.RetryInterval <= 0 {
		opts.RetryInterval = defaults.RetryInterval
	}
	if opts.MaxFailures <= 0 {
		opts.MaxFailures = defaults.MaxFailures
	}
	if opts.MaxSaveRetries <= 0 {
		opts.MaxSaveRetries = defaults.MaxSaveRetries
	}
	if opts.LargeJump <= 0 {
		opts.LargeJump = defaults.LargeJump
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &PlaybackSessionManager{adapter: adapter, store: store, events: events, logger: logger, opts: opts, ctx: ctx, cancel: cancel, sessions: map[uint64]*managedSession{}, byMedia: map[string]uint64{}}
}

// Play loads resume state before launch. A warning means playback started but sync did not.
func (m *PlaybackSessionManager) Play(ctx context.Context, mediaID, filePath, executablePath string) error {
	if m == nil || m.adapter == nil {
		return ErrUnsupported
	}
	m.playMu.Lock()
	defer m.playMu.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("playback session manager is shut down")
	}
	old := m.sessions[m.byMedia[mediaID]]
	m.mu.Unlock()
	if old != nil {
		old.cancel()
		select {
		case <-old.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	history, err := m.store.Load(ctx, mediaID)
	if err != nil {
		return fmt.Errorf("load playback history: %w", err)
	}
	history.MediaID = mediaID
	result, err := m.adapter.Start(ctx, executablePath, filePath)
	if err != nil {
		return err
	}
	if result.Session == nil {
		if result.Warning != nil {
			return &StartedWithoutSyncError{Cause: result.Warning}
		}
		return errors.New("player started without a trackable session")
	}

	id := m.nextID.Add(1)
	sessionCtx, cancel := context.WithCancel(m.ctx)
	s := &managedSession{id: id, mediaID: mediaID, player: result.Session, cancel: cancel, history: history, state: StateStarting, done: make(chan struct{})}
	if history.Duration > 0 && history.Position > 0 && float64(history.Position)/float64(history.Duration) < 0.90 {
		s.seekPending = true
	}
	m.mu.Lock()
	m.sessions[id] = s
	m.byMedia[mediaID] = id
	m.mu.Unlock()
	m.wg.Add(1)
	go m.poll(sessionCtx, s)
	if result.Warning != nil {
		return &StartedWithoutSyncError{Cause: result.Warning}
	}
	return nil
}

func (m *PlaybackSessionManager) poll(ctx context.Context, s *managedSession) {
	defer m.wg.Done()
	defer close(s.done)
	defer s.player.Detach()
	defer m.remove(s)
	ticker := time.NewTicker(m.opts.PollInterval)
	defer ticker.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			s.state = StateStopped
			m.saveWithRetry(context.Background(), s, true)
			return
		case <-ticker.C:
			sample, err := s.player.Sample(ctx)
			if err != nil {
				if errors.Is(err, ErrNotLoaded) {
					continue
				}
				if errors.Is(err, ErrWindowClosed) || errors.Is(err, ErrFileChanged) {
					s.state = StateStopped
					m.saveWithRetry(context.Background(), s, true)
					return
				}
				failures++
				if failures >= m.opts.MaxFailures {
					s.state = StateStopped
					m.log("PotPlayer sync stopped after %d failures: media=%s err=%v", failures, s.mediaID, err)
					m.saveWithRetry(context.Background(), s, true)
					return
				}
				continue
			}
			failures = 0
			if sample.State == StateStopped {
				s.state = StateStopped
				m.saveWithRetry(ctx, s, true)
				return
			}
			if sample.Duration <= 0 || sample.Position < 0 {
				continue
			}
			if s.seekPending {
				s.seekPending = false
				if err := s.player.Seek(ctx, s.history.Position); err != nil {
					m.log("PotPlayer resume seek failed; playback continues: media=%s err=%v", s.mediaID, err)
				} else {
					continue
				}
			}
			s.observed = true
			previousPosition, previousState := s.history.Position, s.state
			s.history.Position, s.history.Duration, s.state = sample.Position, sample.Duration, sample.State
			s.dirty = s.dirty || s.history.Position != previousPosition || s.state != previousState
			jumped := absDuration(sample.Position-previousPosition) >= m.opts.LargeJump
			immediate := sample.State == StateStopped || (sample.State == StatePaused && previousState != StatePaused) || jumped
			if s.dirty && (immediate || s.lastSaved.IsZero() || time.Since(s.lastSaved) >= m.opts.WriteInterval) {
				m.saveWithRetry(ctx, s, false)
			}
		}
	}
}

func (m *PlaybackSessionManager) saveWithRetry(ctx context.Context, s *managedSession, final bool) bool {
	if !s.observed || s.history.Duration <= 0 {
		return true
	}
	history := s.history
	history.Completed = true
	history.UpdatedAt = time.Now()
	for attempt := 1; attempt <= m.opts.MaxSaveRetries; attempt++ {
		saveCtx, cancel := context.WithTimeout(ctx, maxDuration(m.opts.RetryInterval, time.Second))
		err := m.store.Save(saveCtx, &history)
		cancel()
		if err == nil {
			if history.Completed {
				s.history.Completed = true
			}
			s.history.UpdatedAt = history.UpdatedAt
			s.lastSaved = history.UpdatedAt
			s.dirty = false
			m.emit(s, history)
			return true
		}
		if attempt == m.opts.MaxSaveRetries {
			m.log("playback progress save failed after bounded retry: media=%s final=%t position=%s err=%v", s.mediaID, final, history.Position, err)
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(m.opts.RetryInterval):
		}
	}
	return false
}

func (m *PlaybackSessionManager) emit(s *managedSession, history History) {
	if m.events == nil {
		return
	}
	h := history
	percent := 0.0
	if h.Duration > 0 {
		percent = float64(h.Position) / float64(h.Duration) * 100
	}
	m.events.MediaStateUpdated(StateEvent{MediaID: s.mediaID, Position: h.Position.Seconds(), Duration: h.Duration.Seconds(), ProgressPercent: percent, Completed: h.Completed, IsWatched: h.Completed, LastWatchedAt: h.UpdatedAt, PlaybackState: s.state, Revision: m.revision.Add(1)})
}

func (m *PlaybackSessionManager) remove(s *managedSession) {
	m.mu.Lock()
	delete(m.sessions, s.id)
	if m.byMedia[s.mediaID] == s.id {
		delete(m.byMedia, s.mediaID)
	}
	m.mu.Unlock()
}

func (m *PlaybackSessionManager) Shutdown(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	m.cancel()
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *PlaybackSessionManager) log(format string, args ...interface{}) {
	if m.logger != nil {
		m.logger.Errorf(format, args...)
	}
}
func absDuration(v time.Duration) time.Duration {
	if v < 0 {
		return -v
	}
	return v
}
func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
