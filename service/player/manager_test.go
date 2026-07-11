package player

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeAdapter struct {
	mu       sync.Mutex
	sessions []*fakeSession
}

func (a *fakeAdapter) Start(context.Context, string, string) (LaunchResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := &fakeSession{samples: make(chan fakeResult, 16)}
	a.sessions = append(a.sessions, s)
	return LaunchResult{Session: s}, nil
}
func (a *fakeAdapter) latest() *fakeSession {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessions[len(a.sessions)-1]
}

type fakeResult struct {
	sample Sample
	err    error
}
type fakeSession struct {
	samples chan fakeResult
	mu      sync.Mutex
	seeks   []time.Duration
	closed  bool
}

func (s *fakeSession) Sample(ctx context.Context) (Sample, error) {
	select {
	case r := <-s.samples:
		return r.sample, r.err
	case <-ctx.Done():
		return Sample{}, ctx.Err()
	}
}
func (s *fakeSession) Seek(_ context.Context, p time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seeks = append(s.seeks, p)
	return nil
}
func (s *fakeSession) Detach() error { s.mu.Lock(); s.closed = true; s.mu.Unlock(); return nil }

type fakeStore struct {
	mu       sync.Mutex
	loaded   History
	saves    []History
	failures int
}

func (s *fakeStore) Load(context.Context, string) (History, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loaded, nil
}
func (s *fakeStore) Save(_ context.Context, h *History) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failures > 0 {
		s.failures--
		return errors.New("locked")
	}
	s.saves = append(s.saves, *h)
	return nil
}

type fakeEvents struct {
	mu     sync.Mutex
	events []StateEvent
}

func (e *fakeEvents) MediaStateUpdated(v StateEvent) {
	e.mu.Lock()
	e.events = append(e.events, v)
	e.mu.Unlock()
}

func testManager(store *fakeStore) (*PlaybackSessionManager, *fakeAdapter, *fakeEvents) {
	a := &fakeAdapter{}
	e := &fakeEvents{}
	o := DefaultManagerOptions()
	o.PollInterval = time.Millisecond
	o.WriteInterval = 4 * time.Millisecond
	o.RetryInterval = time.Millisecond
	o.LargeJump = 20 * time.Second
	return NewPlaybackSessionManager(a, store, e, nil, o), a, e
}
func sample(pos, dur int, state PlaybackState) fakeResult {
	return fakeResult{sample: Sample{Position: time.Duration(pos) * time.Second, Duration: time.Duration(dur) * time.Second, State: state}}
}
func waitFor(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not reached")
}
func saveCount(s *fakeStore) int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.saves) }

func TestNormalPlaybackMarksWatchedOnFirstCommittedSample(t *testing.T) {
	store := &fakeStore{}
	m, a, e := testManager(store)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	if err := m.Play(context.Background(), "m1", "x.mkv", "potplayer"); err != nil {
		t.Fatal(err)
	}
	p := a.latest()
	p.samples <- sample(5, 100, StatePlaying)
	p.samples <- sample(40, 100, StatePlaying)
	p.samples <- sample(40, 100, StatePaused)
	waitFor(t, func() bool { return saveCount(store) >= 3 })
	store.mu.Lock()
	first := store.saves[0]
	store.mu.Unlock()
	if !first.Completed {
		t.Fatal("first committed playback sample was not marked watched")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.events) == 0 || !e.events[0].IsWatched {
		t.Fatal("missing watched state event")
	}
}

