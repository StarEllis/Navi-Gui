import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import ts from 'typescript';

const source = await readFile(new URL('../src/utils/sortPreferences.ts', import.meta.url), 'utf8');
const compiled = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 },
}).outputText;
const moduleURL = `data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`;
const { loadSortPreferences, persistSortPreferences } = await import(moduleURL);

const createStorage = () => {
    const values = new Map();
    return {
        getItem: (key) => values.get(key) ?? null,
        setItem: (key, value) => values.set(key, value),
    };
};

test('sort choices survive a new application initialization', () => {
    const storage = createStorage();
    const selected = {
        libs: { field: 'release_date', order: 'asc' },
        watched: { field: 'rating', order: 'desc' },
        favorite: { field: 'created_at', order: 'asc' },
    };

    assert.equal(persistSortPreferences(selected, storage), true);
    assert.deepEqual(loadSortPreferences(storage), selected);
});

test('invalid persisted fields fall back independently for each view', () => {
    const storage = createStorage();
    storage.setItem('navi.desktop.sortPreferences.v1', JSON.stringify({
        libs: { field: 'favorite_at', order: 'sideways' },
        watched: { field: 'rating', order: 'asc' },
        favorite: { field: 'not-a-field', order: 'desc' },
    }));

    assert.deepEqual(loadSortPreferences(storage), {
        libs: { field: 'created_at', order: 'desc' },
        watched: { field: 'rating', order: 'asc' },
        favorite: { field: 'favorite_at', order: 'desc' },
    });
});
