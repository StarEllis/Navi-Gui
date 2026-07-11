import { GetMediaDetailBundle } from "../../wailsjs/go/main/App";
import type { AppMedia } from '../types/wails';
import { applyMediaStateUpdate, type MediaStateUpdate } from './mediaPlaybackState';

export type MediaDetailCacheEntry = {
    detail: AppMedia;
    files: string[];
    previews: string[];
    updatedAt: number;
};

const MAX_MEDIA_DETAIL_CACHE_ENTRIES = 24;

type MediaDetailBundle = {
    detail?: AppMedia;
    files?: string[];
    previews?: string[];
};

const mediaDetailCache = new Map<string, MediaDetailCacheEntry>();
const mediaDetailRequests = new Map<string, Promise<MediaDetailCacheEntry>>();

const trimMediaDetailCache = () => {
    while (mediaDetailCache.size > MAX_MEDIA_DETAIL_CACHE_ENTRIES) {
        const oldestKey = mediaDetailCache.keys().next().value;
        if (!oldestKey) {
            return;
        }
        mediaDetailCache.delete(oldestKey);
    }
};

const setMediaDetailCacheEntry = (mediaID: string, entry: MediaDetailCacheEntry) => {
    if (!mediaID) {
        return;
    }

    mediaDetailCache.delete(mediaID);
    mediaDetailCache.set(mediaID, entry);
    trimMediaDetailCache();
};

const mergeMediaDetail = (currentDetail: AppMedia | null | undefined, nextDetail: AppMedia) => {
    if (!currentDetail) {
        return nextDetail;
    }

    const mergedDetail = { ...currentDetail } as AppMedia;
    Object.entries(nextDetail).forEach(([key, value]) => {
        if (value === undefined || value === null) {
            return;
        }

        const currentValue = (mergedDetail as Record<string, unknown>)[key];
        if (typeof value === 'string' && value.trim() === '' && typeof currentValue === 'string' && currentValue.trim() !== '') {
            return;
        }
        if (typeof value === 'number' && value === 0 && typeof currentValue === 'number' && currentValue !== 0) {
            return;
        }
        if (Array.isArray(value) && value.length === 0 && Array.isArray(currentValue) && currentValue.length > 0) {
            return;
        }

        (mergedDetail as Record<string, unknown>)[key] = value;
    });

    return mergedDetail;
};

const mergeFetchedMediaDetail = (currentDetail: AppMedia | null | undefined, nextDetail: AppMedia) => {
    const mergedDetail = mergeMediaDetail(currentDetail, nextDetail);
    if (!currentDetail || typeof currentDetail.revision !== 'number' || currentDetail.revision <= 0) {
        return mergedDetail;
    }

    const playbackFields: Array<keyof MediaStateUpdate> = [
        'position',
        'duration',
        'watch_duration',
        'progress_percent',
        'completed',
        'is_watched',
        'is_favorite',
        'last_watched_at',
        'playback_state',
        'revision',
    ];
    playbackFields.forEach((field) => {
        if (Object.prototype.hasOwnProperty.call(currentDetail, field)) {
            (mergedDetail as Record<string, any>)[field] = (currentDetail as Record<string, any>)[field];
        }
    });
    return mergedDetail;
};

export const getMediaDetailCacheEntry = (mediaID: string) => {
    const normalizedMediaID = typeof mediaID === 'string' ? mediaID.trim() : '';
    if (!normalizedMediaID) {
        return null;
    }

    const cachedEntry = mediaDetailCache.get(normalizedMediaID);
    if (!cachedEntry) {
        return null;
    }

    mediaDetailCache.delete(normalizedMediaID);
    mediaDetailCache.set(normalizedMediaID, cachedEntry);
    return cachedEntry;
};

