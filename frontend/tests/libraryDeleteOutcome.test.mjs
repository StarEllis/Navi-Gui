import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import ts from 'typescript';

const sourceURL = new URL('../src/utils/libraryDeleteOutcome.ts', import.meta.url);
const source = await readFile(sourceURL, 'utf8');
const compiled = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 },
}).outputText;
const moduleURL = `data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`;
const { applyLibraryDeleteOutcome } = await import(moduleURL);

test('committed deletion calls onDeleted even when cache cleanup warns', () => {
    let deletedCalls = 0;
    const warnings = [];
    const handled = applyLibraryDeleteOutcome(
        { deleted: true, warning: 'cache cleanup incomplete' },
        () => { deletedCalls += 1; },
        (warning) => warnings.push(warning),
    );
    assert.equal(handled, true);
    assert.equal(deletedCalls, 1);
    assert.deepEqual(warnings, ['cache cleanup incomplete']);
});

test('failed deletion result does not call onDeleted', () => {
    let deletedCalls = 0;
    const handled = applyLibraryDeleteOutcome(
        { deleted: false },
        () => { deletedCalls += 1; },
        () => {},
    );
    assert.equal(handled, false);
    assert.equal(deletedCalls, 0);
});
