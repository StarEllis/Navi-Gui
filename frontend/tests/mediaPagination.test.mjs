import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import ts from 'typescript';

const sourceURL = new URL('../src/utils/mediaPagination.ts', import.meta.url);
const source = await readFile(sourceURL, 'utf8');
const compiled = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 },
}).outputText;
const moduleURL = `data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`;
const pagination = await import(moduleURL);

const query = (overrides = {}) => ({
    libraryId: 'library-a',
    page: 1,
    pageSize: 120,
    searchTerm: '',
    mediaType: '',
    sortBy: 'created_at',
    sortOrder: 'desc',
    favorite: null,
    watched: null,
    filterType: '',
    filterValue: '',
    ...overrides,
});

test('concurrent identical page requests are deduplicated', async () => {
    let calls = 0;
    let resolve;
    const loader = () => {
        calls += 1;
        return new Promise((done) => { resolve = done; });
    };
    const first = pagination.requestMediaPage(query(), loader, 'gate:1');
    const second = pagination.requestMediaPage(query(), loader, 'gate:1');
    assert.equal(first, second);
    assert.equal(calls, 1);
    resolve({ items: [], total: 0, page: 1, pageSize: 120 });
    await first;
});

test('new generations can request the same page while stale work is pending', async () => {
    let calls = 0;
    const resolvers = [];
    const loader = () => {
        calls += 1;
        return new Promise((resolve, reject) => resolvers.push({ resolve, reject }));
    };
    const oldRequest = pagination.requestMediaPage(query(), loader, 'gate:1');
    const newRequest = pagination.requestMediaPage(query(), loader, 'gate:2');
    assert.equal(calls, 2);
    resolvers[1].resolve({ items: [{ id: 'new' }], total: 1, page: 1, pageSize: 120 });
    assert.equal((await newRequest).items[0].id, 'new');
    resolvers[0].reject(new Error('stale failure'));
    await assert.rejects(oldRequest, /stale failure/);
});

test('loading page state is owned and cleared by page plus generation', () => {
    let loading = pagination.markPageLoading(new Map(), 1, 1);
    loading = pagination.markPageLoading(loading, 1, 2);
    assert.equal(pagination.isPageLoadingForGeneration(loading, 1, 1), false);
    assert.equal(pagination.isPageLoadingForGeneration(loading, 1, 2), true);
    assert.equal(pagination.finishPageLoading(loading, 1, 1), loading);
    loading = pagination.finishPageLoading(loading, 1, 2);
    assert.equal(loading.has(1), false);
});

test('old generations and disposed consumers reject late responses', () => {
    const gate = pagination.createLatestRequestGate();
    const oldGeneration = gate.next();
    const latestGeneration = gate.next();
    assert.equal(gate.accepts(oldGeneration), false);
    assert.equal(gate.accepts(latestGeneration), true);
    gate.dispose();
    assert.equal(gate.accepts(latestGeneration), false);
});

test('visible ranges request bounded adjacent pages and reach the last item', () => {
    assert.deepEqual(pagination.getPagesForVisibleRange(0, 30, 10000), [1, 2]);
    assert.deepEqual(pagination.getPagesForVisibleRange(9960, 10000, 10000), [83, 84]);
    assert.deepEqual(pagination.getPagesForVisibleRange(0, 30, 0), []);
});

test('page cache remains bounded while preserving visible pages', () => {
    let pages = new Map();
    for (let page = 1; page <= 20; page += 1) {
        pages = pagination.putMediaPage(pages, page, [{ id: page }], [page], 7);
    }
    assert.equal(pages.size, 7);
    assert.equal(pages.has(20), true);
    assert.equal(pagination.getMediaAtIndex(new Map([[84, Array.from({ length: 40 }, (_, index) => ({ id: index }))]]), 9999)?.id, 39);
});

test('page and page size are clamped', () => {
    assert.equal(pagination.normalizeMediaPage(-10), 1);
    assert.equal(pagination.normalizeMediaPageSize(10000), 200);
});

test('10k virtual windows stay bounded across scroll, resize, end and empty states', () => {
    const first = pagination.getVirtualGridWindow(10000, 6, 0, 900, 321, 2);
    assert.ok(first.endIndex - first.startIndex <= 42);
    const scrolled = pagination.getVirtualGridWindow(10000, 6, 16000, 900, 321, 2);
    assert.ok(scrolled.startIndex > first.startIndex);
    const resized = pagination.getVirtualGridWindow(10000, 3, 16000, 900, 321, 2);
    assert.notEqual(resized.startIndex, scrolled.startIndex);
    const end = pagination.getVirtualGridWindow(10000, 6, Number.MAX_SAFE_INTEGER, 900, 321, 2);
    assert.equal(end.endIndex, 10000);
    assert.deepEqual(pagination.getVirtualGridWindow(0, 6, 0, 900, 321, 2), {
        totalRows: 0,
        startRow: 0,
        endRow: 0,
        startIndex: 0,
        endIndex: 0,
    });
});
