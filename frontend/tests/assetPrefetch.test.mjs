import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import ts from 'typescript';

const source = await readFile(new URL('../src/utils/assetPrefetch.ts', import.meta.url), 'utf8');
const compiled = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 },
}).outputText;
const moduleURL = `data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`;
const { ASSET_PREFETCH_BYTES, createAssetPrefetcher } = await import(moduleURL);

const recordingFetch = (result = Promise.resolve()) => {
    const calls = [];
    const fetchAsset = (url, init) => {
        calls.push({ url, init });
        return result;
    };
    return { calls, fetchAsset };
};

test('warming asks only for the head of the file', () => {
    const { calls, fetchAsset } = recordingFetch();
    const prefetch = createAssetPrefetcher(fetchAsset);

    assert.equal(prefetch('/local/trailer.mp4'), true);
    assert.equal(calls.length, 1);
    assert.equal(calls[0].url, '/local/trailer.mp4');
    assert.equal(calls[0].init.headers.Range, `bytes=0-${ASSET_PREFETCH_BYTES - 1}`);
});

test('hovering the same tile again does not refetch', () => {
    const { calls, fetchAsset } = recordingFetch();
    const prefetch = createAssetPrefetcher(fetchAsset);

    prefetch('/local/trailer.mp4');
    assert.equal(prefetch('/local/trailer.mp4'), false);
    assert.equal(prefetch('  /local/trailer.mp4  '), false);
    assert.equal(calls.length, 1);
});

test('a failed warm-up can be retried on the next hover', async () => {
    const { calls, fetchAsset } = recordingFetch(Promise.reject(new Error('offline')));
    const prefetch = createAssetPrefetcher(fetchAsset);

    prefetch('/local/trailer.mp4');
    await new Promise((resolve) => setImmediate(resolve));
    assert.equal(prefetch('/local/trailer.mp4'), true);
    assert.equal(calls.length, 2);
});

test('empty paths and a missing fetch are no-ops', () => {
    const { calls, fetchAsset } = recordingFetch();
    const prefetch = createAssetPrefetcher(fetchAsset);

    assert.equal(prefetch(''), false);
    assert.equal(prefetch('   '), false);
    assert.equal(calls.length, 0);
    assert.equal(createAssetPrefetcher(null)('/local/trailer.mp4'), false);
});

test('a throwing fetch does not escape', () => {
    const prefetch = createAssetPrefetcher(() => {
        throw new Error('blocked');
    });
    assert.equal(prefetch('/local/trailer.mp4'), false);
});
