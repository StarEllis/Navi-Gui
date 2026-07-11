import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import ts from 'typescript';

const sourceURL = new URL('../src/utils/scanTaskEvents.ts', import.meta.url);
const source = await readFile(sourceURL, 'utf8');
const compiled = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 },
}).outputText;
const moduleURL = `data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`;
const {
    activateScanTaskFromEvent,
    activateScanTaskFromResponse,
    beginScanRequest,
    canActivateScanTask,
    completeScanTask,
    createScanTaskLifecycle,
    isCurrentScanEvent,
    registerScanEventListeners,
    scanTerminalEndsLoading,
} = await import(moduleURL);

test('terminal event before promise resolution cannot reactivate its task', () => {
    const state = createScanTaskLifecycle();
    const generation = beginScanRequest(state, 'library-a');
    assert.equal(activateScanTaskFromEvent(state, { library_id: 'library-a', task_id: 'task-fast' }), true);
    assert.equal(completeScanTask(state, { library_id: 'library-a', task_id: 'task-fast' }), true);
    assert.equal(activateScanTaskFromResponse(state, 'library-a', generation, 'task-fast'), false);
    assert.equal(state.activeTaskIDs.has('library-a'), false);

    const nextGeneration = beginScanRequest(state, 'library-a');
    assert.equal(activateScanTaskFromEvent(state, { library_id: 'library-a', task_id: 'task-next' }), true);
    assert.equal(activateScanTaskFromResponse(state, 'library-a', nextGeneration, 'task-next'), true);
});

test('old terminal cannot replace a newer task and libraries remain independent', () => {
    const state = createScanTaskLifecycle();
    beginScanRequest(state, 'library-a');
    beginScanRequest(state, 'library-b');
	assert.equal(activateScanTaskFromEvent(state, { library_id: 'library-a', task_id: 'task-a-old' }), true);
	beginScanRequest(state, 'library-a');
	assert.equal(activateScanTaskFromEvent(state, { library_id: 'library-a', task_id: 'task-a-new' }), true);
    assert.equal(activateScanTaskFromEvent(state, { library_id: 'library-b', task_id: 'task-b' }), true);
    assert.equal(completeScanTask(state, { library_id: 'library-a', task_id: 'task-a-old' }), false);
    assert.equal(state.activeTaskIDs.get('library-a'), 'task-a-new');
    assert.equal(state.activeTaskIDs.get('library-b'), 'task-b');
    assert.equal(completeScanTask(state, { library_id: 'library-b', task_id: 'task-b' }), true);
    assert.equal(state.activeTaskIDs.get('library-a'), 'task-a-new');
});

test('late event from an old task is ignored', () => {
    assert.equal(isCurrentScanEvent('task-new', { task_id: 'task-old' }), false);
    assert.equal(isCurrentScanEvent('task-new', { task_id: 'task-new' }), true);
    assert.equal(isCurrentScanEvent(undefined, { task_id: 'task-new' }), false);
});

test('late start event cannot replace a newer active task', () => {
    assert.equal(canActivateScanTask('task-new', { task_id: 'task-old' }), false);
    assert.equal(canActivateScanTask('task-new', { task_id: 'task-new' }), true);
    assert.equal(canActivateScanTask(undefined, { task_id: 'task-new' }), true);
});

test('all terminal outcomes end loading', () => {
    for (const event of ['scan:completed', 'scan:incomplete', 'scan:failed', 'scan:canceled']) {
        assert.equal(scanTerminalEndsLoading(event), true, event);
    }
    assert.equal(scanTerminalEndsLoading('scan:progress'), false);
});

test('listener cleanup releases each registration exactly once', () => {
    const registrations = [];
    const releases = [];
    const cleanup = registerScanEventListeners(
        (event) => {
            registrations.push(event);
            return () => releases.push(event);
        },
        {
            'scan:start': () => {},
            'scan:progress': () => {},
            'scan:completed': () => {},
            'scan:incomplete': () => {},
            'scan:failed': () => {},
            'scan:canceled': () => {},
        },
    );
    assert.equal(new Set(registrations).size, registrations.length);
    cleanup();
    cleanup();
    assert.deepEqual(releases, registrations);
});
