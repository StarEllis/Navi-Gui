export type SortOrder = 'asc' | 'desc';
export type SortField = 'created_at' | 'release_date' | 'video_codec' | 'last_watched' | 'favorite_at' | 'rating';
export type SortViewName = 'libs' | 'watched' | 'favorite';
export type SortConfig = { field: SortField; order: SortOrder };
export type SortPreferences = Record<SortViewName, SortConfig>;

type SortPreferenceStorage = Pick<Storage, 'getItem' | 'setItem'>;

const SORT_PREFERENCES_STORAGE_KEY = 'navi.desktop.sortPreferences.v1';

export const DEFAULT_SORTS: SortPreferences = {
    libs: { field: 'created_at', order: 'desc' },
    watched: { field: 'last_watched', order: 'desc' },
    favorite: { field: 'favorite_at', order: 'desc' },
};

const ALLOWED_FIELDS: Record<SortViewName, ReadonlySet<SortField>> = {
    libs: new Set(['created_at', 'release_date', 'video_codec', 'last_watched']),
    watched: new Set(['last_watched', 'created_at', 'rating']),
    favorite: new Set(['favorite_at', 'created_at', 'rating']),
};

const resolveStorage = (): SortPreferenceStorage | null => {
    if (typeof window === 'undefined' || !window.localStorage) {
        return null;
    }
    return window.localStorage;
};

const normalizeConfig = (view: SortViewName, value: unknown): SortConfig => {
    const candidate = value && typeof value === 'object' ? value as Partial<SortConfig> : {};
    const field = typeof candidate.field === 'string' && ALLOWED_FIELDS[view].has(candidate.field as SortField)
        ? candidate.field as SortField
        : DEFAULT_SORTS[view].field;
    const order = candidate.order === 'asc' || candidate.order === 'desc'
        ? candidate.order
        : DEFAULT_SORTS[view].order;
    return { field, order };
};

export const normalizeSortPreferences = (value: unknown): SortPreferences => {
    const candidate = value && typeof value === 'object' ? value as Partial<SortPreferences> : {};
    return {
        libs: normalizeConfig('libs', candidate.libs),
        watched: normalizeConfig('watched', candidate.watched),
        favorite: normalizeConfig('favorite', candidate.favorite),
    };
};

export const loadSortPreferences = (storage: SortPreferenceStorage | null = resolveStorage()): SortPreferences => {
    if (!storage) {
        return normalizeSortPreferences(null);
    }
    try {
        const raw = storage.getItem(SORT_PREFERENCES_STORAGE_KEY);
        return normalizeSortPreferences(raw ? JSON.parse(raw) : null);
    } catch (_error) {
        return normalizeSortPreferences(null);
    }
};

export const persistSortPreferences = (
    preferences: SortPreferences,
    storage: SortPreferenceStorage | null = resolveStorage(),
) => {
    if (!storage) {
        return false;
    }
    try {
        storage.setItem(SORT_PREFERENCES_STORAGE_KEY, JSON.stringify(normalizeSortPreferences(preferences)));
        return true;
    } catch (_error) {
        return false;
    }
};
