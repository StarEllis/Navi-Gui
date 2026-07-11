package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	DefaultArtworkCacheHighBytes  = int64(2 << 30)
	DefaultArtworkCacheLowBytes   = int64(1536 << 20)
	DefaultArtworkCacheMaxFiles   = 100000
	DefaultArtworkCleanupBatch    = 500
	defaultArtworkMaintainBatches = 4
	defaultArtworkMaintainBudget  = 250 * time.Millisecond
)

var (
	errArtworkReconcileRequired = errors.New("artwork index reconciliation is required")
	errTempCleanupBudgetReached = errors.New("artwork temp cleanup budget reached")
)

type TempCleanupResult struct {
	Completed    bool
	Remaining    bool
	DeletedCount int
	ScannedCount int
	StopReason   string
}

type tempCleanupRequest struct {
	marker    string
	olderThan time.Duration
	batch     int
	backoff   time.Duration
}

type artworkIndexEntry struct {
	Path       string    `json:"path"`
	Size       int64     `json:"size"`
	LastAccess time.Time `json:"last_access"`
	Evicting   bool      `json:"-"`
}

type ArtworkCacheStats struct {
	Files int
	Bytes int64
}

func (c *ArtworkCache) rejectMaintenanceIfClosing() error {
	if c == nil {
		return context.Canceled
	}
	c.mu.Lock()
	closing := c.closing
	c.mu.Unlock()
	if closing {
		return context.Canceled
	}
	return nil
}

func (c *ArtworkCache) loadIndex() {
	_, markerErr := os.Stat(c.reconcilePath)
	c.reconcileMarkerSet = markerErr == nil
	data, err := os.ReadFile(c.indexPath)
	if err != nil {
		c.reconcileNeeded = true
		return
	}
	var entries []artworkIndexEntry
	if json.Unmarshal(data, &entries) != nil {
		c.reconcileNeeded = true
		return
	}
	for _, entry := range entries {
		if !c.IsCachedPath(entry.Path) {
			continue
		}
		entry.Path = filepath.Clean(entry.Path)
		entry.Evicting = false
		c.index[entry.Path] = entry
		c.indexBytes += entry.Size
	}
	c.indexUsable = true
	c.reconcileNeeded = c.reconcileMarkerSet
}

func (c *ArtworkCache) startWorkers() {
	c.mu.Lock()
	c.activeWorkers = 2
	c.mu.Unlock()
	c.workerWG.Add(2)
	go func() { defer c.finishWorker(); c.flushLoop() }()
	go func() { defer c.finishWorker(); c.cleanupLoop() }()
}

func (c *ArtworkCache) addProducerLocked() {
	c.activeProducers++
	c.producerWG.Add(1)
}

func (c *ArtworkCache) finishProducer() {
	c.mu.Lock()
	if c.activeProducers > 0 {
		c.activeProducers--
	}
	drained := c.closing && c.activeProducers == 0
	c.mu.Unlock()
	c.producerWG.Done()
	if drained {
		c.producerDrainOnce.Do(func() { close(c.producerDrained) })
	}
}

func (c *ArtworkCache) finishWorker() {
	c.mu.Lock()
	if c.activeWorkers > 0 {
		c.activeWorkers--
	}
	drained := c.closing && c.activeWorkers == 0
	c.mu.Unlock()
	c.workerWG.Done()
	if drained {
		c.workerDrainOnce.Do(func() { close(c.workerDrained) })
	}
}