export const seedMediaDetailCache = (media: AppMedia) => {
    const mediaID = typeof media?.id === 'string' ? media.id.trim() : '';
    if (!mediaID) {
        return;
    }

    const existingEntry = getMediaDetailCacheEntry(mediaID);
    const seededFiles = typeof media?.file_path === 'string' && media.file_path.trim()
        ? [media.file_path.trim()]
        : [];
    setMediaDetailCacheEntry(mediaID, {
        detail: mergeMediaDetail(existingEntry?.detail, media),
        files: existingEntry?.files.length ? existingEntry.files : seededFiles,
        previews: existingEntry?.previews || [],
        updatedAt: existingEntry?.updatedAt || Date.now(),
    });
};

export const mergeMediaDetailCacheEntry = (media: AppMedia) => {
    const mediaID = typeof media?.id === 'string' ? media.id.trim() : '';
    if (!mediaID) {
        return;
    }

    const existingEntry = getMediaDetailCacheEntry(mediaID);
    setMediaDetailCacheEntry(mediaID, {
        detail: mergeMediaDetail(existingEntry?.detail, media),
        files: existingEntry?.files || [],
        previews: existingEntry?.previews || [],
        updatedAt: Date.now(),
    });
};

export const mergeMediaStateCacheEntry = (update: MediaStateUpdate) => {
    const mediaID = typeof update?.id === 'string' ? update.id.trim() : '';
    if (!mediaID) {
        return;
    }

    const existingEntry = getMediaDetailCacheEntry(mediaID);
    const currentDetail = existingEntry?.detail || ({ id: mediaID } as AppMedia);
    const nextDetail = applyMediaStateUpdate(currentDetail as AppMedia & Record<string, any>, update) as AppMedia;
    if (nextDetail === currentDetail && existingEntry) {
        return;
    }
    setMediaDetailCacheEntry(mediaID, {
        detail: nextDetail,
        files: existingEntry?.files || [],
        previews: existingEntry?.previews || [],
        updatedAt: Date.now(),
    });
};

export const removeMediaDetailCacheEntry = (mediaID: string) => {
    const normalizedMediaID = typeof mediaID === 'string' ? mediaID.trim() : '';
    if (!normalizedMediaID) {
        return;
    }

    mediaDetailCache.delete(normalizedMediaID);
    mediaDetailRequests.delete(normalizedMediaID);
};

export const fetchMediaDetailCacheEntry = async (mediaID: string): Promise<MediaDetailCacheEntry> => {
    const normalizedMediaID = typeof mediaID === 'string' ? mediaID.trim() : '';
    if (!normalizedMediaID) {
        return Promise.reject(new Error('invalid media id'));
    }

    const inFlightRequest = mediaDetailRequests.get(normalizedMediaID);
    if (inFlightRequest) {
        return inFlightRequest;
    }

    const request = GetMediaDetailBundle(normalizedMediaID)
        .then((bundle: MediaDetailBundle) => {
            if (!bundle?.detail) {
                throw new Error('empty media detail bundle');
            }
            const existingEntry = getMediaDetailCacheEntry(normalizedMediaID);
            const nextEntry: MediaDetailCacheEntry = {
                detail: mergeFetchedMediaDetail(existingEntry?.detail, bundle.detail),
                files: Array.isArray(bundle?.files) ? bundle.files : [],
                previews: Array.isArray(bundle?.previews) ? bundle.previews : [],
                updatedAt: Date.now(),
            };
            setMediaDetailCacheEntry(normalizedMediaID, nextEntry);
            return nextEntry;
        })
        .finally(() => {
            mediaDetailRequests.delete(normalizedMediaID);
        });

    mediaDetailRequests.set(normalizedMediaID, request);
    return request;
};

export const prefetchMediaDetailCacheEntry = (mediaID: string) => {
    if (getMediaDetailCacheEntry(mediaID)) {
        return;
    }

    void fetchMediaDetailCacheEntry(mediaID).catch(() => {
        // ignore prefetch failures and let the interactive path retry
    });
};
