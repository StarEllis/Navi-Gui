import { buildMediaSearchText } from './mediaSearch';
import { shouldInvalidateMediaPagination } from './mediaPlaybackState';

const LIBRARIES_STORAGE_KEY = 'navi.desktop.cache.libraries.v1';
const CURRENT_LIBRARY_STORAGE_KEY = 'navi.desktop.cache.currentLibraryId.v1';
const MEDIA_LIST_STORAGE_KEY = 'navi.desktop.cache.mediaList.v1';

const MEDIA_LIST_CACHE_VERSION = 3;
const MEDIA_LIST_CACHE_TTL_MS = 7 * 24 * 60 * 60 * 1000;
const MAX_MEDIA_LIST_ENTRIES = 10;

export type MediaListCacheEntry = {
    items: any[];
    total: number;
};

type PersistedMediaListCacheEntry = MediaListCacheEntry & {
    updatedAt: number;
};

type PersistedMediaListCacheStore = {
    version: number;
    order: string[];
    entries: Record<string, PersistedMediaListCacheEntry>;
};

type InitialLibraryState = {
    libraries: any[];
    currentLibrary: any | null;
};

const mediaListMemoryCache = new Map<string, MediaListCacheEntry>();

const canUseStorage = () => typeof window !== 'undefined' && typeof window.localStorage !== 'undefined';

const readStorageValue = (key: string) => {
    if (!canUseStorage()) {
        return null;
    }

    try {
        return window.localStorage.getItem(key);
    } catch (_error) {
        return null;
    }
};

const writeStorageValue = (key: string, value: string) => {
    if (!canUseStorage()) {
        return false;
    }

    try {
        window.localStorage.setItem(key, value);
        return true;
    } catch (_error) {
        return false;
    }
};

const removeStorageValue = (key: string) => {
    if (!canUseStorage()) {
        return;
    }

    try {
        window.localStorage.removeItem(key);
    } catch (_error) {
        // ignore storage cleanup failures
    }
};

const readStorageJSON = <T,>(key: string): T | null => {
    const raw = readStorageValue(key);
    if (!raw) {
        return null;
    }

    try {
        return JSON.parse(raw) as T;
    } catch (_error) {
        return null;
    }
};

const pickCachedMediaFields = (item: any) => ({
    id: typeof item?.id === 'string' ? item.id : '',
    title: typeof item?.title === 'string' ? item.title : '',
    orig_title: typeof item?.orig_title === 'string' ? item.orig_title : '',
    year: typeof item?.year === 'number' ? item.year : 0,
    code: typeof item?.code === 'string' ? item.code : '',
    actor: typeof item?.actor === 'string' ? item.actor : '',
    genres: typeof item?.genres === 'string' ? item.genres : '',
    studio: typeof item?.studio === 'string' ? item.studio : '',
    maker: typeof item?.maker === 'string' ? item.maker : '',
    label: typeof item?.label === 'string' ? item.label : '',
    release_date_normalized: typeof item?.release_date_normalized === 'string' ? item.release_date_normalized : '',
    poster_path: typeof item?.poster_path === 'string' ? item.poster_path : '',
    backdrop_path: typeof item?.backdrop_path === 'string' ? item.backdrop_path : '',
    file_path: typeof item?.file_path === 'string' ? item.file_path : '',
    duration: typeof item?.duration === 'number' ? item.duration : 0,
    position: typeof item?.position === 'number' ? item.position : 0,
    watch_duration: typeof item?.watch_duration === 'number' ? item.watch_duration : 0,
    progress_percent: typeof item?.progress_percent === 'number' ? item.progress_percent : 0,
    completed: Boolean(item?.completed),
    last_watched_at: typeof item?.last_watched_at === 'string' ? item.last_watched_at : '',
    playback_state: typeof item?.playback_state === 'string' ? item.playback_state : '',
    // Revisions are process-local ordering tokens and must not survive restart.
    revision: 0,
    search_text: buildMediaSearchText(item),
    is_favorite: Boolean(item?.is_favorite),
    is_watched: Boolean(item?.is_watched),
});

const normalizeCachedMediaItems = (items: unknown) => {
    if (!Array.isArray(items)) {
        return [];
    }

    return items
        .map((item) => pickCachedMediaFields(item))
        .filter((item) => item.id);
};