func (c *ArtworkCache) writeIndexSnapshot(entries []artworkIndexEntry) error {
	if err := os.MkdirAll(c.root, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(c.root, ".navi-index-*.part")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if _, err = temp.Write(data); err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return replaceNFOFileAtomic(name, c.indexPath)
}

func (c *ArtworkCache) prepareFileCommit(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.markerMu.Lock()
	defer c.markerMu.Unlock()
	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		return context.Canceled
	}
	c.pendingCommits++
	if c.reconcileMarkerSet {
		c.mu.Unlock()
		return nil
	}
	c.reconcileMarkerSet = true
	c.mu.Unlock()

	if err := os.MkdirAll(c.root, 0o755); err != nil {
		c.completeFileCommit()
		c.resetReconcileMarker()
		return err
	}
	file, err := os.OpenFile(c.reconcilePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err == nil {
		_, err = file.WriteString(time.Now().UTC().Format(time.RFC3339Nano))
		if err == nil {
			err = file.Sync()
		}
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
	}
	if err != nil {
		c.completeFileCommit()
		c.resetReconcileMarker()
		return err
	}
	if err := ctx.Err(); err != nil {
		c.completeFileCommit()
		return err
	}
	return nil
}

func (c *ArtworkCache) completeFileCommit() {
	c.mu.Lock()
	if c.pendingCommits > 0 {
		c.pendingCommits--
	}
	c.mu.Unlock()
}

func (c *ArtworkCache) resetReconcileMarker() {
	c.mu.Lock()
	c.reconcileMarkerSet = false
	c.mu.Unlock()
}

func (c *ArtworkCache) markDirtyLocked(path string, entry *artworkIndexEntry) {
	c.indexVersion++
	c.dirty = true
	if entry == nil {
		delete(c.pendingUpserts, path)
		c.pendingDeletes[path] = struct{}{}
	} else {
		copyEntry := *entry
		copyEntry.Evicting = false
		c.pendingUpserts[path] = copyEntry
		delete(c.pendingDeletes, path)
	}
	select {
	case c.flushCh <- struct{}{}:
	default:
	}
}

func (c *ArtworkCache) recordFile(path string) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	path = filepath.Clean(path)
	c.mu.Lock()
	old := c.index[path]
	entry := artworkIndexEntry{Path: path, Size: info.Size(), LastAccess: time.Now()}
	c.index[path] = entry
	c.indexBytes += entry.Size - old.Size
	c.indexUsable = true
	c.markDirtyLocked(path, &entry)
	overLimit := c.indexBytes > c.highBytes || len(c.index) > c.maxFiles
	c.mu.Unlock()
	if overLimit {
		c.requestCleanup()
	}
}

func (c *ArtworkCache) touch(path string) { c.touchMany([]string{path}) }

func (c *ArtworkCache) touchMany(paths []string) {
	now := time.Now()
	c.mu.Lock()
	for _, path := range paths {
		key := filepath.Clean(path)
		entry, ok := c.index[key]
		if !ok || entry.Evicting || now.Sub(entry.LastAccess) < time.Hour {
			continue
		}
		entry.LastAccess = now
		c.index[key] = entry
		c.markDirtyLocked(key, &entry)
	}
	c.mu.Unlock()
}

func (c *ArtworkCache) flushLoop() {
	var timer *time.Timer
	var timerC <-chan time.Time
	for {
		select {
		case <-c.flushCh:
			if timer == nil {
				timer = time.NewTimer(c.flushDebounce)
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(c.flushDebounce)
			}
			timerC = timer.C
		case <-timerC:
			timerC = nil
			if err := c.flushDirty(); err != nil && !errors.Is(err, errArtworkReconcileRequired) {
				if c.logger != nil {
					c.logger.Warnf("persist artwork index failed: %v", err)
				}
				if timer == nil {
					timer = time.NewTimer(c.flushDebounce)
				} else {
					timer.Reset(c.flushDebounce)
				}
				timerC = timer.C
			}
		case <-c.workerStop:
			if timer != nil {
				timer.Stop()
			}
			return
		}
	}
}

