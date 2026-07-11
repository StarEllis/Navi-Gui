export type MediaStateUpdate = {
    id: string;
    position?: number;
    duration?: number;
    watch_duration?: number;
    progress_percent?: number;
    completed?: boolean;
    is_watched?: boolean;
    is_favorite?: boolean;
    last_watched_at?: string;
    playback_state?: string;
    revision?: number;
};

const MEDIA_STATE_FIELDS: Array<keyof Omit<MediaStateUpdate, 'id'>> = [
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

const finiteNumber = (value: unknown) => (
    typeof value === 'number' && Number.isFinite(value) ? value : null
);

const nonNegativeNumber = (value: unknown) => {
    const normalized = finiteNumber(value);
    return normalized === null ? null : Math.max(0, normalized);
};

const normalizedTimestamp = (value: unknown) => {
    if (typeof value !== 'string') {
        return '';
    }
    const normalized = value.trim();
    return normalized.startsWith('0001-01-01') ? '' : normalized;
};

export const normalizeMediaStateEvent = (data: any): MediaStateUpdate | null => {
    const mediaID = typeof data?.media_id === 'string' ? data.media_id.trim() : '';
    if (!mediaID) {
        return null;
    }

    const update: MediaStateUpdate = { id: mediaID };
    const position = nonNegativeNumber(data?.position);
    const duration = nonNegativeNumber(data?.duration);
    const progressPercent = nonNegativeNumber(data?.progress_percent);
    const revision = finiteNumber(data?.revision);
    const lastWatchedAt = normalizedTimestamp(data?.last_watched_at);
    const playbackState = typeof data?.playback_state === 'string' ? data.playback_state.trim() : '';

    if (position !== null) {
        update.position = position;
    }
    if (duration !== null) {
        update.duration = duration;
        update.watch_duration = duration;
    }
    if (progressPercent !== null) {
        update.progress_percent = Math.min(100, progressPercent);
    }
    if (typeof data?.completed === 'boolean') {
        update.completed = data.completed;
    }
    if (typeof data?.is_watched === 'boolean') {
        update.is_watched = data.is_watched;
    }
    if (typeof data?.is_favorite === 'boolean') {
        update.is_favorite = data.is_favorite;
    }
    if (lastWatchedAt) {
        update.last_watched_at = lastWatchedAt;
    }
    if (playbackState) {
        update.playback_state = playbackState;
    }
    if (revision !== null && revision > 0) {
        update.revision = Math.trunc(revision);
    }

    return Object.keys(update).length > 1 ? update : null;
};

export const applyMediaStateUpdate = <T extends Record<string, any>>(
    current: T,
    update: MediaStateUpdate,
): T => {
    if (!current || current.id !== update.id) {
        return current;
    }

    const currentRevision = finiteNumber(current.revision);
    const nextRevision = finiteNumber(update.revision);
    if (currentRevision !== null && currentRevision > 0 && nextRevision === null) {
        return current;
    }
    if (nextRevision !== null && currentRevision !== null && nextRevision <= currentRevision) {
        return current;
    }

    const changed = MEDIA_STATE_FIELDS.some((field) => (
        Object.prototype.hasOwnProperty.call(update, field) && !Object.is(current[field], update[field])
    ));
    return changed ? { ...current, ...update } : current;
};

export const getChangedMediaStateFields = (
    previous: Record<string, any> | null | undefined,
    next: Record<string, any>,
) => MEDIA_STATE_FIELDS.filter((field) => (
    Object.prototype.hasOwnProperty.call(next, field) && !Object.is(previous?.[field], next[field])
));

export const shouldInvalidateMediaPagination = (
    changedFields: ReadonlyArray<string>,
    filterType: string,
    sortField: string,
) => {
    const changed = new Set(changedFields);
    const membershipChanged = (
        (filterType === 'favorite' && changed.has('is_favorite'))
        || ((filterType === 'watched' || filterType === 'unwatched') && changed.has('is_watched'))
    );
    const sortKeyChanged = (
        (sortField === 'favorite_at' && (changed.has('is_favorite') || changed.has('favorite_at')))
        || (sortField === 'last_watched' && changed.has('last_watched_at'))
        || (sortField === 'rating' && changed.has('rating'))
    );
    return membershipChanged || sortKeyChanged;
};

export const getMediaProgressPercent = (media: Record<string, any> | null | undefined) => {
    const explicitProgress = nonNegativeNumber(media?.progress_percent);
    if (explicitProgress !== null) {
        return Math.min(100, explicitProgress);
    }

    const position = nonNegativeNumber(media?.position);
    const watchDuration = nonNegativeNumber(media?.watch_duration);
    const duration = watchDuration && watchDuration > 0 ? watchDuration : nonNegativeNumber(media?.duration);
    if (position !== null && duration !== null && duration > 0) {
        return Math.min(100, (position / duration) * 100);
    }

    const legacyProgress = nonNegativeNumber(media?.watch_progress);
    return legacyProgress === null ? null : Math.min(100, legacyProgress);
};

export const formatPlaybackTime = (value: unknown) => {
    const normalized = nonNegativeNumber(value);
    if (normalized === null) {
        return '0:00';
    }
    const totalSeconds = Math.floor(normalized);
    const hours = Math.floor(totalSeconds / 3600);
    const minutes = Math.floor((totalSeconds % 3600) / 60);
    const seconds = totalSeconds % 60;
    return hours > 0
        ? `${hours}:${String(minutes).padStart(2, '0')}:${String(seconds).padStart(2, '0')}`
        : `${minutes}:${String(seconds).padStart(2, '0')}`;
};
