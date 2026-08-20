// 「我的评分 / 我的标签」筛选的纯逻辑。没有任何依赖，方便单测。

export type UserTag = {
    id: string;
    name: string;
    category: string;
    count: number;
};

export type UserFilter = {
    // 选中的星级，元素取值 1-5，0 表示「未评分」。
    // 星级之间是「或」：[5,4] 等价于四星以上，[5,3] 就只要五星和三星。
    scores: number[];
    tag_groups: string[][];
};

export const RATING_LEVELS = [5, 4, 3, 2, 1] as const;
export const UNRATED_SCORE = 0;

export const EMPTY_USER_FILTER: UserFilter = { scores: [], tag_groups: [] };

export const isUserFilterEmpty = (filter: UserFilter | null | undefined) => (
    !filter
    || (filter.scores.length === 0 && filter.tag_groups.every((group) => group.length === 0))
);

export const countUserFilterConditions = (filter: UserFilter | null | undefined) => {
    if (!filter) {
        return 0;
    }
    return filter.scores.length + filter.tag_groups.reduce((sum, group) => sum + group.length, 0);
};

// 星级永远按 5→1 存，未评分收尾，这样条件条和名字的顺序都是稳定的。
export const sortScores = (scores: ReadonlyArray<number>) => {
    const unique = Array.from(new Set(scores)).filter((score) => score >= 0 && score <= 5);
    const rated = unique.filter((score) => score > 0).sort((a, b) => b - a);
    return unique.includes(UNRATED_SCORE) ? [...rated, UNRATED_SCORE] : rated;
};

export const toggleScore = (filter: UserFilter, score: number): UserFilter => {
    const next = filter.scores.includes(score)
        ? filter.scores.filter((value) => value !== score)
        : [...filter.scores, score];
    return { ...filter, scores: sortScores(next) };
};

// tag_groups 的分组就是 category：同一 category 的标签归一组（组内 OR，组间 AND），
// 未分组的标签合成一组。按 category 名排序保证组序稳定，后端算 facet 时靠组下标排除自身。
export const groupSelectedTags = (selected: ReadonlySet<string>, tags: ReadonlyArray<UserTag>) => {
    const byCategory = new Map<string, string[]>();
    tags.forEach((tag) => {
        if (!selected.has(tag.id)) {
            return;
        }
        const group = byCategory.get(tag.category) || [];
        group.push(tag.id);
        byCategory.set(tag.category, group);
    });
    return Array.from(byCategory.keys()).sort().map((category) => byCategory.get(category) || []);
};

export const scoreLabel = (score: number) => (score === UNRATED_SCORE ? '未评分' : `${score} 星`);

// 连着一段一直顶到 5 星的，说成「N 星以上」——这正是用户选 5+4 时想表达的意思。
export const ratingConditionLabel = (filter: UserFilter) => {
    const scores = sortScores(filter.scores);
    if (scores.length === 0) {
        return '';
    }
    const rated = scores.filter((score) => score > 0);
    const parts: string[] = [];
    if (rated.length > 1 && rated[0] === 5 && rated[rated.length - 1] === 5 - rated.length + 1) {
        parts.push(`${rated[rated.length - 1]} 星以上`);
    } else {
        rated.forEach((score) => parts.push(scoreLabel(score)));
    }
    if (scores.includes(UNRATED_SCORE)) {
        parts.push('未评分');
    }
    return parts.join(' 或 ');
};

// 「存为常用」的默认名字由条件拼出来，用户可改。
export const describeUserFilter = (filter: UserFilter, tags: ReadonlyArray<UserTag>) => {
    const nameByID = new Map(tags.map((tag) => [tag.id, tag.name]));
    const parts: string[] = [];
    const rating = ratingConditionLabel(filter);
    if (rating) {
        parts.push(rating);
    }
    filter.tag_groups.forEach((group) => {
        const names = group.map((tagID) => nameByID.get(tagID)).filter(Boolean) as string[];
        if (names.length > 0) {
            parts.push(names.join(' 或 '));
        }
    });
    return parts.join(' + ');
};

// 存在 localStorage 里的常用筛选可能还是老结构（min_rating / exact_rating），
// 读出来时就地翻译成星级集合，省得用户的收藏一升级就失效。
export const normalizeUserFilter = (value: any): UserFilter => {
    const tagGroups: string[][] = Array.isArray(value?.tag_groups)
        ? value.tag_groups
            .filter(Array.isArray)
            .map((group: any[]) => group.filter((id) => typeof id === 'string' && id !== ''))
            .filter((group: string[]) => group.length > 0)
        : [];

    if (Array.isArray(value?.scores)) {
        const scores = value.scores.filter((score: any) => Number.isInteger(score) && score >= 0 && score <= 5);
        return { scores: sortScores(scores), tag_groups: tagGroups };
    }

    const exact = Number(value?.exact_rating) || 0;
    const min = Number(value?.min_rating) || 0;
    let scores: number[] = [];
    if (exact === -1) {
        scores = [UNRATED_SCORE];
    } else if (exact > 0) {
        scores = [exact];
    } else if (min > 0) {
        scores = RATING_LEVELS.filter((level) => level >= min);
    }
    return { scores: sortScores(scores), tag_groups: tagGroups };
};