func (c *ArtworkCache) flushDirty() error {
	c.flushMu.Lock()
	defer c.flushMu.Unlock()
	c.mu.Lock()
	if !c.dirty {
		c.mu.Unlock()
		return nil
	}
	if c.reconcileNeeded {
		c.mu.Unlock()
		return errArtworkReconcileRequired
	}
	version := c.indexVersion
	entries := make([]artworkIndexEntry, 0, len(c.index))
	for _, entry := range c.index {
		if !entry.Evicting {
			entry.Evicting = false
			entries = append(entries, entry)
		}
	}
	writer := c.indexWriter
	c.mu.Unlock()
	if writer == nil {
		writer = c.writeIndexSnapshot
	}
	if err := writer(entries); err != nil {
		return err
	}
	c.indexWrites.Add(1)

	removeMarker := false
	c.markerMu.Lock()
	c.mu.Lock()
	if c.indexVersion == version {
		c.dirty = false
		c.persistedVersion = version
		clear(c.pendingUpserts)
		clear(c.pendingDeletes)
		removeMarker = c.reconcileMarkerSet && c.pendingCommits == 0
		if removeMarker {
			c.reconcileMarkerSet = false
		}
	} else {
		select {
		case c.flushCh <- struct{}{}:
		default:
		}
	}
	c.mu.Unlock()
	if removeMarker {
		_ = os.Remove(c.reconcilePath)
	}
	c.markerMu.Unlock()
	return nil
}

func (c *ArtworkCache) Stats() ArtworkCacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return ArtworkCacheStats{Files: len(c.index), Bytes: c.indexBytes}
}

func (c *ArtworkCache) RebuildIndex(ctx context.Context) (ArtworkCacheStats, error) {
	if err := c.rejectMaintenanceIfClosing(); err != nil {
		return ArtworkCacheStats{}, err
	}
	c.walks.Add(1)
	if ctx == nil {
		ctx = context.Background()
	}
	entries := make(map[string]artworkIndexEntry)
	err := filepath.WalkDir(c.root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || path == c.indexPath || strings.HasPrefix(entry.Name(), ".") || strings.Contains(entry.Name(), ".part") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		path = filepath.Clean(path)
		entries[path] = artworkIndexEntry{Path: path, Size: info.Size(), LastAccess: info.ModTime()}
		return nil
	})
	if os.IsNotExist(err) {
		err = nil
	}
	if err != nil {
		return ArtworkCacheStats{}, err
	}
	c.mu.Lock()
	for path, entry := range c.pendingUpserts {
		entries[path] = entry
	}
	for path := range c.pendingDeletes {
		delete(entries, path)
	}
	var bytes int64
	for _, entry := range entries {
		bytes += entry.Size
	}
	c.index = entries
	c.indexBytes = bytes
	c.indexUsable = true
	c.reconcileNeeded = false
	c.indexVersion++
	c.dirty = true
	select {
	case c.flushCh <- struct{}{}:
	default:
	}
	stats := ArtworkCacheStats{Files: len(entries), Bytes: bytes}
	c.mu.Unlock()
	return stats, nil
}

func (c *ArtworkCache) Maintain(ctx context.Context) (ArtworkCacheStats, error) {
	if err := c.rejectMaintenanceIfClosing(); err != nil {
		return ArtworkCacheStats{}, err
	}
	stats, progressed, remains, err := c.maintainBudget(ctx, defaultArtworkMaintainBatches, defaultArtworkMaintainBudget)
	if remains && progressed {
		c.requestCleanup()
	}
	return stats, err
}

func (c *ArtworkCache) maintainBudget(ctx context.Context, maxBatches int, budget time.Duration) (ArtworkCacheStats, bool, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	reconcile := c.reconcileNeeded || !c.indexUsable
	c.mu.Unlock()
	if reconcile {
		if _, err := c.RebuildIndex(ctx); err != nil {
			return ArtworkCacheStats{}, false, false, err
		}
	}
	start := time.Now()
	stats := c.Stats()
	if stats.Bytes <= c.highBytes && stats.Files <= c.maxFiles {
		return stats, false, false, nil
	}
	progressed := false
	for batch := 0; batch < maxBatches && time.Since(start) < budget; batch++ {
		if stats.Bytes <= c.lowBytes && stats.Files <= c.maxFiles {
			return stats, progressed, false, nil
		}
		before := stats
		var err error
		stats, err = c.Cleanup(ctx, c.lowBytes, c.lowBytes, c.maxFiles, c.cleanupBatch)
		if err != nil {
			return stats, progressed, true, err
		}
		if stats.Files == before.Files && stats.Bytes == before.Bytes {
			return stats, progressed, true, nil
		}
		progressed = true
	}
	remains := stats.Bytes > c.lowBytes || stats.Files > c.maxFiles
	return stats, progressed, remains, nil
}

