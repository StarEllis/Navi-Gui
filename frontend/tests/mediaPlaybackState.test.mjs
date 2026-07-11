import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import ts from 'typescript';

const sourceURL = new URL('../src/utils/mediaPlaybackState.ts', import.meta.url);
const source = await readFile(sourceURL, 'utf8');
const compiled = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 },
}).outputText;
const moduleURL = `data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`;
const playback = await import(moduleURL);

const event = (overrides = {}) => ({
    media_id: 'media-1',
    position: 45,
    duration: 100,
    progress_percent: 45,
    completed: false,
    is_watched: false,
    last_watched_at: '2026-07-11T08:00:00Z',
    playback_state: 'playing',
    revision: 1,
    ...overrides,
});

test('normalizes playback state payloads and keeps zero-valued positions', () => {
    assert.deepEqual(playback.normalizeMediaStateEvent(event({ position: 0 })), {
        id: 'media-1',
        position: 0,
        duration: 100,
        watch_duration: 100,
        progress_percent: 45,
        completed: false,
        is_watched: false,
        last_watched_at: '2026-07-11T08:00:00Z',
        playback_state: 'playing',
        revision: 1,
    });
});

test('older and duplicate revisions cannot overwrite newer media state', () => {
    const revisionTwo = playback.normalizeMediaStateEvent(event({ position: 80, progress_percent: 80, revision: 2 }));
    const revisionOne = playback.normalizeMediaStateEvent(event({ position: 20, progress_percent: 20, revision: 1 }));
    const current = playback.applyMediaStateUpdate({ id: 'media-1', title: 'Title' }, revisionTwo);
    assert.equal(current.position, 80);
    assert.equal(playback.applyMediaStateUpdate(current, revisionOne), current);
    assert.equal(playback.applyMediaStateUpdate(current, revisionTwo), current);
});

test('unversioned events cannot overwrite a newer revision', () => {
    const update = playback.normalizeMediaStateEvent({ media_id: 'media-1', is_favorite: true, is_watched: true });
    assert.deepEqual(update, { id: 'media-1', is_watched: true, is_favorite: true });
    const current = { id: 'media-1', revision: 4, is_watched: false };
    assert.equal(playback.applyMediaStateUpdate(current, update), current);
    assert.equal(playback.applyMediaStateUpdate({ id: 'media-1', is_watched: false }, update).is_watched, true);
});

test('pagination invalidation is limited to membership and active sort keys', () => {
    assert.equal(playback.shouldInvalidateMediaPagination(['position', 'progress_percent'], 'watched', 'created_at'), false);
    assert.equal(playback.shouldInvalidateMediaPagination(['is_watched'], 'watched', 'created_at'), true);
    assert.equal(playback.shouldInvalidateMediaPagination(['is_watched'], 'unwatched', 'created_at'), true);
    assert.equal(playback.shouldInvalidateMediaPagination(['last_watched_at'], '', 'last_watched'), true);
    assert.equal(playback.shouldInvalidateMediaPagination(['last_watched_at'], '', 'created_at'), false);
});

test('progress falls back to position and duration and formats playback time', () => {
    assert.equal(playback.getMediaProgressPercent({ position: 45, watch_duration: 90 }), 50);
    assert.equal(playback.getMediaProgressPercent({ progress_percent: 120 }), 100);
    assert.equal(playback.formatPlaybackTime(3661), '1:01:01');
});
