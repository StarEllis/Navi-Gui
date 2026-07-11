import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import ts from 'typescript';

const source = await readFile(new URL('../src/utils/mediaCardState.ts', import.meta.url), 'utf8');
const compiled = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 },
}).outputText;
const moduleURL = `data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`;
const { areMediaCardMediaPropsEqual, shouldOpenMediaFromCardKey } = await import(moduleURL);

test('new media objects refresh card handlers for path, detail and image changes', () => {
    const base = { id: '1', title: 'A', year: 2024, poster_path: 'a.jpg', is_favorite: false, is_watched: false };
    for (const change of [
        { file_path: 'new.mkv' },
        { title: 'B' },
        { poster_path: 'b.jpg' },
        { is_favorite: true },
        { is_watched: true },
        { watch_progress: 20 },
        { thumbnail_updated_at: 'v2' },
        { overview: 'new detail' },
    ]) {
        assert.equal(areMediaCardMediaPropsEqual(base, { ...base, ...change }), false);
    }
});

test('unrelated parent updates preserve cards when media references are unchanged', () => {
    const base = { id: '1', title: 'A', year: 2024, overview: 'old' };
    assert.equal(areMediaCardMediaPropsEqual(base, base), true);
});

test('only the focused card handles Enter and Space', () => {
    assert.equal(shouldOpenMediaFromCardKey('Enter', true), true);
    assert.equal(shouldOpenMediaFromCardKey(' ', true), true);
    assert.equal(shouldOpenMediaFromCardKey('Enter', false), false);
    assert.equal(shouldOpenMediaFromCardKey(' ', false), false);
    assert.equal(shouldOpenMediaFromCardKey('Escape', true), false);
});
