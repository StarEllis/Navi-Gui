import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import ts from 'typescript';

const source = await readFile(new URL('../src/utils/filterNavigation.ts', import.meta.url), 'utf8');
const compiled = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 },
}).outputText;
const moduleURL = `data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`;
const { getFilterReturnLabel, getTopBarBackLabel } = await import(moduleURL);

test('return button label describes the saved source', () => {
    assert.equal(getFilterReturnLabel(null), '');
    assert.equal(getFilterReturnLabel({ view: 'libs', media: { id: 'media-1' } }), '返回详情');
    assert.equal(getFilterReturnLabel({ view: 'actor', media: null }), '返回演员');
    assert.equal(getFilterReturnLabel({ view: 'genre', media: null }), '返回类别');
    assert.equal(getFilterReturnLabel({ view: 'favorite', media: null }), '返回');
});

test('media library root has no back action', () => {
    assert.equal(getTopBarBackLabel(null, 'libs'), '');
    assert.equal(getTopBarBackLabel(null, 'settings'), '');
    assert.equal(getTopBarBackLabel(null, 'actor'), '返回主页');
    assert.equal(getTopBarBackLabel({ view: 'libs', media: { id: 'media-1' } }, 'libs'), '返回详情');
});
