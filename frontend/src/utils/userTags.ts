import { ListMyTags } from '../../wailsjs/go/main/App';
import { normalizeUserFilter } from './userFilter';
import type { UserFilter, UserTag } from './userFilter';

// 纯逻辑住在 userFilter.ts 里（没有依赖，可单测），这里统一转出去，调用方只认一个入口。
export {
    countUserFilterConditions,
    describeUserFilter,
    EMPTY_USER_FILTER,
    groupSelectedTags,
    isUserFilterEmpty,
    normalizeUserFilter,
    RATING_LEVELS,
    ratingConditionLabel,
    scoreLabel,
    sortScores,
    toggleScore,
    UNRATED_SCORE,
} from './userFilter';
export type { UserFilter, UserTag } from './userFilter';

// 未分组的标签一律给中性点：圆点本身就是「这是我打的」的唯一标记，不能没有。
export const UNCATEGORIZED_DOT = 'rgba(255,255,255,.28)';
const CATEGORY_COLORS = ['#e0a05a', '#7fc79a', '#9aa8e0', '#d67878'];

// 分组颜色按「分组名在全部分组里的位置」四色循环，保证同一个分组在哪都是同一个颜色。
export const buildCategoryColorMap = (tags: ReadonlyArray<UserTag>) => {
    const categories = Array.from(new Set(
        tags.map((tag) => tag.category).filter((category) => category !== ''),
    )).sort();
    const colors = new Map<string, string>();
    categories.forEach((category, index) => {
        colors.set(category, CATEGORY_COLORS[index % CATEGORY_COLORS.length]);
    });
    return colors;
};

export const categoryColor = (colors: Map<string, string>, category: string) => (
    colors.get(category) || UNCATEGORIZED_DOT
);

export const UNCATEGORIZED_LABEL = '未分组';

// ==================== 标签表缓存 ====================
// 全库共用一份，进程内存一份，任何写操作后失效重拉。

let cachedTags: UserTag[] = [];
let inflight: Promise<UserTag[]> | null = null;
let loaded = false;
const listeners = new Set<() => void>();

const notify = () => listeners.forEach((listener) => listener());

const normalizeTag = (raw: any): UserTag => ({
    id: typeof raw?.id === 'string' ? raw.id : '',
    name: typeof raw?.name === 'string' ? raw.name : '',
    category: typeof raw?.category === 'string' ? raw.category : '',
    count: Number.isFinite(Number(raw?.count)) ? Math.max(0, Number(raw.count)) : 0,
});

export const subscribeUserTags = (listener: () => void) => {
    listeners.add(listener);
    return () => {
        listeners.delete(listener);
    };
};

export const getUserTagsSnapshot = () => cachedTags;

export const loadUserTags = async (force = false): Promise<UserTag[]> => {
    if (!force && loaded) {
        return cachedTags;
    }
    if (!force && inflight) {
        return inflight;
    }
    const request = ListMyTags()
        .then((rows: any) => {
            cachedTags = (Array.isArray(rows) ? rows : []).map(normalizeTag).filter((tag) => tag.id !== '');
            loaded = true;
            notify();
            return cachedTags;
        })
        .catch((reason) => {
            console.error(reason);
            return cachedTags;
        })
        .finally(() => {
            if (inflight === request) {
                inflight = null;
            }
        });
    inflight = request;
    return request;
};

// 任何标签写操作之后调用：重拉一次并通知所有订阅者。
export const invalidateUserTags = () => {
    loaded = false;
    void loadUserTags(true);
};

// ==================== 常用筛选（只进 localStorage，不进数据库） ====================

export type SavedFilter = {
    id: string;
    name: string;
    libraryID: string;
    filter: UserFilter;
};

const SAVED_FILTERS_KEY = 'navi.savedFilters';

const isValidSavedFilter = (value: any) => (
    Boolean(value)
    && typeof value.id === 'string' && value.id !== ''
    && typeof value.name === 'string'
    && typeof value.libraryID === 'string'
    && Boolean(value.filter)
);

export const loadSavedFilters = (): SavedFilter[] => {
    try {
        const raw = window.localStorage.getItem(SAVED_FILTERS_KEY);
        const parsed = raw ? JSON.parse(raw) : [];
        if (!Array.isArray(parsed)) {
            return [];
        }
        // 老结构（min_rating / exact_rating）就地翻译成星级集合，别让存过的收藏失效
        return parsed.filter(isValidSavedFilter).map((saved: any): SavedFilter => ({
            id: saved.id,
            name: saved.name,
            libraryID: saved.libraryID,
            filter: normalizeUserFilter(saved.filter),
        }));
    } catch (error) {
        console.error(error);
        return [];
    }
};

export const persistSavedFilters = (filters: ReadonlyArray<SavedFilter>) => {
    try {
        window.localStorage.setItem(SAVED_FILTERS_KEY, JSON.stringify(filters));
    } catch (error) {
        console.error(error);
    }
};