const normalizeMediaListCacheStore = (
    store: PersistedMediaListCacheStore | null,
): PersistedMediaListCacheStore => {
    const now = Date.now();
    const normalizedEntries: Record<string, PersistedMediaListCacheEntry> = {};
    const rawEntries = store?.entries || {};

    Object.entries(rawEntries).forEach(([key, entry]) => {
        if (!entry || typeof entry !== 'object') {
            return;
        }

        const updatedAt = typeof entry.updatedAt === 'number' ? entry.updatedAt : 0;
        if (!updatedAt || now-updatedAt > MEDIA_LIST_CACHE_TTL_MS) {
            return;
        }

        normalizedEntries[key] = {
            items: normalizeCachedMediaItems(entry.items),
            total: typeof entry.total === 'number' ? entry.total : 0,
            updatedAt,
        };
    });

    const normalizedOrder = Array.isArray(store?.order)
        ? store!.order.filter((key) => Boolean(normalizedEntries[key]))
        : [];

    Object.keys(normalizedEntries).forEach((key) => {
        if (!normalizedOrder.includes(key)) {
            normalizedOrder.push(key);
        }
    });

    const limitedOrder = normalizedOrder.slice(0, MAX_MEDIA_LIST_ENTRIES);
    const limitedEntries: Record<string, PersistedMediaListCacheEntry> = {};
    limitedOrder.forEach((key) => {
        limitedEntries[key] = normalizedEntries[key];
    });

    return {
        version: MEDIA_LIST_CACHE_VERSION,
        order: limitedOrder,
        entries: limitedEntries,
    };
};

const writeMediaListCacheStore = (store: PersistedMediaListCacheStore) => {
    if (Object.keys(store.entries).length === 0) {
        removeStorageValue(MEDIA_LIST_STORAGE_KEY);
        return;
    }

    writeStorageValue(MEDIA_LIST_STORAGE_KEY, JSON.stringify(store));
};

const readMediaListCacheStore = () => {
    const rawStore = readStorageJSON<PersistedMediaListCacheStore>(MEDIA_LIST_STORAGE_KEY);
    const normalizedStore = normalizeMediaListCacheStore(rawStore);

    const shouldRewrite =
        !rawStore ||
        rawStore.version !== normalizedStore.version ||
        JSON.stringify(rawStore.order || []) !== JSON.stringify(normalizedStore.order) ||
        Object.keys(rawStore.entries || {}).length !== Object.keys(normalizedStore.entries).length;

    if (shouldRewrite) {
        writeMediaListCacheStore(normalizedStore);
    }

    return normalizedStore;
};

export const loadInitialLibraryState = (): InitialLibraryState => {
    const libraries = readStorageJSON<any[]>(LIBRARIES_STORAGE_KEY);
    const normalizedLibraries = Array.isArray(libraries) ? libraries : [];
    const currentLibraryID = readStorageValue(CURRENT_LIBRARY_STORAGE_KEY) || '';
    const currentLibrary = normalizedLibraries.find((library) => library?.id === currentLibraryID) || normalizedLibraries[0] || null;

    return {
        libraries: normalizedLibraries,
        currentLibrary,
    };
};

export const persistLibraries = (libraries: any[]) => {
    if (!Array.isArray(libraries) || libraries.length === 0) {
        removeStorageValue(LIBRARIES_STORAGE_KEY);
        return;
    }

    writeStorageValue(LIBRARIES_STORAGE_KEY, JSON.stringify(libraries));
};

export const persistCurrentLibraryID = (libraryID: string) => {
    const normalizedLibraryID = typeof libraryID === 'string' ? libraryID.trim() : '';
    if (!normalizedLibraryID) {
        removeStorageValue(CURRENT_LIBRARY_STORAGE_KEY);
        return;
    }

    writeStorageValue(CURRENT_LIBRARY_STORAGE_KEY, normalizedLibraryID);
};

export const loadPersistedMediaListCache = (cacheKey: string): MediaListCacheEntry | null => {
    if (!cacheKey) {
        return null;
    }

    const store = readMediaListCacheStore();
    const entry = store.entries[cacheKey];
    if (!entry) {
        return null;
    }

    return {
        items: entry.items,
        total: entry.total,
    };
};

export const getCachedMediaListEntry = (cacheKey: string): MediaListCacheEntry | null => {
    if (!cacheKey) {
        return null;
    }

    const memoryEntry = mediaListMemoryCache.get(cacheKey);
    if (memoryEntry) {
        return memoryEntry;
    }

    const persistedEntry = loadPersistedMediaListCache(cacheKey);
    if (persistedEntry) {
        mediaListMemoryCache.set(cacheKey, persistedEntry);
        return persistedEntry;
    }

    return null;
};