func (c *ArtworkCache) requestCleanup() {
	c.mu.Lock()
	closing := c.closing
	c.mu.Unlock()
	if closing {
		return
	}
	select {
	case c.cleanupCh <- struct{}{}:
	default:
	}
}

func (c *ArtworkCache) requestTempCleanup(request tempCleanupRequest) {
	c.mu.Lock()
	closing := c.closing
	c.mu.Unlock()
	if closing {
		return
	}
	if request.backoff < 100*time.Millisecond {
		request.backoff = 100 * time.Millisecond
	}
	select {
	case c.tempCleanupCh <- request:
	default:
	}
}

func (c *ArtworkCache) cleanupLoop() {
	var pendingTemp *tempCleanupRequest
	var tempTimer *time.Timer
	var tempTimerC <-chan time.Time
	defer func() {
		if tempTimer != nil {
			tempTimer.Stop()
		}
	}()
	resetTempTimer := func(delay time.Duration) {
		if tempTimer == nil {
			tempTimer = time.NewTimer(delay)
		} else {
			if !tempTimer.Stop() {
				select {
				case <-tempTimer.C:
				default:
				}
			}
			tempTimer.Reset(delay)
		}
		tempTimerC = tempTimer.C
	}
	for {
		select {
		case <-c.cleanupCh:
			_, progressed, remains, err := c.maintainBudget(c.ctx, defaultArtworkMaintainBatches, defaultArtworkMaintainBudget)
			if err != nil && !errors.Is(err, context.Canceled) && c.logger != nil {
				c.logger.Warnf("artwork cache maintenance failed: %v", err)
			}
			if remains && progressed {
				timer := time.NewTimer(100 * time.Millisecond)
				select {
				case <-timer.C:
					c.requestCleanup()
				case <-c.ctx.Done():
					if !timer.Stop() {
						<-timer.C
					}
					return
				case <-c.workerStop:
					if !timer.Stop() {
						<-timer.C
					}
					return
				}
			}
		case request := <-c.tempCleanupCh:
			pendingTemp = &request
			resetTempTimer(request.backoff)
		case <-tempTimerC:
			tempTimerC = nil
			if pendingTemp == nil {
				continue
			}
			request := *pendingTemp
			result, err := c.CleanupOldTemps(c.ctx, request.olderThan, request.batch)
			if err != nil && !errors.Is(err, context.Canceled) && c.logger != nil {
				c.logger.Warnf("artwork temp cleanup batch failed: %v", err)
			}
			if result.Completed {
				if err := c.writeTempCleanupMarker(request.marker); err == nil {
					pendingTemp = nil
					continue
				} else if c.logger != nil {
					c.logger.Warnf("write artwork temp cleanup marker failed: %v", err)
				}
			}
			if result.DeletedCount > 0 {
				request.backoff = 100 * time.Millisecond
			} else {
				request.backoff *= 2
				if request.backoff > 5*time.Second {
					request.backoff = 5 * time.Second
				}
			}
			pendingTemp = &request
			resetTempTimer(request.backoff)
		case <-c.ctx.Done():
			return
		case <-c.workerStop:
			return
		}
	}
}

