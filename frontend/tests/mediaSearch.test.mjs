import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import ts from 'typescript';

const source = await readFile(new URL('../src/utils/mediaSearch.ts', import.meta.url), 'utf8');
const compiled = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 },
}).outputText;
const moduleURL = `data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`;
const { normalizeSearchTerm, shouldReplaceActorFilterOnSearchChange } = await import(moduleURL);

test('known simplified, traditional and Japanese actor glyphs share one search form', () => {
    assert.equal(normalizeSearchTerm('三田真鈴'), normalizeSearchTerm('三田真铃'));
    assert.equal(normalizeSearchTerm('瀨名光'), normalizeSearchTerm('濑名光'));
    assert.equal(normalizeSearchTerm('瀬名光'), normalizeSearchTerm('濑名光'));
});

test('editing an actor label replaces the hidden actor filter with a normal search', () => {
    assert.equal(shouldReplaceActorFilterOnSearchChange('actor', '小松空', ''), true);
    assert.equal(shouldReplaceActorFilterOnSearchChange('actor', '小松空', '三田'), true);
    assert.equal(shouldReplaceActorFilterOnSearchChange('actor', '小松空', '小松空 三田'), true);
    assert.equal(shouldReplaceActorFilterOnSearchChange('actor', '小松空', '小松空'), false);
    assert.equal(shouldReplaceActorFilterOnSearchChange('genre', '剧情', '三田'), false);
});
