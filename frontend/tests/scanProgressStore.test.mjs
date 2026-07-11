import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import ts from 'typescript';

const source = await readFile(new URL('../src/utils/scanProgressStore.ts', import.meta.url), 'utf8');
const compiled = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 },
}).outputText;
const moduleURL = `data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`;
const { createScanProgressStore } = await import(moduleURL);

const progress = (libraryId, taskId, current = 1) => ({
    taskId, libraryId, libraryName: libraryId, mode: 'scan', phase: 'progress', current, total: 100, message: '',
});

test('progress is isolated by library and background terminal clears only its active task', () => {
    const store = createScanProgressStore();
    let renders = 0;
    const unsubscribe = store.subscribe(() => { renders += 1; });
    for (let current = 1; current <= 100; current += 1) {
        store.set(progress('library-a', 'task-a', current));
    }
    assert.equal(renders, 100);
    assert.equal(store.getSnapshot('library-a').current, 100);
    assert.equal(store.getSnapshot('library-b'), null);
    store.set(progress('library-b', 'task-b', 20));
    assert.equal(renders, 101);
    assert.equal(store.getSnapshot('library-a').current, 100);
    assert.equal(store.getSnapshot('library-b').current, 20);
    store.complete({ ...progress('library-a', 'task-a', 100), phase: 'completed' });
    assert.equal(store.getSnapshot('library-a'), null);
    assert.equal(store.getLibraryState('library-a').recentTerminal.phase, 'completed');
    assert.equal(store.getSnapshot('library-b').taskId, 'task-b');
    store.clearHistory('library-a');
    assert.equal(store.getLibraryState('library-a').recentTerminal, null);
    assert.equal(store.getSnapshot('library-b').taskId, 'task-b');
    unsubscribe();
});

test('terminal history is bounded without evicting active library tasks', () => {
    const store = createScanProgressStore();
    store.set(progress('active', 'active-task'));
    for (let index = 0; index < 24; index += 1) {
        store.complete({ ...progress(`history-${index}`, `task-${index}`), phase: 'failed' });
    }
    assert.equal(store.getSnapshot('active').taskId, 'active-task');
    assert.equal(store.getLibraryState('history-0').recentTerminal, null);
    assert.equal(store.getLibraryState('history-23').recentTerminal.phase, 'failed');
});