export const persistMediaListCache = (cacheKey: string, entry: MediaListCacheEntry) => {
    if (!cacheKey) {
        return;
    }

    const normalizedEntry = {
        items: normalizeCachedMediaItems(entry.items),
        total: typeof entry.total === 'number' ? entry.total : 0,
    };
    mediaListMemoryCache.set(cacheKey, normalizedEntry);

    const store = readMediaListCacheStore();
    const nextOrder = [cacheKey, ...store.order.filter((key) => key !== cacheKey)].slice(0, MAX_MEDIA_LIST_ENTRIES);
    const nextEntries: Record<string, PersistedMediaListCacheEntry> = {};

    nextOrder.forEach((key) => {
        if (key === cacheKey) {
            nextEntries[key] = {
                items: normalizedEntry.items,
                total: normalizedEntry.total,
                updatedAt: Date.now(),
            };
            return;
        }

        if (store.entries[key]) {
            nextEntries[key] = store.entries[key];
        }
    });

    writeMediaListCacheStore({
        version: MEDIA_LIST_CACHE_VERSION,
        order: nextOrder,
        entries: nextEntries,
    });
};

const writeUpdatedMediaListCacheStore = (store: PersistedMediaListCacheStore) => {
    const normalizedStore = normalizeMediaListCacheStore(store);
    writeMediaListCacheStore(normalizedStore);
    mediaListMemoryCache.clear();
    normalizedStore.order.forEach((key) => {
        const entry = normalizedStore.entries[key];
        if (!entry) {
            return;
        }
        mediaListMemoryCache.set(key, {
            items: entry.items,
            total: entry.total,
        });
    });
};

const mergeCachedMediaFields = (item: any, media: any) => ({
    ...item,
    ...pickCachedMediaFields({
        ...item,
        ...media,
    }),
});

const shouldInvalidateMediaListCacheKey = (cacheKey: string, changedFields: ReadonlyArray<string>) => {
    if (changedFields.length === 0) {
        return false;
    }
    try {
        const parsed = JSON.parse(cacheKey);
        if (!Array.isArray(parsed)) {
            return false;
        }
        return shouldInvalidateMediaPagination(changedFields, String(parsed[4] || ''), String(parsed[2] || ''));
    } catch (_error) {
        return false;
    }
};

export const mergeMediaIntoCachedMediaLists = (media: any, changedFields: ReadonlyArray<string> = []) => {
    const mediaID = typeof media?.id === 'string' ? media.id.trim() : '';
    if (!mediaID) {
        return;
    }

    const store = readMediaListCacheStore();
    let changed = false;
    const nextEntries: Record<string, PersistedMediaListCacheEntry> = {};
    const nextOrder: string[] = [];

    store.order.forEach((key) => {
        const entry = store.entries[key];
        if (!entry) {
            return;
        }
        if (shouldInvalidateMediaListCacheKey(key, changedFields)) {
            changed = true;
            return;
        }
        nextOrder.push(key);

        let entryChanged = false;
        const nextItems = entry.items.map((item) => {
            if (item?.id !== mediaID) {
                return item;
            }

            entryChanged = true;
            return mergeCachedMediaFields(item, media);
        });

        nextEntries[key] = entryChanged
            ? {
                ...entry,
                items: nextItems,
                updatedAt: Date.now(),
            }
            : entry;
        changed = changed || entryChanged;
    });

    if (!changed) {
        return;
    }

    writeUpdatedMediaListCacheStore({
        ...store,
        order: nextOrder,
        entries: nextEntries,
    });
};

export const removeMediaFromCachedMediaLists = (mediaID: string) => {
    const normalizedMediaID = typeof mediaID === 'string' ? mediaID.trim() : '';
    if (!normalizedMediaID) {
        return;
    }

    const store = readMediaListCacheStore();
    let changed = false;
    const nextEntries: Record<string, PersistedMediaListCacheEntry> = {};

    store.order.forEach((key) => {
        const entry = store.entries[key];
        if (!entry) {
            return;
        }

        const nextItems = entry.items.filter((item) => item?.id !== normalizedMediaID);
        if (nextItems.length === entry.items.length) {
            nextEntries[key] = entry;
            return;
        }

        changed = true;
        nextEntries[key] = {
            ...entry,
            items: nextItems,
            total: Math.max(0, entry.total - (entry.items.length - nextItems.length)),
            updatedAt: Date.now(),
        };
    });

    if (!changed) {
        return;
    }

    writeUpdatedMediaListCacheStore({
        ...store,
        entries: nextEntries,
    });
};