func (c *ArtworkCache) Cleanup(ctx context.Context, highBytes, lowBytes int64, maxFiles, batch int) (ArtworkCacheStats, error) {
	if err := c.rejectMaintenanceIfClosing(); err != nil {
		return ArtworkCacheStats{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if batch < 1 {
		batch = DefaultArtworkCleanupBatch
	}
	c.mu.Lock()
	stats := ArtworkCacheStats{Files: len(c.index), Bytes: c.indexBytes}
	entries := make([]artworkIndexEntry, 0, len(c.index))
	for _, entry := range c.index {
		entries = append(entries, entry)
	}
	if stats.Bytes <= highBytes && stats.Files <= maxFiles {
		c.mu.Unlock()
		return stats, nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].LastAccess.Before(entries[j].LastAccess) })
	c.mu.Unlock()
	removed := 0
	var cleanupErr error
	for _, candidate := range entries {
		if stats.Bytes <= lowBytes && stats.Files <= maxFiles || removed >= batch {
			break
		}
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		removedNow, err := c.evictPath(candidate.Path)
		if err != nil {
			if cleanupErr == nil {
				cleanupErr = err
			}
			continue
		}
		if removedNow {
			stats.Files--
			stats.Bytes -= candidate.Size
			removed++
		}
	}
	if cleanupErr != nil {
		return stats, cleanupErr
	}
	return stats, nil
}

func (c *ArtworkCache) evictPath(path string) (bool, error) {
	path = filepath.Clean(path)
	c.mu.Lock()
	entry, ok := c.index[path]
	if !ok {
		c.mu.Unlock()
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				return false, nil
			}
			return false, err
		}
		c.mu.Lock()
		entry, ok = c.index[path]
		if !ok {
			entry = artworkIndexEntry{Path: path, Size: info.Size(), LastAccess: info.ModTime()}
			c.index[path] = entry
			c.indexBytes += entry.Size
		}
	}
	if entry.Evicting || c.active[path] > 0 {
		c.mu.Unlock()
		return false, nil
	}
	entry.Evicting = true
	c.index[path] = entry
	c.mu.Unlock()

	err := c.remove(path)
	if os.IsNotExist(err) {
		err = nil
	}
	c.mu.Lock()
	current, stillPresent := c.index[path]
	if err != nil {
		if stillPresent {
			current.Evicting = false
			c.index[path] = current
		}
		c.mu.Unlock()
		return false, err
	}
	if stillPresent {
		delete(c.index, path)
		c.indexBytes -= current.Size
		c.markDirtyLocked(path, nil)
	}
	c.mu.Unlock()
	return true, nil
}

func (c *ArtworkCache) CleanupOldTemps(ctx context.Context, olderThan time.Duration, batch int) (TempCleanupResult, error) {
	result := TempCleanupResult{}
	if err := c.rejectMaintenanceIfClosing(); err != nil {
		result.Remaining = true
		result.StopReason = "canceled"
		return result, err
	}
	c.walks.Add(1)
	if batch < 1 {
		batch = 200
	}
	cutoff, start := time.Now().Add(-olderThan), time.Now()
	var cleanupErr error
	err := filepath.WalkDir(c.root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				result.Remaining = true
				result.StopReason = "canceled"
				return err
			}
		}
		if result.DeletedCount >= batch {
			result.Remaining = true
			result.StopReason = "batch_limit"
			return errTempCleanupBudgetReached
		}
		if time.Since(start) >= defaultArtworkMaintainBudget {
			result.Remaining = true
			result.StopReason = "time_budget"
			return errTempCleanupBudgetReached
		}
		if entry.IsDir() {
			return nil
		}
		result.ScannedCount++
		if !strings.Contains(entry.Name(), ".part") && !strings.HasPrefix(entry.Name(), ".navi-") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			result.Remaining = true
			result.StopReason = "inspect_failed"
			if cleanupErr == nil {
				cleanupErr = err
			}
			return nil
		}
		path = filepath.Clean(path)
		c.mu.Lock()
		inUse := c.active[path] > 0
		c.mu.Unlock()
		if !info.ModTime().Before(cutoff) {
			return nil
		}
		if inUse {
			result.Remaining = true
			if result.StopReason == "" {
				result.StopReason = "reserved"
			}
			return nil
		}
		if removeErr := c.remove(path); removeErr == nil || os.IsNotExist(removeErr) {
			result.DeletedCount++
		} else {
			result.Remaining = true
			result.StopReason = "delete_failed"
			if cleanupErr == nil {
				cleanupErr = removeErr
			}
		}
		return nil
	})
	if errors.Is(err, errTempCleanupBudgetReached) {
		err = nil
	}
	if err != nil {
		return result, err
	}
	if cleanupErr != nil {
		return result, cleanupErr
	}
	result.Completed = !result.Remaining
	if result.Completed {
		result.StopReason = "completed"
	}
	return result, nil
}

