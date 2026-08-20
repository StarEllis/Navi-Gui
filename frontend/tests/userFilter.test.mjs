import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import ts from 'typescript';

const source = await readFile(new URL('../src/utils/userFilter.ts', import.meta.url), 'utf8');
const compiled = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 },
}).outputText;
const moduleURL = `data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`;
const {
    countUserFilterConditions,
    describeUserFilter,
    groupSelectedTags,
    isUserFilterEmpty,
    normalizeUserFilter,
    ratingConditionLabel,
    sortScores,
    toggleScore,
} = await import(moduleURL);

const tag = (id, name, category) => ({ id, name, category, count: 0 });

const TAGS = [
    tag('low', '70-80分美女', '颜值'),
    tag('high', '80-90分', '颜值'),
    tag('shape', '身材好', '身材'),
    tag('loose', '剧情不错', ''),
    tag('loose2', '画质好', ''),
];

test('同一分组的标签落进同一组，不同分组各自成组', () => {
    const groups = groupSelectedTags(new Set(['low', 'high', 'shape']), TAGS);
    // 「颜值」在前、「身材」在后：按分组名排序，组序才稳定
    assert.deepEqual(groups, [['shape'], ['low', 'high']]);
});

test('未分组的标签合成一组，不会拆成互相 AND 的多组', () => {
    const groups = groupSelectedTags(new Set(['loose', 'loose2']), TAGS);
    assert.deepEqual(groups, [['loose', 'loose2']]);
});

test('未分组和有分组混选时，未分组那一组排在最前（空字符串最小）', () => {
    const groups = groupSelectedTags(new Set(['loose', 'shape']), TAGS);
    assert.deepEqual(groups, [['loose'], ['shape']]);
});

test('没选中任何标签时不产生空组', () => {
    assert.deepEqual(groupSelectedTags(new Set(), TAGS), []);
});

test('组序只由分组名决定，与选中顺序无关', () => {
    const a = groupSelectedTags(new Set(['shape', 'low']), TAGS);
    const b = groupSelectedTags(new Set(['low', 'shape']), TAGS);
    assert.deepEqual(a, b);
});

test('空条件的判定：未评分是一条真实条件，不是「没选」', () => {
    assert.equal(isUserFilterEmpty({ scores: [], tag_groups: [] }), true);
    assert.equal(isUserFilterEmpty({ scores: [], tag_groups: [[]] }), true);
    assert.equal(isUserFilterEmpty({ scores: [0], tag_groups: [] }), false);
    assert.equal(isUserFilterEmpty({ scores: [5], tag_groups: [] }), false);
    assert.equal(isUserFilterEmpty({ scores: [], tag_groups: [['low']] }), false);
});

test('顶栏计数：每个星级各算一条，标签逐个算', () => {
    assert.equal(countUserFilterConditions({ scores: [5, 4], tag_groups: [['low', 'high'], ['shape']] }), 5);
    assert.equal(countUserFilterConditions({ scores: [], tag_groups: [] }), 0);
    assert.equal(countUserFilterConditions(null), 0);
});

test('星级永远按 5→1 存，未评分收尾', () => {
    assert.deepEqual(sortScores([3, 5, 0, 4]), [5, 4, 3, 0]);
    assert.deepEqual(sortScores([1, 1, 2]), [2, 1]);
    assert.deepEqual(sortScores([]), []);
});

test('点一下加进去，再点一下摘掉', () => {
    const base = { scores: [], tag_groups: [] };
    const withFive = toggleScore(base, 5);
    assert.deepEqual(withFive.scores, [5]);
    const withFour = toggleScore(withFive, 4);
    assert.deepEqual(withFour.scores, [5, 4]);
    assert.deepEqual(toggleScore(withFour, 5).scores, [4]);
});

test('连着一段顶到 5 星的说成「N 星以上」', () => {
    assert.equal(ratingConditionLabel({ scores: [5, 4], tag_groups: [] }), '4 星以上');
    assert.equal(ratingConditionLabel({ scores: [5, 4, 3], tag_groups: [] }), '3 星以上');
    assert.equal(ratingConditionLabel({ scores: [5, 4, 3, 2, 1], tag_groups: [] }), '1 星以上');
});

test('不连着的星级逐档列出来，不能糊成「N 星以上」', () => {
    assert.equal(ratingConditionLabel({ scores: [5, 3], tag_groups: [] }), '5 星 或 3 星');
    assert.equal(ratingConditionLabel({ scores: [4, 2], tag_groups: [] }), '4 星 或 2 星');
    // 连着但没顶到 5 星，也不是「以上」
    assert.equal(ratingConditionLabel({ scores: [3, 2], tag_groups: [] }), '3 星 或 2 星');
});

test('单档和未评分', () => {
    assert.equal(ratingConditionLabel({ scores: [5], tag_groups: [] }), '5 星');
    assert.equal(ratingConditionLabel({ scores: [0], tag_groups: [] }), '未评分');
    assert.equal(ratingConditionLabel({ scores: [5, 0], tag_groups: [] }), '5 星 或 未评分');
    assert.equal(ratingConditionLabel({ scores: [5, 4, 0], tag_groups: [] }), '4 星以上 或 未评分');
    assert.equal(ratingConditionLabel({ scores: [], tag_groups: [] }), '');
});

test('常用筛选的默认名字把「组内是或」写出来', () => {
    const name = describeUserFilter(
        { scores: [5], tag_groups: [['low', 'high'], ['shape']] },
        TAGS,
    );
    assert.equal(name, '5 星 + 70-80分美女 或 80-90分 + 身材好');
});

test('已经被删掉的标签 id 不会在名字里留下空档', () => {
    const name = describeUserFilter(
        { scores: [], tag_groups: [['low', 'gone']] },
        TAGS,
    );
    assert.equal(name, '70-80分美女');
});

test('老结构的常用筛选读出来翻译成星级集合', () => {
    // 4 星以上 -> [5,4]
    assert.deepEqual(normalizeUserFilter({ min_rating: 4, exact_rating: 0, tag_groups: [] }).scores, [5, 4]);
    // 只要五星 -> [5]
    assert.deepEqual(normalizeUserFilter({ min_rating: 0, exact_rating: 5, tag_groups: [] }).scores, [5]);
    // 未评分 -> [0]
    assert.deepEqual(normalizeUserFilter({ min_rating: 0, exact_rating: -1, tag_groups: [] }).scores, [0]);
    // 不限 -> []
    assert.deepEqual(normalizeUserFilter({ min_rating: 0, exact_rating: 0, tag_groups: [] }).scores, []);
    // 标签组照原样带过来
    assert.deepEqual(
        normalizeUserFilter({ min_rating: 3, tag_groups: [['a', 'b'], []] }),
        { scores: [5, 4, 3], tag_groups: [['a', 'b']] },
    );
});

test('新结构原样通过，脏数据被剔掉', () => {
    assert.deepEqual(normalizeUserFilter({ scores: [4, 9, -2, 5], tag_groups: [] }).scores, [5, 4]);
    assert.deepEqual(normalizeUserFilter(null), { scores: [], tag_groups: [] });
});