func TestResumeWaitsForLoadedSampleAndSkipsCompleted(t *testing.T) {
	store := &fakeStore{loaded: History{Position: 25 * time.Second, Duration: 100 * time.Second, Completed: true}}
	m, a, _ := testManager(store)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	_ = m.Play(context.Background(), "m", "x", "potplayer")
	p := a.latest()
	p.samples <- fakeResult{err: ErrNotLoaded}
	p.samples <- sample(1, 100, StatePlaying)
	waitFor(t, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return len(p.seeks) == 1 })
	p.mu.Lock()
	if p.seeks[0] != 25*time.Second {
		t.Fatal(p.seeks)
	}
	p.mu.Unlock()
	store2 := &fakeStore{loaded: History{Position: 95 * time.Second, Duration: 100 * time.Second}}
	m2, a2, _ := testManager(store2)
	t.Cleanup(func() { _ = m2.Shutdown(context.Background()) })
	_ = m2.Play(context.Background(), "m2", "x", "potplayer")
	a2.latest().samples <- sample(1, 100, StatePlaying)
	time.Sleep(10 * time.Millisecond)
	a2.latest().mu.Lock()
	defer a2.latest().mu.Unlock()
	if len(a2.latest().seeks) != 0 {
		t.Fatal("resumed after 90 percent")
	}
}

func TestTerminalErrorsRetryAndShutdown(t *testing.T) {
	for _, terminal := range []error{ErrWindowClosed, ErrFileChanged} {
		t.Run(terminal.Error(), func(t *testing.T) {
			store := &fakeStore{}
			m, a, _ := testManager(store)
			_ = m.Play(context.Background(), "m", "x", "potplayer")
			p := a.latest()
			p.samples <- sample(10, 100, StatePlaying)
			waitFor(t, func() bool { return saveCount(store) > 0 })
			p.samples <- fakeResult{err: terminal}
			waitFor(t, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.closed })
			_ = m.Shutdown(context.Background())
		})
	}
	store := &fakeStore{failures: 2}
	m, a, _ := testManager(store)
	_ = m.Play(context.Background(), "retry", "x", "potplayer")
	a.latest().samples <- sample(10, 100, StatePaused)
	waitFor(t, func() bool { return saveCount(store) == 1 })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestIPCTimeoutAndTwoSessions(t *testing.T) {
	store := &fakeStore{}
	m, a, _ := testManager(store)
	_ = m.Play(context.Background(), "one", "x", "potplayer")
	one := a.latest()
	_ = m.Play(context.Background(), "two", "y", "potplayer")
	two := a.latest()
	one.samples <- sample(10, 100, StatePlaying)
	two.samples <- sample(20, 100, StatePlaying)
	waitFor(t, func() bool { return saveCount(store) >= 2 })
	for i := 0; i < 3; i++ {
		one.samples <- fakeResult{err: ErrIPCTimeout}
	}
	waitFor(t, func() bool { one.mu.Lock(); defer one.mu.Unlock(); return one.closed })
	if two.closed {
		t.Fatal("timeout stopped another session")
	}
	_ = m.Shutdown(context.Background())
}

func TestReplacingMediaSessionFinalizesOldBeforeNewRevision(t *testing.T) {
	store := &fakeStore{}
	m, adapter, events := testManager(store)
	if err := m.Play(context.Background(), "same", "old.mkv", "potplayer"); err != nil {
		t.Fatal(err)
	}
	old := adapter.latest()
	old.samples <- sample(12, 100, StatePlaying)
	waitFor(t, func() bool { return saveCount(store) == 1 })
	if err := m.Play(context.Background(), "same", "new.mkv", "potplayer"); err != nil {
		t.Fatal(err)
	}
	newSession := adapter.latest()
	newSession.samples <- sample(30, 100, StatePlaying)
	waitFor(t, func() bool { return saveCount(store) >= 2 })
	old.samples <- sample(80, 100, StatePlaying)
	time.Sleep(10 * time.Millisecond)
	store.mu.Lock()
	last := store.saves[len(store.saves)-1]
	store.mu.Unlock()
	if last.Position != 30*time.Second {
		t.Fatalf("old session overwrote new: %s", last.Position)
	}
	events.mu.Lock()
	for i := 1; i < len(events.events); i++ {
		if events.events[i].Revision <= events.events[i-1].Revision {
			t.Fatal("non-monotonic revisions")
		}
	}
	events.mu.Unlock()
	_ = m.Shutdown(context.Background())
}
