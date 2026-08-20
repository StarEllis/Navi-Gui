import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import ts from 'typescript';

const source = await readFile(new URL('../src/utils/mediaSearch.ts', import.meta.url), 'utf8');
const compiled = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 },
}).outputText;
const moduleURL = `data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`;
const { expandSearchTokenRange } = await import(moduleURL);

test('double clicking a number keeps the leading hyphen of a code', () => {
    assert.deepEqual(expandSearchTokenRange('-450', 1, 4), { start: 0, end: 4 });
    assert.deepEqual(expandSearchTokenRange('SSIS-950', 5, 8), { start: 0, end: 8 });
    assert.deepEqual(expandSearchTokenRange('SSIS-950', 0, 4), { start: 0, end: 8 });
});

test('expansion stops at whitespace and CJK characters', () => {
    assert.deepEqual(expandSearchTokenRange('三上 SSIS-950 悠亚', 3, 7), { start: 3, end: 11 });
    assert.deepEqual(expandSearchTokenRange('番号SSIS-950', 7, 10), { start: 2, end: 10 });
});

test('non-token selections are left untouched', () => {
    assert.deepEqual(expandSearchTokenRange('-450', 2, 2), { start: 2, end: 2 });
    assert.deepEqual(expandSearchTokenRange('三上悠亚-950', 0, 4), { start: 0, end: 4 });
});
