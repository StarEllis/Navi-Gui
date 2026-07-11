import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import ts from 'typescript';

const source = await readFile(new URL('../src/utils/listViewState.ts', import.meta.url), 'utf8');
const compiled = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 },
}).outputText;
const moduleURL = `data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`;
const { putBoundedScrollState } = await import(moduleURL);

test('scroll positions remain isolated by route key and bounded', () => {
    let state = {};
    state = putBoundedScrollState(state, 'library-a/search-a', 400, 3);
    state = putBoundedScrollState(state, 'library-b/search-a', 900, 3);
    assert.equal(state['library-a/search-a'], 400);
    assert.equal(state['library-b/search-a'], 900);
    state = putBoundedScrollState(state, 'library-a/sort-new', 0, 3);
    state = putBoundedScrollState(state, 'library-c/search-a', 100, 3);
    assert.equal(Object.keys(state).length, 3);
    assert.equal(state['library-a/search-a'], undefined);
});

test('deleted or invalid positions clamp safely', () => {
    assert.equal(putBoundedScrollState({}, 'library-a', -100)['library-a'], 0);
});
