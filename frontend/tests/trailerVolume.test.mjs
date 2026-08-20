import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import ts from 'typescript';

const source = await readFile(new URL('../src/utils/trailerVolume.ts', import.meta.url), 'utf8');
const compiled = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 },
}).outputText;
const moduleURL = `data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`;
const {
    DEFAULT_TRAILER_VOLUME,
    loadTrailerVolume,
    normalizeTrailerVolume,
    persistTrailerVolume,
} = await import(moduleURL);

const createStorage = (initial = {}) => {
    const entries = { ...initial };
    return {
        entries,
        getItem: (key) => (key in entries ? entries[key] : null),
        setItem: (key, value) => {
            entries[key] = value;
        },
    };
};

test('missing or malformed preference falls back to full volume', () => {
    assert.deepEqual(normalizeTrailerVolume(null), DEFAULT_TRAILER_VOLUME);
    assert.deepEqual(normalizeTrailerVolume({ volume: 'loud' }), DEFAULT_TRAILER_VOLUME);
    assert.deepEqual(loadTrailerVolume(createStorage()), DEFAULT_TRAILER_VOLUME);
    assert.deepEqual(
        loadTrailerVolume(createStorage({ 'navi.desktop.trailerVolume.v1': '{oops' })),
        DEFAULT_TRAILER_VOLUME,
    );
});

test('volume is clamped to the 0-1 range the video element accepts', () => {
    assert.equal(normalizeTrailerVolume({ volume: 4 }).volume, 1);
    assert.equal(normalizeTrailerVolume({ volume: -2 }).volume, 0);
    assert.equal(normalizeTrailerVolume({ volume: Number.NaN }).volume, 1);
});

test('a saved preference survives the round trip', () => {
    const storage = createStorage();
    assert.equal(persistTrailerVolume({ volume: 0.35, muted: false }, storage), true);
    assert.deepEqual(loadTrailerVolume(storage), { volume: 0.35, muted: false });

    persistTrailerVolume({ volume: 0.35, muted: true }, storage);
    assert.deepEqual(loadTrailerVolume(storage), { volume: 0.35, muted: true });
});

test('no storage available is not an error', () => {
    assert.deepEqual(loadTrailerVolume(null), DEFAULT_TRAILER_VOLUME);
    assert.equal(persistTrailerVolume({ volume: 0.5, muted: false }, null), false);
});
