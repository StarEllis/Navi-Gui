package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

var ErrProcessGovernorStopped = errors.New("process governor is stopped")

type processGovernor struct {
	ctx    context.Context
	cancel context.CancelFunc
	slots  chan struct{}
	stop   sync.Once
	active atomic.Int64
	peak   atomic.Int64
	starts atomic.Int64
	mu     sync.Mutex
	calls  map[string]*governedCall
}

type governedCall struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int
	output  []byte
	err     error
}

type ProcessDiagnostics struct{ Starts, Active, Peak int64 }

func (g *processGovernor) diagnostics() ProcessDiagnostics {
	if g == nil {
		return ProcessDiagnostics{}
	}
	return ProcessDiagnostics{Starts: g.starts.Load(), Active: g.active.Load(), Peak: g.peak.Load()}
}

func newProcessGovernor(parent context.Context, limit int) *processGovernor {
	if parent == nil {
		parent = context.Background()
	}
	if limit < 1 {
		limit = 1
	}
	ctx, cancel := context.WithCancel(parent)
	return &processGovernor{ctx: ctx, cancel: cancel, slots: make(chan struct{}, limit), calls: make(map[string]*governedCall)}
}

func (g *processGovernor) runKeyed(waiter context.Context, key string, fn func(context.Context) ([]byte, error)) ([]byte, error) {
	if g == nil {
		return fn(waiter)
	}
	if waiter == nil {
		waiter = context.Background()
	}
	g.mu.Lock()
	if g.ctx.Err() != nil {
		g.mu.Unlock()
		return nil, ErrProcessGovernorStopped
	}
	if call, ok := g.calls[key]; ok {
		call.waiters++
		g.mu.Unlock()
		return g.waitCall(waiter, call)
	}
	runCtx, cancel := context.WithCancel(g.ctx)
	call := &governedCall{done: make(chan struct{}), cancel: cancel, waiters: 1}
	g.calls[key] = call
	g.mu.Unlock()
	var canceled, canceledLast atomic.Bool
	stopWatch := context.AfterFunc(waiter, func() {
		canceled.Store(true)
		canceledLast.Store(g.releaseWaiter(call))
	})
	if err := g.acquire(runCtx); err != nil {
		g.mu.Lock()
		call.err = err
		delete(g.calls, key)
		close(call.done)
		g.mu.Unlock()
		return nil, err
	}
	go func() {
		call.output, call.err = g.runAcquired(runCtx, fn)
		g.mu.Lock()
		delete(g.calls, key)
		close(call.done)
		g.mu.Unlock()
	}()
	if !stopWatch() || canceled.Load() {
		if canceledLast.Load() {
			<-call.done
		}
		return nil, waiter.Err()
	}
	return g.waitCall(waiter, call)
}

func (g *processGovernor) releaseWaiter(call *governedCall) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	call.waiters--
	last := call.waiters == 0
	if last {
		call.cancel()
	}
	return last
}

func (g *processGovernor) waitCall(waiter context.Context, call *governedCall) ([]byte, error) {
	select {
	case <-call.done:
		return append([]byte(nil), call.output...), call.err
	case <-waiter.Done():
		last := g.releaseWaiter(call)
		if last {
			<-call.done
		}
		return nil, waiter.Err()
	}
}

func (g *processGovernor) run(waiter context.Context, fn func(context.Context) ([]byte, error)) ([]byte, error) {
	if g == nil {
		return fn(waiter)
	}
	if waiter == nil {
		waiter = context.Background()
	}
	if err := g.acquire(waiter); err != nil {
		return nil, err
	}
	return g.runAcquired(waiter, fn)
}

func (g *processGovernor) acquire(waiter context.Context) error {
	select {
	case <-g.ctx.Done():
		return ErrProcessGovernorStopped
	case <-waiter.Done():
		return waiter.Err()
	case g.slots <- struct{}{}:
	}
	return nil
}

func (g *processGovernor) runAcquired(waiter context.Context, fn func(context.Context) ([]byte, error)) ([]byte, error) {
	active := g.active.Add(1)
	for {
		peak := g.peak.Load()
		if active <= peak || g.peak.CompareAndSwap(peak, active) {
			break
		}
	}
	g.starts.Add(1)
	runCtx, cancel := context.WithCancel(waiter)
	stopCancel := context.AfterFunc(g.ctx, cancel)
	defer func() {
		stopCancel()
		cancel()
		g.active.Add(-1)
		<-g.slots
	}()
	return fn(runCtx)
}

func (g *processGovernor) shutdown() {
	if g != nil {
		g.stop.Do(g.cancel)
	}
}

type probeCall struct {
	done   chan struct{}
	output []byte
	err    error
}

// probeScope owns completed probe results for exactly one task lifetime.
// Concurrent callers share one call; canceling a waiter does not cancel it.
type probeScope struct {
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	closed   bool
	results  map[string][]byte
	inflight map[string]*probeCall
	requests atomic.Int64
	hits     atomic.Int64
}

func newProbeScope(parent context.Context) *probeScope {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	return &probeScope{ctx: ctx, cancel: cancel, results: make(map[string][]byte), inflight: make(map[string]*probeCall)}
}

func (s *probeScope) do(waiter context.Context, key string, fn func(context.Context) ([]byte, error)) ([]byte, error) {
	s.requests.Add(1)
	if waiter == nil {
		waiter = context.Background()
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrProcessGovernorStopped
	}
	if output, ok := s.results[key]; ok {
		s.hits.Add(1)
		copyOutput := append([]byte(nil), output...)
		s.mu.Unlock()
		return copyOutput, nil
	}
	if call, ok := s.inflight[key]; ok {
		s.hits.Add(1)
		s.mu.Unlock()
		select {
		case <-waiter.Done():
			return nil, waiter.Err()
		case <-call.done:
			return append([]byte(nil), call.output...), call.err
		}
	}
	call := &probeCall{done: make(chan struct{})}
	s.inflight[key] = call
	s.mu.Unlock()

	go func() {
		output, err := fn(s.ctx)
		s.mu.Lock()
		call.output, call.err = append([]byte(nil), output...), err
		delete(s.inflight, key)
		if err == nil && !s.closed {
			s.results[key] = append([]byte(nil), output...)
		}
		close(call.done)
		s.mu.Unlock()
	}()

	select {
	case <-waiter.Done():
		return nil, waiter.Err()
	case <-call.done:
		return append([]byte(nil), call.output...), call.err
	}
}

func (s *probeScope) close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.results = nil
		s.cancel()
	}
	s.mu.Unlock()
}