func (c *ArtworkCache) CleanupOldTempsIfDue(ctx context.Context, interval, olderThan time.Duration, batch int) error {
	if err := c.rejectMaintenanceIfClosing(); err != nil {
		return err
	}
	marker := filepath.Join(c.root, ".temp-cleanup")
	if info, err := os.Stat(marker); err == nil && time.Since(info.ModTime()) < interval {
		return nil
	}
	result, err := c.CleanupOldTemps(ctx, olderThan, batch)
	if result.Remaining {
		c.requestTempCleanup(tempCleanupRequest{marker: marker, olderThan: olderThan, batch: batch, backoff: 100 * time.Millisecond})
	}
	if err != nil {
		return err
	}
	if !result.Completed {
		return nil
	}
	return c.writeTempCleanupMarker(marker)
}

func (c *ArtworkCache) writeTempCleanupMarker(marker string) error {
	if err := os.MkdirAll(c.root, 0o755); err != nil {
		return err
	}
	return os.WriteFile(marker, []byte(time.Now().UTC().Format(time.RFC3339)), 0o600)
}

func (c *ArtworkCache) BeginShutdown() {
	if c == nil {
		return
	}
	c.shutdownBeginOnce.Do(func() {
		c.mu.Lock()
		c.closing = true
		producersDrained := c.activeProducers == 0
		workersDrained := c.activeWorkers == 0
		c.mu.Unlock()
		c.cancel()
		close(c.workerStop)
		if producersDrained {
			c.producerDrainOnce.Do(func() { close(c.producerDrained) })
		}
		if workersDrained {
			c.workerDrainOnce.Do(func() { close(c.workerDrained) })
		}
	})
}

func (c *ArtworkCache) ShutdownContext(ctx context.Context) error {
	if c == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.BeginShutdown()
	select {
	case <-c.producerDrained:
	case <-ctx.Done():
		return fmt.Errorf("artwork producers did not stop before shutdown deadline: %w", ctx.Err())
	}
	select {
	case <-c.workerDrained:
	case <-ctx.Done():
		return fmt.Errorf("artwork maintenance workers did not stop before shutdown deadline: %w", ctx.Err())
	}
	c.shutdownFlushOnce.Do(func() {
		go func() {
			c.shutdownFlushErr = c.flushDirty()
			close(c.shutdownFlushDone)
		}()
	})
	select {
	case <-c.shutdownFlushDone:
		if errors.Is(c.shutdownFlushErr, errArtworkReconcileRequired) {
			return nil
		}
		return c.shutdownFlushErr
	case <-ctx.Done():
		return fmt.Errorf("final artwork index save did not stop before shutdown deadline: %w", ctx.Err())
	}
}

func (c *ArtworkCache) Shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := c.ShutdownContext(ctx); err != nil && c.logger != nil {
		c.logger.Warnf("artwork cache shutdown incomplete: %v", err)
	}
}

func (c *ArtworkCache) debugIndexState() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return fmt.Sprintf("usable=%t reconcile=%t dirty=%t version=%d persisted=%d", c.indexUsable, c.reconcileNeeded, c.dirty, c.indexVersion, c.persistedVersion)
}
